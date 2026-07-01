package env

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLocalEnv_ReadWriteFile(t *testing.T) {
	env := NewLocalEnv(t.TempDir())
	path := filepath.Join(env.Cwd(), "test.txt")
	data := []byte("hello world")

	if err := env.WriteFile(context.Background(), path, data, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := env.ReadFile(context.Background(), path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	if string(got) != string(data) {
		t.Errorf("got %q, want %q", got, data)
	}
}

func TestLocalEnv_Stat(t *testing.T) {
	env := NewLocalEnv(t.TempDir())
	path := filepath.Join(env.Cwd(), "test.txt")
	data := []byte("hello")

	env.WriteFile(context.Background(), path, data, 0644)

	fi, err := env.Stat(context.Background(), path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}

	if fi.Name != "test.txt" {
		t.Errorf("Name: got %q, want %q", fi.Name, "test.txt")
	}
	if fi.Path != path {
		t.Errorf("Path: got %q, want %q", fi.Path, path)
	}
	if fi.Size != int64(len(data)) {
		t.Errorf("Size: got %d, want %d", fi.Size, len(data))
	}
	if fi.IsDir {
		t.Error("IsDir: got true, want false")
	}
	if fi.ModTime.IsZero() {
		t.Error("ModTime should not be zero")
	}
}

func TestLocalEnv_StatDir(t *testing.T) {
	env := NewLocalEnv(t.TempDir())
	dirPath := filepath.Join(env.Cwd(), "subdir")

	env.MkdirAll(context.Background(), dirPath)

	fi, err := env.Stat(context.Background(), dirPath)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}

	if !fi.IsDir {
		t.Error("IsDir: got false, want true")
	}
	if fi.Size != 0 {
		t.Logf("Size for dir: %d (platform-dependent)", fi.Size)
	}
}

func TestLocalEnv_ListDir(t *testing.T) {
	env := NewLocalEnv(t.TempDir())

	env.WriteFile(context.Background(), filepath.Join(env.Cwd(), "a.txt"), []byte("a"), 0644)
	env.WriteFile(context.Background(), filepath.Join(env.Cwd(), "b.txt"), []byte("bb"), 0644)
	env.MkdirAll(context.Background(), filepath.Join(env.Cwd(), "sub"))

	infos, err := env.ListDir(context.Background(), env.Cwd())
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}

	if len(infos) != 3 {
		t.Fatalf("got %d entries, want 3", len(infos))
	}

	names := make(map[string]bool)
	for _, fi := range infos {
		names[fi.Name] = true
	}
	for _, name := range []string{"a.txt", "b.txt", "sub"} {
		if !names[name] {
			t.Errorf("missing entry %q", name)
		}
	}

	found := false
	for _, fi := range infos {
		if fi.Name == "sub" && fi.IsDir {
			found = true
			break
		}
	}
	if !found {
		t.Error("no directory entry found for 'sub'")
	}
}

func TestLocalEnv_Exists(t *testing.T) {
	env := NewLocalEnv(t.TempDir())
	path := filepath.Join(env.Cwd(), "exists.txt")

	env.WriteFile(context.Background(), path, []byte("x"), 0644)

	ok, err := env.Exists(context.Background(), path)
	if err != nil {
		t.Fatalf("Exists(existing): %v", err)
	}
	if !ok {
		t.Error("Exists(existing): got false, want true")
	}

	ok, err = env.Exists(context.Background(), filepath.Join(env.Cwd(), "nope.txt"))
	if err != nil {
		t.Fatalf("Exists(missing): %v", err)
	}
	if ok {
		t.Error("Exists(missing): got true, want false")
	}
}

func TestLocalEnv_MkdirAll_Remove(t *testing.T) {
	env := NewLocalEnv(t.TempDir())
	deep := filepath.Join(env.Cwd(), "a", "b", "c")

	if err := env.MkdirAll(context.Background(), deep); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	ok, err := env.Exists(context.Background(), deep)
	if err != nil {
		t.Fatalf("Exists after MkdirAll: %v", err)
	}
	if !ok {
		t.Error("directory not found after MkdirAll")
	}

	if err := env.Remove(context.Background(), filepath.Join(env.Cwd(), "a")); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	ok, err = env.Exists(context.Background(), deep)
	if err != nil {
		t.Fatalf("Exists after Remove: %v", err)
	}
	if ok {
		t.Error("directory still exists after Remove")
	}
}

func TestLocalEnv_Exec(t *testing.T) {
	env := NewLocalEnv(t.TempDir())

	result, err := env.Exec(context.Background(), "echo hello", ExecOptions{})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}

	if strings.TrimSpace(result.Stdout) != "hello" {
		t.Errorf("Stdout: got %q, want %q", result.Stdout, "hello\n")
	}
	if result.ExitCode != 0 {
		t.Errorf("ExitCode: got %d, want 0", result.ExitCode)
	}
}

func TestLocalEnv_ExecStderr(t *testing.T) {
	env := NewLocalEnv(t.TempDir())

	result, err := env.Exec(context.Background(), "echo error >&2", ExecOptions{})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}

	if result.Stderr == "" {
		t.Error("Stderr should not be empty")
	}
	if strings.TrimSpace(result.Stderr) != "error" {
		t.Errorf("Stderr: got %q, want %q", result.Stderr, "error\n")
	}
}

func TestLocalEnv_ExecExitCode(t *testing.T) {
	env := NewLocalEnv(t.TempDir())

	_, err := env.Exec(context.Background(), "exit 42", ExecOptions{})
	if err == nil {
		t.Fatal("expected error for non-zero exit")
	}

	if exitErr, ok := err.(interface{ ExitCode() int }); ok {
		t.Logf("exit code: %d", exitErr.ExitCode())
	}
}

func TestLocalEnv_ExecTimeout(t *testing.T) {
	env := NewLocalEnv(t.TempDir())

	ctx := context.Background()
	_, err := env.Exec(ctx, "sleep 10", ExecOptions{Timeout: 10 * time.Millisecond})
	if err == nil {
		t.Fatal("expected error for timed-out command")
	}
}

func TestLocalEnv_ExecEnvOverride(t *testing.T) {
	env := NewLocalEnv(t.TempDir())

	result, err := env.Exec(context.Background(), "echo $MYVAR", ExecOptions{
		Env: map[string]string{"MYVAR": "custom_value"},
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}

	if strings.TrimSpace(result.Stdout) != "custom_value" {
		t.Errorf("Stdout: got %q, want %q", result.Stdout, "custom_value\n")
	}
}

func TestLocalEnv_ExecCustomCwd(t *testing.T) {
	env := NewLocalEnv(t.TempDir())
	subDir := filepath.Join(env.Cwd(), "sub")
	env.MkdirAll(context.Background(), subDir)

	result, err := env.Exec(context.Background(), "pwd", ExecOptions{Cwd: subDir})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}

	stdout := strings.TrimSpace(result.Stdout)

	resolvedCwd, _ := filepath.EvalSymlinks(subDir)
	resolvedStdout, _ := filepath.EvalSymlinks(stdout)
	if resolvedStdout != resolvedCwd {
		t.Errorf("Stdout: got %q, want %q", stdout, subDir)
	}
}

func TestLocalEnv_Cwd(t *testing.T) {
	env := NewLocalEnv("/tmp/test-cwd")
	if env.Cwd() != "/tmp/test-cwd" {
		t.Errorf("Cwd: got %q, want %q", env.Cwd(), "/tmp/test-cwd")
	}
}

func TestLocalEnv_ImplementsEnv(t *testing.T) {
	var _ Env = (*LocalEnv)(nil)
}
