package prompt_test

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aaliyan1230/go-rig/internal/agent"
	"github.com/aaliyan1230/go-rig/internal/env"
	"github.com/aaliyan1230/go-rig/internal/prompt"
	"github.com/aaliyan1230/go-rig/internal/tools"
)

var update = flag.Bool("update", false, "rewrite golden files")

// stubTool is a minimal agent.Tool that does not implement
// prompt.Contributor; contributingTool below adds it.
type stubTool struct{ name string }

func (s stubTool) ToolName() string            { return s.name }
func (s stubTool) ToolDescription() string     { return "stub" }
func (s stubTool) ToolSchema() json.RawMessage { return json.RawMessage(`{}`) }
func (s stubTool) Execute(context.Context, string, json.RawMessage) (agent.ToolResult, error) {
	return agent.ToolResult{}, nil
}

type contributingTool struct {
	stubTool
	snippet    string
	guidelines []string
}

func (c contributingTool) PromptSnippet() string      { return c.snippet }
func (c contributingTool) PromptGuidelines() []string { return c.guidelines }

func builtinTools() []agent.Tool {
	e := env.NewLocalEnv("/project")
	return []agent.Tool{tools.NewReadTool(e), tools.NewWriteTool(e), tools.NewEditTool(e), tools.NewBashTool(e)}
}

func TestBuildDefaultMatchesGolden(t *testing.T) {
	got := prompt.Build(prompt.Options{
		Tools:        builtinTools(),
		ContextFiles: []prompt.ContextFile{{Path: "/project/AGENTS.md", Content: "Run go test before finishing."}},
		Cwd:          "/project",
	})

	golden := filepath.Join("testdata", "default.golden")
	if *update {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden (run with -update to create): %v", err)
	}
	if got != string(want) {
		t.Errorf("prompt differs from %s (run with -update if intended)\n--- got ---\n%s\n--- want ---\n%s", golden, got, want)
	}
}

func TestBuildCustomPromptKeepsContextAndCwd(t *testing.T) {
	got := prompt.Build(prompt.Options{
		CustomPrompt: "You are a reviewer.",
		Tools:        builtinTools(),
		ContextFiles: []prompt.ContextFile{{Path: "/p/AGENTS.md", Content: "rules"}},
		Cwd:          "/p",
	})

	if !strings.HasPrefix(got, "You are a reviewer.\n\n") {
		t.Errorf("custom prompt should lead:\n%s", got)
	}
	for _, absent := range []string{"<tools>", "<rules>", "go-rig, a coding agent harness"} {
		if strings.Contains(got, absent) {
			t.Errorf("custom prompt should replace %q:\n%s", absent, got)
		}
	}
	for _, present := range []string{`<project_instructions path="/p/AGENTS.md">`, "<cwd>\n/p\n</cwd>"} {
		if !strings.Contains(got, present) {
			t.Errorf("missing %q:\n%s", present, got)
		}
	}
}

func TestBuildListsOnlyContributingTools(t *testing.T) {
	got := prompt.Build(prompt.Options{Tools: []agent.Tool{
		stubTool{name: "silent"},
		contributingTool{stubTool: stubTool{name: "loud"}, snippet: "Says things"},
	}})

	if !strings.Contains(got, "- loud: Says things") {
		t.Errorf("contributing tool missing:\n%s", got)
	}
	if strings.Contains(got, "silent") {
		t.Errorf("tool without a snippet should not be listed:\n%s", got)
	}
}

func TestBuildWithoutSnippetsSaysNone(t *testing.T) {
	got := prompt.Build(prompt.Options{})
	if !strings.Contains(got, "<tools>\n(none)\n") {
		t.Errorf("expected (none) tool list:\n%s", got)
	}
}

func TestBuildDeduplicatesGuidelines(t *testing.T) {
	got := prompt.Build(prompt.Options{Tools: []agent.Tool{
		contributingTool{stubTool: stubTool{name: "a"}, snippet: "A", guidelines: []string{"Shared rule", "Be concise in your responses"}},
		contributingTool{stubTool: stubTool{name: "b"}, snippet: "B", guidelines: []string{"  Shared rule  ", ""}},
	}})

	if n := strings.Count(got, "Shared rule"); n != 1 {
		t.Errorf("Shared rule appears %d times, want 1:\n%s", n, got)
	}
	if n := strings.Count(got, "Be concise in your responses"); n != 1 {
		t.Errorf("fixed rule appears %d times, want 1:\n%s", n, got)
	}
}

func TestBuildBashRuleDependsOnSearchTools(t *testing.T) {
	const bashRule = "Use bash for file operations like ls, rg, find"
	bash := stubTool{name: "bash"}

	if got := prompt.Build(prompt.Options{Tools: []agent.Tool{bash}}); !strings.Contains(got, bashRule) {
		t.Errorf("bash-only tool set should get bash rule:\n%s", got)
	}
	if got := prompt.Build(prompt.Options{Tools: []agent.Tool{bash, stubTool{name: "grep"}}}); strings.Contains(got, bashRule) {
		t.Errorf("grep present: bash rule should be omitted:\n%s", got)
	}
}

func TestBuildOmitsEmptySections(t *testing.T) {
	got := prompt.Build(prompt.Options{Tools: builtinTools()})
	if strings.Contains(got, "<project_context>") || strings.Contains(got, "<cwd>") {
		t.Errorf("empty context and cwd should be omitted:\n%s", got)
	}
}

func TestBuildNormalizesWindowsCwd(t *testing.T) {
	got := prompt.Build(prompt.Options{Cwd: `C:\work\repo`})
	if !strings.Contains(got, "<cwd>\nC:/work/repo\n</cwd>") {
		t.Errorf("cwd not normalized:\n%s", got)
	}
}
