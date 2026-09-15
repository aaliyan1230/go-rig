package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/aaliyan1230/go-rig/internal/agent"
	"github.com/aaliyan1230/go-rig/internal/env"
	"github.com/aaliyan1230/go-rig/internal/message"
)

var writeSchema = json.RawMessage(`{
	"type":"object",
	"properties":{
		"path":{"type":"string","description":"Path to the file to create or overwrite, relative to the working directory or absolute."},
		"content":{"type":"string","description":"Complete UTF-8 text content to write to the file."}
	},
	"required":["path","content"],
	"additionalProperties":false
}`)

type writeArgs struct {
	Path    string  `json:"path"`
	Content *string `json:"content"`
}

// WriteTool creates or overwrites a complete text file through Env. It is
// intentionally small: callers that need a surgical change should use edit.
type WriteTool struct {
	env env.Env
}

var _ agent.Tool = (*WriteTool)(nil)

func NewWriteTool(environment env.Env) *WriteTool {
	return &WriteTool{env: environment}
}

func (t *WriteTool) ToolName() string { return "write" }

func (t *WriteTool) ToolDescription() string {
	return "Create or overwrite a UTF-8 text file. Parent directories are created automatically; use edit for precise replacements."
}

func (t *WriteTool) ToolSchema() json.RawMessage { return writeSchema }

func (t *WriteTool) Execute(ctx context.Context, _ string, params json.RawMessage) (agent.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}

	var args writeArgs
	if err := decodeToolArgs(params, "write", &args); err != nil {
		return agent.ToolResult{}, err
	}
	args.Path = strings.TrimSpace(args.Path)
	if args.Path == "" {
		return agent.ToolResult{}, fmt.Errorf("invalid write arguments: path is required")
	}
	if args.Content == nil {
		return agent.ToolResult{}, fmt.Errorf("invalid write arguments: content must be a string")
	}

	path := resolvePath(t.env, args.Path)
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}
	if err := t.env.MkdirAll(ctx, filepath.Dir(path)); err != nil {
		return agent.ToolResult{}, fmt.Errorf("write %q: create parent directories: %w", args.Path, err)
	}
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}
	if err := t.env.WriteFile(ctx, path, []byte(*args.Content), 0o644); err != nil {
		return agent.ToolResult{}, fmt.Errorf("write %q: %w", args.Path, err)
	}
	if err := ctx.Err(); err != nil {
		return agent.ToolResult{}, err
	}

	return agent.ToolResult{Content: []message.ContentBlock{message.TextContent{
		Text: fmt.Sprintf("wrote %d bytes to %s", len([]byte(*args.Content)), args.Path),
	}}}, nil
}

func resolvePath(environment env.Env, path string) string {
	if !filepath.IsAbs(path) {
		path = filepath.Join(environment.Cwd(), path)
	}
	return filepath.Clean(path)
}
