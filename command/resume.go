// `duck --resume [name]`: with no arg, open the picker TUI over existing
// sessions; with a name, attach that session directly (DESIGN §1, §6). It is a
// FLAG on bare `duck`, not a subcommand — `duck --resume` mirrors
// `claude --resume`. With a name it attaches that session directly; with no arg
// it runs the picker and, on a chosen session, execs the attach AFTER the TUI
// has fully torn down (so ssh/tmux own a clean TTY).
package command

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/DigiBugCat/duck/internal/paths"
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
		// A cwd sync conflict (e.g. the hub already has this folder and a merge
		// would be needed → actions.ErrHubNonEmpty) — or any sync failure — must NOT
		// block the picker. --resume's job is to browse/attach existing sessions;
		// mirroring cwd is a best-effort side-effect. Warn and open the picker anyway,
		// scoped to the contracted cwd. A genuinely dead hub still surfaces in the
		// picker's own error screen when it reads the session list.
		fmt.Fprintf(os.Stderr, "duck: skipping cwd sync: %v\n", err)
		cwdDir = paths.Contract(cwd)
	}
	if name != "" {
		// Direct attach by internal tmux name, through the reconnect loop so a
		// transport drop reconnects and a ^c give-up is remembered per-terminal.
		runAttachLoop(w.sessions, name, "", w.tsshAttach)
		return nil
	}

	// Label the tab while the picker is up; the attach below re-labels it with
	// the chosen session's name.
	setTerminalTitle("duck")

	// Names come from each session's live tmux pane title (Claude Code writes a
	// task summary there), resolved for free with no codex call — so --resume does
	// NOT force codex auto-naming. Codex stays the opt-in per-folder fallback
	// (build wires cfg.AutoNameEnabled) for sessions with no useful pane title;
	// ^n in the picker still generates one on demand.
	chosen, display, doUpdate, err := tui.Run(w.app, cwdDir, updateCheck)
	if err != nil {
		setTerminalTitle("") // leaving without attaching: hand the tab back
		return err
	}
	if doUpdate {
		setTerminalTitle("")
		// ^u in the picker: self-update now that the TUI has torn down (brew-free,
		// pulls the binary from the GitHub release). The user re-runs duck after.
		return selfUpdateNow()
	}
	if chosen == "" {
		setTerminalTitle("")
		return nil // user quit without choosing
	}
	// The TUI has fully torn down; hand the process off to the reconnect loop.
	runAttachLoop(w.sessions, chosen, display, w.tsshAttach)
	return nil
}

// updateCheck is the picker's background update check (passed to tui.Run): hit
// the GitHub releases API and, if a newer release exists, post an
// UpdateAvailableMsg so the picker shows the ^u banner. Returns nil (no message)
// on any error, a dev build, or when already current — the picker just shows
// nothing. Network failure is silent by design: an update hint must never be a
// blocker.
func updateCheck() tea.Msg {
	rel, err := fetchLatestRelease()
	if err != nil {
		return nil
	}
	if latest, newer := updateAvailable(rel); newer {
		return tui.UpdateAvailableMsg{Latest: "v" + latest}
	}
	return nil
}
