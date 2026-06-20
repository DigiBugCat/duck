// `duck -c` / `duck --continue`: reattach the MOST RECENT session for cwd
// (DESIGN §1–2). It is a FLAG on bare `duck`, not a subcommand — `duck -c`
// mirrors `claude -c`. No new session: it ensures cwd is synced THROUGH THE SAME
// SYNC-AWARENESS GATE as bare `duck` (so a risky/home folder never silently
// starts a mirror), resolves the most-recent @duck_dir-matching session via
// session.Recent, and attaches. Errors clearly if none exist.
package command

import (
	"fmt"
	"os"

	"github.com/DigiBugCat/duck/internal/session"
)

// continueDeps is the seam runContinueWith drives so the tty-memory-vs-Recent
// lookup order is unit-testable without real config/ssh (mirrors root.go's
// overrideRunner pattern). Production is satisfied by the wiring + tty store.
type continueDeps struct {
	currentTTY func() string
	memGet     func(tty string) (string, bool)
	memPrune   func(tty string) error
	hasSession func(name string) (bool, error)
	ensureDir  func() (tildeDir string, err error)
	recent     func(dir string) (session.Sess, bool, error)
	attach     func(name string) // routes through the reconnect loop
}

// runContinueWith resolves which session `duck -c` reattaches and attaches it.
// Lookup order: (1) per-terminal memory — a remembered session for THIS terminal
// that STILL EXISTS on the hub is resumed directly (no dir-sync); a stale entry
// is pruned and we fall through. (2) dir-based — EnsureSyncedGated then the most
// recent @duck_dir-matching session via Recent; none → the instructive error.
func runContinueWith(d continueDeps) error {
	if tty := d.currentTTY(); tty != "" {
		if name, ok := d.memGet(tty); ok {
			live, herr := d.hasSession(name)
			if herr == nil && live {
				d.attach(name)
				return nil
			}
			_ = d.memPrune(tty)
		}
	}
	tildeDir, err := d.ensureDir()
	if err != nil {
		return err
	}
	s, ok, err := d.recent(tildeDir)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no session for %s yet — run `duck` to start one", tildeDir)
	}
	d.attach(s.Name)
	return nil
}

// runContinue is the dispatch target when bare `duck` is invoked with
// -c / --continue. It reattaches the most-recent session for cwd.
func runContinue() error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	w, err := build()
	if err != nil {
		return err
	}
	// The tty-memory hit path does NOT run EnsureSyncedGated (an explicit reconnect
	// to a known live session, not a fresh dir-sync); the dir fallback gates the
	// sync the same way bare `duck` does so an unknown risky/home folder never fires
	// a mirror here.
	return runContinueWith(continueDeps{
		currentTTY: CurrentTTY,
		memGet:     ttyMemGet,
		memPrune:   ttyMemPrune,
		hasSession: w.sessions.HasSession,
		ensureDir:  func() (string, error) { return w.flow.EnsureSyncedGated(cwd) },
		recent:     w.sessions.Recent,
		attach:     func(name string) { runAttachLoop(w.sessions, name, w.tsshAttach) },
	})
}
