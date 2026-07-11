// Package manager builds the in-pane command line that launches a duck
// workspace's MANAGER — the Claude that runs in the main pane and drives the
// workspace's agent flock. A duck workspace is an EMPLOYEE whose manager
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
// It lives in its own internal package (not command/) so both the command layer
// (bare `duck` / `duck claude`) and session heal/revive share ONE implementation
// without an import cycle.
package manager

import (
	"os"
	"strings"

	"github.com/DigiBugCat/duck/internal/paths"
)

// channelFlags are the Claude Code channel-sidecar flags duck auto-appends so
// the manager hears its workspace agents and duck-side publishes. NOTE the dev
// flag takes the server LIST as its own argument (`claude
// --dangerously-load-development-channels server:duck-agents`) — it is not a
// boolean, and combining it with a separate --channels does NOT extend the
// allowlist bypass to those entries (research-preview docs), so the dev flag
// alone is the whole story. Kept as data so Line and ChannelsWired share one
// source of truth.
var channelFlags = []string{"--dangerously-load-development-channels", "server:duck-agents"}

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

// Line is the single in-pane launch line for the workspace manager: bare
// `claude` with each extraArg shell-quoted so the pane's shell re-parses them
// exactly as given, then the channel flags UNLESS already wired
// (ChannelsWired), then the prompt-hook settings UNLESS the caller passed
// their own --settings. There is no sidebar to arm — agents are ordinary
// panes/windows of the session, so the launch line is just claude + channel
// flags. MCP registration rides the hub-side PersistentPreRun hook.
func Line(extraArgs []string) string {
	line := "claude"
	for _, a := range extraArgs {
		line += " " + paths.Quote(a)
	}
	if !ChannelsWired(extraArgs) {
		for _, f := range channelFlags {
			line += " " + paths.Quote(f)
		}
	}
	if !settingsWired(extraArgs) {
		line += " --settings " + paths.Quote(hookSettings)
	}
	return line
}

// hookSettings is the inline Claude Code settings JSON the launch line carries:
// a UserPromptSubmit hook targeting `duck channel hook` (payload on stdin, same
// contract as the codex agent hooks). The FIRST real prompt stamps @duck_state
// on the manager pane, which is what lets session.IsUntouched tell an
// accidentally-started workspace (manager launched, never prompted) from a
// worked-in one. Bare word `duck` on purpose: the line runs in the HUB pane's
// shell, so the hub's PATH resolves it — a laptop-side absolute path would be
// wrong there. No single quotes in the JSON, so paths.Quote wraps it safely.
const hookSettings = `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"duck channel hook"}]}]}}`

// settingsWired reports whether extraArgs already carry a --settings flag, in
// which case duck's hook overlay stays out of the way (claude takes one
// --settings; duck must not fight the user's).
func settingsWired(extraArgs []string) bool {
	for _, a := range extraArgs {
		if a == "--settings" || strings.HasPrefix(a, "--settings=") {
			return true
		}
	}
	return false
}
