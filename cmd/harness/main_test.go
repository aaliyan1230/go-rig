package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/aaliyan1230/go-rig/internal/agent"
	"github.com/aaliyan1230/go-rig/internal/config"
	"github.com/aaliyan1230/go-rig/internal/env"
	"github.com/aaliyan1230/go-rig/internal/provider/faux"
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
