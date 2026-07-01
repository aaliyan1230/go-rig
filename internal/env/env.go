package env

import (
	"context"
	"os"
	"time"
)

type FileInfo struct {
	Name    string
	Path    string
	Size    int64
	IsDir   bool
	ModTime time.Time
}

type ExecOptions struct {
	Cwd     string
	Env     map[string]string
	Timeout time.Duration
}

type ExecResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

type Env interface {
	ReadFile(ctx context.Context, path string) ([]byte, error)
	WriteFile(ctx context.Context, path string, data []byte, perm os.FileMode) error
	Stat(ctx context.Context, path string) (FileInfo, error)
	ListDir(ctx context.Context, path string) ([]FileInfo, error)
	Exists(ctx context.Context, path string) (bool, error)
	MkdirAll(ctx context.Context, path string) error
	Remove(ctx context.Context, path string) error
	Exec(ctx context.Context, command string, opts ExecOptions) (ExecResult, error)
	Cwd() string
}
