//go:build unix

// detach.go launches a prepared run's engine as its own detached process.
// `duck workflows run` returns a handle while the run continues without a
// parent. The child is the hidden `duck workflows exec <run-id>` verb in its
// own session (Setsid), so it survives the spawning CLI and Stop can address
// its process group.
package workflow

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// StartDetached forks `<selfBin> workflows exec <runID>` into its own
// session, engine stderr going to <run-dir>/engine.log. Returns the pid.
func StartDetached(selfBin, runID string) (int, error) {
	dir, err := RunDir(runID)
	if err != nil {
		return 0, err
	}
	logf, err := os.OpenFile(filepath.Join(dir, "engine.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, err
	}
	defer logf.Close()
	cmd := exec.Command(selfBin, "workflows", "exec", runID)
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("start workflow engine: %w", err)
	}
	pid := cmd.Process.Pid
	// Reap the child when it exits so a long-lived parent (the sidecar)
	// doesn't accumulate zombies.
	go func() { _ = cmd.Wait() }()
	return pid, nil
}
