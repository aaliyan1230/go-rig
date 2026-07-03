package sse

import (
	"context"
	"strings"
	"testing"
	"time"
)

func collect(ctx context.Context, input string) []Event {
	ch := Decode(ctx, strings.NewReader(input))
	var events []Event
	for evt := range ch {
		events = append(events, evt)
	}
	return events
}

func TestSimpleEvent(t *testing.T) {
	events := collect(context.Background(), "event: greeting\ndata: hello\n\n")
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != "greeting" {
		t.Errorf("expected type 'greeting', got %q", events[0].Type)
	}
	if events[0].Data != "hello" {
		t.Errorf("expected data 'hello', got %q", events[0].Data)
	}
}

func TestNoEventType(t *testing.T) {
	events := collect(context.Background(), "data: hello\n\n")
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != "" {
		t.Errorf("expected empty type, got %q", events[0].Type)
	}
	if events[0].Data != "hello" {
		t.Errorf("expected data 'hello', got %q", events[0].Data)
	}
}

func TestMultiLineData(t *testing.T) {
	events := collect(context.Background(), "data: line1\ndata: line2\ndata: line3\n\n")
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Data != "line1\nline2\nline3" {
		t.Errorf("expected multi-line data, got %q", events[0].Data)
	}
}

func TestCommentsSkipped(t *testing.T) {
	events := collect(context.Background(), ": this is a comment\ndata: hello\n\n")
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Data != "hello" {
		t.Errorf("expected 'hello', got %q", events[0].Data)
	}
}

func TestMultipleEvents(t *testing.T) {
	input := "data: first\n\ndata: second\n\ndata: third\n\n"
	events := collect(context.Background(), input)
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}
	for i, want := range []string{"first", "second", "third"} {
		if events[i].Data != want {
			t.Errorf("event %d: expected %q, got %q", i, want, events[i].Data)
		}
	}
}

func TestEOFFlush(t *testing.T) {
	events := collect(context.Background(), "data: no trailing newline")
	if len(events) != 1 {
		t.Fatalf("expected 1 event on EOF flush, got %d", len(events))
	}
	if events[0].Data != "no trailing newline" {
		t.Errorf("expected 'no trailing newline', got %q", events[0].Data)
	}
}

func TestEmptyData(t *testing.T) {
	events := collect(context.Background(), "data:\n\n")
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Data != "" {
		t.Errorf("expected empty data, got %q", events[0].Data)
	}
}

func TestCRLFLineEndings(t *testing.T) {
	events := collect(context.Background(), "event: test\r\ndata: crlf\r\n\r\n")
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != "test" {
		t.Errorf("expected type 'test', got %q", events[0].Type)
	}
	if events[0].Data != "crlf" {
		t.Errorf("expected 'crlf', got %q", events[0].Data)
	}
}

func TestBareCRLineEndings(t *testing.T) {
	events := collect(context.Background(), "data: bare cr\r\r")
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Data != "bare cr" {
		t.Errorf("expected 'bare cr', got %q", events[0].Data)
	}
}

func TestContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ch := Decode(ctx, strings.NewReader("data: should not arrive\n\n"))
	var events []Event
	for evt := range ch {
		events = append(events, evt)
	}
	if len(events) > 1 {
		t.Errorf("expected at most 1 event after cancel, got %d", len(events))
	}
}

func TestDataWithColonInValue(t *testing.T) {
	events := collect(context.Background(), "data: key: value\n\n")
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Data != "key: value" {
		t.Errorf("expected 'key: value', got %q", events[0].Data)
	}
}

func TestNoSpaceAfterColon(t *testing.T) {
	events := collect(context.Background(), "data:nospace\n\n")
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Data != "nospace" {
		t.Errorf("expected 'nospace', got %q", events[0].Data)
	}
}

func TestEventTypeWithData(t *testing.T) {
	input := "event: message_start\ndata: {\"type\":\"message_start\"}\n\nevent: content_block_delta\ndata: {\"delta\":\"hi\"}\n\n"
	events := collect(context.Background(), input)
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Type != "message_start" {
		t.Errorf("event 0 type: expected 'message_start', got %q", events[0].Type)
	}
	if events[1].Type != "content_block_delta" {
		t.Errorf("event 1 type: expected 'content_block_delta', got %q", events[1].Type)
	}
}

func TestLargePayload(t *testing.T) {
	big := strings.Repeat("x", 500_000)
	events := collect(context.Background(), "data: "+big+"\n\n")
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if len(events[0].Data) != 500_000 {
		t.Errorf("expected 500000 bytes, got %d", len(events[0].Data))
	}
}

func TestOnlyComments(t *testing.T) {
	events := collect(context.Background(), ": comment1\n: comment2\n\n")
	if len(events) != 0 {
		t.Errorf("expected 0 events for comment-only input, got %d", len(events))
	}
}

func TestChannelCloses(t *testing.T) {
	ch := Decode(context.Background(), strings.NewReader("data: done\n\n"))
	done := make(chan struct{})
	go func() {
		for range ch {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("channel did not close within timeout")
	}
}
