package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aaliyan1230/go-rig/internal/env"
)

func TestWriteToolCreatesParentsAndReportsBytes(t *testing.T) {
	dir := t.TempDir()
	tool := NewWriteTool(env.NewLocalEnv(dir))
	content := "package config\n"
	result, err := tool.Execute(context.Background(), "call-1", json.RawMessage(`{"path":"src/config.go","content":"package config\n"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := toolResultText(t, result.Content); !strings.Contains(got, "wrote 15 bytes") || !strings.Contains(got, "src/config.go") {
		t.Fatalf("success result = %q, want path and byte count", got)
	}
	data, err := os.ReadFile(filepath.Join(dir, "src", "config.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != content {
		t.Fatalf("file content = %q, want %q", data, content)
	}
	info, err := os.Stat(filepath.Join(dir, "src"))
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatal("parent path is not a directory")
	}
}

func TestWriteToolOverwritesAndAllowsEmptyContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	tool := NewWriteTool(env.NewLocalEnv(dir))
	if _, err := tool.Execute(context.Background(), "call-1", json.RawMessage(`{"path":"note.txt","content":"new"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Execute(context.Background(), "call-2", json.RawMessage(`{"path":"note.txt","content":""}`)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 {
		t.Fatalf("file content = %q, want empty", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("overwrite changed permissions to %o, want 0600", got)
	}
}

func TestWriteToolRejectsInvalidArgumentsWithoutWriting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "existing.txt")
	if err := os.WriteFile(path, []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	tool := NewWriteTool(env.NewLocalEnv(dir))
	for _, input := range []string{
		`{"path":"existing.txt"}`,
		`{"path":"existing.txt","content":null}`,
		`{"path":"","content":"changed"}`,
		`{"path":"existing.txt","content":123}`,
		`{"path":"existing.txt","content":"changed","extra":true}`,
	} {
		t.Run(input, func(t *testing.T) {
			if _, err := tool.Execute(context.Background(), "call-1", json.RawMessage(input)); err == nil {
				t.Fatalf("expected invalid arguments error for %s", input)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != "keep" {
				t.Fatalf("invalid call changed file to %q", data)
			}
		})
	}
}
