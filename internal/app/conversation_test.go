package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aaliyan1230/go-rig/internal/agent"
	"github.com/aaliyan1230/go-rig/internal/env"
	"github.com/aaliyan1230/go-rig/internal/message"
	"github.com/aaliyan1230/go-rig/internal/provider"
	"github.com/aaliyan1230/go-rig/internal/provider/faux"
	"github.com/aaliyan1230/go-rig/internal/session"
	"github.com/aaliyan1230/go-rig/internal/tools"
)

type recordingClient struct {
	client   provider.ModelClient
	requests []provider.StreamParams
}

func (c *recordingClient) Stream(ctx context.Context, p provider.StreamParams) <-chan provider.StreamEvent {
	c.requests = append(c.requests, p)
	return c.client.Stream(ctx, p)
}

type streamFunc func(context.Context, provider.StreamParams) <-chan provider.StreamEvent

func (f streamFunc) Stream(ctx context.Context, p provider.StreamParams) <-chan provider.StreamEvent {
	return f(ctx, p)
}

func options(client provider.ModelClient, s Session) Options {
	return Options{Client: client, Session: s, Config: agent.RunConfig{Model: "test-model", MaxTurns: 10}}
}

func createSession(t *testing.T) (*session.Store, *session.Session, string) {
	t.Helper()
	store, cwd := session.NewStore(t.TempDir()), t.TempDir()
	s, err := store.Create(context.Background(), cwd)
	if err != nil {
		t.Fatal(err)
	}
	return store, s, cwd
}

func prompt(t *testing.T, c *Conversation, text string) {
	t.Helper()
	if err := c.Prompt(context.Background(), text, nil); err != nil {
		t.Fatal(err)
	}
}

func TestConversationRemembersToolResultsAcrossPromptsAndResume(t *testing.T) {
	store, s, cwd := createSession(t)
	if err := os.WriteFile(filepath.Join(cwd, "note.txt"), []byte("project fact"), 0600); err != nil {
		t.Fatal(err)
	}
	client := &recordingClient{client: faux.NewClient(
		faux.ToolCallMessage("read", "read-1", map[string]any{"path": "note.txt"}),
		faux.TextMessage("Read the note."), faux.TextMessage("I remember."),
	)}
	opts := options(client, s)
	opts.Tools = []agent.Tool{tools.NewReadTool(env.NewLocalEnv(cwd))}
	c := New(opts)
	prompt(t, c, "read the note")
	prompt(t, c, "what was in it?")
	if len(client.requests) != 3 || len(client.requests[2].Messages) != 5 {
		t.Fatalf("history duplicated or missing: %#v", client.requests)
	}
	if result, ok := client.requests[2].Messages[2].(message.ToolResultMessage); !ok || result.ToolCallID != "read-1" {
		t.Fatalf("missing result: %#v", client.requests[2].Messages)
	}
	if c.Info().MessageCount != 6 {
		t.Fatal(c.Info())
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.OpenLatest(context.Background(), cwd)
	if err != nil {
		t.Fatal(err)
	}
	resumedClient := &recordingClient{client: faux.NewClient(faux.TextMessage("After restart."))}
	resumed := New(options(resumedClient, reopened))
	defer resumed.Close()
	prompt(t, resumed, "continue")
	if len(resumedClient.requests[0].Messages) != 7 || resumed.Info().MessageCount != 8 {
		t.Fatal("resume lost or duplicated history")
	}
}

type failingSession struct {
	appendCalls int
	closeCalls  int
}

func (*failingSession) Messages() []message.AgentMessage { return nil }
func (s *failingSession) Append(context.Context, message.AgentMessage) error {
	s.appendCalls++
	return errors.New("disk full")
}
func (s *failingSession) Close() error { s.closeCalls++; return nil }

func TestConversationKeepsMemoryAfterPersistenceFailure(t *testing.T) {
	s := &failingSession{}
	client := &recordingClient{client: faux.NewClient(faux.TextMessage("first"), faux.TextMessage("second"))}
	opts := options(client, s)
	warnings := 0
	opts.Warn = func(err error) {
		warnings++
		if !strings.Contains(err.Error(), "disk full") {
			t.Error(err)
		}
	}
	c := New(opts)
	defer c.Close()
	prompt(t, c, "one")
	prompt(t, c, "two")
	if c.Info().PersistenceEnabled || warnings != 1 || s.appendCalls != 1 || c.Info().MessageCount != 4 {
		t.Fatalf("info=%+v warnings=%d writes=%d", c.Info(), warnings, s.appendCalls)
	}
	if len(client.requests[1].Messages) != 3 {
		t.Fatal("second prompt lost in-memory history")
	}
}

func TestNewSessionIsTransactionalAndRebuildsPrompt(t *testing.T) {
	store, s, cwd := createSession(t)
	client := &recordingClient{client: faux.NewClient(faux.TextMessage("first"), faux.TextMessage("new"))}
	opts := options(client, s)
	opts.SystemPrompt = "initial instructions"
	fail := true
	opts.CreateSession = func(ctx context.Context) (Session, error) {
		if fail {
			return nil, errors.New("cannot create")
		}
		return store.Create(ctx, cwd)
	}
	builds := 0
	opts.BuildSystemPrompt = func(context.Context) string { builds++; return "new instructions" }
	c := New(opts)
	defer c.Close()
	prompt(t, c, "one")
	if err := c.SetModel("next-model"); err != nil {
		t.Fatal(err)
	}
	before := c.Info()
	if c.NewSession(context.Background()) == nil {
		t.Fatal("expected creation error")
	}
	if c.Info() != before || builds != 0 {
		t.Fatal("failed /new changed conversation")
	}
	fail = false
	if err := c.NewSession(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c.Info().Metadata.ID == before.Metadata.ID || c.Info().MessageCount != 0 || c.Info().Model != "next-model" || !c.Info().PersistenceEnabled || builds != 1 {
		t.Fatal(c.Info())
	}
	if err := s.Append(context.Background(), message.UserMessage{}); err == nil {
		t.Fatal("old session still open")
	}
	prompt(t, c, "fresh")
	request := client.requests[1]
	if len(request.Messages) != 1 || request.SystemPrompt != "new instructions" || request.Model != "next-model" {
		t.Fatalf("wrong new request: %+v", request)
	}
}

func TestProviderErrorDoesNotPreventNextPrompt(t *testing.T) {
	calls := 0
	client := &recordingClient{client: streamFunc(func(ctx context.Context, _ provider.StreamParams) <-chan provider.StreamEvent {
		calls++
		if calls > 1 {
			return faux.NewClient(faux.TextMessage("recovered")).Stream(ctx, provider.StreamParams{})
		}
		ch := make(chan provider.StreamEvent, 1)
		ch <- provider.StreamError{Err: errors.New("unavailable"), Message: message.AssistantMessage{StopReason: message.StopReasonError}}
		close(ch)
		return ch
	})}
	c := New(options(client, nil))
	defer c.Close()
	if err := c.Prompt(context.Background(), "first", nil); err == nil {
		t.Fatal("expected error")
	}
	prompt(t, c, "try again")
	if len(client.requests[1].Messages) != 3 || c.Info().MessageCount != 4 {
		t.Fatal("failed request was lost or duplicated")
	}
}

func TestTurnLimitResetsForEachUserPrompt(t *testing.T) {
	_, s, cwd := createSession(t)
	if err := os.WriteFile(filepath.Join(cwd, "note.txt"), []byte("fact"), 0600); err != nil {
		t.Fatal(err)
	}
	client := &recordingClient{client: faux.NewClient(
		faux.ToolCallMessage("read", "limited-call", map[string]any{"path": "note.txt"}),
		faux.TextMessage("next request works"),
	)}
	opts := options(client, s)
	opts.Config.MaxTurns = 1
	opts.Tools = []agent.Tool{tools.NewReadTool(env.NewLocalEnv(cwd))}
	c := New(opts)
	defer c.Close()
	if err := c.Prompt(context.Background(), "read", nil); !errors.Is(err, agent.ErrMaxTurns) {
		t.Fatalf("expected turn limit, got %v", err)
	}
	prompt(t, c, "summarize")
	if len(client.requests) != 2 || len(client.requests[1].Messages) != 4 || c.Info().MessageCount != 5 {
		t.Fatal("turn limit leaked into next prompt or lost tool outcome")
	}
}

func TestFailedStreamDoesNotPersistUnfinishedToolCalls(t *testing.T) {
	for _, terminalError := range []bool{true, false} {
		t.Run(map[bool]string{true: "provider-error", false: "unexpected-close"}[terminalError], func(t *testing.T) {
			store, s, cwd := createSession(t)
			client := streamFunc(func(context.Context, provider.StreamParams) <-chan provider.StreamEvent {
				ch := make(chan provider.StreamEvent, 5)
				ch <- provider.StreamStart{}
				ch <- provider.TextDelta{Delta: "available"}
				ch <- provider.ToolCallStart{Index: 0, ID: "unfinished", Name: "write"}
				ch <- provider.ToolCallDelta{Index: 0, Delta: `{"path":`}
				if terminalError {
					ch <- provider.StreamError{Err: errors.New("connection lost")}
				}
				close(ch)
				return ch
			})
			c := New(options(client, s))
			if c.Prompt(context.Background(), "start", nil) == nil {
				t.Fatal("expected stream failure")
			}
			if !c.Info().PersistenceEnabled {
				t.Fatal("invalid tool fragment reached persistence")
			}
			c.Close()
			reopened, err := store.OpenLatest(context.Background(), cwd)
			if err != nil {
				t.Fatal(err)
			}
			assistant := reopened.Messages()[1].(message.AssistantMessage)
			if assistant.StopReason != message.StopReasonError || len(assistant.Content) != 1 || assistant.Content[0].(message.TextContent).Text != "available" {
				t.Fatalf("invalid failed message: %#v", assistant)
			}
			resumed := New(options(faux.NewClient(faux.TextMessage("recovered")), reopened))
			defer resumed.Close()
			prompt(t, resumed, "try again")
		})
	}
}

func TestCancelStreamPersistsOnlyAvailableTextAndCanResume(t *testing.T) {
	for _, partialCall := range []bool{false, true} {
		t.Run(map[bool]string{false: "text", true: "partial-tool-call"}[partialCall], func(t *testing.T) {
			store, s, cwd := createSession(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			client := streamFunc(func(ctx context.Context, _ provider.StreamParams) <-chan provider.StreamEvent {
				ch := make(chan provider.StreamEvent)
				go func() {
					defer close(ch)
					events := []provider.StreamEvent{provider.StreamStart{}, provider.TextDelta{Delta: "available text"}}
					if partialCall {
						events = append(events, provider.ToolCallStart{Index: 0, ID: "incomplete", Name: "write"}, provider.ToolCallDelta{Index: 0, Delta: `{"path":`})
					}
					for _, event := range events {
						select {
						case ch <- event:
						case <-ctx.Done():
							return
						}
					}
					<-ctx.Done()
				}()
				return ch
			})
			c := New(options(client, s))
			err := c.Prompt(ctx, "start", func(event agent.AgentEvent) {
				if update, ok := event.(agent.MessageUpdate); ok {
					_, isCall := update.Event.(provider.ToolCallDelta)
					_, isText := update.Event.(provider.TextDelta)
					if (partialCall && isCall) || (!partialCall && isText) {
						cancel()
					}
				}
			})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("expected cancellation, got %v", err)
			}
			if !c.Info().PersistenceEnabled || c.Info().MessageCount != 2 {
				t.Fatal("cancellation disabled persistence or dropped final message")
			}
			if err := c.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := store.OpenLatest(context.Background(), cwd)
			if err != nil {
				t.Fatal(err)
			}
			assistant := reopened.Messages()[1].(message.AssistantMessage)
			if assistant.StopReason != message.StopReasonAborted || len(assistant.Content) != 1 || assistant.Content[0].(message.TextContent).Text != "available text" {
				t.Fatalf("invalid aborted assistant: %#v", assistant)
			}
			resumed := New(options(faux.NewClient(faux.TextMessage("continued")), reopened))
			defer resumed.Close()
			prompt(t, resumed, "continue")
		})
	}
}

type cancelingTool struct {
	cancel context.CancelFunc
	path   string
	calls  int
}

func (*cancelingTool) ToolName() string            { return "change" }
func (*cancelingTool) ToolDescription() string     { return "Change one file." }
func (*cancelingTool) ToolSchema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t *cancelingTool) Execute(context.Context, string, json.RawMessage) (agent.ToolResult, error) {
	t.calls++
	if err := os.WriteFile(t.path, []byte("changed"), 0600); err != nil {
		return agent.ToolResult{}, err
	}
	t.cancel()
	return agent.ToolResult{Content: []message.ContentBlock{message.TextContent{Text: "changed"}}}, nil
}

func TestCancelToolBatchKeepsCompletedEffectAndClosesAllCalls(t *testing.T) {
	store, s, cwd := createSession(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tool := &cancelingTool{cancel: cancel, path: filepath.Join(cwd, "changed.txt")}
	assistant := faux.TextMessage("")
	assistant.StopReason = message.StopReasonToolUse
	assistant.Content = []message.ContentBlock{
		message.ToolCall{ID: "first", Name: "change", Arguments: json.RawMessage(`{}`)},
		message.ToolCall{ID: "second", Name: "change", Arguments: json.RawMessage(`{}`)},
	}
	opts := options(faux.NewClient(assistant), s)
	opts.Tools = []agent.Tool{tool}
	c := New(opts)
	if err := c.Prompt(ctx, "change", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if tool.calls != 1 || c.Info().MessageCount != 4 {
		t.Fatalf("calls=%d info=%+v", tool.calls, c.Info())
	}
	if data, err := os.ReadFile(tool.path); err != nil || string(data) != "changed" {
		t.Fatal("completed change lost")
	}
	c.Close()
	reopened, err := store.OpenLatest(context.Background(), cwd)
	if err != nil {
		t.Fatal(err)
	}
	messages := reopened.Messages()
	first, second := messages[2].(message.ToolResultMessage), messages[3].(message.ToolResultMessage)
	if first.IsError || !second.IsError || second.ToolCallID != "second" || !strings.Contains(second.Content[0].(message.TextContent).Text, "not executed: canceled") {
		t.Fatalf("invalid outcomes: %#v %#v", first, second)
	}
	client := &recordingClient{client: faux.NewClient(faux.TextMessage("continued"))}
	resumed := New(options(client, reopened))
	defer resumed.Close()
	prompt(t, resumed, "continue")
	if !reflect.DeepEqual(client.requests[0].Messages[:4], message.ConvertToLLM(messages)) {
		t.Fatal("resume changed completed history")
	}
}

func TestConversationRejectsInvalidInputAndClosesOnce(t *testing.T) {
	s := &failingSession{}
	c := New(options(faux.NewClient(), s))
	for _, name := range []string{"", "  ", "two models"} {
		if c.SetModel(name) == nil {
			t.Fatalf("accepted %q", name)
		}
	}
	if c.Prompt(context.Background(), "  ", nil) == nil {
		t.Fatal("accepted blank prompt")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !errors.Is(c.Prompt(ctx, "hello", nil), context.Canceled) || c.Info().MessageCount != 0 {
		t.Fatal("already canceled prompt added history")
	}
	c.Close()
	c.Close()
	if s.closeCalls != 1 {
		t.Fatal("session closed more than once")
	}
	if c.Prompt(context.Background(), "hello", nil) == nil || c.SetModel("other") == nil || c.NewSession(context.Background()) == nil {
		t.Fatal("closed conversation accepted work")
	}
}
