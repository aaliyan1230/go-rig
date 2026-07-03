package openai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/aaliyan1230/go-rig/internal/message"
	"github.com/aaliyan1230/go-rig/internal/provider"
)

const cannedTextResponse = `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}

data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}

data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"content":", world!"},"finish_reason":null}]}

data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]

`

func TestClientStream_SimpleText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("expected Bearer test-key, got %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected application/json, got %q", r.Header.Get("Content-Type"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, cannedTextResponse)
	}))
	defer server.Close()

	client, err := NewClient("test-key", server.URL)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	params := provider.StreamParams{
		SystemPrompt: "Be helpful",
		Messages: []message.Message{
			message.UserMessage{
				Content: []message.ContentBlock{message.TextContent{Text: "hi"}},
			},
		},
		Model:     "gpt-4o",
		MaxTokens: 100,
	}

	ch := client.Stream(context.Background(), params)

	var events []provider.StreamEvent
	for evt := range ch {
		events = append(events, evt)
	}

	if len(events) < 3 {
		t.Fatalf("expected at least 3 events (start + deltas + done), got %d", len(events))
	}

	if _, ok := events[0].(provider.StreamStart); !ok {
		t.Errorf("first event should be StreamStart, got %T", events[0])
	}

	var text string
	for _, evt := range events {
		if td, ok := evt.(provider.TextDelta); ok {
			text += td.Delta
		}
	}
	if text != "Hello, world!" {
		t.Errorf("expected 'Hello, world!', got %q", text)
	}

	last := events[len(events)-1]
	done, ok := last.(provider.StreamDone)
	if !ok {
		t.Fatalf("last event should be StreamDone, got %T", last)
	}
	if done.Message.StopReason != message.StopReasonStop {
		t.Errorf("expected StopReasonStop, got %q", done.Message.StopReason)
	}
	if len(done.Message.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(done.Message.Content))
	}
	tc, ok := done.Message.Content[0].(message.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", done.Message.Content[0])
	}
	if tc.Text != "Hello, world!" {
		t.Errorf("expected 'Hello, world!' in final message, got %q", tc.Text)
	}
}

func TestClientStream_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"Invalid API key"}}`)
	}))
	defer server.Close()

	client, _ := NewClient("bad-key", server.URL)
	ch := client.Stream(context.Background(), provider.StreamParams{
		Messages: []message.Message{
			message.UserMessage{
				Content: []message.ContentBlock{message.TextContent{Text: "hi"}},
			},
		},
		Model: "gpt-4o",
	})

	var events []provider.StreamEvent
	for evt := range ch {
		events = append(events, evt)
	}

	if len(events) != 1 {
		t.Fatalf("expected 1 event (error), got %d", len(events))
	}
	errEvt, ok := events[0].(provider.StreamError)
	if !ok {
		t.Fatalf("expected StreamError, got %T", events[0])
	}
	if errEvt.Message.StopReason != message.StopReasonError {
		t.Errorf("expected StopReasonError, got %q", errEvt.Message.StopReason)
	}
	if errEvt.Err == nil || errEvt.Err.Error() != "Invalid API key" {
		t.Errorf("expected 'Invalid API key' error, got %v", errEvt.Err)
	}
}

func TestClientStream_ContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"id\":\"1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"},\"finish_reason\":null}]}\n\n")
		flusher.Flush()
		// Block until the request context is done
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	client, _ := NewClient("test-key", server.URL)
	ch := client.Stream(ctx, provider.StreamParams{
		Messages: []message.Message{
			message.UserMessage{
				Content: []message.ContentBlock{message.TextContent{Text: "hi"}},
			},
		},
		Model: "gpt-4o",
	})

	// Read first event (StreamStart)
	<-ch
	// Cancel
	cancel()

	// Drain remaining events
	var lastEvent provider.StreamEvent
	for evt := range ch {
		lastEvent = evt
	}

	if lastEvent == nil {
		return // acceptable: channel closed immediately on cancel
	}
	switch e := lastEvent.(type) {
	case provider.StreamError:
		// Expected: aborted or error
		_ = e
	case provider.StreamDone:
		// Also acceptable if the stream finished before cancellation took effect
	default:
		// TextDelta is fine too if it arrived before cancel
	}
}

const cannedToolCallResponse = `data: {"id":"chatcmpl-2","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}

data: {"id":"chatcmpl-2","choices":[{"index":0,"delta":{"content":"Let me read that."},"finish_reason":null}]}

data: {"id":"chatcmpl-2","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_abc","type":"function","function":{"name":"read","arguments":""}}]},"finish_reason":null}]}

data: {"id":"chatcmpl-2","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]},"finish_reason":null}]}

data: {"id":"chatcmpl-2","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"/tmp/foo\"}"}}]},"finish_reason":null}]}

data: {"id":"chatcmpl-2","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: [DONE]

`

func TestClientStream_ToolCallResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, cannedToolCallResponse)
	}))
	defer server.Close()

	client, _ := NewClient("test-key", server.URL)
	ch := client.Stream(context.Background(), provider.StreamParams{
		Messages: []message.Message{
			message.UserMessage{
				Content: []message.ContentBlock{message.TextContent{Text: "read /tmp/foo"}},
			},
		},
		Model: "gpt-4o",
	})

	var (
		hasStart     bool
		hasTextDelta bool
		hasToolStart bool
		hasToolDelta bool
		hasDone      bool
	)

	for evt := range ch {
		switch e := evt.(type) {
		case provider.StreamStart:
			hasStart = true
		case provider.TextDelta:
			hasTextDelta = true
			_ = e
		case provider.ToolCallStart:
			hasToolStart = true
			if e.Name != "read" || e.ID != "call_abc" {
				t.Errorf("unexpected ToolCallStart: %+v", e)
			}
		case provider.ToolCallDelta:
			hasToolDelta = true
		case provider.StreamDone:
			hasDone = true
			if e.Message.StopReason != message.StopReasonToolUse {
				t.Errorf("expected StopReasonToolUse, got %q", e.Message.StopReason)
			}
			if len(e.Message.Content) != 2 {
				t.Fatalf("expected 2 content blocks (text + tool), got %d", len(e.Message.Content))
			}
		case provider.StreamError:
			t.Fatalf("unexpected error: %v", e.Err)
		}
	}

	if !hasStart {
		t.Error("missing StreamStart")
	}
	if !hasTextDelta {
		t.Error("missing TextDelta")
	}
	if !hasToolStart {
		t.Error("missing ToolCallStart")
	}
	if !hasToolDelta {
		t.Error("missing ToolCallDelta")
	}
	if !hasDone {
		t.Error("missing StreamDone")
	}
}

func TestNewClient_EmptyKey(t *testing.T) {
	_, err := NewClient("", "")
	if err == nil {
		t.Error("expected error for empty API key")
	}
}

func TestNewClient_DefaultBaseURL(t *testing.T) {
	c, err := NewClient("key", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.baseURL != defaultBaseURL {
		t.Errorf("expected %q, got %q", defaultBaseURL, c.baseURL)
	}
}

func TestNewClient_TrailingSlash(t *testing.T) {
	c, err := NewClient("key", "https://example.com/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.baseURL != "https://example.com" {
		t.Errorf("expected trimmed URL, got %q", c.baseURL)
	}
}

func TestResolveAPIKey_Explicit(t *testing.T) {
	key, err := ResolveAPIKey("explicit-key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "explicit-key" {
		t.Errorf("expected 'explicit-key', got %q", key)
	}
}

func TestResolveAPIKey_EnvVar(t *testing.T) {
	os.Setenv("OPENAI_API_KEY", "env-key")
	defer os.Unsetenv("OPENAI_API_KEY")

	key, err := ResolveAPIKey("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "env-key" {
		t.Errorf("expected 'env-key', got %q", key)
	}
}

func TestResolveAPIKey_Missing(t *testing.T) {
	os.Unsetenv("OPENAI_API_KEY")
	_, err := ResolveAPIKey("")
	if err == nil {
		t.Error("expected error for missing key")
	}
}

func TestClientInterfaceCompliance(t *testing.T) {
	var _ provider.ModelClient = (*Client)(nil)
}
