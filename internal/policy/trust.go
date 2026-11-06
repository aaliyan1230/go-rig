package policy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func ProjectRoot(cwd string) (string, error) {
	canonical, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return "", err
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return "", err
	}
	for dir := canonical; ; dir = filepath.Dir(dir) {
		info, err := os.Stat(filepath.Join(dir, ".git"))
		if err == nil && (info.IsDir() || info.Mode().IsRegular()) {
			return dir, nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	return canonical, nil
}

type trustRecord struct {
	Version      int    `json:"version"`
	Root         string `json:"root"`
	Instructions *bool  `json:"instructions"`
}
type TrustStore struct {
	Base      string
	decisions map[string]bool
}

func (s *TrustStore) path(root string) string {
	sum := sha256.Sum256([]byte(root))
	return filepath.Join(s.Base, "go-rig", "trust", hex.EncodeToString(sum[:])+".json")
}

func (s *TrustStore) Get(root string) (bool, bool, error) {
	if value, ok := s.decisions[root]; ok {
		return value, true, nil
	}
	data, err := os.ReadFile(s.path(root))
	if errors.Is(err, os.ErrNotExist) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	var record trustRecord
	if err = json.Unmarshal(data, &record); err != nil {
		return false, false, fmt.Errorf("invalid trust record: %w", err)
	}
	if record.Version != 1 || record.Root != root || record.Instructions == nil {
		return false, false, fmt.Errorf("invalid trust record for %q", root)
	}
	return *record.Instructions, true, nil
}

func (s *TrustStore) Save(root string, allow bool) error {
	// Keep the explicit decision for this process even if durable saving fails.
	if s.decisions == nil {
		s.decisions = map[string]bool{}
	}
	s.decisions[root] = allow
	path := s.path(root)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".trust-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	data, err := json.Marshal(trustRecord{Version: 1, Root: root, Instructions: &allow})
	if err != nil {
		file.Close()
		return err
	}
	if _, err = file.Write(append(data, '\n')); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func (s *TrustStore) Resolve(ctx context.Context, mode, cwd string, paths []string, ask Confirm, warn func(error)) (bool, error) {
	if len(paths) == 0 {
		return false, nil
	}
	if mode == "yes" {
		return true, nil
	}
	if mode == "no" {
		return false, nil
	}
	root, err := ProjectRoot(cwd)
	if err != nil {
		return false, err
	}
	if mode == "auto" {
		allow, found, err := s.Get(root)
		if err != nil {
			return false, err
		}
		if found {
			return allow, nil
		}
	}
	if ask == nil {
		if warn != nil {
			warn(fmt.Errorf("skipping untrusted project instructions for %q; use --project-trust=yes to load them", root))
		}
		return false, nil
	}
	quoted := make([]string, len(paths))
	for i, path := range paths {
		quoted[i] = fmt.Sprintf("%q", path)
	}
	allow, err := ask(ctx, Request{Kind: "trust", Title: "Trust automatic project instructions?", Details: fmt.Sprintf("Project: %q\nSources (may include inherited instructions):\n%s\nThis decision covers instructions only, not executable extensions or tool access.", root, strings.Join(quoted, "\n"))})
	if err != nil {
		return false, err
	}
	if err = s.Save(root, allow); err != nil && warn != nil {
		warn(fmt.Errorf("trust decision not saved: %w", err))
	}
	return allow, nil
}
