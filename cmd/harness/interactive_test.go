package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/aaliyan1230/go-rig/internal/agent"
	"github.com/aaliyan1230/go-rig/internal/app"
	"github.com/aaliyan1230/go-rig/internal/message"
	"github.com/aaliyan1230/go-rig/internal/provider"
	"github.com/aaliyan1230/go-rig/internal/provider/faux"
	"github.com/aaliyan1230/go-rig/internal/session"
	"github.com/chzyer/readline"
)

type inputResult struct {
	line string
	err  error
}
type fakeInput struct{ results []inputResult }

func (f *fakeInput) Readline() (string, error) {
	if len(f.results) == 0 {
		return "", io.EOF
	}
	r := f.results[0]
	f.results = f.results[1:]
	return r.line, r.err
}
func (*fakeInput) Close() error { return nil }
func inputLines(lines ...string) *fakeInput {
	f := &fakeInput{}
	for _, line := range lines {
		f.results = append(f.results, inputResult{line: line})
	}
	return f
}
func testContext() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}

func TestInteractiveConversationCommandsAndHistory(t *testing.T) {
	store, cwd := session.NewStore(t.TempDir()), t.TempDir()
	s, err := store.Create(context.Background(), cwd)
	if err != nil {
		t.Fatal(err)
	}
	client := &recordingClient{inner: faux.NewClient(faux.TextMessage("bananas"), faux.TextMessage("remembered"), faux.TextMessage("fresh"))}
	c := app.New(app.Options{
		Client: client, Session: s, Cwd: cwd, Config: agent.RunConfig{Model: "original", MaxTurns: 10},
		CreateSession: func(ctx context.Context) (app.Session, error) { return store.Create(ctx, cwd) },
	})
	defer c.Close()
	f := inputLines("", "   ", "/help", "/model", "/model too many", "/invalid", "/quit nope", "remember bananas", "/session", "/model next", "recall fruit", "/new", "/session", "new conversation", "/quit")
	f.results = append([]inputResult{{line: "discard this", err: readline.ErrInterrupt}}, f.results...)
	var stdout, stderr bytes.Buffer
	if code := interactiveLoop(c, f, &stdout, &stderr, testContext); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	requests := client.Requests()
	if len(requests) != 3 || len(requests[0].Messages) != 1 || len(requests[1].Messages) != 3 || len(requests[2].Messages) != 1 {
		t.Fatalf("unexpected requests: %+v", requests)
	}
	if requests[0].Model != "original" || requests[1].Model != "next" || requests[2].Model != "next" {
		t.Fatal("model selection not retained")
	}
	if c.Info().MessageCount != 2 || c.Info().Metadata.ID == s.Metadata().ID {
		t.Fatal("/new failed")
	}
	for _, text := range []string{"usage: /model <name>", "unknown command: /invalid", "usage: /quit"} {
		if !strings.Contains(stderr.String(), text) {
			t.Fatalf("missing %q: %s", text, stderr.String())
		}
	}
	for _, text := range []string{cwd, "Messages: 2", "Messages: 0", "Persistence: enabled", "remembered", "fresh", "/session"} {
		if !strings.Contains(stdout.String(), text) {
			t.Fatalf("missing %q: %s", text, stdout.String())
		}
	}
	old, err := store.OpenLatest(context.Background(), cwd)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	if len(old.Messages()) != 2 {
		t.Fatal("local commands polluted session history")
	}
}

func TestInteractiveEOFAndInputFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code int
	}{{"EOF", io.EOF, 0}, {"failure", errors.New("reader failed"), 1}} {
		t.Run(tc.name, func(t *testing.T) {
			client := &recordingClient{inner: faux.NewClient()}
			c := app.New(app.Options{Client: client})
			defer c.Close()
			var stdout, stderr bytes.Buffer
			if code := interactiveLoop(c, &fakeInput{results: []inputResult{{err: tc.err}}}, &stdout, &stderr, testContext); code != tc.code {
				t.Fatalf("exit %d, want %d", code, tc.code)
			}
			if len(client.Requests()) != 0 {
				t.Fatal("EOF/error called provider")
			}
			if tc.code == 1 && !strings.Contains(stderr.String(), "reader failed") {
				t.Fatal(stderr.String())
			}
		})
	}
}

func TestInteractiveCancellationAndProviderFailureReturnToInput(t *testing.T) {
	for _, cancelFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "cancel", false: "provider-error"}[cancelFirst], func(t *testing.T) {
			calls := 0
			client := cliStreamFunc(func(ctx context.Context, p provider.StreamParams) <-chan provider.StreamEvent {
				calls++
				if calls == 1 && !cancelFirst {
					ch := make(chan provider.StreamEvent, 1)
					ch <- provider.StreamError{Err: errors.New("provider unavailable")}
					close(ch)
					return ch
				}
				return faux.NewClient(faux.TextMessage("after failure")).Stream(ctx, p)
			})
			c := app.New(app.Options{Client: client, Config: agent.RunConfig{Model: "test", MaxTurns: 10}})
			defer c.Close()
			contexts := 0
			newContext := func() (context.Context, context.CancelFunc) {
				contexts++
				ctx, cancel := testContext()
				if contexts == 1 && cancelFirst {
					cancel()
				}
				return ctx, cancel
			}
			var stdout, stderr bytes.Buffer
			if code := interactiveLoop(c, inputLines("first", "try again", "/quit"), &stdout, &stderr, newContext); code != 0 {
				t.Fatal(code)
			}
			if !strings.Contains(stdout.String(), "after failure") {
				t.Fatal(stdout.String())
			}
			want := "provider unavailable"
			if cancelFirst {
				want = "Canceled."
			}
			if !strings.Contains(stderr.String(), want) {
				t.Fatal(stderr.String())
			}
		})
	}
}

type cliStreamFunc func(context.Context, provider.StreamParams) <-chan provider.StreamEvent

func (f cliStreamFunc) Stream(ctx context.Context, p provider.StreamParams) <-chan provider.StreamEvent {
	return f(ctx, p)
}

func TestCLIRejectsEmptyPromptAndNonTerminalInteractiveMode(t *testing.T) {
	stdin, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	for _, tc := range []struct {
		args []string
		want string
	}{{[]string{"-p", ""}, "usage:"}, {nil, "requires terminal stdin and stdout"}, {[]string{"--continue"}, "use -p"}} {
		var stdout, stderr bytes.Buffer
		if code := cli(tc.args, stdin, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), tc.want) {
			t.Fatalf("args=%v exit=%d stderr=%s", tc.args, code, stderr.String())
		}
	}
}

func TestInteractiveToolOutputIsCompactAndBounded(t *testing.T) {
	var stdout, stderr bytes.Buffer
	r := eventRenderer{stdout: &stdout, stderr: &stderr, interactive: true}
	args := json.RawMessage(`{"path":"file.go","content":"PRIVATE FILE CONTENT","oldText":"HUGE OLD TEXT"}`)
	r.Render(agent.ToolExecStart{ToolName: "write", Args: args})
	if strings.Contains(stderr.String(), "PRIVATE") || strings.Contains(stderr.String(), "HUGE") || !strings.Contains(stderr.String(), "file.go") {
		t.Fatal(stderr.String())
	}
	long := strings.Repeat("line\n", 30)
	r.Render(agent.ToolExecEnd{ToolName: "read", Result: agent.ToolResult{Content: []message.ContentBlock{message.TextContent{Text: long}}}})
	if strings.Count(stderr.String(), "line\n") != 10 || !strings.Contains(stderr.String(), "[preview truncated]") {
		t.Fatal(stderr.String())
	}
	preview := boundedText(strings.Repeat("漢", 1000), 1024, 10)
	if len(preview) > 1024 || !utf8.ValidString(preview) {
		t.Fatal("invalid byte truncation")
	}
	if len(boundedText(strings.Repeat("x", 3000), 1024, 10)) != 1024 {
		t.Fatal("byte limit not enforced")
	}
}
