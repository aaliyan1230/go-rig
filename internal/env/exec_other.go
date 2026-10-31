//go:build !darwin && !linux

package env

import "os/exec"

// CommandContext retains its normal per-process cancellation on other systems.
func configureCommandCancellation(_ *exec.Cmd) {}
