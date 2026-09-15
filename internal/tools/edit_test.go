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

func TestEditToolSingleReplacementAndDeletion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	if err := os.WriteFile(path, []byte("first\nremove me\nlast\n"), 0644); err != nil {
		t.Fatal(err)
	}
	tool := NewEditTool(env.NewLocalEnv(dir))

	result, err := tool.Execute(context.Background(), "call-1", json.RawMessage(`{"path":"main.go","oldText":"remove me\n","newText":"replaced\n"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := toolResultText(t, result.Content); !strings.Contains(got, "1 replacement") {
		t.Fatalf("success result = %q", got)
	}
	if _, err := tool.Execute(context.Background(), "call-2", json.RawMessage(`{"path":"main.go","oldText":"first\n","newText":""}`)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "replaced\nlast\n" {
		t.Fatalf("edited content = %q", data)
	}
}

func TestEditToolBatchUsesOriginalAndPreservesOrder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	original := "alpha\nsecond\nomega\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	tool := NewEditTool(env.NewLocalEnv(dir))
	params := map[string]any{
		"path": "main.go",
		"edits": []map[string]string{
			{"oldText": "alpha", "newText": "first"},
			{"oldText": "omega", "newText": "last"},
		},
	}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	result, err := tool.Execute(context.Background(), "call-1", raw)
	if err != nil {
		t.Fatal(err)
	}
	if got := toolResultText(t, result.Content); !strings.Contains(got, "2 replacement") {
		t.Fatalf("success result = %q", got)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "first\nsecond\nlast\n" {
		t.Fatalf("batch content = %q", data)
	}
}

func TestEditToolRejectsInvalidBatchesAndLeavesFileUnchanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	original := "aaa\nbbb\nbbb\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	tool := NewEditTool(env.NewLocalEnv(dir))
	tests := map[string]string{
		"missing match":    `{"path":"file.txt","oldText":"missing","newText":"x"}`,
		"duplicate match":  `{"path":"file.txt","oldText":"bb","newText":"x"}`,
		"both forms":       `{"path":"file.txt","oldText":"aaa","newText":"x","edits":[{"oldText":"bbb","newText":"y"}]}`,
		"null batch mixed": `{"path":"file.txt","oldText":"aaa","newText":"x","edits":null}`,
		"empty batch":      `{"path":"file.txt","edits":[]}`,
		"missing new":      `{"path":"file.txt","oldText":"aaa"}`,
		"empty old":        `{"path":"file.txt","oldText":"","newText":"x"}`,
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := tool.Execute(context.Background(), "call-1", json.RawMessage(input)); err == nil {
				t.Fatalf("expected an error for %s", input)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != original {
				t.Fatalf("failed edit changed file to %q", data)
			}
		})
	}
}

func TestEditToolRejectsOverlappingBatchRanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(path, []byte("abcd"), 0644); err != nil {
		t.Fatal(err)
	}
	tool := NewEditTool(env.NewLocalEnv(dir))
	_, err := tool.Execute(context.Background(), "call-1", json.RawMessage(`{"path":"file.txt","edits":[{"oldText":"abc","newText":"x"},{"oldText":"bcd","newText":"y"}]}`))
	if err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("error = %v, want overlapping batch ranges to be rejected", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "abcd" {
		t.Fatalf("overlap failure changed file to %q", data)
	}
}

func TestEditToolRejectsOverlappingMatchesWithinOneOldText(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(path, []byte("aaa"), 0644); err != nil {
		t.Fatal(err)
	}
	tool := NewEditTool(env.NewLocalEnv(dir))
	_, err := tool.Execute(context.Background(), "call-1", json.RawMessage(`{"path":"file.txt","oldText":"aa","newText":"x"}`))
	if err == nil || !strings.Contains(err.Error(), "exactly once") {
		t.Fatalf("error = %v, want overlapping occurrences to be rejected", err)
	}
}

func TestEditToolRejectsNonUTF8File(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "binary"), []byte{0xff}, 0644); err != nil {
		t.Fatal(err)
	}
	tool := NewEditTool(env.NewLocalEnv(dir))
	_, err := tool.Execute(context.Background(), "call-1", json.RawMessage(`{"path":"binary","oldText":"x","newText":"y"}`))
	if err == nil || !strings.Contains(err.Error(), "not valid UTF-8") {
		t.Fatalf("error = %v, want UTF-8 validation error", err)
	}
}
