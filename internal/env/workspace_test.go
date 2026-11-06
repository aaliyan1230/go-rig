package env

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceFileOperationsAndEscapes(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	ctx := context.Background()
	e, err := NewWorkspaceEnv(root, "/bin/bash")
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if err = e.MkdirAll(ctx, filepath.Join(root, "a", "b")); err != nil {
		t.Fatal(err)
	}
	if err = e.WriteFile(ctx, filepath.Join(root, "a", "b", "file"), []byte("inside"), 0600); err != nil {
		t.Fatal(err)
	}
	if data, err := e.ReadFile(ctx, "a/b/file"); err != nil || string(data) != "inside" {
		t.Fatal(string(data), err)
	}
	if exists, err := e.Exists(ctx, filepath.Join(root, "a/b/file")); err != nil || !exists {
		t.Fatal(exists, err)
	}
	if _, err = e.ListDir(ctx, "a/b"); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(outside, "file"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	rel, _ := filepath.Rel(root, filepath.Join(outside, "file"))
	os.Symlink(outside, filepath.Join(root, "escape"))
	os.Symlink("a/b/file", filepath.Join(root, "internal"))
	os.Symlink(filepath.Join(root, "a/b/file"), filepath.Join(root, "absolute-link"))
	for _, path := range []string{filepath.Join(outside, "file"), rel, "escape/file", "absolute-link"} {
		if _, err = e.ReadFile(ctx, path); err == nil {
			t.Fatalf("read escape %q", path)
		}
		if err = e.WriteFile(ctx, path, []byte("bad"), 0600); err == nil {
			t.Fatalf("write escape %q", path)
		}
	}
	if err = e.MkdirAll(ctx, "escape/new/parents"); err == nil {
		t.Fatal("created directories outside root")
	}
	if _, err = os.Stat(filepath.Join(outside, "new")); !os.IsNotExist(err) {
		t.Fatal("outside directory changed")
	}
	if data, err := e.ReadFile(ctx, "internal"); err != nil || string(data) != "inside" {
		t.Fatal(string(data), err)
	}
	if data, _ := os.ReadFile(filepath.Join(outside, "file")); string(data) != "outside" {
		t.Fatal("outside file changed")
	}
	if err = e.Remove(ctx, "."); err == nil {
		t.Fatal("removed root")
	}
	if err = e.Remove(ctx, "a"); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceDoesNotConstrainShellAndDefaultEnvUnchanged(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	ctx := context.Background()
	path := filepath.Join(outside, "file")
	os.WriteFile(path, []byte("outside"), 0600)
	local := NewLocalEnv(root)
	if data, err := local.ReadFile(ctx, path); err != nil || string(data) != "outside" {
		t.Fatal(data, err)
	}
	e, err := NewWorkspaceEnv(root, "/bin/bash")
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	result, err := e.Exec(ctx, "printf unrestricted-shell", ExecOptions{Cwd: outside})
	if err != nil || result.Stdout != "unrestricted-shell" {
		t.Fatal(result, err)
	}
}
