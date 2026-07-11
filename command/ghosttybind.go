// duck owns its Ghostty keybinds: ⌘K opens the palette and ⌘⇧F the fleet by
// sending the tmux prefix sequence (\x02 = Ctrl-b). Hand-editing the config on
// every laptop proved fragile (a trailing literal space in `text:\x02 ` is
// stripped by Ghostty's parser — hence the explicit \x20), so any laptop-side
// duck invocation ensures a managed block in the Ghostty config, marker-fenced
// like the AGENT.md import. Ghostty reads config only at launch/reload, so
// when the block is ADDED duck prints a one-line reload hint (⌘⇧,).
package command

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	ghosttyBeginMark = "# >>> duck keybinds (managed by duck; do not edit) >>>"
	ghosttyEndMark   = "# <<< duck keybinds <<<"
)

// ghosttyKeybinds is the managed block body. \x02 is the tmux prefix (Ctrl-b);
// \x20 is an explicit space — a literal trailing space is stripped by
// Ghostty's config parser and the palette silently never opens.
const ghosttyKeybinds = ghosttyBeginMark + `
keybind = cmd+k=text:\x02\x20
keybind = cmd+shift+f=text:\x02f
` + ghosttyEndMark + "\n"

// ensureGhosttyKeybinds installs or refreshes the managed keybind block in
// ~/.config/ghostty/config. Entirely best-effort: no Ghostty on this machine
// (no config dir and no app bundle) means no-op, and no error ever surfaces —
// this must never block an attach.
func ensureGhosttyKeybinds() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	dir := filepath.Join(home, ".config", "ghostty")
	cfg := filepath.Join(dir, "config")
	if _, err := os.Stat(dir); err != nil {
		// No XDG ghostty dir: only bootstrap one when Ghostty itself is
		// installed (macOS app bundle) — never litter machines without it.
		if _, aerr := os.Stat("/Applications/Ghostty.app"); aerr != nil {
			return
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return
		}
	}
	raw, _ := os.ReadFile(cfg) // missing file reads as empty
	s := string(raw)
	want := strings.TrimSuffix(ghosttyKeybinds, "\n")
	if i := strings.Index(s, ghosttyBeginMark); i >= 0 {
		j := strings.Index(s, ghosttyEndMark)
		if j < i {
			return // mangled fence — leave it for a human
		}
		if s[i:j+len(ghosttyEndMark)] == want {
			return // up to date
		}
		s = s[:i] + want + s[j+len(ghosttyEndMark):]
	} else {
		if s != "" && !strings.HasSuffix(s, "\n") {
			s += "\n"
		}
		s += "\n" + ghosttyKeybinds
		defer fmt.Fprintln(os.Stderr, "duck: added ⌘K (palette) / ⌘⇧F (fleet) keybinds to ghostty — reload config with ⌘⇧, to activate")
	}
	_ = os.WriteFile(cfg, []byte(s), 0o644)
}
