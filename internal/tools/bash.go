package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aaliyan1230/go-rig/internal/agent"
	"github.com/aaliyan1230/go-rig/internal/env"
	"github.com/aaliyan1230/go-rig/internal/message"
)

const DefaultBashTimeoutSeconds = 30

const maxBashTimeoutSeconds = int64(1<<63-1) / int64(time.Second)

var bashSchema = json.RawMessage(`{
	"type":"object",
	"properties":{
		"command":{"type":"string","description":"Bash command to execute in the current working directory."},
		"timeout":{"type":"integer","minimum":1,"description":"Timeout in seconds (default: 30)."}
	},
	"required":["command"],
	"additionalProperties":false
}`)

type bashArgs struct {
	Command string `json:"command"`
	Timeout *int   `json:"timeout"`
}

// BashTool runs a command through Env and returns a bounded, model-readable
// transcript. Keeping execution behind Env makes this tool testable and leaves
// room for a future container or remote implementation.
type BashTool struct {
	env env.Env
}

var _ agent.Tool = (*BashTool)(nil)

func NewBashTool(environment env.Env) *BashTool {
	return &BashTool{env: environment}
}

func (t *BashTool) ToolName() string { return "bash" }

func (t *BashTool) ToolDescription() string {
	return "Execute a bash command in the current working directory. Returns labeled stdout and stderr, bounded to the last 2,000 lines or 50 KiB. The default timeout is 30 seconds."
}

func (t *BashTool) ToolSchema() json.RawMessage { return bashSchema }

func (t *BashTool) Execute(ctx context.Context, _ string, params json.RawMessage) (agent.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}

	var args bashArgs
	if err := decodeToolArgs(params, "bash", &args); err != nil {
		return agent.ToolResult{}, err
	}
	if strings.TrimSpace(args.Command) == "" {
		return agent.ToolResult{}, fmt.Errorf("invalid bash arguments: command is required")
	}
	if jsonFieldIsNull(params, "timeout") {
		return agent.ToolResult{}, fmt.Errorf("invalid bash arguments: timeout must be an integer")
	}

	timeoutSeconds := DefaultBashTimeoutSeconds
	if args.Timeout != nil {
		if *args.Timeout <= 0 {
			return agent.ToolResult{}, fmt.Errorf("invalid bash arguments: timeout must be positive")
		}
		timeoutSeconds = *args.Timeout
	}
	if int64(timeoutSeconds) > maxBashTimeoutSeconds {
		return agent.ToolResult{}, fmt.Errorf("invalid bash arguments: timeout is too large")
	}
	timeout := time.Duration(timeoutSeconds) * time.Second
	if timeout <= 0 {
		return agent.ToolResult{}, fmt.Errorf("invalid bash arguments: timeout is too large")
	}

	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result, execErr := t.env.Exec(execCtx, args.Command, env.ExecOptions{
		Cwd:     t.env.Cwd(),
		Timeout: timeout,
	})
	output, _ := formatBashOutput(result.Stdout, result.Stderr)

	if err := execCtx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			return agent.ToolResult{}, fmt.Errorf("bash command timed out after %d seconds:\n%s", timeoutSeconds, output)
		}
		return agent.ToolResult{}, fmt.Errorf("bash command canceled: %w\n%s", err, output)
	}
	if execErr != nil {
		if errors.Is(execErr, context.DeadlineExceeded) && ctx.Err() == nil {
			return agent.ToolResult{}, fmt.Errorf("bash command timed out after %d seconds:\n%s", timeoutSeconds, output)
		}
		if errors.Is(execErr, context.Canceled) || errors.Is(execErr, context.DeadlineExceeded) {
			return agent.ToolResult{}, fmt.Errorf("bash command canceled: %w\n%s", execErr, output)
		}
		if result.ExitCode != 0 {
			return agent.ToolResult{}, fmt.Errorf("bash command exited with code %d:\n%s", result.ExitCode, output)
		}
		return agent.ToolResult{}, fmt.Errorf("bash command failed: %w\n%s", execErr, output)
	}
	if result.ExitCode != 0 {
		return agent.ToolResult{}, fmt.Errorf("bash command exited with code %d:\n%s", result.ExitCode, output)
	}

	return agent.ToolResult{Content: []message.ContentBlock{message.TextContent{Text: output}}}, nil
}

func formatBashOutput(stdout, stderr string) (string, truncation) {
	if stdout == "" && stderr == "" {
		return "(no output)", truncation{}
	}

	full := buildBashOutput(stdout, stderr, truncation{}, truncation{})
	fullInfo := truncation{total: logicalLineCount(full), shown: logicalLineCount(full)}
	if len(full) <= DefaultReadMaxBytes && fullInfo.total <= DefaultReadMaxLines {
		return full, fullInfo
	}

	// Keep both headings visible even when one stream is very large. Budgets
	// are split between stdout and stderr, then unused space from a short
	// stream is given to the other stream.
	headerBytes := len("STDOUT:\n\nSTDERR:\n")
	availableBytes := DefaultReadMaxBytes - headerBytes
	if availableBytes < 0 {
		availableBytes = 0
	}
	availableLines := DefaultReadMaxLines - 3 // two headings plus the separator
	if availableLines < 0 {
		availableLines = 0
	}
	outBytes, errBytes := splitBudget(len(stdout), len(stderr), availableBytes)
	outLines, errLines := splitBudget(logicalLineCount(stdout), logicalLineCount(stderr), availableLines)
	boundedStdout, stdoutInfo := truncateTail(stdout, outLines, outBytes)
	boundedStderr, stderrInfo := truncateTail(stderr, errLines, errBytes)
	bounded := buildBashOutput(boundedStdout, boundedStderr, stdoutInfo, stderrInfo)

	totalLines := logicalLineCount(stdout) + logicalLineCount(stderr) + 3
	shownLines := logicalLineCount(boundedStdout) + logicalLineCount(boundedStderr) + 3
	reason := "line limit"
	if len(full) > DefaultReadMaxBytes {
		reason = "byte limit"
	}
	info := truncation{truncated: true, reason: reason, shown: shownLines, total: totalLines}
	notice := fmt.Sprintf("[truncated: showing last %d of %d lines; %s reached]", info.shown, info.total, info.reason)
	return strings.TrimSuffix(bounded, "\n") + "\n\n" + notice, info
}

func buildBashOutput(stdout, stderr string, stdoutInfo, stderrInfo truncation) string {
	var builder strings.Builder
	builder.WriteString("STDOUT:\n")
	if stdout == "" {
		if stdoutInfo.truncated {
			builder.WriteString("(earlier stdout omitted)\n")
		} else {
			builder.WriteString("(empty)\n")
		}
	} else {
		builder.WriteString(stdout)
		if !strings.HasSuffix(stdout, "\n") {
			builder.WriteByte('\n')
		}
	}
	builder.WriteString("\nSTDERR:\n")
	if stderr == "" {
		if stderrInfo.truncated {
			builder.WriteString("(earlier stderr omitted)")
		} else {
			builder.WriteString("(empty)")
		}
	} else {
		builder.WriteString(stderr)
	}
	return builder.String()
}

func splitBudget(first, second, total int) (int, int) {
	if total <= 0 {
		return 0, 0
	}
	if first+second <= total {
		return first, second
	}
	if first == 0 {
		return 0, minInt(second, total)
	}
	if second == 0 {
		return minInt(first, total), 0
	}

	firstBudget := minInt(first, total/2)
	secondBudget := minInt(second, total-firstBudget)
	remaining := total - firstBudget - secondBudget
	if remaining > 0 && firstBudget < first {
		extra := minInt(remaining, first-firstBudget)
		firstBudget += extra
		remaining -= extra
	}
	if remaining > 0 && secondBudget < second {
		secondBudget += minInt(remaining, second-secondBudget)
	}
	return firstBudget, secondBudget
}
