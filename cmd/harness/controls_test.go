package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aaliyan1230/go-rig/internal/agent"
	"github.com/aaliyan1230/go-rig/internal/config"
	"github.com/aaliyan1230/go-rig/internal/policy"
)

func modelServer(t *testing.T, respond func(map[string]any, int) map[string]any) (*httptest.Server, func() []map[string]any) {
	t.Helper()
	var mu sync.Mutex
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		requests = append(requests, request)
		n := len(requests)
		mu.Unlock()
		delta := respond(request, n)
		finish := "stop"
		if delta["tool_calls"] != nil {
			finish = "tool_calls"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		data, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}}})
		fmt.Fprintf(w, "data: %s\n\n", data)
		fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":%q}]}\n\ndata: [DONE]\n\n", finish)
	}))
	t.Cleanup(server.Close)
	return server, func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return append([]map[string]any(nil), requests...)
	}
}

func serverTool(name, id, args string) map[string]any {
	return map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": id, "type": "function", "function": map[string]any{"name": name, "arguments": args}}}}
}

func TestPrintModePausesWithStatusTwoAndCanResume(t *testing.T) {
	server, requests := modelServer(t, func(_ map[string]any, n int) map[string]any {
		if n <= 2 {
			return serverTool("read", fmt.Sprint(n), `{"path":"missing-test-file"}`)
		}
		return map[string]any{"role": "assistant", "content": "resumed"}
	})
	stdin, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := cli([]string{"-p", "work", "--max-turns", "2", "--api-key", "test", "--base-url", server.URL, "--session-dir", root, "--no-context-files"}, stdin, &stdout, &stderr)
	if code != 2 || len(requests()) != 2 || strings.Contains(stdout.String(), "Allow?") || !strings.Contains(stderr.String(), "Progress saved") || strings.Contains(stderr.String(), "error:") {
		t.Fatalf("code=%d out=%s err=%s", code, stdout.String(), stderr.String())
	}
	cfg := config.DefaultConfig()
	var diagnostics bytes.Buffer
	controls := policy.Defaults()
	controls.NoContextFiles = true
	c, err := openConversationControlled(cfg, "test", server.URL, true, root, controls, nil, &diagnostics)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	result, err := c.Continue(context.Background(), nil)
	if err != nil || result.Status != agent.EndCompleted {
		t.Fatal(result, err)
	}
	last := requests()[2]
	messages := last["messages"].([]any)
	users := 0
	for _, m := range messages {
		if m.(map[string]any)["role"] == "user" {
			users++
		}
	}
	if users != 1 || len(messages) != 6 {
		t.Fatalf("continuation duplicated prompt: %+v", messages)
	}
}

func TestControlledStartupTrustToolsAndWorkspace(t *testing.T) {
	server, requests := modelServer(t, func(_ map[string]any, _ int) map[string]any {
		return map[string]any{"role": "assistant", "content": "done"}
	})
	cwd := t.TempDir()
	os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("DISTINCT_PROJECT_INSTRUCTION"), 0600)
	cfg := config.DefaultConfig()
	cfg.Env.WorkingDir = cwd
	root := t.TempDir()
	controls := policy.Defaults()
	controls.PathScope = "workspace"
	list := "read"
	controls.Tools = &list
	var diagnostics bytes.Buffer
	asked := 0
	ask := func(_ context.Context, r policy.Request) (bool, error) {
		asked++
		if r.Kind != "trust" {
			t.Error(r)
		}
		matches, _ := filepath.Glob(filepath.Join(root, "go-rig", "sessions", "*", "*.jsonl"))
		if len(matches) != 0 {
			t.Error("session created before trust decision")
		}
		return true, nil
	}
	c, err := openConversationControlled(cfg, "test", server.URL, false, root, controls, ask, &diagnostics)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Prompt(context.Background(), "work", nil); err != nil {
		t.Fatal(err)
	}
	c.Close()
	request := requests()[0]
	tools := request["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["function"].(map[string]any)["name"] != "read" {
		t.Fatal(tools)
	}
	system := request["messages"].([]any)[0].(map[string]any)["content"].(string)
	if !strings.Contains(system, "DISTINCT_PROJECT_INSTRUCTION") || !strings.Contains(system, "filesystem scope: workspace") || asked != 1 {
		t.Fatal(system, asked)
	}
	// A fresh process configuration reuses saved trust without asking.
	c, err = openConversationControlled(cfg, "test", server.URL, true, root, controls, nil, &diagnostics)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	// Explicit no-context skips discovery and even a malformed trust record.
	files, _ := filepath.Glob(filepath.Join(root, "go-rig", "trust", "*.json"))
	os.WriteFile(files[0], []byte("broken"), 0600)
	controls.NoContextFiles = true
	c, err = openConversationControlled(cfg, "test", server.URL, false, root, controls, nil, &diagnostics)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
}

func TestUnknownProjectInstructionsSkippedWithoutUI(t *testing.T) {
	server, requests := modelServer(t, func(_ map[string]any, _ int) map[string]any {
		return map[string]any{"role": "assistant", "content": "done"}
	})
	cwd := t.TempDir()
	os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("DO_NOT_LOAD_THIS"), 0600)
	cfg := config.DefaultConfig()
	cfg.Env.WorkingDir = cwd
	var diagnostics bytes.Buffer
	c, err := openConversationControlled(cfg, "test", server.URL, false, t.TempDir(), policy.Defaults(), nil, &diagnostics)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Prompt(context.Background(), "hello", nil)
	system := requests()[0]["messages"].([]any)[0].(map[string]any)["content"].(string)
	if strings.Contains(system, "DO_NOT_LOAD_THIS") || !strings.Contains(diagnostics.String(), "skipping untrusted") {
		t.Fatal(system, diagnostics.String())
	}
}

func TestInvalidControlFlagsFailBeforeSessionCreation(t *testing.T) {
	stdin, _ := os.Open(os.DevNull)
	defer stdin.Close()
	for _, args := range [][]string{{"--max-turns", "0"}, {"--bash-timeout", "0"}, {"--tools", "read", "--no-tools"}, {"--approval", "unknown"}, {"--path-scope", "unknown"}, {"--project-trust", "unknown"}, {"--tools", "unknown"}} {
		root := t.TempDir()
		full := append([]string{"-p", "hello", "--api-key", "test", "--session-dir", root}, args...)
		var out, diagnostics bytes.Buffer
		if code := cli(full, stdin, &out, &diagnostics); code != 1 {
			t.Fatal(args, code)
		}
		matches, _ := filepath.Glob(filepath.Join(root, "go-rig", "sessions", "*", "*.jsonl"))
		if len(matches) != 0 {
			t.Fatal("invalid controls created session")
		}
	}
}
