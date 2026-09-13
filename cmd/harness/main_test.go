package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aaliyan1230/go-rig/internal/agent"
	"github.com/aaliyan1230/go-rig/internal/config"
	"github.com/aaliyan1230/go-rig/internal/env"
	"github.com/aaliyan1230/go-rig/internal/message"
	"github.com/aaliyan1230/go-rig/internal/provider"
	"github.com/aaliyan1230/go-rig/internal/provider/faux"
	"github.com/aaliyan1230/go-rig/internal/session"
	builtintools "github.com/aaliyan1230/go-rig/internal/tools"
)

func TestStreamPrompt_SimpleText(t *testing.T) {
	client := faux.NewClient(faux.TextMessage("Hello from faux!"))
	cfg := config.DefaultConfig()

	var stdout, stderr bytes.Buffer
	exitCode := streamPrompt(client, cfg, "say hello", nil, &stdout, &stderr)

	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d; stderr: %s", exitCode, stderr.String())
	}
	if stdout.String() != "Hello from faux!\n" {
		t.Errorf("expected 'Hello from faux!\\n', got %q", stdout.String())
	}
}

func TestStreamPrompt_Error(t *testing.T) {
	client := faux.NewClient() // empty queue → error

	var stdout, stderr bytes.Buffer
	exitCode := streamPrompt(client, config.DefaultConfig(), "hi", nil, &stdout, &stderr)

	if exitCode != 1 {
		t.Errorf("expected exit code 1, got %d", exitCode)
	}
	if stderr.Len() == 0 {
		t.Error("expected error output on stderr")
	}
}

func TestStreamPrompt_MultipleDeltas(t *testing.T) {
	client := faux.NewClient(faux.TextMessage("The quick brown fox jumps over the lazy dog"))
	cfg := config.DefaultConfig()

	var stdout, stderr bytes.Buffer
	exitCode := streamPrompt(client, cfg, "tell me something", nil, &stdout, &stderr)

	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d; stderr: %s", exitCode, stderr.String())
	}
	expected := "The quick brown fox jumps over the lazy dog\n"
	if stdout.String() != expected {
		t.Errorf("expected %q, got %q", expected, stdout.String())
	}
}

func TestStreamPrompt_ShowsToolStatusOnStderr(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}
	client := faux.NewClient(
		faux.ToolCallMessage("read", "call-1", map[string]any{"path": "note.txt"}),
		faux.TextMessage("I read the note."),
	)

	var stdout, stderr bytes.Buffer
	exitCode := streamPrompt(
		client,
		config.DefaultConfig(),
		"read note.txt",
		[]agent.Tool{builtintools.NewReadTool(env.NewLocalEnv(dir))},
		&stdout,
		&stderr,
	)

	if exitCode != 0 {
		t.Fatalf("expected success, got %d: %s", exitCode, stderr.String())
	}
	if stdout.String() != "I read the note.\n" {
		t.Fatalf("unexpected stdout: %q", stdout.String())
	}
	if got := stderr.String(); got != "tool: read {\"path\":\"note.txt\"}\ntool: read complete\n" {
		t.Fatalf("unexpected stderr: %q", got)
	}
}

func TestStreamPromptPersistsPromptAndCompletedMessages(t *testing.T) {
	root := t.TempDir()
	cwd := t.TempDir()
	store := session.NewStore(root)
	active, err := store.Create(context.Background(), cwd)
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	exitCode := streamPromptWithSession(
		faux.NewClient(faux.TextMessage("persisted answer")),
		config.DefaultConfig(),
		"remember this prompt",
		nil,
		active,
		&stdout,
		&stderr,
	)
	if exitCode != 0 {
		t.Fatalf("expected success, got %d: %s", exitCode, stderr.String())
	}
	if stdout.String() != "persisted answer\n" {
		t.Fatalf("unexpected stdout: %q", stdout.String())
	}

	reopened, err := store.OpenLatest(context.Background(), cwd)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	messages := reopened.Messages()
	if len(messages) != 2 {
		t.Fatalf("persisted %d messages, want prompt and assistant", len(messages))
	}
	if _, ok := messages[0].(message.UserMessage); !ok {
		t.Fatalf("first persisted message = %T, want user", messages[0])
	}
	assistant, ok := messages[1].(message.AssistantMessage)
	if !ok {
		t.Fatalf("second persisted message = %T, want assistant", messages[1])
	}
	if assistantText(assistant) != "persisted answer" {
		t.Fatalf("persisted assistant text = %q", assistantText(assistant))
	}
}

func TestStreamPromptPersistsToolRoundTripInOrder(t *testing.T) {
	root := t.TempDir()
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "note.txt"), []byte("hello from tool\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	active, err := session.NewStore(root).Create(context.Background(), cwd)
	if err != nil {
		t.Fatal(err)
	}
	client := faux.NewClient(
		faux.ToolCallMessage("read", "call-1", map[string]any{"path": "note.txt"}),
		faux.TextMessage("I read the note."),
	)
	var stdout, stderr bytes.Buffer
	exitCode := streamPromptWithSession(
		client,
		config.DefaultConfig(),
		"read note.txt",
		[]agent.Tool{builtintools.NewReadTool(env.NewLocalEnv(cwd))},
		active,
		&stdout,
		&stderr,
	)
	if exitCode != 0 {
		t.Fatalf("expected success, got %d: %s", exitCode, stderr.String())
	}

	reopened, err := session.NewStore(root).OpenLatest(context.Background(), cwd)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	messages := reopened.Messages()
	if len(messages) != 4 {
		t.Fatalf("persisted %d messages, want user, assistant tool call, tool result, assistant", len(messages))
	}
	if _, ok := messages[0].(message.UserMessage); !ok {
		t.Fatalf("message 0 = %T, want user", messages[0])
	}
	firstAssistant, ok := messages[1].(message.AssistantMessage)
	if !ok {
		t.Fatalf("message 1 = %T, want assistant", messages[1])
	}
	if len(firstAssistant.Content) != 1 {
		t.Fatalf("tool-call assistant content = %#v", firstAssistant.Content)
	}
	call, ok := firstAssistant.Content[0].(message.ToolCall)
	if !ok || call.ID != "call-1" || call.Name != "read" {
		t.Fatalf("persisted tool call = %#v", firstAssistant.Content[0])
	}
	result, ok := messages[2].(message.ToolResultMessage)
	if !ok || result.ToolCallID != "call-1" || result.ToolName != "read" || result.IsError {
		t.Fatalf("persisted tool result = %#v", messages[2])
	}
	lastAssistant, ok := messages[3].(message.AssistantMessage)
	if !ok || assistantText(lastAssistant) != "I read the note." {
		t.Fatalf("final assistant = %#v", messages[3])
	}
}

func TestStreamPromptContinueReplaysHistoryToProvider(t *testing.T) {
	root := t.TempDir()
	cwd := t.TempDir()
	store := session.NewStore(root)
	initial, err := store.Create(context.Background(), cwd)
	if err != nil {
		t.Fatal(err)
	}
	priorUser := message.UserMessage{Content: []message.ContentBlock{message.TextContent{Text: "old question"}}, Timestamp: 1}
	priorAssistant := faux.TextMessage("old answer")
	if err := initial.Append(context.Background(), priorUser); err != nil {
		t.Fatal(err)
	}
	if err := initial.Append(context.Background(), priorAssistant); err != nil {
		t.Fatal(err)
	}
	if err := initial.Close(); err != nil {
		t.Fatal(err)
	}

	active, err := store.OpenLatest(context.Background(), cwd)
	if err != nil {
		t.Fatal(err)
	}
	client := &recordingClient{inner: faux.NewClient(faux.TextMessage("new answer"))}
	var stdout, stderr bytes.Buffer
	exitCode := streamPromptWithSession(client, config.DefaultConfig(), "new question", nil, active, &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("expected success, got %d: %s", exitCode, stderr.String())
	}

	requests := client.Requests()
	if len(requests) != 1 {
		t.Fatalf("provider saw %d requests, want one", len(requests))
	}
	if len(requests[0].Messages) != 3 {
		t.Fatalf("provider saw %d messages, want 2 replayed plus current prompt", len(requests[0].Messages))
	}
	if got, ok := requests[0].Messages[0].(message.UserMessage); !ok || textContent(got.Content) != "old question" {
		t.Fatalf("provider message 0 = %#v", requests[0].Messages[0])
	}
	if got, ok := requests[0].Messages[1].(message.AssistantMessage); !ok || assistantText(got) != "old answer" {
		t.Fatalf("provider message 1 = %#v", requests[0].Messages[1])
	}
	if got, ok := requests[0].Messages[2].(message.UserMessage); !ok || textContent(got.Content) != "new question" {
		t.Fatalf("provider message 2 = %#v", requests[0].Messages[2])
	}

	reopened, err := store.OpenLatest(context.Background(), cwd)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got := len(reopened.Messages()); got != 4 {
		t.Fatalf("continued session has %d messages, want 4", got)
	}
}

func TestRunContinueWithoutSessionFailsClearly(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Env.WorkingDir = t.TempDir()
	var stdout, stderr bytes.Buffer
	exitCode := runWithOptions(cfg, "continue me", "test-key", "http://127.0.0.1:1", true, t.TempDir(), &stdout, &stderr)
	if exitCode != 1 {
		t.Fatalf("expected startup failure, got %d", exitCode)
	}
	if !strings.Contains(stderr.String(), "no session found") || !strings.Contains(stderr.String(), "session startup") {
		t.Fatalf("unclear no-session error: %q", stderr.String())
	}
}

func TestStreamPromptAppendFailureWarnsAndKeepsOutput(t *testing.T) {
	client := faux.NewClient(faux.TextMessage("still visible"))
	failing := &failingSession{err: errors.New("disk is full")}
	var stdout, stderr bytes.Buffer
	exitCode := streamPromptWithSession(client, config.DefaultConfig(), "hello", nil, failing, &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("expected model output to succeed, got %d", exitCode)
	}
	if stdout.String() != "still visible\n" {
		t.Fatalf("assistant output was suppressed: %q", stdout.String())
	}
	if got := strings.Count(stderr.String(), "warning: session persistence disabled:"); got != 1 {
		t.Fatalf("got %d persistence warnings, want one: %q", got, stderr.String())
	}
	if failing.appendCalls != 1 {
		t.Fatalf("append called %d times after failure, want one", failing.appendCalls)
	}
}

type recordingClient struct {
	mu       sync.Mutex
	inner    provider.ModelClient
	requests []provider.StreamParams
}

func (c *recordingClient) Stream(ctx context.Context, params provider.StreamParams) <-chan provider.StreamEvent {
	c.mu.Lock()
	copyParams := params
	copyParams.Messages = append([]message.Message(nil), params.Messages...)
	copyParams.Tools = append([]provider.ToolSpec(nil), params.Tools...)
	c.requests = append(c.requests, copyParams)
	c.mu.Unlock()
	return c.inner.Stream(ctx, params)
}

func (c *recordingClient) Requests() []provider.StreamParams {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]provider.StreamParams(nil), c.requests...)
}

type failingSession struct {
	err         error
	appendCalls int
}

func (s *failingSession) Messages() []message.AgentMessage { return nil }

func (s *failingSession) Append(context.Context, message.AgentMessage) error {
	s.appendCalls++
	return s.err
}

func (s *failingSession) Close() error { return nil }

func textContent(blocks []message.ContentBlock) string {
	for _, block := range blocks {
		if text, ok := block.(message.TextContent); ok {
			return text.Text
		}
	}
	return ""
}
