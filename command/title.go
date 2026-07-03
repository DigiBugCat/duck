// title.go sets the terminal tab/window title via the OSC 0 escape (the one
// terminals — Ghostty, iTerm2, Terminal.app, kitty — map to the tab label), so
// a duck attach names the tab after the session instead of leaving whatever the
// shell last wrote. Best-effort and write-only: there is no reliable way to
// read the old title back, so duck does not try to restore it — the shell's
// own precmd title hook (or the next command) takes the tab back over after
// duck exits.
package command

import (
	"fmt"
	"os"
	"strings"
)

// setTerminalTitle renames the terminal tab to title. No-op when stdout is not
// a terminal (piped/redirected runs must not emit escape bytes into output).
func setTerminalTitle(title string) {
	fi, err := os.Stdout.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return
	}
	fmt.Fprintf(os.Stdout, "\x1b]0;%s\x07", sanitizeTitle(title))
}

// sanitizeTitle strips C0 (ESC, BEL, newlines, …), DEL, and C1 (CSI, ST, …)
// control characters so a display name can never smuggle a second escape
// sequence into the title write, even on terminals that honor C1 controls.
func sanitizeTitle(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			return -1
		}
		return r
	}, s)
}
