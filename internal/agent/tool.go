package agent

import (
	"context"
	"encoding/json"

	"github.com/aaliyan1230/go-rig/internal/message"
	"github.com/aaliyan1230/go-rig/internal/provider"
)

// Tool is an executable capability available to the agent.
//
// Tool is deliberately open: concrete tools live in the tools package and can
// be added without teaching the agent loop about their implementation.
type Tool interface {
	ToolName() string
	ToolDescription() string
	ToolSchema() json.RawMessage
	Execute(ctx context.Context, toolCallID string, params json.RawMessage) (ToolResult, error)
}

type ToolResult struct {
	Content []message.ContentBlock
}

// ToolSpecs converts agent tools to provider-facing tool specs.
func ToolSpecs(tools []Tool) []provider.ToolSpec {
	specs := make([]provider.ToolSpec, len(tools))
	for i, t := range tools {
		specs[i] = provider.ToolSpec{
			Name:        t.ToolName(),
			Description: t.ToolDescription(),
			Parameters:  t.ToolSchema(),
		}
	}
	return specs
}
