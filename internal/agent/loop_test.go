package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aaliyan1230/go-rig/internal/message"
	"github.com/aaliyan1230/go-rig/internal/provider"
)

type scriptedClient struct {
	mu        sync.Mutex
	responses []message.AssistantMessage
	requests  []provider.StreamParams
}

func (c *scriptedClient) Stream(ctx context.Context, params provider.StreamParams) <-chan provider.StreamEvent {
	ch := make(chan provider.StreamEvent, 1)
	c.mu.Lock()
	c.requests = append(c.requests, params)
	var response message.AssistantMessage
	if len(c.responses) > 0 {
		response = c.responses[0]
		c.responses = c.responses[1:]
	}
	c.mu.Unlock()

	go func() {
		defer close(ch)
		sendScriptEvent(ch, ctx, provider.StreamStart{})
		for index, block := range response.Content {
			switch value := block.(type) {
			case message.TextContent:
				sendScriptEvent(ch, ctx, provider.TextDelta{Delta: value.Text})
			case message.ToolCall:
				sendScriptEvent(ch, ctx, provider.ToolCallStart{Index: index, ID: value.ID, Name: value.Name})
				sendScriptEvent(ch, ctx, provider.ToolCallDelta{Index: index, Delta: string(value.Arguments)})
			}
		}
		sendScriptEvent(ch, ctx, provider.StreamDone{Message: response})
	}()
	return ch
}

func sendScriptEvent(ch chan<- provider.StreamEvent, ctx context.Context, event provider.StreamEvent) {
	select {
	case ch <- event:
	case <-ctx.Done():
	}
}

type echoTool struct {
	values []string
}

func (t *echoTool) ToolName() string        { return "echo" }
func (t *echoTool) ToolDescription() string { return "Echo a string." }
func (t *echoTool) ToolSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}},"required":["value"]}`)
}
func (t *echoTool) Execute(_ context.Context, _ string, params json.RawMessage) (ToolResult, error) {
	var input struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(params, &input); err != nil {
		return ToolResult{}, err
	}
	t.values = append(t.values, input.Value)
	return ToolResult{Content: []message.ContentBlock{message.TextContent{Text: "echoed: " + input.Value}}}, nil
}

func TestRunCompletesOneSequentialToolRoundTrip(t *testing.T) {
	client := &scriptedClient{responses: []message.AssistantMessage{
		assistantWithToolCall("call-1", "echo", `{"value":"hello"}`),
		assistantWithText("done"),
	}}
	tool := &echoTool{}

	events := collectEvents(Run(
		context.Background(),
		client,
		[]message.AgentMessage{userPrompt("please echo hello")},
		AgentContext{SystemPrompt: "test", Tools: []Tool{tool}},
		RunConfig{Model: "faux", MaxTurns: 2},
	))

	if len(client.requests) != 2 {
		t.Fatalf("expected two provider requests, got %d", len(client.requests))
	}
	if len(client.requests[0].Tools) != 1 || client.requests[0].Tools[0].Name != "echo" {
		t.Fatalf("first request did not include echo tool: %#v", client.requests[0].Tools)
	}
	if len(client.requests[1].Messages) != 3 {
		t.Fatalf("second request should contain user, assistant, and tool result; got %d", len(client.requests[1].Messages))
	}
	if _, ok := client.requests[1].Messages[2].(message.ToolResultMessage); !ok {
		t.Fatalf("third second-request message should be a tool result, got %T", client.requests[1].Messages[2])
	}
	if len(tool.values) != 1 || tool.values[0] != "hello" {
		t.Fatalf("tool did not execute expected arguments: %#v", tool.values)
	}

	end := agentEnd(t, events)
	if end.Err != nil {
		t.Fatalf("unexpected terminal error: %v", end.Err)
	}
	if len(end.Messages) != 4 {
		t.Fatalf("expected prompt, two assistant messages, and result; got %d", len(end.Messages))
	}
	assertEventTypes(t, events, []string{
		"AgentStart", "TurnStart", "MessageStart", "MessageEnd", "MessageStart", "MessageUpdate", "MessageUpdate", "MessageEnd",
		"ToolExecStart", "ToolExecEnd", "MessageStart", "MessageEnd", "TurnEnd",
		"TurnStart", "MessageStart", "MessageUpdate", "MessageEnd", "TurnEnd", "AgentEnd",
	})
}

func TestRunMakesUnknownToolFailureVisibleToModel(t *testing.T) {
	client := &scriptedClient{responses: []message.AssistantMessage{
		assistantWithToolCall("call-1", "missing", `{"path":"x"}`),
		assistantWithText("I could not read that file."),
	}}

	events := collectEvents(Run(
		context.Background(),
		client,
		[]message.AgentMessage{userPrompt("read x")},
		AgentContext{},
		RunConfig{Model: "faux", MaxTurns: 2},
	))

	result, ok := client.requests[1].Messages[2].(message.ToolResultMessage)
	if !ok || !result.IsError {
		t.Fatalf("expected model-visible failed tool result, got %#v", client.requests[1].Messages[2])
	}
	if agentEnd(t, events).Err != nil {
		t.Fatalf("unknown tool should not terminate the agent: %v", agentEnd(t, events).Err)
	}
}

func TestRunStopsAtMaximumTurnsAfterToolBatch(t *testing.T) {
	client := &scriptedClient{responses: []message.AssistantMessage{
		assistantWithToolCall("call-1", "echo", `{"value":"hello"}`),
	}}

	events := collectEvents(Run(
		context.Background(),
		client,
		[]message.AgentMessage{userPrompt("echo hello")},
		AgentContext{Tools: []Tool{&echoTool{}}},
		RunConfig{Model: "faux", MaxTurns: 1},
	))

	if len(client.requests) != 1 {
		t.Fatalf("expected one provider request, got %d", len(client.requests))
	}
	if end := agentEnd(t, events); end.Err != nil || end.Reason != EndTurnLimit || end.Turns != 1 {
		t.Fatalf("expected paused outcome, got %+v", end)
	}
}

func TestContinueEligibility(t *testing.T) {
	assistant := assistantWithToolCall("call", "echo", `{"value":"hi"}`)
	result := message.ToolResultMessage{ToolCallID: "call", ToolName: "echo"}
	for _, tc := range []struct {
		name     string
		messages []message.AgentMessage
		want     bool
	}{
		{"empty", nil, false},
		{"user", []message.AgentMessage{userPrompt("hi")}, true},
		{"completed", []message.AgentMessage{assistantWithText("done")}, false},
		{"unexecuted", []message.AgentMessage{assistant}, false},
		{"batch", []message.AgentMessage{assistant, result}, true},
		{"orphan", []message.AgentMessage{result}, false},
		{"wrong call", []message.AgentMessage{assistant, message.ToolResultMessage{ToolCallID: "other"}}, false},
		{"duplicate result", []message.AgentMessage{assistant, result, result}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := CanContinue(tc.messages); got != tc.want {
				t.Fatalf("got %t want %t", got, tc.want)
			}
		})
	}
}

func TestTurnRenewalCancellationDoesNotRequestAgain(t *testing.T) {
	client := &scriptedClient{responses: []message.AssistantMessage{assistantWithToolCall("call", "echo", `{"value":"hi"}`), assistantWithText("must not run")}}
	events := collectEvents(Run(context.Background(), client, []message.AgentMessage{userPrompt("work")}, AgentContext{Tools: []Tool{&echoTool{}}}, RunConfig{Model: "test", MaxTurns: 1, OnTurnLimit: func(context.Context, TurnLimitInfo) (bool, error) { return false, context.Canceled }}))
	end := agentEnd(t, events)
	if end.Reason != EndCanceled || !errors.Is(end.Err, context.Canceled) || end.Turns != 1 || len(client.requests) != 1 {
		t.Fatal(end, len(client.requests))
	}
}

func TestRunExecutesMultipleToolCallsSequentially(t *testing.T) {
	client := &scriptedClient{responses: []message.AssistantMessage{
		{
			Content: []message.ContentBlock{
				message.ToolCall{ID: "call-1", Name: "echo", Arguments: json.RawMessage(`{"value":"first"}`)},
				message.ToolCall{ID: "call-2", Name: "echo", Arguments: json.RawMessage(`{"value":"second"}`)},
			},
			Model: "faux", Provider: "faux", StopReason: message.StopReasonToolUse, Timestamp: time.Now().UnixMilli(),
		},
		assistantWithText("done"),
	}}
	tool := &echoTool{}

	events := collectEvents(Run(
		context.Background(), client, []message.AgentMessage{userPrompt("echo twice")},
		AgentContext{Tools: []Tool{tool}}, RunConfig{Model: "faux", MaxTurns: 2},
	))

	if got, want := len(tool.values), 2; got != want || tool.values[0] != "first" || tool.values[1] != "second" {
		t.Fatalf("tools did not run in source order: %#v", tool.values)
	}
	if agentEnd(t, events).Err != nil {
		t.Fatalf("unexpected terminal error: %v", agentEnd(t, events).Err)
	}
}

func TestRunCancelsDuringToolExecution(t *testing.T) {
	client := &scriptedClient{responses: []message.AssistantMessage{
		assistantWithToolCall("call-1", "wait", `{}`),
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tool := &blockingTool{}

	events := make([]AgentEvent, 0)
	for event := range Run(ctx, client, []message.AgentMessage{userPrompt("wait")}, AgentContext{Tools: []Tool{tool}}, RunConfig{Model: "faux", MaxTurns: 2}) {
		events = append(events, event)
		if _, ok := event.(ToolExecStart); ok {
			cancel()
		}
	}

	if !errors.Is(agentEnd(t, events).Err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", agentEnd(t, events).Err)
	}
	if len(client.requests) != 1 {
		t.Fatalf("cancellation should prevent a second request, got %d", len(client.requests))
	}
}

func TestRunStopsOnProviderFailure(t *testing.T) {
	want := errors.New("provider unavailable")
	events := collectEvents(Run(
		context.Background(), streamErrorClient{err: want}, []message.AgentMessage{userPrompt("hello")},
		AgentContext{}, RunConfig{Model: "faux", MaxTurns: 1},
	))
	if !errors.Is(agentEnd(t, events).Err, want) {
		t.Fatalf("expected provider error, got %v", agentEnd(t, events).Err)
	}
}

func TestRunReportsUnexpectedStreamClosure(t *testing.T) {
	events := collectEvents(Run(
		context.Background(), closedClient{}, []message.AgentMessage{userPrompt("hello")},
		AgentContext{}, RunConfig{Model: "faux", MaxTurns: 1},
	))
	if got := agentEnd(t, events).Err; got == nil || got.Error() != "model stream closed without a terminal event" {
		t.Fatalf("unexpected terminal error: %v", got)
	}
}

type blockingTool struct{}

func (*blockingTool) ToolName() string            { return "wait" }
func (*blockingTool) ToolDescription() string     { return "Wait for cancellation." }
func (*blockingTool) ToolSchema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (*blockingTool) Execute(ctx context.Context, _ string, _ json.RawMessage) (ToolResult, error) {
	<-ctx.Done()
	return ToolResult{}, ctx.Err()
}

type streamErrorClient struct {
	err error
}

func (c streamErrorClient) Stream(ctx context.Context, _ provider.StreamParams) <-chan provider.StreamEvent {
	ch := make(chan provider.StreamEvent, 1)
	go func() {
		defer close(ch)
		sendScriptEvent(ch, ctx, provider.StreamError{Err: c.err, Message: message.AssistantMessage{
			StopReason:   message.StopReasonError,
			ErrorMessage: c.err.Error(),
			Timestamp:    time.Now().UnixMilli(),
		}})
	}()
	return ch
}

type closedClient struct{}

func (closedClient) Stream(_ context.Context, _ provider.StreamParams) <-chan provider.StreamEvent {
	ch := make(chan provider.StreamEvent)
	close(ch)
	return ch
}

func collectEvents(ch <-chan AgentEvent) []AgentEvent {
	events := make([]AgentEvent, 0)
	for event := range ch {
		events = append(events, event)
	}
	return events
}

func agentEnd(t *testing.T, events []AgentEvent) AgentEnd {
	t.Helper()
	if len(events) == 0 {
		t.Fatal("expected events")
	}
	end, ok := events[len(events)-1].(AgentEnd)
	if !ok {
		t.Fatalf("expected final AgentEnd, got %T", events[len(events)-1])
	}
	return end
}

func assertEventTypes(t *testing.T, events []AgentEvent, want []string) {
	t.Helper()
	got := make([]string, len(events))
	for i, event := range events {
		got[i] = eventName(event)
	}
	if len(got) != len(want) {
		t.Fatalf("event count: got %d (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event %d: got %s, want %s; all events: %v", i, got[i], want[i], got)
		}
	}
}

func eventName(event AgentEvent) string {
	switch event.(type) {
	case AgentStart:
		return "AgentStart"
	case AgentEnd:
		return "AgentEnd"
	case TurnStart:
		return "TurnStart"
	case TurnEnd:
		return "TurnEnd"
	case MessageStart:
		return "MessageStart"
	case MessageUpdate:
		return "MessageUpdate"
	case MessageEnd:
		return "MessageEnd"
	case ToolExecStart:
		return "ToolExecStart"
	case ToolExecEnd:
		return "ToolExecEnd"
	default:
		return "unknown"
	}
}

func userPrompt(text string) message.UserMessage {
	return message.UserMessage{
		Content:   []message.ContentBlock{message.TextContent{Text: text}},
		Timestamp: time.Now().UnixMilli(),
	}
}

func assistantWithText(text string) message.AssistantMessage {
	return message.AssistantMessage{
		Content:    []message.ContentBlock{message.TextContent{Text: text}},
		Model:      "faux",
		Provider:   "faux",
		StopReason: message.StopReasonStop,
		Timestamp:  time.Now().UnixMilli(),
	}
}

func assistantWithToolCall(id, name, args string) message.AssistantMessage {
	return message.AssistantMessage{
		Content: []message.ContentBlock{message.ToolCall{
			ID:        id,
			Name:      name,
			Arguments: json.RawMessage(args),
		}},
		Model:      "faux",
		Provider:   "faux",
		StopReason: message.StopReasonToolUse,
		Timestamp:  time.Now().UnixMilli(),
	}
}
