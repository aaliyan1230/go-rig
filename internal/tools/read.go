package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/aaliyan1230/go-rig/internal/agent"
	"github.com/aaliyan1230/go-rig/internal/env"
	"github.com/aaliyan1230/go-rig/internal/message"
)

var readSchema = json.RawMessage(`{
	"type":"object",
	"properties":{"path":{"type":"string","description":"Path to a text file, relative to the working directory or absolute."}},
	"required":["path"],
	"additionalProperties":false
}`)

type readArgs struct {
	Path string `json:"path"`
}

// ReadTool reads one UTF-8 text file through the configured environment.
// Paging is deliberately deferred: Phase 2 proves the tool round-trip first.
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
	return "Read a UTF-8 text file. Output is truncated to 2,000 lines or 50 KiB, whichever comes first."
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

	path := args.Path
	if !filepath.IsAbs(path) {
		path = filepath.Join(t.env.Cwd(), path)
	}
	path = filepath.Clean(path)

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

	text, info := truncateHead(string(data), DefaultReadMaxLines, DefaultReadMaxBytes)
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
	return args, nil
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
