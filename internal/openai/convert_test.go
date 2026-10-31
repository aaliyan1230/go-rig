package openai

import (
	"encoding/json"
	"testing"

	"github.com/aaliyan1230/go-rig/internal/message"
	"github.com/aaliyan1230/go-rig/internal/provider"
)

func TestConvertMessages_SystemPrompt(t *testing.T) {
	msgs := convertMessages("You are helpful.", nil)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Role != "system" {
		t.Errorf("expected role 'system', got %q", msgs[0].Role)
	}
	if msgs[0].Content != "You are helpful." {
		t.Errorf("expected system content, got %v", msgs[0].Content)
	}
}

func TestConvertMessages_NoSystemPrompt(t *testing.T) {
	msgs := convertMessages("", []message.Message{
		message.UserMessage{
			Content: []message.ContentBlock{message.TextContent{Text: "hi"}},
		},
	})
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message (no system), got %d", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Errorf("expected role 'user', got %q", msgs[0].Role)
	}
}

func TestEmptyFailedAssistantHasExplicitContent(t *testing.T) {
	converted := convertAssistantMessage(message.AssistantMessage{StopReason: message.StopReasonError})
	encoded, err := json.Marshal(converted)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["content"]) != `""` {
		t.Fatalf("empty failed assistant cannot be replayed: %s", encoded)
	}
}

func TestConvertUserMessage_TextOnly(t *testing.T) {
	msgs := convertMessages("", []message.Message{
		message.UserMessage{
			Content: []message.ContentBlock{
				message.TextContent{Text: "hello"},
				message.TextContent{Text: "world"},
			},
		},
	})
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	content, ok := msgs[0].Content.(string)
	if !ok {
		t.Fatalf("expected string content, got %T", msgs[0].Content)
	}
	if content != "hello\nworld" {
		t.Errorf("expected 'hello\\nworld', got %q", content)
	}
}

func TestConvertUserMessage_WithImage(t *testing.T) {
	msgs := convertMessages("", []message.Message{
		message.UserMessage{
			Content: []message.ContentBlock{
				message.TextContent{Text: "look at this"},
				message.ImageContent{Data: "abc123", MimeType: "image/png"},
			},
		},
	})
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	parts, ok := msgs[0].Content.([]apiContentPart)
	if !ok {
		t.Fatalf("expected []apiContentPart, got %T", msgs[0].Content)
	}
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts, got %d", len(parts))
	}
	if parts[0].Type != "text" || parts[0].Text != "look at this" {
		t.Errorf("unexpected text part: %+v", parts[0])
	}
	if parts[1].Type != "image_url" || parts[1].ImageURL == nil {
		t.Fatalf("expected image_url part, got %+v", parts[1])
	}
	expectedURL := "data:image/png;base64,abc123"
	if parts[1].ImageURL.URL != expectedURL {
		t.Errorf("expected URL %q, got %q", expectedURL, parts[1].ImageURL.URL)
	}
}

func TestConvertAssistantMessage_TextOnly(t *testing.T) {
	msgs := convertMessages("", []message.Message{
		message.AssistantMessage{
			Content: []message.ContentBlock{
				message.TextContent{Text: "I can help"},
			},
		},
	})
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Role != "assistant" {
		t.Errorf("expected role 'assistant', got %q", msgs[0].Role)
	}
	content, ok := msgs[0].Content.(string)
	if !ok {
		t.Fatalf("expected string content, got %T", msgs[0].Content)
	}
	if content != "I can help" {
		t.Errorf("expected 'I can help', got %q", content)
	}
	if len(msgs[0].ToolCalls) != 0 {
		t.Errorf("expected no tool calls, got %d", len(msgs[0].ToolCalls))
	}
}

func TestConvertAssistantMessage_WithToolCalls(t *testing.T) {
	msgs := convertMessages("", []message.Message{
		message.AssistantMessage{
			Content: []message.ContentBlock{
				message.TextContent{Text: "Let me read that"},
				message.ToolCall{
					ID:        "call_1",
					Name:      "read",
					Arguments: json.RawMessage(`{"path":"/tmp/foo.go"}`),
				},
			},
		},
	})
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	content, ok := msgs[0].Content.(string)
	if !ok {
		t.Fatalf("expected string content, got %T", msgs[0].Content)
	}
	if content != "Let me read that" {
		t.Errorf("expected text, got %q", content)
	}
	if len(msgs[0].ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(msgs[0].ToolCalls))
	}
	tc := msgs[0].ToolCalls[0]
	if tc.ID != "call_1" || tc.Type != "function" || tc.Function.Name != "read" {
		t.Errorf("unexpected tool call: %+v", tc)
	}
	if tc.Function.Arguments != `{"path":"/tmp/foo.go"}` {
		t.Errorf("unexpected arguments: %q", tc.Function.Arguments)
	}
}

func TestConvertToolResultMessage(t *testing.T) {
	msgs := convertMessages("", []message.Message{
		message.ToolResultMessage{
			ToolCallID: "call_1",
			ToolName:   "read",
			Content: []message.ContentBlock{
				message.TextContent{Text: "file contents here"},
			},
			IsError: false,
		},
	})
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Role != "tool" {
		t.Errorf("expected role 'tool', got %q", msgs[0].Role)
	}
	if msgs[0].ToolCallID != "call_1" {
		t.Errorf("expected tool_call_id 'call_1', got %q", msgs[0].ToolCallID)
	}
	content, ok := msgs[0].Content.(string)
	if !ok {
		t.Fatalf("expected string content, got %T", msgs[0].Content)
	}
	if content != "file contents here" {
		t.Errorf("expected 'file contents here', got %q", content)
	}
}

func TestConvertTools(t *testing.T) {
	tools := convertTools([]provider.ToolSpec{
		{
			Name:        "read",
			Description: "Read a file",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
		},
	})
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].Type != "function" {
		t.Errorf("expected type 'function', got %q", tools[0].Type)
	}
	if tools[0].Function.Name != "read" {
		t.Errorf("expected name 'read', got %q", tools[0].Function.Name)
	}
	if tools[0].Function.Description != "Read a file" {
		t.Errorf("expected description, got %q", tools[0].Function.Description)
	}
}

func TestConvertTools_Empty(t *testing.T) {
	tools := convertTools(nil)
	if tools != nil {
		t.Errorf("expected nil for empty tools, got %v", tools)
	}
}

func TestBuildRequest(t *testing.T) {
	params := provider.StreamParams{
		SystemPrompt: "Be helpful",
		Messages: []message.Message{
			message.UserMessage{
				Content: []message.ContentBlock{message.TextContent{Text: "hi"}},
			},
		},
		Model:     "gpt-4o",
		MaxTokens: 4096,
	}
	req := buildRequest(params)
	if req.Model != "gpt-4o" {
		t.Errorf("expected model 'gpt-4o', got %q", req.Model)
	}
	if !req.Stream {
		t.Error("expected stream=true")
	}
	if len(req.Messages) != 2 {
		t.Fatalf("expected 2 messages (system + user), got %d", len(req.Messages))
	}
	if req.Messages[0].Role != "system" {
		t.Errorf("expected first message role 'system', got %q", req.Messages[0].Role)
	}
}

func TestBuildRequest_JSONRoundTrip(t *testing.T) {
	params := provider.StreamParams{
		SystemPrompt: "Be helpful",
		Messages: []message.Message{
			message.UserMessage{
				Content: []message.ContentBlock{message.TextContent{Text: "hello"}},
			},
		},
		Tools: []provider.ToolSpec{
			{
				Name:        "read",
				Description: "Read a file",
				Parameters:  json.RawMessage(`{"type":"object"}`),
			},
		},
		Model:     "gpt-4o",
		MaxTokens: 4096,
	}
	req := buildRequest(params)
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if decoded["model"] != "gpt-4o" {
		t.Errorf("expected model gpt-4o in JSON")
	}
	if decoded["stream"] != true {
		t.Errorf("expected stream true in JSON")
	}
	msgs, ok := decoded["messages"].([]any)
	if !ok || len(msgs) != 2 {
		t.Errorf("expected 2 messages in JSON, got %v", decoded["messages"])
	}
	tools, ok := decoded["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Errorf("expected 1 tool in JSON, got %v", decoded["tools"])
	}
}

func TestConvertMessages_FullConversation(t *testing.T) {
	msgs := convertMessages("system prompt", []message.Message{
		message.UserMessage{
			Content: []message.ContentBlock{message.TextContent{Text: "read /tmp/foo"}},
		},
		message.AssistantMessage{
			Content: []message.ContentBlock{
				message.ToolCall{
					ID:        "call_1",
					Name:      "read",
					Arguments: json.RawMessage(`{"path":"/tmp/foo"}`),
				},
			},
		},
		message.ToolResultMessage{
			ToolCallID: "call_1",
			ToolName:   "read",
			Content:    []message.ContentBlock{message.TextContent{Text: "contents"}},
		},
		message.AssistantMessage{
			Content: []message.ContentBlock{
				message.TextContent{Text: "Here are the contents"},
			},
		},
	})

	expected := []string{"system", "user", "assistant", "tool", "assistant"}
	if len(msgs) != len(expected) {
		t.Fatalf("expected %d messages, got %d", len(expected), len(msgs))
	}
	for i, want := range expected {
		if msgs[i].Role != want {
			t.Errorf("message %d: expected role %q, got %q", i, want, msgs[i].Role)
		}
	}
}
