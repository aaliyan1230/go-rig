package provider

import (
	"context"
	"encoding/json"

	"github.com/aaliyan1230/go-rig/internal/message"
)

type ModelClient interface {
	Stream(ctx context.Context, params StreamParams) <-chan StreamEvent
}

type StreamParams struct {
	SystemPrompt string
	Messages     []message.Message
	Tools        []ToolSpec
	Model        string
	MaxTokens    int
}

type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}
