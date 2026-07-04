// `duck claude [args...]`: launch claude inside a fresh duck-managed tmux
// session (running `cass claude <args>`) and attach. It is the target of the
// host-side `claude` shell wrapper: when you start claude in a plain
// terminal/Ghostty window (NOT already inside a tmux/duck session), the wrapper
// routes here so the session is a first-class duck session — named, @duck_dir
// stamped, listed by `duck --resume`, and evict/revivable — and therefore
// resumable remotely from the laptop. Inside an existing duck session the
// wrapper runs claude directly, so this never nests a session in a session.
package command

import (
	"os"
	"strings"

	"github.com/DigiBugCat/duck/internal/claude"
	"github.com/DigiBugCat/duck/internal/paths"
	"github.com/spf13/cobra"
)

// channelsWired reports whether the user already opted into (or out of)
// Claude's channel flags, so duck's auto-wiring stays out of the way.
func channelsWired(args []string) bool {
	if os.Getenv("DUCK_NO_CHANNELS") != "" {
		return true
	}
	for _, a := range args {
		if a == "--channels" || strings.HasPrefix(a, "--channels=") ||
			a == "--dangerously-load-development-channels" {
			return true
		}
	}
	return false
}

// claudeConfigHome resolves the dir holding Claude's config (~/.claude.json),
// honoring CLAUDE_CONFIG_DIR the way command/agentdoc.go does.
func claudeConfigHome() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

// claudeRunner is the in-session program the launched command runs. cass wraps
// claude with per-project .cass.toml + MCP-key refresh; the host-side wrapper
// alias resolves `claude` to this, so we invoke cass explicitly here (the
// session's own shell would otherwise re-trigger the alias and loop).
const claudeRunner = "cass claude"

var claudeCmd = &cobra.Command{
	Use:   "claude [args...]",
	Short: "Launch claude in a fresh duck session (so it's resumable remotely)",
	Long: `Create a new duck-managed tmux session in the current directory, run
'cass claude' (passing through any args) inside it, and attach. The session is a
first-class duck session — it shows up in 'duck --resume' and is evict/revivable
— so a claude you start in a plain terminal is resumable from your laptop.

This is meant to be driven by the host-side 'claude' shell wrapper, which calls
it ONLY when you are not already inside a tmux/duck session, so it never nests a
duck session inside another.`,
	// Pass every arg straight through to claude (e.g. --resume, --model) instead of
	// letting cobra interpret them as duck flags.
	DisableFlagParsing: true,
	RunE: func(c *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		w, err := build()
		if err != nil {
			return err
		}
		// Wire the Claude Code channel sidecar so the manager Claude hears its
		// sidebar agents and duck-side publishes. Best-effort throughout — a
		// registry write or a read-only home must NEVER block launching claude.
		// NOTE: the in-tmux `claude` shell function (used when you start claude
		// already inside a duck session) bypasses `duck claude`, so it needs the
		// same --channels flags added independently — a docs/wrapper problem, not
		// something this code path can fix.
		if !channelsWired(args) {
			if home := claudeConfigHome(); home != "" {
				_, _ = claude.NewRegistry(home).EnsureMCPServer("duck-agents", map[string]any{
					"command": "duck",
					"args":    []any{"channel", "serve"},
				})
			}
			args = append(args, "--channels", "server:duck-agents", "--dangerously-load-development-channels")
		}
		tildeDir := paths.Contract(cwd)
		// Always a FRESH session: each claude you launch is its own resumable
		// session, mirroring starting a new claude conversation.
		id, _, err := w.flow.EnsureSession(tildeDir, true)
		if err != nil {
			return err
		}
		// Build the in-pane command line: open the agent sidebar first (duck
		// panel is idempotent and quick; agents spawned later land in it), then
		// `cass claude` plus each arg shell-quoted so the pane's shell re-parses
		// them exactly as given. Panel failure must never block claude.
		line := "duck panel >/dev/null 2>&1; " + claudeRunner
		for _, a := range args {
			line += " " + paths.Quote(a)
		}
		if err := w.sessions.Send(id, line); err != nil {
			return err
		}
		// Hand off to the interactive attach (reconnect loop), same as bare `duck`.
		runAttachLoop(w.sessions, id, "", w.tsshAttach)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(claudeCmd)
}
