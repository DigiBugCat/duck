// `duck loop on|off`: mark (or unmark) the CURRENT tmux session as running a
// /loop by setting the @duck_loop user option on it. The picker (`duck --resume`)
// pins sessions carrying this marker to the very top with a ↻ glyph, so an
// autonomous loop you are not attached to never gets buried.
//
// This runs INSIDE the session it marks — on the hub, where the loop actually
// runs — using the LOCAL tmux binary (no SSH): `tmux set-option @duck_loop …`
// without -t targets the current session. It is meant to be called from the loop
// itself (e.g. the loop prompt runs `duck loop on` on the first iteration and
// `duck loop off` when it ends) or from a Claude Code hook. duck never sets this
// marker on its own — it has no view into Claude Code's loop state, so the signal
// has to come from the loop side; duck only READS it in the picker.
package command

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
)

var loopCmd = &cobra.Command{
	Use:       "loop <on|off>",
	Short:     "Mark the current tmux session as running a /loop (pins it in the picker)",
	Args:      cobra.ExactArgs(1),
	ValidArgs: []string{"on", "off"},
	RunE: func(c *cobra.Command, args []string) error {
		on, err := parseLoopState(args[0])
		if err != nil {
			return err
		}
		return setLoopMarker(on)
	},
}

// parseLoopState maps the on/off argument to a boolean, rejecting anything else
// with a clear message so a typo never silently no-ops.
func parseLoopState(arg string) (bool, error) {
	switch arg {
	case "on", "1", "true":
		return true, nil
	case "off", "0", "false":
		return false, nil
	default:
		return false, fmt.Errorf("loop: want 'on' or 'off', got %q", arg)
	}
}

// setLoopMarker sets (@duck_loop=1) or clears (-u) the marker on the current tmux
// session via the local tmux binary. It requires being inside tmux ($TMUX set) —
// the loop runs in the hub session, so that is exactly where this is called.
func setLoopMarker(on bool) error {
	if os.Getenv("TMUX") == "" {
		return fmt.Errorf("loop: not inside a tmux session (run this in the hub session the loop runs in)")
	}
	var args []string
	if on {
		args = []string{"set-option", loopMarkerOption, "1"}
	} else {
		// -u unsets the option so the session drops back to its normal rank.
		args = []string{"set-option", "-u", loopMarkerOption}
	}
	out, err := exec.Command("tmux", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("loop: tmux %v: %v: %s", args, err, out)
	}
	if on {
		fmt.Println("loop: marked this session (pinned in the picker)")
	} else {
		fmt.Println("loop: unmarked this session")
	}
	return nil
}

// loopMarkerOption is the tmux user option the picker reads to pin looped
// sessions. It MUST match internal/session.loopOption (kept in sync by hand —
// the command layer does not import the private const).
const loopMarkerOption = "@duck_loop"
