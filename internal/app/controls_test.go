package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/aaliyan1230/go-rig/internal/agent"
	"github.com/aaliyan1230/go-rig/internal/env"
	"github.com/aaliyan1230/go-rig/internal/message"
	"github.com/aaliyan1230/go-rig/internal/policy"
	"github.com/aaliyan1230/go-rig/internal/provider/faux"
	"github.com/aaliyan1230/go-rig/internal/tools"
)

func toolResponses(n int) []message.AssistantMessage {
	var responses []message.AssistantMessage
	for i := 0; i < n; i++ {
		responses = append(responses, faux.ToolCallMessage("read", fmt.Sprintf("call-%d", i), map[string]any{"path": "note"}))
	}
	return append(responses, faux.TextMessage("finished"))
}

func TestRepeatedRenewalPersistsBeforeConfirmation(t *testing.T) {
	_, s, cwd := createSession(t)
	os.WriteFile(filepath.Join(cwd, "note"), []byte("fact"), 0600)
	client := &recordingClient{client: faux.NewClient(toolResponses(4)...)}
	opts := options(client, s)
	opts.Config.MaxTurns = 2
	opts.Tools = []agent.Tool{tools.NewReadTool(env.NewLocalEnv(cwd))}
	opts.Broker = policy.NewBroker()
	var c *Conversation
	asked := 0
	opts.Confirm = func(_ context.Context, r policy.Request) (bool, error) {
		asked++
		want := 1 + 4*asked
		if r.Kind != "turns" || len(s.Messages()) != want || c.Info().MessageCount != want {
			t.Fatalf("prompt before persistence: kind=%s saved=%d memory=%d", r.Kind, len(s.Messages()), c.Info().MessageCount)
		}
		return true, nil
	}
	c = New(opts)
	defer c.Close()
	result, err := c.Prompt(context.Background(), "work", nil)
	if err != nil || result.Status != agent.EndCompleted || result.Turns != 5 || asked != 2 {
		t.Fatalf("result=%+v err=%v asked=%d", result, err, asked)
	}
	if c.Info().MessageCount != 10 {
		t.Fatal(c.Info())
	}
	for i, r := range client.requests {
		if len(r.Messages) != 1+2*i {
			t.Fatalf("request %d has duplicated/missing messages: %d", i, len(r.Messages))
		}
		users := 0
		for _, m := range r.Messages {
			if _, ok := m.(message.UserMessage); ok {
				users++
			}
		}
		if users != 1 {
			t.Fatal("renewal fabricated another user message")
		}
	}
}

func TestPauseRestartAndContinueWithoutNewPrompt(t *testing.T) {
	store, s, cwd := createSession(t)
	client := &recordingClient{client: faux.NewClient(toolResponses(2)...)}
	opts := options(client, s)
	opts.Config.MaxTurns = 2
	opts.Tools = []agent.Tool{tools.NewReadTool(env.NewLocalEnv(cwd))}
	opts.Broker = policy.NewBroker()
	opts.Confirm = func(context.Context, policy.Request) (bool, error) { return false, nil }
	c := New(opts)
	result, err := c.Prompt(context.Background(), "work", nil)
	if err != nil || result.Status != agent.EndTurnLimit || !c.Info().CanContinue {
		t.Fatal(result, err, c.Info())
	}
	c.Close()
	reopened, err := store.OpenLatest(context.Background(), cwd)
	if err != nil {
		t.Fatal(err)
	}
	resumedClient := &recordingClient{client: faux.NewClient(faux.TextMessage("finished"))}
	resumed := New(options(resumedClient, reopened))
	defer resumed.Close()
	result, err = resumed.Continue(context.Background(), nil)
	if err != nil || result.Status != agent.EndCompleted || result.Turns != 1 || len(resumedClient.requests[0].Messages) != 5 || resumed.Info().MessageCount != 6 {
		t.Fatal(result, err, resumed.Info())
	}
	if _, err = resumed.Continue(context.Background(), nil); !errors.Is(err, agent.ErrCannotContinue) {
		t.Fatal("completed conversation accepted continuation", err)
	}
}

func TestCompletionAtBudgetBoundaryDoesNotAsk(t *testing.T) {
	opts := options(faux.NewClient(faux.TextMessage("done")), nil)
	opts.Config.MaxTurns = 1
	opts.Broker = policy.NewBroker()
	opts.Confirm = func(context.Context, policy.Request) (bool, error) {
		t.Fatal("finished task asked to renew")
		return false, nil
	}
	c := New(opts)
	defer c.Close()
	result, err := c.Prompt(context.Background(), "work", nil)
	if err != nil || result.Status != agent.EndCompleted || result.Turns != 1 {
		t.Fatal(result, err)
	}
}

func TestApprovalDenialAndCancellationHaveNoEffects(t *testing.T) {
	for _, mode := range []string{"allow", "deny", "cancel", "no-ui", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			_, s, cwd := createSession(t)
			target := filepath.Join(cwd, "file")
			client := &recordingClient{client: faux.NewClient(faux.ToolCallMessage("write", "change", map[string]any{"path": "file", "content": "new"}), faux.TextMessage("handled"))}
			opts := options(client, s)
			opts.Tools = []agent.Tool{tools.NewWriteTool(env.NewLocalEnv(cwd))}
			opts.Broker = policy.NewBroker()
			if mode != "no-ui" && mode != "disabled" {
				opts.Confirm = func(_ context.Context, r policy.Request) (bool, error) {
					if r.Kind != "tool" || len(s.Messages()) != 2 {
						t.Fatal("approval before assistant call was saved")
					}
					if mode == "cancel" {
						return false, context.Canceled
					}
					return mode == "allow", nil
				}
				opts.Config.BeforeToolCall = policy.ApprovalGate("ask", opts.Broker.Ask)
			} else {
				opts.Config.BeforeToolCall = policy.ApprovalGate("ask", nil)
			}
			if mode == "disabled" {
				opts.Tools = nil
			}
			c := New(opts)
			defer c.Close()
			_, err := c.Prompt(context.Background(), "work", nil)
			if mode == "cancel" {
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			_, statErr := os.Stat(target)
			if mode == "allow" {
				if statErr != nil {
					t.Fatal(statErr)
				}
			} else if !os.IsNotExist(statErr) {
				t.Fatal("denied tool changed a file")
			}
			result := s.Messages()[2].(message.ToolResultMessage)
			if result.IsError != (mode != "allow") {
				t.Fatalf("wrong tool result: %+v", result)
			}
			if mode == "disabled" && len(client.requests[0].Tools) != 0 {
				t.Fatal("disabled schema still advertised")
			}
		})
	}
}

func TestRenewalAfterPersistenceFailureAndCancelWhileAsking(t *testing.T) {
	for _, abort := range []bool{false, true} {
		t.Run(fmt.Sprint(abort), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			client := &recordingClient{client: faux.NewClient(toolResponses(1)...)}
			failing := &failingSession{}
			opts := options(client, failing)
			opts.Config.MaxTurns = 1
			opts.Tools = []agent.Tool{tools.NewReadTool(env.NewLocalEnv(t.TempDir()))}
			opts.Broker = policy.NewBroker()
			warnings := 0
			opts.Warn = func(error) { warnings++ }
			opts.Confirm = func(ctx context.Context, r policy.Request) (bool, error) {
				if abort {
					cancel()
					<-ctx.Done()
					return false, ctx.Err()
				}
				return true, nil
			}
			c := New(opts)
			defer c.Close()
			result, err := c.Prompt(ctx, "work", nil)
			if abort {
				if !errors.Is(err, context.Canceled) || result.Status != agent.EndCanceled || len(client.requests) != 1 {
					t.Fatal(result, err)
				}
			} else if err != nil || result.Status != agent.EndCompleted || len(client.requests[1].Messages) != 3 {
				t.Fatal(result, err)
			}
			if warnings != 1 || failing.appendCalls != 1 || c.Info().PersistenceEnabled {
				t.Fatal("persistence failure handling changed")
			}
		})
	}
}

func TestNewSessionPromptFailureKeepsOldSession(t *testing.T) {
	_, s, _ := createSession(t)
	opts := options(faux.NewClient(faux.TextMessage("remember")), s)
	created := false
	opts.CreateSession = func(context.Context) (Session, error) { created = true; return nil, errors.New("must not create") }
	opts.BuildSystemPrompt = func(context.Context) (string, error) { return "", errors.New("trust record corrupt") }
	c := New(opts)
	defer c.Close()
	prompt(t, c, "hello")
	before := c.Info()
	if err := c.NewSession(context.Background()); err == nil || created || c.Info() != before {
		t.Fatal("prompt failure discarded active session")
	}
}
