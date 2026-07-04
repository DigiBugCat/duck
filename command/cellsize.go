// Client-terminal cell measurement. Over SSH the client never reports pixel
// sizes to tmux (TIOCGWINSZ xpixel/ypixel = 0), so anything hub-side that
// needs pixel geometry (gosling's kitty rendering, chafa sizing) is guessing
// from whatever cell size tmux imagines — and the guess varies by client.
// duck, however, runs ON the laptop BEFORE tmux exists in the chain, where
// XTWINOPS 16 (CSI 16 t → CSI 6 ; height ; width t) gets a truthful answer
// from the real terminal. The attach path measures once and stamps the
// result on the hub's tmux server (@duck_client_cell, global option): tmux
// is the database, renderers read the stamp instead of guessing.
package command

import (
	"fmt"
	"os"
	"time"

	"github.com/charmbracelet/x/term"
	"golang.org/x/sys/unix"
)

// localCellSize queries the controlling terminal for its cell size in
// pixels. Returns ok=false on any failure (not a tty, no reply in time) —
// callers treat the measurement as best-effort.
func localCellSize() (w, h int, ok bool) {
	tty := os.Stdin
	if !term.IsTerminal(tty.Fd()) {
		return 0, 0, false
	}
	oldState, err := term.MakeRaw(tty.Fd())
	if err != nil {
		return 0, 0, false
	}
	defer term.Restore(tty.Fd(), oldState)

	if _, err := os.Stdout.WriteString("\x1b[16t"); err != nil {
		return 0, 0, false
	}
	// Poll for the reply with a wall-clock deadline. NOTE: os.File
	// SetReadDeadline is NOT supported on terminal devices (it errors and
	// an early return here silently skipped every measurement) — nonblock
	// polling is the portable pattern.
	fd := int(tty.Fd())
	buf := make([]byte, 64)
	acc := ""
	deadline := time.Now().Add(250 * time.Millisecond)
	for time.Now().Before(deadline) && len(acc) < 64 {
		_ = unix.SetNonblock(fd, true)
		n, _ := unix.Read(fd, buf)
		_ = unix.SetNonblock(fd, false)
		if n > 0 {
			acc += string(buf[:n])
			var hh, ww int
			if _, serr := fmt.Sscanf(lastCSI(acc), "\x1b[6;%d;%dt", &hh, &ww); serr == nil && ww > 0 && hh > 0 {
				return ww, hh, true
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	return 0, 0, false
}

// lastCSI trims accumulated input to the final ESC so a reply preceded by
// stray buffered bytes still parses.
func lastCSI(s string) string {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == 0x1b {
			return s[i:]
		}
	}
	return s
}
