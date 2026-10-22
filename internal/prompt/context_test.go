package prompt_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aaliyan1230/go-rig/internal/env"
	"github.com/aaliyan1230/go-rig/internal/prompt"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// loadUnder loads context files for cwd and keeps only those under root, so
// a real AGENTS.md somewhere above the temp directory cannot affect a test.
func loadUnder(t *testing.T, root, cwd string) ([]prompt.ContextFile, []error) {
	t.Helper()
	files, warnings := prompt.LoadContextFiles(context.Background(), env.NewLocalEnv(cwd), cwd)
	var kept []prompt.ContextFile
	for _, f := range files {
		if strings.HasPrefix(f.Path, root+string(filepath.Separator)) {
			kept = append(kept, f)
		}
	}
	return kept, warnings
}

func paths(files []prompt.ContextFile, root string) []string {
	out := make([]string, len(files))
	for i, f := range files {
		rel, _ := filepath.Rel(root, f.Path)
		out[i] = filepath.ToSlash(rel)
	}
	return out
}

func TestLoadContextFilesOrdersOutermostFirst(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "AGENTS.md"), "root")
	writeFile(t, filepath.Join(root, "a", "CLAUDE.md"), "a")
	writeFile(t, filepath.Join(root, "a", "b", "AGENTS.md"), "b")

	files, warnings := loadUnder(t, root, filepath.Join(root, "a", "b"))
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	got := strings.Join(paths(files, root), ",")
	if want := "AGENTS.md,a/CLAUDE.md,a/b/AGENTS.md"; got != want {
		t.Fatalf("order = %s, want %s", got, want)
	}
	if files[0].Content != "root" || files[2].Content != "b" {
		t.Errorf("contents not loaded: %+v", files)
	}
}

func TestLoadContextFilesPrefersAgentsOverClaude(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "AGENTS.md"), "agents")
	writeFile(t, filepath.Join(root, "CLAUDE.md"), "claude")

	files, _ := loadUnder(t, root, root)
	if len(files) != 1 || files[0].Content != "agents" {
		t.Fatalf("want only AGENTS.md, got %+v", files)
	}
}

func TestLoadContextFilesSkipsDirectoryNamedAgents(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "AGENTS.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "CLAUDE.md"), "claude")

	files, warnings := loadUnder(t, root, root)
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if len(files) != 1 || files[0].Content != "claude" {
		t.Fatalf("want CLAUDE.md fallback, got %+v", files)
	}
}

func TestLoadContextFilesStripsBOM(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "AGENTS.md"), "\uFEFFhello")

	files, _ := loadUnder(t, root, root)
	if len(files) != 1 || files[0].Content != "hello" {
		t.Fatalf("BOM not stripped: %+v", files)
	}
}

func TestLoadContextFilesWarnsOnUnreadableAndContinues(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read files regardless of permissions")
	}
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "AGENTS.md"), "root")
	locked := filepath.Join(root, "a", "AGENTS.md")
	writeFile(t, locked, "secret")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o644) })

	files, warnings := loadUnder(t, root, filepath.Join(root, "a"))
	if len(warnings) != 1 || !strings.Contains(warnings[0].Error(), locked) {
		t.Fatalf("want one warning naming %s, got %v", locked, warnings)
	}
	if len(files) != 1 || files[0].Content != "root" {
		t.Fatalf("readable ancestor should still load, got %+v", files)
	}
}

func TestLoadContextFilesNoneFound(t *testing.T) {
	root := t.TempDir()
	files, warnings := loadUnder(t, root, root)
	if len(files) != 0 || len(warnings) != 0 {
		t.Fatalf("want nothing, got files=%+v warnings=%v", files, warnings)
	}
}
