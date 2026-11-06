package env

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// WorkspaceEnv constrains filesystem methods, not Exec. os.Root performs the
// actual traversal checks when opening files, including during symlink races.
type WorkspaceEnv struct {
	*LocalEnv
	root      *os.Root
	canonical string
}

func NewWorkspaceEnv(cwd, shell string) (*WorkspaceEnv, error) {
	root, err := os.OpenRoot(cwd)
	if err != nil {
		return nil, err
	}
	canonical, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		root.Close()
		return nil, err
	}
	return &WorkspaceEnv{LocalEnv: NewLocalEnvWithShell(cwd, shell), root: root, canonical: canonical}, nil
}
func (e *WorkspaceEnv) Close() error { return e.root.Close() }
func (e *WorkspaceEnv) relative(path string) (string, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(e.Cwd(), path)
	}
	for _, base := range []string{e.Cwd(), e.canonical} {
		rel, err := filepath.Rel(base, path)
		if err == nil && filepath.IsLocal(rel) {
			return rel, nil
		}
	}
	return "", fmt.Errorf("path %q is outside workspace %q", path, e.Cwd())
}
func (e *WorkspaceEnv) ReadFile(ctx context.Context, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p, err := e.relative(path)
	if err != nil {
		return nil, err
	}
	return e.root.ReadFile(p)
}
func (e *WorkspaceEnv) WriteFile(ctx context.Context, path string, data []byte, perm os.FileMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p, err := e.relative(path)
	if err != nil {
		return err
	}
	return e.root.WriteFile(p, data, perm)
}
func (e *WorkspaceEnv) Stat(ctx context.Context, path string) (FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return FileInfo{}, err
	}
	p, err := e.relative(path)
	if err != nil {
		return FileInfo{}, err
	}
	fi, err := e.root.Stat(p)
	if err != nil {
		return FileInfo{}, err
	}
	return FileInfo{Name: fi.Name(), Path: path, Size: fi.Size(), IsDir: fi.IsDir(), ModTime: fi.ModTime()}, nil
}
func (e *WorkspaceEnv) Exists(ctx context.Context, path string) (bool, error) {
	_, err := e.Stat(ctx, path)
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}
func (e *WorkspaceEnv) MkdirAll(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p, err := e.relative(path)
	if err != nil {
		return err
	}
	return e.root.MkdirAll(p, 0755)
}
func (e *WorkspaceEnv) Remove(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p, err := e.relative(path)
	if err != nil {
		return err
	}
	if p == "." {
		return fmt.Errorf("cannot remove workspace root")
	}
	return e.root.RemoveAll(p)
}
func (e *WorkspaceEnv) ListDir(ctx context.Context, path string) ([]FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p, err := e.relative(path)
	if err != nil {
		return nil, err
	}
	f, err := e.root.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	entries, err := f.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var result []FileInfo
	for _, entry := range entries {
		fi, err := e.Stat(ctx, filepath.Join(path, entry.Name()))
		if err != nil {
			return nil, err
		}
		result = append(result, fi)
	}
	return result, nil
}
