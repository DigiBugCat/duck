// lock.go: a per-workspace advisory file lock serializing panel arms. Two
// `duck panel` runs for the same session at the same instant (attach arming vs
// the auto-launched manager line / the user's claude shell function) must not
// interleave Open's read-then-split sequence — the loser waits, then re-reads
// the live roles and converges as a heal.
package panel

import (
	"os"
	"path/filepath"
	"syscall"
)

// lockPanel takes an exclusive blocking flock for the session's panel arm and
// returns the release func. Lock files live under the user's runtime-ish home
// dir (~/.duck/locks); they are tiny and never cleaned — flock state dies with
// the fd, the files are just names.
func lockPanel(outer string) (func(), error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(home, ".duck", "locks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "panel-"+outer+".lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
