//go:build darwin || linux

package env

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestExecCancellationKillsChildHoldingPipes(t *testing.T) {
	cwd := t.TempDir()
	e := NewLocalEnv(cwd)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := e.Exec(ctx, "sleep 30 & child=$!; printf '%s' \"$child\" > child.pid; printf started; wait", ExecOptions{})
		done <- err
	}()
	pidPath := filepath.Join(cwd, "child.pid")
	deadline := time.Now().Add(3 * time.Second)
	var pid int
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(pidPath)
		if err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
			if pid > 0 {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	if pid == 0 {
		cancel()
		<-done
		t.Fatal("child did not start")
	}
	// Cleanup is limited to the specific test process if an assertion fails.
	defer syscall.Kill(pid, syscall.SIGKILL)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected cancellation error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation blocked on child output pipes")
	}
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) == syscall.ESRCH {
			return
		}
		// On Linux, a container's PID 1 may leave the dead child as a zombie.
		// A zombie cannot execute or hold output pipes and is not a survivor.
		state, _ := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
		if strings.HasPrefix(strings.TrimSpace(string(state)), "Z") {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("child survived process-group cancellation")
}
