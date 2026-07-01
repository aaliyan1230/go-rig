package env

import (
	"bytes"
	"context"
	"os"
	"os/exec"
)

type LocalEnv struct {
	cwd       string
	shellPath string
}

func NewLocalEnv(cwd string) *LocalEnv {
	return &LocalEnv{
		cwd:       cwd,
		shellPath: "/bin/bash",
	}
}

func (e *LocalEnv) ReadFile(_ context.Context, path string) ([]byte, error) {
	return os.ReadFile(path)
}

func (e *LocalEnv) WriteFile(_ context.Context, path string, data []byte, perm os.FileMode) error {
	return os.WriteFile(path, data, perm)
}

func (e *LocalEnv) Stat(_ context.Context, path string) (FileInfo, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return FileInfo{}, err
	}
	return FileInfo{
		Name:    fi.Name(),
		Path:    path,
		Size:    fi.Size(),
		IsDir:   fi.IsDir(),
		ModTime: fi.ModTime(),
	}, nil
}

func (e *LocalEnv) ListDir(_ context.Context, path string) ([]FileInfo, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	infos := make([]FileInfo, 0, len(entries))
	for _, entry := range entries {
		fi, err := entry.Info()
		if err != nil {
			return nil, err
		}
		infos = append(infos, FileInfo{
			Name:    fi.Name(),
			Path:    path + "/" + fi.Name(),
			Size:    fi.Size(),
			IsDir:   fi.IsDir(),
			ModTime: fi.ModTime(),
		})
	}
	return infos, nil
}

func (e *LocalEnv) Exists(_ context.Context, path string) (bool, error) {
	_, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}

func (e *LocalEnv) MkdirAll(_ context.Context, path string) error {
	return os.MkdirAll(path, 0755)
}

func (e *LocalEnv) Remove(_ context.Context, path string) error {
	return os.RemoveAll(path)
}

func (e *LocalEnv) Exec(ctx context.Context, command string, opts ExecOptions) (ExecResult, error) {
	execCtx := ctx
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		execCtx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(execCtx, e.shellPath, "-c", command)
	cmd.Dir = e.cwd
	if opts.Cwd != "" {
		cmd.Dir = opts.Cwd
	}

	cmd.Env = os.Environ()
	if len(opts.Env) > 0 {
		for k, v := range opts.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	result := ExecResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: 0,
	}

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		}
		return result, err
	}

	return result, nil
}

func (e *LocalEnv) Cwd() string {
	return e.cwd
}
