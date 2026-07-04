// Package manager builds the in-pane command line that launches a duck
// workspace's MANAGER — the Claude that runs in the main pane and drives the
// workspace's sidebar agent flock. A duck workspace is an EMPLOYEE whose manager
// is claude in the main pane, so duck OWNS this launch line (channel flags
// included) and sends it into the main pane on workspace creation/heal, rather
// than the human typing `claude` by hand.
//
// The line always invokes the BARE WORD `claude` (not `cass claude`, not
// `command claude`) so the interactive shell's `claude` FUNCTION keeps owning
// profile flags (`--ben`/`--will` set CLAUDE_CONFIG_DIR + an OAuth token; they
// are NOT claude flags). That function's only routing is intercept-then-`command
// claude`; inside a tmux/duck pane it never loops back to `duck claude`. Profile
// args the human passes to duck are forwarded VERBATIM onto the line.
//
// It lives in its own internal package (not command/) so BOTH the command layer
// (bare `duck` / `duck claude`) and the hub-side routines tick (heal) share ONE
// implementation without an import cycle (command imports routines).
package manager

import (
	"os"
	"strings"

	"github.com/DigiBugCat/duck/internal/paths"
)

// channelFlags are the Claude Code channel-sidecar flags duck auto-appends so
// the manager hears its sidebar agents and duck-side publishes. Kept as data so
// Line and ChannelsWired share one source of truth.
var channelFlags = []string{"--channels", "server:duck-agents", "--dangerously-load-development-channels"}

// ChannelsWired reports whether the caller already opted into (or out of)
// Claude's channel flags, so duck's auto-wiring stays out of the way. It is true
// when DUCK_NO_CHANNELS is set OR extraArgs already carry --channels /
// --dangerously-load-development-channels (in any form).
func ChannelsWired(extraArgs []string) bool {
	if os.Getenv("DUCK_NO_CHANNELS") != "" {
		return true
	}
	for _, a := range extraArgs {
		if a == "--channels" || strings.HasPrefix(a, "--channels=") ||
			a == "--dangerously-load-development-channels" {
			return true
		}
	}
	return false
}

// Line is the single in-pane launch line for the workspace manager. It opens the
// sidebar first (duck panel is idempotent and quick; the hub-side `duck` run
// also self-installs the duck-agents MCP registration via the PersistentPreRun
// hook), then runs bare `claude` with each extraArg shell-quoted so the pane's
// shell re-parses them exactly as given, then the channel flags UNLESS they are
// already wired (ChannelsWired).
func Line(extraArgs []string) string {
	line := "duck panel >/dev/null 2>&1; claude"
	for _, a := range extraArgs {
		line += " " + paths.Quote(a)
	}
	if !ChannelsWired(extraArgs) {
		for _, f := range channelFlags {
			line += " " + paths.Quote(f)
		}
	}
	return line
}
