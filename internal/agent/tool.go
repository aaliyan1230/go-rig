package agent

import (
	"context"
	"encoding/json"

	"github.com/aaliyan1230/go-rig/internal/message"
	"github.com/aaliyan1230/go-rig/internal/provider"
)

// Tool is a sealed interface for executable tools available to the agent.
type Tool interface {
	tool()
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
