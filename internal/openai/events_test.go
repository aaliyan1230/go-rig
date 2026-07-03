package openai

import (
	"encoding/json"
	"testing"

	"github.com/aaliyan1230/go-rig/internal/message"
	"github.com/aaliyan1230/go-rig/internal/provider"
)

func strPtr(s string) *string { return &s }

func TestProcessChunk_TextDelta(t *testing.T) {
	s := newStreamState("gpt-4o", "openai")
	events := s.processChunk(chatCompletionChunk{
		Choices: []choice{{
			Delta: delta{Content: strPtr("Hello")},
		}},
	})
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	td, ok := events[0].(provider.TextDelta)
	if !ok {
		t.Fatalf("expected TextDelta, got %T", events[0])
	}
	if td.Delta != "Hello" {
		t.Errorf("expected 'Hello', got %q", td.Delta)
	}
}

func TestProcessChunk_EmptyContent(t *testing.T) {
	s := newStreamState("gpt-4o", "openai")
	events := s.processChunk(chatCompletionChunk{
		Choices: []choice{{
			Delta: delta{Content: strPtr("")},
		}},
	})
	if len(events) != 0 {
		t.Errorf("expected 0 events for empty content, got %d", len(events))
	}
}

func TestProcessChunk_NilContent(t *testing.T) {
	s := newStreamState("gpt-4o", "openai")
	events := s.processChunk(chatCompletionChunk{
		Choices: []choice{{
			Delta: delta{Role: "assistant"},
		}},
	})
	if len(events) != 0 {
		t.Errorf("expected 0 events for nil content, got %d", len(events))
	}
}

func TestProcessChunk_ToolCallStart(t *testing.T) {
	s := newStreamState("gpt-4o", "openai")
	events := s.processChunk(chatCompletionChunk{
		Choices: []choice{{
			Delta: delta{
				ToolCalls: []toolCallDelta{{
					Index: 0,
					ID:    "call_1",
					Type:  "function",
					Function: &functionDelta{
						Name: "read",
					},
				}},
			},
		}},
	})
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	tcs, ok := events[0].(provider.ToolCallStart)
	if !ok {
		t.Fatalf("expected ToolCallStart, got %T", events[0])
	}
	if tcs.Index != 0 || tcs.ID != "call_1" || tcs.Name != "read" {
		t.Errorf("unexpected ToolCallStart: %+v", tcs)
	}
}

func TestProcessChunk_ToolCallDelta(t *testing.T) {
	s := newStreamState("gpt-4o", "openai")
	// First: start the tool call
	s.processChunk(chatCompletionChunk{
		Choices: []choice{{
			Delta: delta{
				ToolCalls: []toolCallDelta{{
					Index:    0,
					ID:       "call_1",
					Function: &functionDelta{Name: "read"},
				}},
			},
		}},
	})
	// Then: stream arguments
	events := s.processChunk(chatCompletionChunk{
		Choices: []choice{{
			Delta: delta{
				ToolCalls: []toolCallDelta{{
					Index:    0,
					Function: &functionDelta{Arguments: `{"path":`},
				}},
			},
		}},
	})
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	tcd, ok := events[0].(provider.ToolCallDelta)
	if !ok {
		t.Fatalf("expected ToolCallDelta, got %T", events[0])
	}
	if tcd.Index != 0 || tcd.Delta != `{"path":` {
		t.Errorf("unexpected ToolCallDelta: %+v", tcd)
	}
}

func TestProcessChunk_FinishReason_Stop(t *testing.T) {
	s := newStreamState("gpt-4o", "openai")
	s.processChunk(chatCompletionChunk{
		Choices: []choice{{
			Delta:        delta{},
			FinishReason: strPtr("stop"),
		}},
	})
	if s.stopReason != message.StopReasonStop {
		t.Errorf("expected StopReasonStop, got %q", s.stopReason)
	}
}

func TestProcessChunk_FinishReason_Length(t *testing.T) {
	s := newStreamState("gpt-4o", "openai")
	s.processChunk(chatCompletionChunk{
		Choices: []choice{{
			Delta:        delta{},
			FinishReason: strPtr("length"),
		}},
	})
	if s.stopReason != message.StopReasonLength {
		t.Errorf("expected StopReasonLength, got %q", s.stopReason)
	}
}

func TestProcessChunk_FinishReason_ToolCalls(t *testing.T) {
	s := newStreamState("gpt-4o", "openai")
	s.processChunk(chatCompletionChunk{
		Choices: []choice{{
			Delta:        delta{},
			FinishReason: strPtr("tool_calls"),
		}},
	})
	if s.stopReason != message.StopReasonToolUse {
		t.Errorf("expected StopReasonToolUse, got %q", s.stopReason)
	}
}

func TestProcessChunk_Usage(t *testing.T) {
	s := newStreamState("gpt-4o", "openai")
	s.processChunk(chatCompletionChunk{
		Choices: []choice{{Delta: delta{}}},
		Usage: &chunkUsage{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
		},
	})
	if s.usage.Input != 100 {
		t.Errorf("expected input 100, got %d", s.usage.Input)
	}
	if s.usage.Output != 50 {
		t.Errorf("expected output 50, got %d", s.usage.Output)
	}
}

func TestProcessChunk_ModelFromChunk(t *testing.T) {
	s := newStreamState("", "openai")
	s.processChunk(chatCompletionChunk{
		Model:   "gpt-4o-2024-08-06",
		Choices: []choice{{Delta: delta{Content: strPtr("hi")}}},
	})
	if s.model != "gpt-4o-2024-08-06" {
		t.Errorf("expected model from chunk, got %q", s.model)
	}
}

func TestBuildMessage_TextOnly(t *testing.T) {
	s := newStreamState("gpt-4o", "openai")
	s.processChunk(chatCompletionChunk{
		Choices: []choice{{Delta: delta{Content: strPtr("Hello ")}}},
	})
	s.processChunk(chatCompletionChunk{
		Choices: []choice{{Delta: delta{Content: strPtr("world")}}},
	})
	s.processChunk(chatCompletionChunk{
		Choices: []choice{{Delta: delta{}, FinishReason: strPtr("stop")}},
	})

	msg := s.buildMessage()
	if len(msg.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(msg.Content))
	}
	tc, ok := msg.Content[0].(message.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", msg.Content[0])
	}
	if tc.Text != "Hello world" {
		t.Errorf("expected 'Hello world', got %q", tc.Text)
	}
	if msg.StopReason != message.StopReasonStop {
		t.Errorf("expected StopReasonStop, got %q", msg.StopReason)
	}
	if msg.Model != "gpt-4o" {
		t.Errorf("expected model 'gpt-4o', got %q", msg.Model)
	}
}

func TestBuildMessage_WithToolCalls(t *testing.T) {
	s := newStreamState("gpt-4o", "openai")
	s.processChunk(chatCompletionChunk{
		Choices: []choice{{
			Delta: delta{
				ToolCalls: []toolCallDelta{{
					Index: 0,
					ID:    "call_1",
					Function: &functionDelta{
						Name:      "read",
						Arguments: `{"path":`,
					},
				}},
			},
		}},
	})
	s.processChunk(chatCompletionChunk{
		Choices: []choice{{
			Delta: delta{
				ToolCalls: []toolCallDelta{{
					Index:    0,
					Function: &functionDelta{Arguments: `"/tmp"}`},
				}},
			},
		}},
	})
	s.processChunk(chatCompletionChunk{
		Choices: []choice{{
			Delta:        delta{},
			FinishReason: strPtr("tool_calls"),
		}},
	})

	msg := s.buildMessage()
	if len(msg.Content) != 1 {
		t.Fatalf("expected 1 content block (tool call), got %d", len(msg.Content))
	}
	tc, ok := msg.Content[0].(message.ToolCall)
	if !ok {
		t.Fatalf("expected ToolCall, got %T", msg.Content[0])
	}
	if tc.ID != "call_1" || tc.Name != "read" {
		t.Errorf("unexpected tool call: %+v", tc)
	}
	var args map[string]string
	if err := json.Unmarshal(tc.Arguments, &args); err != nil {
		t.Fatalf("failed to parse arguments: %v", err)
	}
	if args["path"] != "/tmp" {
		t.Errorf("expected path '/tmp', got %q", args["path"])
	}
}

func TestBuildMessage_TextAndToolCalls(t *testing.T) {
	s := newStreamState("gpt-4o", "openai")
	s.processChunk(chatCompletionChunk{
		Choices: []choice{{Delta: delta{Content: strPtr("Let me read that.")}}},
	})
	s.processChunk(chatCompletionChunk{
		Choices: []choice{{
			Delta: delta{
				ToolCalls: []toolCallDelta{{
					Index:    0,
					ID:       "call_1",
					Function: &functionDelta{Name: "read", Arguments: `{"path":"/tmp"}`},
				}},
			},
		}},
	})

	msg := s.buildMessage()
	if len(msg.Content) != 2 {
		t.Fatalf("expected 2 content blocks (text + tool call), got %d", len(msg.Content))
	}
	if _, ok := msg.Content[0].(message.TextContent); !ok {
		t.Errorf("expected TextContent first, got %T", msg.Content[0])
	}
	if _, ok := msg.Content[1].(message.ToolCall); !ok {
		t.Errorf("expected ToolCall second, got %T", msg.Content[1])
	}
}

func TestCompleteStreamSequence(t *testing.T) {
	s := newStreamState("gpt-4o", "openai")

	// First chunk: role announcement (no content)
	events := s.processChunk(chatCompletionChunk{
		ID:    "chatcmpl-123",
		Model: "gpt-4o-2024-08-06",
		Choices: []choice{{
			Index: 0,
			Delta: delta{Role: "assistant", Content: strPtr("")},
		}},
	})
	if len(events) != 0 {
		t.Errorf("first chunk: expected 0 events, got %d", len(events))
	}

	// Text deltas
	allEvents := make([]provider.StreamEvent, 0)
	for _, text := range []string{"Hello", ", ", "world", "!"} {
		events = s.processChunk(chatCompletionChunk{
			Choices: []choice{{Delta: delta{Content: strPtr(text)}}},
		})
		allEvents = append(allEvents, events...)
	}
	if len(allEvents) != 4 {
		t.Errorf("expected 4 text delta events, got %d", len(allEvents))
	}

	// Finish
	s.processChunk(chatCompletionChunk{
		Choices: []choice{{
			Delta:        delta{},
			FinishReason: strPtr("stop"),
		}},
	})

	msg := s.buildMessage()
	tc, ok := msg.Content[0].(message.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", msg.Content[0])
	}
	if tc.Text != "Hello, world!" {
		t.Errorf("expected 'Hello, world!', got %q", tc.Text)
	}
	if msg.StopReason != message.StopReasonStop {
		t.Errorf("expected stop, got %q", msg.StopReason)
	}
	if msg.Model != "gpt-4o-2024-08-06" {
		t.Errorf("expected model from first chunk, got %q", msg.Model)
	}
}
