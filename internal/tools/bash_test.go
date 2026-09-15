package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aaliyan1230/go-rig/internal/env"
)

type bashTestEnv struct {
	cwd     string
	stdout  string
	stderr  string
	exit    int
	execErr error
	gotOpts env.ExecOptions
}

func (e *bashTestEnv) ReadFile(context.Context, string) ([]byte, error) {
	return nil, errors.New("unused")
}
func (e *bashTestEnv) WriteFile(context.Context, string, []byte, os.FileMode) error {
	return errors.New("unused")
}
func (e *bashTestEnv) Stat(context.Context, string) (env.FileInfo, error) {
	return env.FileInfo{}, errors.New("unused")
}
func (e *bashTestEnv) ListDir(context.Context, string) ([]env.FileInfo, error) {
	return nil, errors.New("unused")
}
func (e *bashTestEnv) Exists(context.Context, string) (bool, error) {
	return false, errors.New("unused")
}
func (e *bashTestEnv) MkdirAll(context.Context, string) error { return errors.New("unused") }
func (e *bashTestEnv) Remove(context.Context, string) error   { return errors.New("unused") }
func (e *bashTestEnv) Exec(_ context.Context, _ string, opts env.ExecOptions) (env.ExecResult, error) {
	e.gotOpts = opts
	return env.ExecResult{Stdout: e.stdout, Stderr: e.stderr, ExitCode: e.exit}, e.execErr
}
func (e *bashTestEnv) Cwd() string { return e.cwd }

var _ env.Env = (*bashTestEnv)(nil)

func TestBashToolReturnsLabeledOutputAndCwd(t *testing.T) {
	fake := &bashTestEnv{cwd: "/project", stdout: "hello\n", stderr: "warning\n"}
	tool := NewBashTool(fake)
	result, err := tool.Execute(context.Background(), "call-1", json.RawMessage(`{"command":"go test ./...","timeout":7}`))
	if err != nil {
		t.Fatal(err)
	}
	got := toolResultText(t, result.Content)
	if !strings.Contains(got, "STDOUT:\nhello") || !strings.Contains(got, "STDERR:\nwarning") {
		t.Fatalf("labeled output = %q", got)
	}
	if fake.gotOpts.Cwd != "/project" || fake.gotOpts.Timeout != 7*time.Second {
		t.Fatalf("Exec options = %#v, want cwd and seven-second timeout", fake.gotOpts)
	}
}

func TestBashToolUsesThirtySecondDefault(t *testing.T) {
	fake := &bashTestEnv{cwd: "/project"}
	if _, err := NewBashTool(fake).Execute(context.Background(), "call-1", json.RawMessage(`{"command":"true"}`)); err != nil {
		t.Fatal(err)
	}
	if fake.gotOpts.Timeout != 30*time.Second {
		t.Fatalf("default timeout = %v, want 30s", fake.gotOpts.Timeout)
	}
}

func TestBashToolEmptyOutput(t *testing.T) {
	tool := NewBashTool(&bashTestEnv{cwd: "/project"})
	result, err := tool.Execute(context.Background(), "call-1", json.RawMessage(`{"command":"true"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := toolResultText(t, result.Content); got != "(no output)" {
		t.Fatalf("empty output = %q, want (no output)", got)
	}
}

func TestBashToolNonZeroIncludesStatusAndOutput(t *testing.T) {
	fake := &bashTestEnv{cwd: "/project", stdout: "partial\n", stderr: "bad\n", exit: 2, execErr: errors.New("exit status 2")}
	tool := NewBashTool(fake)
	_, err := tool.Execute(context.Background(), "call-1", json.RawMessage(`{"command":"false"}`))
	if err == nil {
		t.Fatal("expected non-zero command error")
	}
	for _, want := range []string{"exited with code 2", "STDOUT:", "partial", "STDERR:", "bad"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err, want)
		}
	}
}

func TestBashToolTimeoutAndCancellationAreClear(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		fake := &bashTestEnv{cwd: "/project", execErr: context.DeadlineExceeded}
		_, err := NewBashTool(fake).Execute(context.Background(), "call-1", json.RawMessage(`{"command":"sleep 10","timeout":1}`))
		if err == nil || !strings.Contains(err.Error(), "timed out after 1 seconds") {
			t.Fatalf("error = %v, want timeout status", err)
		}
	})
	t.Run("canceled", func(t *testing.T) {
		fake := &bashTestEnv{cwd: "/project", execErr: context.Canceled}
		_, err := NewBashTool(fake).Execute(context.Background(), "call-1", json.RawMessage(`{"command":"sleep 10"}`))
		if err == nil || !strings.Contains(err.Error(), "canceled") {
			t.Fatalf("error = %v, want cancellation status", err)
		}
	})
}

func TestBashToolTruncatesToTail(t *testing.T) {
	var builder strings.Builder
	for i := 1; i <= DefaultReadMaxLines+5; i++ {
		fmt.Fprintf(&builder, "line-%d\n", i)
	}
	fake := &bashTestEnv{cwd: "/project", stdout: builder.String()}
	result, err := NewBashTool(fake).Execute(context.Background(), "call-1", json.RawMessage(`{"command":"many"}`))
	if err != nil {
		t.Fatal(err)
	}
	got := toolResultText(t, result.Content)
	if !strings.HasPrefix(got, "STDOUT:\n") || !strings.Contains(got, "STDERR:\n") || !strings.Contains(got, "truncated") || !strings.Contains(got, "line-2005") {
		t.Fatalf("tail output = %q", got[len(got)-min(len(got), 250):])
	}
	if strings.Contains(got, "line-1\n") {
		t.Fatal("tail output unexpectedly retained the first line")
	}
}

func TestBashToolRejectsInvalidArguments(t *testing.T) {
	tool := NewBashTool(&bashTestEnv{cwd: "/project"})
	for _, input := range []string{
		`{}`,
		`{"command":"  "}`,
		`{"command":"true","timeout":0}`,
		`{"command":"true","timeout":-1}`,
		`{"command":"true","timeout":null}`,
		`{"command":123}`,
		`{"command":"true","unknown":true}`,
	} {
		t.Run(input, func(t *testing.T) {
			_, err := tool.Execute(context.Background(), "call-1", json.RawMessage(input))
			if err == nil {
				t.Fatalf("expected invalid arguments error for %s", input)
			}
		})
	}
}
