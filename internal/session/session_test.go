package session

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aaliyan1230/go-rig/internal/message"
)

func TestCreateWritesHeaderAndSafeLayout(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(t.TempDir(), "a project")

	store := NewStore(root)
	sess, err := store.Create(context.Background(), cwd)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	meta := sess.Metadata()
	normalizedCwd, err := filepath.Abs(cwd)
	if err != nil {
		t.Fatal(err)
	}
	wantDir := filepath.Join(root, "go-rig", "sessions", cwdKey(filepath.Clean(normalizedCwd)))
	if filepath.Dir(meta.Path) != wantDir {
		t.Fatalf("session path directory = %q, want %q", filepath.Dir(meta.Path), wantDir)
	}
	if filepath.Base(filepath.Dir(meta.Path)) != cwdKey(filepath.Clean(normalizedCwd)) {
		t.Fatalf("unsafe or unexpected cwd key: %q", filepath.Base(filepath.Dir(meta.Path)))
	}
	if strings.Contains(filepath.Base(filepath.Dir(meta.Path)), string(filepath.Separator)) {
		t.Fatal("cwd key contains a path separator")
	}
	if !strings.HasSuffix(meta.Path, ".jsonl") {
		t.Fatalf("session path does not end in .jsonl: %q", meta.Path)
	}

	dirInfo, err := os.Stat(filepath.Dir(meta.Path))
	if err != nil {
		t.Fatal(err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("session directory permissions = %o, want 700", got)
	}
	fileInfo, err := os.Stat(meta.Path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("session file permissions = %o, want 600", got)
	}

	lines := readLines(t, meta.Path)
	if len(lines) != 1 {
		t.Fatalf("new session has %d lines, want one header", len(lines))
	}
	var header sessionHeader
	if err := json.Unmarshal([]byte(lines[0]), &header); err != nil {
		t.Fatal(err)
	}
	if header.Type != "session" || header.Version != currentVersion {
		t.Fatalf("header = %#v, want type session/version %d", header, currentVersion)
	}
	if header.ID != meta.ID || header.Cwd != filepath.Clean(normalizedCwd) || header.Timestamp != meta.CreatedAt {
		t.Fatalf("header does not match metadata: %#v vs %#v", header, meta)
	}
	if _, err := time.Parse(time.RFC3339Nano, header.Timestamp); err != nil {
		t.Fatalf("header timestamp is not RFC3339Nano: %v", err)
	}
}

func TestRoundTripMessagesAndParentChain(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(t.TempDir(), "project")
	store := NewStore(root)
	sess, err := store.Create(context.Background(), cwd)
	if err != nil {
		t.Fatal(err)
	}

	user := message.UserMessage{
		Content:   []message.ContentBlock{message.TextContent{Text: "read config"}},
		Timestamp: 101,
	}
	assistant := message.AssistantMessage{
		Content:    []message.ContentBlock{message.TextContent{Text: "Here is config."}, message.ToolCall{ID: "call-1", Name: "read", Arguments: json.RawMessage(`{"path":"config"}`)}},
		Model:      "faux-1",
		Provider:   "faux",
		Usage:      message.Usage{Input: 2, Output: 3, Cost: message.Cost{Total: 0.25}},
		StopReason: message.StopReasonToolUse,
		Timestamp:  102,
	}
	toolResult := message.ToolResultMessage{
		ToolCallID: "call-1",
		ToolName:   "read",
		Content:    []message.ContentBlock{message.TextContent{Text: "port: 8080"}},
		Timestamp:  103,
	}
	want := []message.AgentMessage{user, assistant, toolResult}
	for _, msg := range want {
		if err := sess.Append(context.Background(), msg); err != nil {
			t.Fatal(err)
		}
	}
	meta := sess.Metadata()
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}

	lines := readLines(t, meta.Path)
	if len(lines) != 4 {
		t.Fatalf("session has %d lines, want header plus 3 messages", len(lines))
	}
	var previousID string
	seenIDs := make(map[string]bool)
	for i, line := range lines[1:] {
		var entry messageEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatal(err)
		}
		if entry.ID == "" || seenIDs[entry.ID] {
			t.Fatalf("entry %d has missing or duplicate id %q", i, entry.ID)
		}
		seenIDs[entry.ID] = true
		if i == 0 {
			if entry.ParentID != nil {
				t.Fatalf("first parentId = %q, want null", *entry.ParentID)
			}
		} else if entry.ParentID == nil || *entry.ParentID != previousID {
			t.Fatalf("entry %d parentId = %v, want %q", i, entry.ParentID, previousID)
		}
		if _, err := time.Parse(time.RFC3339Nano, entry.Timestamp); err != nil {
			t.Fatalf("entry %d timestamp is invalid: %v", i, err)
		}
		previousID = entry.ID
	}

	reopened, err := store.OpenLatest(context.Background(), cwd)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got := reopened.Messages(); !reflect.DeepEqual(got, want) {
		t.Fatalf("replayed messages = %#v, want %#v", got, want)
	}
}

func TestReopenAndAppendContinuesParentChain(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(t.TempDir(), "project")
	store := NewStore(root)
	first, err := store.Create(context.Background(), cwd)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Append(context.Background(), userMessage("first")); err != nil {
		t.Fatal(err)
	}
	path := first.Metadata().Path
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.OpenLatest(context.Background(), cwd)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Append(context.Background(), userMessage("second")); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}

	lines := readLines(t, path)
	if len(lines) != 3 {
		t.Fatalf("session has %d lines, want header plus 2 messages", len(lines))
	}
	var firstEntry, secondEntry messageEntry
	if err := json.Unmarshal([]byte(lines[1]), &firstEntry); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(lines[2]), &secondEntry); err != nil {
		t.Fatal(err)
	}
	if secondEntry.ParentID == nil || *secondEntry.ParentID != firstEntry.ID {
		t.Fatalf("reopened append parentId = %v, want %q", secondEntry.ParentID, firstEntry.ID)
	}
}

func TestOpenLatestUsesModificationTimeAndIsolatesCwd(t *testing.T) {
	root := t.TempDir()
	cwdA := filepath.Join(t.TempDir(), "one", "project")
	cwdB := filepath.Join(t.TempDir(), "two", "project")
	store := NewStore(root)

	old, err := store.Create(context.Background(), cwdA)
	if err != nil {
		t.Fatal(err)
	}
	oldID := old.Metadata().ID
	oldPath := old.Metadata().Path
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	newer, err := store.Create(context.Background(), cwdA)
	if err != nil {
		t.Fatal(err)
	}
	newID := newer.Metadata().ID
	newPath := newer.Metadata().Path
	if err := newer.Close(); err != nil {
		t.Fatal(err)
	}
	other, err := store.Create(context.Background(), cwdB)
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}

	base := time.Now().Add(-time.Hour)
	if err := os.Chtimes(oldPath, base, base); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newPath, base.Add(time.Minute), base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	latest, err := store.OpenLatest(context.Background(), cwdA)
	if err != nil {
		t.Fatal(err)
	}
	defer latest.Close()
	if got := latest.Metadata().ID; got != newID {
		t.Fatalf("latest session = %q, want %q", got, newID)
	}
	if latest.Metadata().ID == oldID {
		t.Fatal("selected the older session")
	}

	otherLatest, err := store.OpenLatest(context.Background(), cwdB)
	if err != nil {
		t.Fatal(err)
	}
	defer otherLatest.Close()
	if otherLatest.Metadata().Cwd == latest.Metadata().Cwd {
		t.Fatal("different working directories were not isolated")
	}
}

func TestOpenLatestDoesNotSkipCorruptLatest(t *testing.T) {
	root := t.TempDir()
	cwd := t.TempDir()
	store := NewStore(root)
	valid, err := store.Create(context.Background(), cwd)
	if err != nil {
		t.Fatal(err)
	}
	validPath := valid.Metadata().Path
	if err := valid.Close(); err != nil {
		t.Fatal(err)
	}

	corruptPath := filepath.Join(filepath.Dir(validPath), "99999999T999999.000000000Z_corrupt.jsonl")
	if err := os.WriteFile(corruptPath, []byte(`{"type":"session","version":99}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-time.Hour)
	newTime := oldTime.Add(time.Minute)
	if err := os.Chtimes(validPath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(corruptPath, newTime, newTime); err != nil {
		t.Fatal(err)
	}

	_, err = store.OpenLatest(context.Background(), cwd)
	if err == nil || !strings.Contains(err.Error(), "invalid session") {
		t.Fatalf("error = %v, want corruption from newest file", err)
	}
}

func TestOpenLatestNoSession(t *testing.T) {
	store := NewStore(t.TempDir())
	cwd := filepath.Join(t.TempDir(), "missing")
	_, err := store.OpenLatest(context.Background(), cwd)
	if !errors.Is(err, ErrNoSession) {
		t.Fatalf("error = %v, want ErrNoSession", err)
	}

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "go-rig", "sessions", cwdKey(cwd)), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(root).OpenLatest(context.Background(), cwd); !errors.Is(err, ErrNoSession) {
		t.Fatalf("empty directory error = %v, want ErrNoSession", err)
	}
}

func TestOpenLatestRejectsCorruptSessions(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
	}{
		{
			name: "malformed header",
			lines: []string{
				`{"type":"session","version":2,"id":"session-1","timestamp":"2026-09-13T10:00:00Z","cwd":"/project"}`,
			},
		},
		{
			name: "malformed entry",
			lines: []string{
				validHeader("/project"),
				`{"type":"message","id":`,
			},
		},
		{
			name: "unknown role",
			lines: []string{
				validHeader("/project"),
				validEntry("entry-1", nil, `{"role":"mystery","content":[],"timestamp":1}`),
			},
		},
		{
			name: "duplicate id",
			lines: []string{
				validHeader("/project"),
				validEntry("entry-1", nil, `{"role":"user","content":[],"timestamp":1}`),
				validEntry("entry-1", stringPtr("entry-1"), `{"role":"user","content":[],"timestamp":2}`),
			},
		},
		{
			name: "broken parent",
			lines: []string{
				validHeader("/project"),
				validEntry("entry-1", nil, `{"role":"user","content":[],"timestamp":1}`),
				validEntry("entry-2", stringPtr("not-entry-1"), `{"role":"user","content":[],"timestamp":2}`),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			cwd := "/project"
			writeFixture(t, root, cwd, tc.lines)
			_, err := NewStore(root).OpenLatest(context.Background(), cwd)
			if err == nil || !strings.Contains(err.Error(), "invalid session") {
				t.Fatalf("error = %v, want an invalid session error", err)
			}
		})
	}
}

func TestAppendAfterCloseFails(t *testing.T) {
	sess, err := NewStore(t.TempDir()).Create(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(context.Background(), userMessage("too late")); err == nil {
		t.Fatal("append after close unexpectedly succeeded")
	}
}

func userMessage(text string) message.UserMessage {
	return message.UserMessage{Content: []message.ContentBlock{message.TextContent{Text: text}}, Timestamp: time.Now().UnixMilli()}
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return lines
}

func validHeader(cwd string) string {
	return `{"type":"session","version":1,"id":"session-1","timestamp":"2026-09-13T10:00:00Z","cwd":"` + cwd + `"}`
}

func validEntry(id string, parentID *string, msg string) string {
	parent := "null"
	if parentID != nil {
		parent = `"` + *parentID + `"`
	}
	return `{"type":"message","id":"` + id + `","parentId":` + parent + `,"timestamp":"2026-09-13T10:00:01Z","message":` + msg + `}`
}

func writeFixture(t *testing.T, root, cwd string, lines []string) {
	t.Helper()
	normalized, err := normalizeCwd(cwd)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "go-rig", "sessions", cwdKey(normalized))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "20260913T100000.000000000Z_fixture.jsonl")
	data := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func stringPtr(value string) *string {
	return &value
}
