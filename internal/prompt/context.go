package prompt

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/aaliyan1230/go-rig/internal/env"
)

// contextFileNames are checked in order in each directory; the first regular
// file found wins, so a directory contributes at most one context file.
var contextFileNames = []string{"AGENTS.md", "CLAUDE.md"}

// LoadContextFiles walks from cwd up to the filesystem root and returns the
// context file found in each directory, outermost first. That order lets a
// file closer to the working directory refine or override a broader one.
//
// Missing files are skipped silently. Any other stat or read failure is
// returned as a warning and loading continues: a broken instruction file
// should not stop the harness from starting.
func LoadContextFiles(ctx context.Context, environment env.Env, cwd string) ([]ContextFile, []error) {
	paths, warnings := DiscoverContextFiles(ctx, environment, cwd)
	files, readWarnings := ReadContextFiles(ctx, environment, paths)
	return files, append(warnings, readWarnings...)
}

// DiscoverContextFiles inspects names and metadata, never instruction content.
func DiscoverContextFiles(ctx context.Context, environment env.Env, cwd string) ([]string, []error) {
	var paths []string
	var warnings []error
	dir := filepath.Clean(cwd)
	for {
		path, ok, err := discoverContextFileFromDir(ctx, environment, dir)
		if err != nil {
			warnings = append(warnings, err)
		}
		if ok {
			paths = append(paths, path)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	// Collected innermost first; reverse so the root-most file comes first.
	for i, j := 0, len(paths)-1; i < j; i, j = i+1, j-1 {
		paths[i], paths[j] = paths[j], paths[i]
	}
	return paths, warnings
}

func ReadContextFiles(ctx context.Context, environment env.Env, paths []string) ([]ContextFile, []error) {
	var files []ContextFile
	var warnings []error
	for _, path := range paths {
		data, err := environment.ReadFile(ctx, path)
		if err != nil {
			warnings = append(warnings, err)
			continue
		}
		files = append(files, ContextFile{Path: path, Content: strings.TrimPrefix(string(data), "\uFEFF")})
	}
	return files, warnings
}

func discoverContextFileFromDir(ctx context.Context, environment env.Env, dir string) (string, bool, error) {
	for _, name := range contextFileNames {
		path := filepath.Join(dir, name)
		info, err := environment.Stat(ctx, path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", false, err
		}
		if info.IsDir {
			continue
		}
		return path, true, nil
	}
	return "", false, nil
}
