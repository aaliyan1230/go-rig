package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/aaliyan1230/go-rig/internal/agent"
	"github.com/aaliyan1230/go-rig/internal/env"
	"github.com/aaliyan1230/go-rig/internal/message"
)

var readSchema = json.RawMessage(`{
	"type":"object",
	"properties":{
		"path":{"type":"string","description":"Path to a text file, relative to the working directory or absolute."},
		"offset":{"type":"integer","minimum":1,"description":"1-indexed line number to start reading from (default: 1)."},
		"limit":{"type":"integer","minimum":1,"description":"Maximum number of lines to return."}
	},
	"required":["path"],
	"additionalProperties":false
}`)

type readArgs struct {
	Path   string `json:"path"`
	Offset *int   `json:"offset"`
	Limit  *int   `json:"limit"`
}

// ReadTool reads one UTF-8 text file through the configured environment. It
// supports small, line-oriented pages so the model can inspect large files
// without putting the entire file in its context window.
type ReadTool struct {
	env env.Env
}

var _ agent.Tool = (*ReadTool)(nil)

func NewReadTool(environment env.Env) *ReadTool {
	return &ReadTool{env: environment}
}

func (t *ReadTool) ToolName() string {
	return "read"
}

func (t *ReadTool) ToolDescription() string {
	return "Read a UTF-8 text file, optionally starting at a 1-indexed line offset and limiting the number of lines. Output is truncated to 2,000 lines or 50 KiB, whichever comes first."
}

func (t *ReadTool) ToolSchema() json.RawMessage {
	return readSchema
}

func (t *ReadTool) Execute(ctx context.Context, _ string, params json.RawMessage) (agent.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}

	args, err := decodeReadArgs(params)
	if err != nil {
		return agent.ToolResult{}, err
	}

	path := resolvePath(t.env, args.Path)

	data, err := t.env.ReadFile(ctx, path)
	if err != nil {
		return agent.ToolResult{}, fmt.Errorf("read %q: %w", args.Path, err)
	}
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}
	if !utf8.Valid(data) {
		return agent.ToolResult{}, fmt.Errorf("read %q: file is not valid UTF-8 text", args.Path)
	}

	textData := string(data)
	lines := splitLogicalLines(textData)
	offset := 1
	if args.Offset != nil {
		offset = *args.Offset
	}
	// An empty file has no logical lines, but offset=1 is still a useful and
	// harmless request: it should return an empty result rather than fail.
	if (len(lines) == 0 && offset > 1) || (len(lines) > 0 && offset > len(lines)) {
		return agent.ToolResult{}, fmt.Errorf("read %q: offset %d is beyond end of file (%d lines)", args.Path, offset, len(lines))
	}

	start := 0
	if len(lines) > 0 {
		start = offset - 1
	}
	end := len(lines)
	requestedEnd := end
	if args.Limit != nil && start < end {
		if *args.Limit > end-start {
			requestedEnd = end
		} else {
			requestedEnd = start + *args.Limit
		}
		if requestedEnd > end { // defensive; the branch above avoids overflow
			requestedEnd = end
		}
		end = requestedEnd
	}

	selected := joinLogicalLines(lines[start:end])
	text, info := truncateHead(selected, DefaultReadMaxLines, DefaultReadMaxBytes)
	if info.truncated {
		text = strings.TrimSuffix(text, "\n")
		text += fmt.Sprintf(
			"\n\n[truncated: showing first %d of %d lines; %s of %d lines or %d KiB reached]",
			info.shown,
			info.total,
			info.reason,
			DefaultReadMaxLines,
			DefaultReadMaxBytes/1024,
		)
	}

	shown := info.shown
	if !info.truncated {
		shown = logicalLineCount(text)
	}
	nextOffset := offset + shown
	if shown == 0 && (info.truncated || args.Limit != nil) {
		// A very long first line can consume the byte budget before a complete
		// line fits. Advance by one logical line so a caller can still make
		// progress instead of receiving the same page forever.
		nextOffset = offset + 1
	}
	if nextOffset <= len(lines) && (args.Limit != nil || info.truncated) {
		text = strings.TrimSuffix(text, "\n")
		text += fmt.Sprintf("\n\n[more lines available: Use offset=%d to continue]", nextOffset)
	}

	return agent.ToolResult{Content: []message.ContentBlock{message.TextContent{Text: text}}}, nil
}

func decodeReadArgs(params json.RawMessage) (readArgs, error) {
	decoder := json.NewDecoder(bytes.NewReader(params))
	decoder.DisallowUnknownFields()

	var args readArgs
	if err := decoder.Decode(&args); err != nil {
		return readArgs{}, fmt.Errorf("invalid read arguments: %w", err)
	}
	if err := ensureOnlyOneJSONValue(decoder); err != nil {
		return readArgs{}, err
	}
	args.Path = strings.TrimSpace(args.Path)
	if args.Path == "" {
		return readArgs{}, fmt.Errorf("invalid read arguments: path is required")
	}
	if jsonFieldIsNull(params, "offset") {
		return readArgs{}, fmt.Errorf("invalid read arguments: offset must be an integer")
	}
	if jsonFieldIsNull(params, "limit") {
		return readArgs{}, fmt.Errorf("invalid read arguments: limit must be an integer")
	}
	if args.Offset != nil && *args.Offset < 1 {
		return readArgs{}, fmt.Errorf("invalid read arguments: offset must be at least 1")
	}
	if args.Limit != nil && *args.Limit < 1 {
		return readArgs{}, fmt.Errorf("invalid read arguments: limit must be positive")
	}
	return args, nil
}

// splitLogicalLines keeps each line's original newline terminator. A terminal
// newline does not create a second, empty logical line, which makes offsets
// line up with what a person normally calls the lines in a file.
func splitLogicalLines(text string) []string {
	if text == "" {
		return nil
	}
	lines := strings.SplitAfter(text, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func joinLogicalLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	var builder strings.Builder
	for _, line := range lines {
		builder.WriteString(line)
	}
	return builder.String()
}

func ensureOnlyOneJSONValue(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if err == io.EOF {
		return nil
	}
	if err == nil {
		return fmt.Errorf("invalid read arguments: multiple JSON values")
	}
	return fmt.Errorf("invalid read arguments: %w", err)
}
