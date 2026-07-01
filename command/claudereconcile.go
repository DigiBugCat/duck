// Background Claude-history reconcile: the flow calls an injected closure at the
// end of coSyncClaude on every gated `duck`. When history co-sync is on and a
// throttle interval has elapsed, that closure spawns a fully detached `duck
// __bg-claude-reconcile` child (same shape as the auto-updater) that maps
// foreign-machine transcripts onto this machine's path form and registers them —
// and, over SSH, does the same on the hub — so hub/other-laptop sessions become
// resumable here automatically. Detached so it never delays the interactive
// command; throttled so a burst of `duck` runs doesn't re-fork or re-hit SSH.
package command

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/DigiBugCat/duck/internal/config"
	"github.com/spf13/cobra"
)

// tryLockSpawn takes a NON-BLOCKING exclusive lock on a sidecar so that when
// several `duck` invocations fire near-simultaneously (a burst of session
// attaches), exactly one enters the due-check-and-spawn path — the rest bail
// instead of each stamping and forking a redundant background reconcile. Returns
// an unlock func and whether the lock was acquired; a failure to open/lock is
// treated as "not acquired" so we simply skip this round rather than herd.
func tryLockSpawn() (func(), bool) {
	p, err := claudeReconcileStampPath()
	if err != nil {
		return func() {}, false
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return func() {}, false
	}
	f, err := os.OpenFile(p+".spawn-lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return func() {}, false
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return func() {}, false // another duck is already deciding — don't herd
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, true
}

// bgClaudeReconcileCmdName is the hidden re-exec target the detached reconcile runs.
const bgClaudeReconcileCmdName = "__bg-claude-reconcile"

// claudeReconcileInterval throttles the background reconcile: short enough that a
// session opened on the hub becomes locally resumable within minutes, long enough
// that a tight loop of `duck` invocations doesn't repeatedly fork, hit the anchor
// over SSH, or rewrite ~/.claude.json.
const claudeReconcileInterval = 5 * time.Minute

// claudeReconcileStampPath is the throttle marker; its mtime records the last run.
func claudeReconcileStampPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".duck", "last-claude-reconcile"), nil
}

// dueForClaudeReconcile reports whether the throttle interval has elapsed (true
// when the stamp is missing — first run always runs). A stat error other than
// not-exist is "not due" so a glitch never busy-spawns the reconcile.
func dueForClaudeReconcile(now time.Time) bool {
	p, err := claudeReconcileStampPath()
	if err != nil {
		return false
	}
	fi, err := os.Stat(p)
	if err != nil {
		return os.IsNotExist(err)
	}
	return now.Sub(fi.ModTime()) >= claudeReconcileInterval
}

// touchClaudeReconcileStamp records "ran now". Best-effort.
func touchClaudeReconcileStamp(now time.Time) {
	p, err := claudeReconcileStampPath()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return
	}
	if f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600); err == nil {
		_ = f.Close()
	}
	_ = os.Chtimes(p, now, now)
}

// newClaudeReconciler builds the closure injected into flow via
// SetClaudeReconciler. The common case is a single os.Stat (the throttle check);
// when due, it stamps and spawns a detached `duck __bg-claude-reconcile` that
// outlives this process and does the SSH/anchor/registry work off to the side,
// so the interactive command is never delayed. cfg is captured at wiring time so
// flow needs no config import. The syncClaude gate flow already checked is
// re-checked here (belt-and-suspenders, intentional).
func newClaudeReconciler(cfg *config.Config) func() {
	return func() {
		if cfg == nil || !cfg.SyncClaudeHistoryEnabled() {
			return
		}
		// Serialize the due-check + stamp + spawn across concurrent `duck`
		// processes so a burst of attaches yields ONE background reconcile, not one
		// per process. If another duck holds it, skip this round entirely.
		unlock, ok := tryLockSpawn()
		if !ok {
			return
		}
		defer unlock()
		now := time.Now()
		if !dueForClaudeReconcile(now) {
			return
		}
		touchClaudeReconcileStamp(now)
		self, err := os.Executable()
		if err != nil {
			return
		}
		c := exec.Command(self, bgClaudeReconcileCmdName)
		c.Stdin, c.Stdout, c.Stderr = nil, nil, nil
		c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := c.Start(); err == nil {
			_ = c.Process.Release()
		}
	}
}

// bgClaudeReconcileCmd is the hidden re-exec target: it runs the full laptop
// reconcile (this machine + the hub over SSH) in the detached child. It always
// exits 0 — a failed background reconcile is never surfaced (the explicit
// `duck claude-history reconcile` reports real errors).
var bgClaudeReconcileCmd = &cobra.Command{
	Use:    bgClaudeReconcileCmdName,
	Hidden: true,
	Args:   cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		_ = runClaudeReconcile(io.Discard, io.Discard, reconcileParams{})
		return nil
	},
}

func init() {
	rootCmd.AddCommand(bgClaudeReconcileCmd)
}
