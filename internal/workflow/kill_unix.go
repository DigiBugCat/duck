//go:build unix

package workflow

import "syscall"

// syscallKill wraps syscall.Kill behind a build tag so workflow.go stays
// portable-looking; duck only ships on unix hubs.
func syscallKill(pid, sig int) error {
	return syscall.Kill(pid, syscall.Signal(sig))
}

// Stop terminates a run: SIGTERM to the engine's process group (the engine
// puts itself and every worker in one), so workers die with it.
func Stop(pid int) error {
	// Negative pid = the whole group.
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
		return syscall.Kill(pid, syscall.SIGTERM)
	}
	return nil
}
