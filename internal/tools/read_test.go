package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/aaliyan1230/go-rig/internal/env"
	"github.com/aaliyan1230/go-rig/internal/message"
)

func TestReadToolReadsRelativeUTF8File(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello, world\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := NewReadTool(env.NewLocalEnv(dir))
	result, err := tool.Execute(context.Background(), "call-1", json.RawMessage(`{"path":"hello.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := toolResultText(t, result.Content); got != "hello, world\n" {
		t.Fatalf("unexpected content: %q", got)
	}
}

func TestReadToolSupportsOffsetAndLimit(t *testing.T) {
	dir := t.TempDir()
	content := "one\ntwo\nthree\nfour\nfive\n"
	if err := os.WriteFile(filepath.Join(dir, "lines.txt"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	tool := NewReadTool(env.NewLocalEnv(dir))
	result, err := tool.Execute(context.Background(), "call-1", json.RawMessage(`{"path":"lines.txt","offset":2,"limit":2}`))
	if err != nil {
		t.Fatal(err)
	}
	got := toolResultText(t, result.Content)
	if !strings.Contains(got, "two\nthree\n") {
		t.Fatalf("page content = %q, want lines two and three", got)
	}
	if !strings.Contains(got, "Use offset=4 to continue") {
		t.Fatalf("page is missing continuation hint: %q", got)
	}
}

func TestReadToolRejectsInvalidPagingArguments(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "lines.txt"), []byte("one\ntwo\n"), 0644); err != nil {
		t.Fatal(err)
	}
	tool := NewReadTool(env.NewLocalEnv(dir))
	for _, input := range []string{
		`{"path":"lines.txt","offset":0}`,
		`{"path":"lines.txt","offset":-1}`,
		`{"path":"lines.txt","limit":0}`,
		`{"path":"lines.txt","limit":-2}`,
		`{"path":"lines.txt","offset":1.5}`,
		`{"path":"lines.txt","limit":null}`,
	} {
		t.Run(input, func(t *testing.T) {
			_, err := tool.Execute(context.Background(), "call-1", json.RawMessage(input))
			if err == nil {
				t.Fatalf("expected an error for %s", input)
			}
		})
	}
}

func TestReadToolRejectsOffsetBeyondEnd(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "lines.txt"), []byte("one\ntwo\n"), 0644); err != nil {
		t.Fatal(err)
	}
	tool := NewReadTool(env.NewLocalEnv(dir))
	_, err := tool.Execute(context.Background(), "call-1", json.RawMessage(`{"path":"lines.txt","offset":3}`))
	if err == nil || !strings.Contains(err.Error(), "offset 3 is beyond end of file") {
		t.Fatalf("error = %v, want a clear out-of-range error", err)
	}
}

func TestReadToolAllowsOffsetOneForEmptyFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "empty.txt"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	tool := NewReadTool(env.NewLocalEnv(dir))
	result, err := tool.Execute(context.Background(), "call-1", json.RawMessage(`{"path":"empty.txt","offset":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := toolResultText(t, result.Content); got != "" {
		t.Fatalf("empty file result = %q, want empty string", got)
	}
}

func TestReadToolRejectsInvalidArguments(t *testing.T) {
	tool := NewReadTool(env.NewLocalEnv(t.TempDir()))
	for _, input := range []string{
		`{}`,
		`{"path":""}`,
		`{"path":"file.txt","extra":true}`,
		`{"path":123}`,
		`{"path":"file.txt"} {}`,
		`not-json`,
	} {
		t.Run(input, func(t *testing.T) {
			_, err := tool.Execute(context.Background(), "call-1", json.RawMessage(input))
			if err == nil {
				t.Fatalf("expected an error for %s", input)
			}
		})
	}
}

func TestReadToolTruncatesAtLineLimit(t *testing.T) {
	dir := t.TempDir()
	content := strings.Repeat("line\n", DefaultReadMaxLines+1)
	if err := os.WriteFile(filepath.Join(dir, "large.txt"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	tool := NewReadTool(env.NewLocalEnv(dir))
	result, err := tool.Execute(context.Background(), "call-1", json.RawMessage(`{"path":"large.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	got := toolResultText(t, result.Content)
	if !strings.Contains(got, "[truncated: showing first 2000 of 2001 lines; line limit") {
		t.Fatalf("missing line-limit notice: %q", got[len(got)-min(len(got), 180):])
	}
}

func TestReadToolTruncatesAtByteLimitWithoutPartialUTF8(t *testing.T) {
	dir := t.TempDir()
	content := strings.Repeat("界", DefaultReadMaxBytes/len("界")+1)
	if err := os.WriteFile(filepath.Join(dir, "wide.txt"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	tool := NewReadTool(env.NewLocalEnv(dir))
	result, err := tool.Execute(context.Background(), "call-1", json.RawMessage(`{"path":"wide.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	got := toolResultText(t, result.Content)
	if !strings.Contains(got, "showing first 0 of 1 lines; byte limit") {
		t.Fatalf("missing byte-limit notice: %q", got)
	}
	if !utf8.ValidString(got) {
		t.Fatal("tool output is not valid UTF-8")
	}
}

func TestReadToolRejectsNonUTF8File(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "binary.dat"), []byte{0xff, 0xfe}, 0644); err != nil {
		t.Fatal(err)
	}

	tool := NewReadTool(env.NewLocalEnv(dir))
	_, err := tool.Execute(context.Background(), "call-1", json.RawMessage(`{"path":"binary.dat"}`))
	if err == nil || !strings.Contains(err.Error(), "not valid UTF-8") {
		t.Fatalf("expected UTF-8 error, got %v", err)
	}
}

func TestReadToolReportsMissingFilesAndDirectories(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "folder"), 0755); err != nil {
		t.Fatal(err)
	}
	tool := NewReadTool(env.NewLocalEnv(dir))

	for _, path := range []string{"missing.txt", "folder"} {
		t.Run(path, func(t *testing.T) {
			_, err := tool.Execute(context.Background(), "call-1", json.RawMessage(`{"path":"`+path+`"}`))
			if err == nil {
				t.Fatalf("expected error for %s", path)
			}
		})
	}
}

func toolResultText(t *testing.T, blocks []message.ContentBlock) string {
	t.Helper()
	if len(blocks) != 1 {
		t.Fatalf("expected one content block, got %d", len(blocks))
	}
	text, ok := blocks[0].(message.TextContent)
	if !ok {
		t.Fatalf("expected text content, got %T", blocks[0])
	}
	return text.Text
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
