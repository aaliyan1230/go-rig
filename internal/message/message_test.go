package message

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestContentBlockRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		block ContentBlock
	}{
		{
			name:  "text",
			block: TextContent{Text: "hello world"},
		},
		{
			name:  "image",
			block: ImageContent{Data: "aGVsbG8=", MimeType: "image/png"},
		},
		{
			name:  "thinking",
			block: ThinkingContent{Thinking: "let me consider...", Signature: "sig123"},
		},
		{
			name:  "thinking_redacted",
			block: ThinkingContent{Thinking: "", Signature: "opaque", Redacted: true},
		},
		{
			name:  "toolCall",
			block: ToolCall{ID: "call_1", Name: "read", Arguments: json.RawMessage(`{"path":"/tmp/foo"}`)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := MarshalContentBlock(tt.block)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}

			got, err := UnmarshalContentBlock(data)
			if err != nil {
				t.Fatalf("unmarshal: %v", err)
			}

			if !reflect.DeepEqual(got, tt.block) {
				t.Errorf("round trip mismatch:\n  got:  %+v\n  want: %+v", got, tt.block)
			}
		})
	}
}

func TestContentBlockTypeField(t *testing.T) {
	data, err := MarshalContentBlock(TextContent{Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}

	if raw["type"] != "text" {
		t.Errorf("expected type=text, got %v", raw["type"])
	}
}

func TestUserMessageRoundTrip(t *testing.T) {
	msg := UserMessage{
		Content:   []ContentBlock{TextContent{Text: "what is this file?"}},
		Timestamp: 1700000000000,
	}

	data, err := MarshalMessage(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	got, err := UnmarshalMessage(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	gotUser, ok := got.(UserMessage)
	if !ok {
		t.Fatalf("expected UserMessage, got %T", got)
	}
	if !reflect.DeepEqual(gotUser, msg) {
		t.Errorf("round trip mismatch:\n  got:  %+v\n  want: %+v", gotUser, msg)
	}
}

func TestAssistantMessageRoundTrip(t *testing.T) {
	msg := AssistantMessage{
		Content: []ContentBlock{
			ThinkingContent{Thinking: "analyzing..."},
			TextContent{Text: "here is the answer"},
			ToolCall{ID: "call_1", Name: "read", Arguments: json.RawMessage(`{"path":"main.go"}`)},
		},
		Model:    "claude-sonnet-4-6",
		Provider: "anthropic",
		Usage: Usage{
			Input:  100,
			Output: 50,
			Cost:   Cost{Input: 0.001, Output: 0.002, Total: 0.003},
		},
		StopReason: StopReasonToolUse,
		Timestamp:  1700000001000,
	}

	data, err := MarshalMessage(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	got, err := UnmarshalMessage(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	gotAssistant, ok := got.(AssistantMessage)
	if !ok {
		t.Fatalf("expected AssistantMessage, got %T", got)
	}
	if !reflect.DeepEqual(gotAssistant, msg) {
		t.Errorf("round trip mismatch:\n  got:  %+v\n  want: %+v", gotAssistant, msg)
	}
}

func TestToolResultMessageRoundTrip(t *testing.T) {
	msg := ToolResultMessage{
		ToolCallID: "call_1",
		ToolName:   "read",
		Content:    []ContentBlock{TextContent{Text: "file contents here"}},
		IsError:    false,
		Timestamp:  1700000002000,
	}

	data, err := MarshalMessage(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	got, err := UnmarshalMessage(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	gotResult, ok := got.(ToolResultMessage)
	if !ok {
		t.Fatalf("expected ToolResultMessage, got %T", got)
	}
	if !reflect.DeepEqual(gotResult, msg) {
		t.Errorf("round trip mismatch:\n  got:  %+v\n  want: %+v", gotResult, msg)
	}
}

func TestToolResultMessageError(t *testing.T) {
	msg := ToolResultMessage{
		ToolCallID: "call_2",
		ToolName:   "bash",
		Content:    []ContentBlock{TextContent{Text: "command not found"}},
		IsError:    true,
		Timestamp:  1700000003000,
	}

	data, err := MarshalMessage(msg)
	if err != nil {
		t.Fatal(err)
	}

	got, err := UnmarshalMessage(data)
	if err != nil {
		t.Fatal(err)
	}

	gotResult := got.(ToolResultMessage)
	if !gotResult.IsError {
		t.Error("expected IsError=true")
	}
}

func TestMessageRoleField(t *testing.T) {
	data, err := MarshalMessage(UserMessage{
		Content:   []ContentBlock{TextContent{Text: "hi"}},
		Timestamp: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}

	if raw["role"] != "user" {
		t.Errorf("expected role=user, got %v", raw["role"])
	}
}

func TestConvertToLLM(t *testing.T) {
	msgs := []AgentMessage{
		UserMessage{Content: []ContentBlock{TextContent{Text: "hello"}}, Timestamp: 1},
		AssistantMessage{
			Content:    []ContentBlock{TextContent{Text: "hi"}},
			Model:      "test",
			Provider:   "test",
			StopReason: StopReasonStop,
			Timestamp:  2,
		},
		ToolResultMessage{
			ToolCallID: "c1",
			ToolName:   "read",
			Content:    []ContentBlock{TextContent{Text: "data"}},
			Timestamp:  3,
		},
	}

	got := ConvertToLLM(msgs)
	if len(got) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(got))
	}

	if _, ok := got[0].(UserMessage); !ok {
		t.Errorf("got[0]: expected UserMessage, got %T", got[0])
	}
	if _, ok := got[1].(AssistantMessage); !ok {
		t.Errorf("got[1]: expected AssistantMessage, got %T", got[1])
	}
	if _, ok := got[2].(ToolResultMessage); !ok {
		t.Errorf("got[2]: expected ToolResultMessage, got %T", got[2])
	}
}

func TestUnmarshalUnknownRole(t *testing.T) {
	data := []byte(`{"role":"unknown","timestamp":1}`)
	_, err := UnmarshalMessage(data)
	if err == nil {
		t.Fatal("expected error for unknown role")
	}
}

func TestUnmarshalUnknownContentType(t *testing.T) {
	data := []byte(`{"type":"unknown","foo":"bar"}`)
	_, err := UnmarshalContentBlock(data)
	if err == nil {
		t.Fatal("expected error for unknown content type")
	}
}

func TestToolCallArgumentsPreserved(t *testing.T) {
	original := `{"path":"/tmp/foo","line":42,"enabled":true}`
	tc := ToolCall{ID: "c1", Name: "read", Arguments: json.RawMessage(original)}

	data, err := MarshalContentBlock(tc)
	if err != nil {
		t.Fatal(err)
	}

	got, err := UnmarshalContentBlock(data)
	if err != nil {
		t.Fatal(err)
	}

	gotTC := got.(ToolCall)
	var originalMap, gotMap map[string]any
	json.Unmarshal([]byte(original), &originalMap)
	json.Unmarshal(gotTC.Arguments, &gotMap)

	if !reflect.DeepEqual(originalMap, gotMap) {
		t.Errorf("arguments not preserved:\n  got:  %s\n  want: %s", gotTC.Arguments, original)
	}
}

func TestUserMessageWithImage(t *testing.T) {
	msg := UserMessage{
		Content: []ContentBlock{
			TextContent{Text: "what's in this image?"},
			ImageContent{Data: "iVBORw0KGgo=", MimeType: "image/png"},
		},
		Timestamp: 1700000000000,
	}

	data, err := MarshalMessage(msg)
	if err != nil {
		t.Fatal(err)
	}

	got, err := UnmarshalMessage(data)
	if err != nil {
		t.Fatal(err)
	}

	gotUser := got.(UserMessage)
	if len(gotUser.Content) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(gotUser.Content))
	}
	if _, ok := gotUser.Content[0].(TextContent); !ok {
		t.Errorf("content[0]: expected TextContent, got %T", gotUser.Content[0])
	}
	if _, ok := gotUser.Content[1].(ImageContent); !ok {
		t.Errorf("content[1]: expected ImageContent, got %T", gotUser.Content[1])
	}
}
