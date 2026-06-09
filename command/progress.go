// progress.go is the production flow.Progress: a single redrawing spinner line
// "<braille> syncing <dir> → hub · <status>" that makes a large first sync
// VISIBLE instead of looking frozen. It renders ONLY when stdout is a terminal
// (so a piped/redirected `duck` stays \r-free) and always writes to STDERR so
// piped stdout stays clean. On Stop it clears the line and prints "✓ synced"
// (or an error note). The isTTY check is injected so the no-\r-when-not-a-TTY
// behavior is unit-tested without a real terminal.
//
// The spinner SELF-ANIMATES on its own ticker (animate goroutine) rather than
// only advancing inside Update: a long BLOCKING step — the reconcile rsync
// scan, a quiet mutagen scan — issues no Update for many seconds, and without a
// self-tick the braille froze mid-frame and the screen read as hung. All shared
// state is mutex-guarded so the ticker goroutine, the exec stdout copier
// goroutine that drives Update from rsync's --progress stream, and the caller
// never race; Stop JOINS the ticker before the final write so the cleared line
// is the last thing written.
package command

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/mattn/go-isatty"
)

// spinnerFrames are the braille cells cycled to animate the line.
var spinnerFrames = []rune{'⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'}

// spinTick is how often the braille frame advances ON ITS OWN, independent of
// Update — so a blocking sync step with no status updates still animates.
const spinTick = 100 * time.Millisecond

// ttyProgress renders the redrawing sync-wait spinner. w is the sink (stderr in
// production); isTTY gates ALL output — when false nothing is written (no \r
// spam in a pipe/CI). active tracks whether a line is currently drawn so Start
// is idempotent (a force/merge Reconcile may begin the line before waitSteady)
// and Stop is safe-once. mu guards every field touched after Start so the
// animate goroutine and Update (called from rsync's stdout copier) serialize.
type ttyProgress struct {
	w     io.Writer
	isTTY bool

	// interval overrides the self-animation tick (0 → spinTick). Injected by
	// tests so the self-spin is proven without a real 100ms wall-clock wait.
	interval time.Duration

	mu     sync.Mutex
	target string
	status string
	frame  int
	active bool
	stop   chan struct{} // closed by Stop to end the animate goroutine
	done   chan struct{} // closed by animate on exit so Stop can join it
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

// Start begins the redrawing line for target (the dir being synced) and launches
// the self-animation ticker so the spinner spins even while the caller is blocked
// in a long step that issues no Update. It is idempotent: if a line is already
// active (e.g. Reconcile started it) the target is kept and Start does nothing —
// so reconcile→add reads as one continuous line with a single trailing ✓, and a
// second ticker is never launched.
func (p *ttyProgress) Start(action, target string) {
	if !p.isTTY {
		return
	}
	p.mu.Lock()
	if p.active {
		p.mu.Unlock()
		return
	}
	p.target = target
	p.status = ""
	p.frame = 0
	p.active = true
	p.stop = make(chan struct{})
	p.done = make(chan struct{})
	p.render()
	stop, done := p.stop, p.done
	p.mu.Unlock()
	go p.animate(stop, done)
}

// animate advances the spinner frame on its own ticker until stop is closed, so
// the line stays alive during a blocking step. Each tick touches shared state
// under p.mu, serializing with Update/Stop and the rsync stdout copier goroutine.
func (p *ttyProgress) animate(stop, done chan struct{}) {
	defer close(done)
	iv := p.interval
	if iv <= 0 {
		iv = spinTick
	}
	t := time.NewTicker(iv)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			p.mu.Lock()
			if p.active {
				p.frame = (p.frame + 1) % len(spinnerFrames)
				p.render()
			}
			p.mu.Unlock()
		}
	}
}

// Update sets the live status (a mutagen phase, or a streamed rsync progress
// line) and redraws. No-op when not a TTY or before Start. Safe to call from the
// exec copier goroutine that streams rsync's stdout — mu serializes it with the
// animate ticker.
func (p *ttyProgress) Update(status string) {
	if !p.isTTY {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.active {
		return
	}
	p.status = status
	p.frame = (p.frame + 1) % len(spinnerFrames)
	p.render()
}

// Stop clears the spinner line and prints a final note: "✓ synced" on success,
// a short error note otherwise. It stops AND JOINS the animate goroutine before
// the final write so no ticker redraw races (or follows) the cleared line.
// Safe-once: a second Stop is a no-op.
func (p *ttyProgress) Stop(ok bool) {
	if !p.isTTY {
		return
	}
	p.mu.Lock()
	if !p.active {
		p.mu.Unlock()
		return
	}
	p.active = false
	stop, done := p.stop, p.done
	p.mu.Unlock()

	// Join the ticker with the mutex RELEASED: the animate goroutine needs p.mu to
	// observe the stop signal, so holding it here would deadlock the join.
	if stop != nil {
		close(stop)
	}
	if done != nil {
		<-done
	}

	p.mu.Lock()
	// Clear the current line, then print the terminal note on its own line.
	fmt.Fprint(p.w, "\r\033[K")
	if ok {
		fmt.Fprintf(p.w, "✓ synced %s\n", p.target)
	} else {
		fmt.Fprintf(p.w, "✗ sync incomplete for %s\n", p.target)
	}
	p.mu.Unlock()
}

// render redraws the single spinner line in place with \r. The \033[K clears to
// end of line so a shorter status never leaves stale characters behind. MUST be
// called with p.mu held.
func (p *ttyProgress) render() {
	line := fmt.Sprintf("%c syncing %s → hub", spinnerFrames[p.frame], p.target)
	if p.status != "" {
		line += " · " + p.status
	}
	fmt.Fprintf(p.w, "\r\033[K%s", line)
}
