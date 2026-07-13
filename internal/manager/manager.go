// Package manager builds the command line that launches a workspace manager.
package manager

import (
	"strings"

	"github.com/DigiBugCat/duck/internal/paths"
)

// ActivityOption is stamped by Claude Code after the manager receives its first prompt.
// Session cleanup uses this dedicated marker to distinguish an unused manager from one
// the user worked in.
const ActivityOption = "@duck_manager_active"

// Line builds the manager launch line and adds the activity hook unless the caller
// supplied its own settings overlay.
func Line(extraArgs []string) string {
	line := "claude"
	for _, a := range extraArgs {
		line += " " + paths.Quote(a)
	}
	if !settingsWired(extraArgs) {
		line += " --settings " + paths.Quote(hookSettings)
	}
	return line
}

const hookSettings = `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"tmux set-option -p -t \"$TMUX_PANE\" @duck_manager_active 1"}]}]}}`

func settingsWired(extraArgs []string) bool {
	for _, a := range extraArgs {
		if a == "--settings" || strings.HasPrefix(a, "--settings=") {
			return true
		}
	}
	return false
}
