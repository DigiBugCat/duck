// progress.go is the production flow.Progress: a single redrawing spinner line
// "<braille> syncing <dir> → hub · <status>" that makes a large first sync
// VISIBLE instead of looking frozen. It renders ONLY when stdout is a terminal
// (so a piped/redirected `duck` stays \r-free) and always writes to STDERR so
// piped stdout stays clean. On Stop it clears the line and prints "✓ synced"
// (or an error note). The isTTY check is injected so the no-\r-when-not-a-TTY
// behavior is unit-tested without a real terminal.
package command

import (
	"fmt"
	"io"
	"os"

	"github.com/mattn/go-isatty"
)

// spinnerFrames are the braille cells cycled on each Update to animate the line.
var spinnerFrames = []rune{'⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'}

// ttyProgress renders the redrawing sync-wait spinner. w is the sink (stderr in
// production); isTTY gates ALL output — when false nothing is written (no \r
// spam in a pipe/CI). active tracks whether a line is currently drawn so Start
// is idempotent (a force/merge Reconcile may begin the line before waitSteady)
// and Stop is safe-once.
type ttyProgress struct {
	w      io.Writer
	isTTY  bool
	target string
	frame  int
	active bool
}

// newTTYProgress builds the production reporter: it writes to STDERR (so piped
// stdout stays clean) and renders only when STDOUT is a terminal (the property
// item (4) requires: suppress when the piped stream is dirty-able).
func newTTYProgress() *ttyProgress {
	return &ttyProgress{
		w:     os.Stderr,
		isTTY: isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd()),
	}
}

// Start begins the redrawing line for target (the dir being synced). It is
// idempotent: if a line is already active (e.g. Reconcile started it) the
// target is kept and Start does nothing, so reconcile→add reads as one
// continuous line with a single trailing ✓.
func (p *ttyProgress) Start(action, target string) {
	if !p.isTTY || p.active {
		return
	}
	p.target = target
	p.active = true
	p.render("")
}

// Update advances the spinner and redraws the line with the live status. No-op
// when not a TTY or before Start.
func (p *ttyProgress) Update(status string) {
	if !p.isTTY || !p.active {
		return
	}
	p.frame = (p.frame + 1) % len(spinnerFrames)
	p.render(status)
}

// Stop clears the spinner line and prints a final note: "✓ synced" on success,
// a short error note otherwise. Safe-once: a second Stop is a no-op.
func (p *ttyProgress) Stop(ok bool) {
	if !p.isTTY || !p.active {
		return
	}
	p.active = false
	// Clear the current line, then print the terminal note on its own line.
	fmt.Fprint(p.w, "\r\033[K")
	if ok {
		fmt.Fprintf(p.w, "✓ synced %s\n", p.target)
	} else {
		fmt.Fprintf(p.w, "✗ sync incomplete for %s\n", p.target)
	}
}

// render redraws the single spinner line in place with \r. The \033[K clears to
// end of line so a shorter status never leaves stale characters behind.
func (p *ttyProgress) render(status string) {
	line := fmt.Sprintf("%c syncing %s → hub", spinnerFrames[p.frame], p.target)
	if status != "" {
		line += " · " + status
	}
	fmt.Fprintf(p.w, "\r\033[K%s", line)
}
