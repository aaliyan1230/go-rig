package faux

import (
	"context"
	"testing"

	"github.com/aaliyan1230/go-rig/internal/message"
	"github.com/aaliyan1230/go-rig/internal/provider"
)

func TestSingleTextResponse(t *testing.T) {
	client := NewClient(TextMessage("Hello, world!"))
	ch := client.Stream(context.Background(), provider.StreamParams{Model: "faux-1"})

	var text string
	var gotStart, gotDone bool
	for evt := range ch {
		switch e := evt.(type) {
		case provider.StreamStart:
			gotStart = true
		case provider.TextDelta:
			text += e.Delta
		case provider.StreamDone:
			gotDone = true
			if e.Message.StopReason != message.StopReasonStop {
				t.Errorf("expected StopReasonStop, got %q", e.Message.StopReason)
			}
		case provider.StreamError:
			t.Fatalf("unexpected error: %v", e.Err)
		}
	}
	if !gotStart {
		t.Error("missing StreamStart")
	}
	if !gotDone {
		t.Error("missing StreamDone")
	}
	if text != "Hello, world!" {
		t.Errorf("expected 'Hello, world!', got %q", text)
	}
}

func TestEmptyQueue(t *testing.T) {
	client := NewClient()
	ch := client.Stream(context.Background(), provider.StreamParams{Model: "faux-1"})

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
}

func TestMultipleResponses(t *testing.T) {
	client := NewClient(
		TextMessage("first"),
		TextMessage("second"),
		TextMessage("third"),
	)

	for i, want := range []string{"first", "second", "third"} {
		ch := client.Stream(context.Background(), provider.StreamParams{Model: "faux-1"})
		var text string
		for evt := range ch {
			if td, ok := evt.(provider.TextDelta); ok {
				text += td.Delta
			}
		}
		if text != want {
			t.Errorf("call %d: expected %q, got %q", i, want, text)
		}
	}

	if client.CallCount != 3 {
		t.Errorf("expected 3 calls, got %d", client.CallCount)
	}
}

func TestContextCancellation(t *testing.T) {
	client := NewClient(TextMessage("this should be interrupted"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	ch := client.Stream(ctx, provider.StreamParams{Model: "faux-1"})
	var events []provider.StreamEvent
	for evt := range ch {
		events = append(events, evt)
	}
	// Should get very few events due to immediate cancellation
	if len(events) > 3 {
		t.Errorf("expected few events after cancel, got %d", len(events))
	}
}

func TestToolCallResponse(t *testing.T) {
	client := NewClient(ToolCallMessage("read", "call_1", map[string]any{"path": "/tmp"}))
	ch := client.Stream(context.Background(), provider.StreamParams{Model: "faux-1"})

	var gotToolStart, gotToolDelta bool
	for evt := range ch {
		switch e := evt.(type) {
		case provider.ToolCallStart:
			gotToolStart = true
			if e.Name != "read" || e.ID != "call_1" {
				t.Errorf("unexpected ToolCallStart: %+v", e)
			}
		case provider.ToolCallDelta:
			gotToolDelta = true
		case provider.StreamDone:
			if e.Message.StopReason != message.StopReasonToolUse {
				t.Errorf("expected StopReasonToolUse, got %q", e.Message.StopReason)
			}
		case provider.StreamError:
			t.Fatalf("unexpected error: %v", e.Err)
		}
	}
	if !gotToolStart {
		t.Error("missing ToolCallStart")
	}
	if !gotToolDelta {
		t.Error("missing ToolCallDelta")
	}
}

func TestInterfaceCompliance(t *testing.T) {
	var _ provider.ModelClient = (*Client)(nil)
}
