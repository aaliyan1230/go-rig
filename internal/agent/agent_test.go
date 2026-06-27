package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aaliyan1230/go-rig/internal/message"
	"github.com/aaliyan1230/go-rig/internal/provider"
)

// fakeTool is a test implementation of the Tool interface.
type fakeTool struct {
	name        string
	description string
	schema      json.RawMessage
}

func (f fakeTool) tool()                        {}
func (f fakeTool) ToolName() string             { return f.name }
func (f fakeTool) ToolDescription() string      { return f.description }
func (f fakeTool) ToolSchema() json.RawMessage  { return f.schema }
func (f fakeTool) Execute(_ context.Context, _ string, _ json.RawMessage) (ToolResult, error) {
	return ToolResult{Content: []message.ContentBlock{message.TextContent{Text: "ok"}}}, nil
}

func TestToolSpecs(t *testing.T) {
	tools := []Tool{
		fakeTool{
			name:        "read",
			description: "Read a file",
			schema:      json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
		},
		fakeTool{
			name:        "bash",
			description: "Run a shell command",
			schema:      json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}}}`),
		},
	}

	specs := ToolSpecs(tools)

	if len(specs) != 2 {
		t.Fatalf("expected 2 specs, got %d", len(specs))
	}
	if specs[0].Name != "read" {
		t.Errorf("specs[0].Name: got %q, want %q", specs[0].Name, "read")
	}
	if specs[1].Name != "bash" {
		t.Errorf("specs[1].Name: got %q, want %q", specs[1].Name, "bash")
	}
	if specs[0].Description != "Read a file" {
		t.Errorf("specs[0].Description: got %q, want %q", specs[0].Description, "Read a file")
	}
}

func TestAgentEventTypeSwitch(t *testing.T) {
	events := []AgentEvent{
		AgentStart{},
		AgentEnd{},
		TurnStart{},
		TurnEnd{},
		MessageStart{},
		MessageUpdate{Event: provider.TextDelta{Delta: "hi"}},
		MessageEnd{},
		ToolExecStart{ToolCallID: "c1", ToolName: "read", Args: json.RawMessage(`{}`)},
		ToolExecUpdate{ToolCallID: "c1", ToolName: "read"},
		ToolExecEnd{ToolCallID: "c1", ToolName: "read", IsError: false},
	}

	for i, event := range events {
		switch event.(type) {
		case AgentStart:
		case AgentEnd:
		case TurnStart:
		case TurnEnd:
		case MessageStart:
		case MessageUpdate:
		case MessageEnd:
		case ToolExecStart:
		case ToolExecUpdate:
		case ToolExecEnd:
		default:
			t.Errorf("events[%d]: unhandled type %T", i, event)
		}
	}
}

func TestToolExecute(t *testing.T) {
	tool := fakeTool{name: "test", description: "test tool", schema: json.RawMessage(`{}`)}
	result, err := tool.Execute(context.Background(), "call_1", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(result.Content))
	}
	tc, ok := result.Content[0].(message.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", result.Content[0])
	}
	if tc.Text != "ok" {
		t.Errorf("got text %q, want %q", tc.Text, "ok")
	}
}
