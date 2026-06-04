// `duck --resume [name]`: with no arg, open the picker TUI over existing
// sessions; with a name, attach that session directly (DESIGN §1, §6). It is a
// FLAG on bare `duck`, not a subcommand — `duck --resume` mirrors
// `claude --resume`. With a name it attaches that session directly; with no arg
// it runs the picker and, on a chosen session, execs the attach AFTER the TUI
// has fully torn down (so ssh/tmux own a clean TTY).
package command

import (
	"os"

	"github.com/DigiBugCat/duck/internal/tui"
)

// runResume is the dispatch target when bare `duck` is invoked with --resume.
// name is the optional positional session name; empty means open the picker.
func runResume(name string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	w, err := build()
	if err != nil {
		return err
	}
	// Per DESIGN §1, --resume ensures cwd is synced before attaching (like -c),
	// so resuming work in a project keeps its mirror live. Idempotent when the
	// dir is already a running bundle. Gated like bare `duck` (decideSync) so an
	// unknown risky/home folder with no remembered "sync" and no consent never
	// silently starts a mirror; the gate returns the tilde-form dir either way so
	// the picker sees the same dir as before.
	cwdDir, err := w.flow.EnsureSyncedGated(cwd)
	if err != nil {
		return err
	}
	if name != "" {
		// Direct attach by internal tmux name, through the reconnect loop so a
		// transport drop reconnects and a ^c give-up is remembered per-terminal.
		runAttachLoop(w.sessions, name)
		return nil
	}

	chosen, err := tui.Run(w.app, cwdDir)
	if err != nil {
		return err
	}
	if chosen == "" {
		return nil // user quit without choosing
	}
	// The TUI has fully torn down; hand the process off to the reconnect loop.
	runAttachLoop(w.sessions, chosen)
	return nil
}
