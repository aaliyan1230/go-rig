// Package session provides the append-only JSONL transcript used by the
// command-line harness.
//
// A session deliberately knows nothing about providers or the agent loop. It
// stores the messages emitted by the loop and can replay them into a future
// AgentContext.
package session

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/aaliyan1230/go-rig/internal/message"
)

const currentVersion = 1

// ErrNoSession reports that no session exists for a working directory.
var ErrNoSession = errors.New("no session found")

// Metadata describes the file-backed session.
type Metadata struct {
	ID        string
	Cwd       string
	Path      string
	CreatedAt string
}

// Store locates sessions below one user-configurable root. The root passed to
// NewStore is a base directory; the store creates go-rig/sessions below it.
type Store struct {
	root string
}

// NewStore creates a session store rooted at root. Files are created lazily by
// Create or OpenLatest.
func NewStore(root string) *Store {
	return &Store{root: root}
}

// Session is an open append-only JSONL transcript. Phase 3 is intentionally
// single-process and linear: it does not coordinate multiple writers or
// expose branch selection yet.
type Session struct {
	mu       sync.Mutex
	metadata Metadata
	file     *os.File
	messages []message.AgentMessage
	lastID   string
	closed   bool
	broken   bool
}

type sessionHeader struct {
	Type      string `json:"type"`
	Version   int    `json:"version"`
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	Cwd       string `json:"cwd"`
}

type messageEntry struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	ParentID  *string         `json:"parentId"`
	Timestamp string          `json:"timestamp"`
	Message   json.RawMessage `json:"message"`
}

// Create makes a new session for cwd and writes its header as the first JSONL
// line.
func (s *Store) Create(ctx context.Context, cwd string) (*Session, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}

	normalizedCwd, err := normalizeCwd(cwd)
	if err != nil {
		return nil, err
	}
	root, err := s.sessionsRoot()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(root, cwdKey(normalizedCwd))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create session directory %q: %w", dir, err)
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}

	createdAt := time.Now().UTC()
	createdAtString := createdAt.Format(time.RFC3339Nano)
	id, err := newID()
	if err != nil {
		return nil, fmt.Errorf("generate session id: %w", err)
	}
	path := filepath.Join(dir, fileTimestamp(createdAt)+"_"+id+".jsonl")

	header := sessionHeader{
		Type:      "session",
		Version:   currentVersion,
		ID:        id,
		Timestamp: createdAtString,
		Cwd:       normalizedCwd,
	}
	data, err := json.Marshal(header)
	if err != nil {
		return nil, fmt.Errorf("encode session header: %w", err)
	}

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create session file %q: %w", path, err)
	}
	if err := writeLine(file, data); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("write session header %q: %w", path, err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("sync session header %q: %w", path, err)
	}

	return &Session{
		metadata: Metadata{ID: id, Cwd: normalizedCwd, Path: path, CreatedAt: createdAtString},
		file:     file,
	}, nil
}

// OpenLatest opens the newest modified session for cwd and replays its
// messages.
func (s *Store) OpenLatest(ctx context.Context, cwd string) (*Session, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}

	normalizedCwd, err := normalizeCwd(cwd)
	if err != nil {
		return nil, err
	}
	root, err := s.sessionsRoot()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(root, cwdKey(normalizedCwd))
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w for %q", ErrNoSession, normalizedCwd)
		}
		return nil, fmt.Errorf("list sessions in %q: %w", dir, err)
	}

	var latestPath string
	var latestModTime time.Time
	var found bool
	for _, entry := range entries {
		if err := contextErr(ctx); err != nil {
			return nil, err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		info, statErr := entry.Info()
		if statErr != nil {
			return nil, fmt.Errorf("stat session %q: %w", entry.Name(), statErr)
		}
		if !found || info.ModTime().After(latestModTime) ||
			(info.ModTime().Equal(latestModTime) && entry.Name() > filepath.Base(latestPath)) {
			latestPath = filepath.Join(dir, entry.Name())
			latestModTime = info.ModTime()
			found = true
		}
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("%w for %q", ErrNoSession, normalizedCwd)
	}

	return openSession(ctx, latestPath, normalizedCwd)
}

// Metadata returns a copy of the session metadata.
func (s *Session) Metadata() Metadata {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.metadata
}

// Messages returns a copy of the replayable messages currently in the file.
func (s *Session) Messages() []message.AgentMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]message.AgentMessage(nil), s.messages...)
}

// Append serializes m as one message entry and appends it to the session. The
// write and fsync complete before the in-memory replay state is updated.
func (s *Session) Append(ctx context.Context, m message.AgentMessage) error {
	if err := contextErr(ctx); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := contextErr(ctx); err != nil {
		return err
	}
	if s.closed {
		return errors.New("session is closed")
	}
	if s.broken {
		return errors.New("session is unavailable after a previous write failure")
	}
	if s.file == nil {
		return errors.New("session is not open")
	}
	if m == nil {
		return errors.New("cannot append a nil message")
	}

	providerMessage, storedMessage, err := concreteMessage(m)
	if err != nil {
		return err
	}
	messageData, err := message.MarshalMessage(providerMessage)
	if err != nil {
		return fmt.Errorf("encode session message: %w", err)
	}
	id, err := newID()
	if err != nil {
		return fmt.Errorf("generate entry id: %w", err)
	}
	var parentID *string
	if s.lastID != "" {
		parent := s.lastID
		parentID = &parent
	}
	entry := messageEntry{
		Type:      "message",
		ID:        id,
		ParentID:  parentID,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Message:   messageData,
	}
	entryData, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode session entry: %w", err)
	}
	if err := writeLine(s.file, entryData); err != nil {
		s.broken = true
		return fmt.Errorf("append session entry %q: %w", s.metadata.Path, err)
	}
	if err := s.file.Sync(); err != nil {
		s.broken = true
		return fmt.Errorf("sync session entry %q: %w", s.metadata.Path, err)
	}

	s.lastID = id
	s.messages = append(s.messages, storedMessage)
	return nil
}

// Close releases the append handle. It is safe to call more than once.
func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.file == nil {
		return nil
	}
	return s.file.Close()
}

func (s *Store) sessionsRoot() (string, error) {
	if strings.TrimSpace(s.root) == "" {
		return "", errors.New("session root is empty")
	}
	abs, err := filepath.Abs(s.root)
	if err != nil {
		return "", fmt.Errorf("resolve session root %q: %w", s.root, err)
	}
	return filepath.Join(abs, "go-rig", "sessions"), nil
}

func openSession(ctx context.Context, path, expectedCwd string) (*Session, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	header, messages, lastID, err := readSession(ctx, path)
	if err != nil {
		return nil, err
	}
	if header.Cwd != expectedCwd {
		return nil, invalidSession(path, 1, "cwd does not match the requested working directory")
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open session file %q for append: %w", path, err)
	}
	return &Session{
		metadata: Metadata{ID: header.ID, Cwd: header.Cwd, Path: path, CreatedAt: header.Timestamp},
		file:     file,
		messages: messages,
		lastID:   lastID,
	}, nil
}

func readSession(ctx context.Context, path string) (sessionHeader, []message.AgentMessage, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return sessionHeader{}, nil, "", fmt.Errorf("open session %q: %w", path, err)
	}
	defer file.Close()

	reader := bufio.NewReader(file)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return sessionHeader{}, nil, "", fmt.Errorf("read session header %q: %w", path, err)
	}
	if strings.TrimSpace(line) == "" {
		return sessionHeader{}, nil, "", invalidSession(path, 1, "missing session header")
	}
	header, err := parseHeader(line, path)
	if err != nil {
		return sessionHeader{}, nil, "", err
	}

	messages := make([]message.AgentMessage, 0)
	seen := map[string]struct{}{header.ID: {}}
	lastID := ""
	lineNumber := 1
	for {
		if err := contextErr(ctx); err != nil {
			return sessionHeader{}, nil, "", err
		}
		line, readErr := reader.ReadString('\n')
		lineNumber++
		if len(strings.TrimSpace(line)) > 0 {
			entry, parseErr := parseEntry(line, path, lineNumber, lastID, seen)
			if parseErr != nil {
				return sessionHeader{}, nil, "", parseErr
			}
			messages = append(messages, entry.message)
			seen[entry.id] = struct{}{}
			lastID = entry.id
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return sessionHeader{}, nil, "", fmt.Errorf("read session %q: %w", path, readErr)
		}
	}
	return header, messages, lastID, nil
}

func parseHeader(line, path string) (sessionHeader, error) {
	var header sessionHeader
	if err := decodeJSON(line, &header); err != nil {
		return sessionHeader{}, invalidSession(path, 1, "first line is not valid JSON", err)
	}
	if header.Type != "session" {
		return sessionHeader{}, invalidSession(path, 1, "first line is not a session header")
	}
	if header.Version != currentVersion {
		return sessionHeader{}, invalidSession(path, 1, fmt.Sprintf("unsupported session version %d", header.Version))
	}
	if strings.TrimSpace(header.ID) == "" {
		return sessionHeader{}, invalidSession(path, 1, "session header is missing id")
	}
	if err := validateTimestamp(header.Timestamp); err != nil {
		return sessionHeader{}, invalidSession(path, 1, "session header has invalid timestamp", err)
	}
	if header.Cwd == "" {
		return sessionHeader{}, invalidSession(path, 1, "session header is missing cwd")
	}
	return header, nil
}

type parsedEntry struct {
	id      string
	message message.AgentMessage
}

func concreteMessage(m message.AgentMessage) (message.Message, message.AgentMessage, error) {
	switch value := m.(type) {
	case message.UserMessage:
		return value, value, nil
	case *message.UserMessage:
		if value == nil {
			return nil, nil, errors.New("cannot append a nil message")
		}
		return *value, *value, nil
	case message.AssistantMessage:
		return value, value, nil
	case *message.AssistantMessage:
		if value == nil {
			return nil, nil, errors.New("cannot append a nil message")
		}
		return *value, *value, nil
	case message.ToolResultMessage:
		return value, value, nil
	case *message.ToolResultMessage:
		if value == nil {
			return nil, nil, errors.New("cannot append a nil message")
		}
		return *value, *value, nil
	default:
		return nil, nil, fmt.Errorf("unsupported session message type %T", m)
	}
}

func parseEntry(line, path string, lineNumber int, previousID string, seen map[string]struct{}) (parsedEntry, error) {
	var entry messageEntry
	if err := decodeJSON(line, &entry); err != nil {
		return parsedEntry{}, invalidSession(path, lineNumber, "entry is not valid JSON", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &fields); err != nil {
		return parsedEntry{}, invalidSession(path, lineNumber, "entry is not valid JSON", err)
	}
	if _, present := fields["parentId"]; !present {
		return parsedEntry{}, invalidSession(path, lineNumber, "entry is missing parentId")
	}
	if entry.Type != "message" {
		return parsedEntry{}, invalidSession(path, lineNumber, fmt.Sprintf("unsupported entry type %q", entry.Type))
	}
	if strings.TrimSpace(entry.ID) == "" {
		return parsedEntry{}, invalidSession(path, lineNumber, "entry is missing id")
	}
	if _, exists := seen[entry.ID]; exists {
		return parsedEntry{}, invalidSession(path, lineNumber, fmt.Sprintf("duplicate entry id %q", entry.ID))
	}
	if previousID == "" {
		if entry.ParentID != nil {
			return parsedEntry{}, invalidSession(path, lineNumber, "first entry parentId must be null")
		}
	} else if entry.ParentID == nil || *entry.ParentID != previousID {
		return parsedEntry{}, invalidSession(path, lineNumber, fmt.Sprintf("parentId must be %q", previousID))
	}
	if err := validateTimestamp(entry.Timestamp); err != nil {
		return parsedEntry{}, invalidSession(path, lineNumber, "entry has invalid timestamp", err)
	}
	if len(entry.Message) == 0 || string(entry.Message) == "null" {
		return parsedEntry{}, invalidSession(path, lineNumber, "entry is missing message")
	}
	decoded, err := message.UnmarshalMessage(entry.Message)
	if err != nil {
		return parsedEntry{}, invalidSession(path, lineNumber, "entry message is invalid", err)
	}
	agentMessage, ok := decoded.(message.AgentMessage)
	if !ok {
		return parsedEntry{}, invalidSession(path, lineNumber, fmt.Sprintf("unsupported message type %T", decoded))
	}
	return parsedEntry{id: entry.ID, message: agentMessage}, nil
}

func decodeJSON(data string, target any) error {
	decoder := json.NewDecoder(strings.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func invalidSession(path string, line int, message string, cause ...error) error {
	if len(cause) > 0 && cause[0] != nil {
		return fmt.Errorf("invalid session %q at line %d: %s: %w", path, line, message, cause[0])
	}
	return fmt.Errorf("invalid session %q at line %d: %s", path, line, message)
}

func validateTimestamp(value string) error {
	if value == "" {
		return errors.New("timestamp is empty")
	}
	if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
		return err
	}
	return nil
}

func normalizeCwd(cwd string) (string, error) {
	if strings.TrimSpace(cwd) == "" {
		return "", errors.New("working directory is empty")
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return "", fmt.Errorf("resolve working directory %q: %w", cwd, err)
	}
	return filepath.Clean(abs), nil
}

func cwdKey(cwd string) string {
	base := filepath.Base(cwd)
	if base == "." || base == string(filepath.Separator) || base == "" {
		base = "root"
	}
	var safe strings.Builder
	for _, r := range base {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' {
			safe.WriteRune(r)
		} else {
			safe.WriteByte('-')
		}
	}
	if safe.Len() == 0 {
		safe.WriteString("root")
	}
	digest := sha256.Sum256([]byte(cwd))
	return safe.String() + "-" + hex.EncodeToString(digest[:])
}

func fileTimestamp(t time.Time) string {
	return t.UTC().Format("20060102T150405.000000000Z")
}

func newID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	hexValue := hex.EncodeToString(bytes[:])
	return hexValue[:8] + "-" + hexValue[8:12] + "-" + hexValue[12:16] + "-" + hexValue[16:20] + "-" + hexValue[20:], nil
}

func writeLine(file *os.File, data []byte) error {
	data = append(data, '\n')
	written := 0
	for written < len(data) {
		n, err := file.Write(data[written:])
		written += n
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
