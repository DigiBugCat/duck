// Background auto-update: on any `duck` invocation, duck spawns a detached child
// (`duck __bg-update`) that checks the GitHub release and, when a newer version
// exists, atomically replaces the binary in place. The CURRENT process keeps
// running on the old inode (safe on Unix); the NEXT `duck` run picks up the new
// binary. It is default-on, throttled to one check per interval via a stamp
// file, and a no-op for `dev` builds (a from-source binary must never be
// clobbered by a release). Opt out with `duck config auto-update off` or by
// setting DUCK_NO_AUTO_UPDATE.
package command

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/DigiBugCat/duck/internal/config"
	"github.com/spf13/cobra"
)

// bgUpdateCmdName is the hidden subcommand the detached updater runs. Kept out of
// the help output (it is an internal re-exec target, never typed by a user).
const bgUpdateCmdName = "__bg-update"

// backgroundUpdateInterval throttles how often the detached check runs: at most
// once per interval, gated by the mtime of the stamp file. One GitHub API call
// per hour is well within the unauthenticated rate limit and keeps a freshly
// published release landing on the next run within the hour.
const backgroundUpdateInterval = time.Hour

// updateStampPath is the throttle marker; its mtime records the last check.
func updateStampPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".duck", "last-update-check"), nil
}

// dueForBackgroundUpdate reports whether the throttle interval has elapsed since
// the last check (true when the stamp is missing — first run always checks). A
// stat error other than not-exist is treated as "not due" so a permissions
// glitch never busy-spawns the updater.
func dueForBackgroundUpdate(now time.Time) bool {
	p, err := updateStampPath()
	if err != nil {
		return false
	}
	fi, err := os.Stat(p)
	if err != nil {
		return os.IsNotExist(err)
	}
	return now.Sub(fi.ModTime()) >= backgroundUpdateInterval
}

// touchUpdateStamp records "checked now" by writing the stamp file (creating
// ~/.duck if needed). Best-effort: a failure just means the next run re-checks.
func touchUpdateStamp(now time.Time) {
	p, err := updateStampPath()
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

// backgroundUpdateEnabled gathers the gates that disable auto-update regardless
// of throttle: a dev (from-source) build, the DUCK_NO_AUTO_UPDATE escape hatch,
// and the per-user config opt-out. Pure inputs so it is unit-testable.
func backgroundUpdateEnabled(ver, envOptOut string, cfgEnabled bool) bool {
	if ver == "dev" {
		return false // never clobber a from-source binary with a release.
	}
	if envOptOut != "" {
		return false
	}
	return cfgEnabled
}

// maybeStartBackgroundUpdate is the rootCmd PersistentPreRun hook. When auto-
// update is enabled and the throttle interval has elapsed, it stamps "checked
// now" and spawns a fully detached `duck __bg-update` that outlives this process,
// then returns immediately so the user's command is never delayed. It is a no-op
// for the update/bg-update commands themselves (no recursion, and an explicit
// `duck update` should not race a background one).
func maybeStartBackgroundUpdate(cmd *cobra.Command) {
	switch cmd.Name() {
	case bgUpdateCmdName, "update":
		return
	}
	cfg, _ := config.Load()
	if !backgroundUpdateEnabled(version, os.Getenv("DUCK_NO_AUTO_UPDATE"), cfg.AutoUpdateEnabled()) {
		return
	}
	now := time.Now()
	if !dueForBackgroundUpdate(now) {
		return
	}
	touchUpdateStamp(now)

	self, err := os.Executable()
	if err != nil {
		return
	}
	c := exec.Command(self, bgUpdateCmdName)
	// Fully detach: no stdio (any installRelease prints go nowhere) and a new
	// session so the child outlives this process and its terminal.
	c.Stdin, c.Stdout, c.Stderr = nil, nil, nil
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := c.Start(); err == nil {
		_ = c.Process.Release()
	}
}

// bgUpdateCmd is the hidden re-exec target: it performs the actual check+install
// in the detached child. It always exits 0 — a failed background update is never
// surfaced (the user's next foreground `duck update` would report a real error).
var bgUpdateCmd = &cobra.Command{
	Use:    bgUpdateCmdName,
	Hidden: true,
	Args:   cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		rel, err := fetchLatestRelease()
		if err != nil {
			return nil
		}
		if _, newer := updateAvailable(rel); !newer {
			return nil
		}
		_ = installRelease(rel)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(bgUpdateCmd)
	// Spawn the throttled background updater before any command runs. Subcommands
	// don't define their own PersistentPreRun, so this root hook covers them all.
	rootCmd.PersistentPreRun = func(cmd *cobra.Command, _ []string) {
		maybeStartBackgroundUpdate(cmd)
		if home, err := os.UserHomeDir(); err == nil {
			ensureAgentNotes(home)
			ensureExecutorNotes(home)
			cleanupDuckAgentsRegistration(home)
		}
	}
}
