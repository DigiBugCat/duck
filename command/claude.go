// `duck claude [args...]`: launch claude inside a fresh duck-managed tmux
// session and attach. It is the target of the host-side `claude` shell wrapper:
// when you start claude in a plain terminal/Ghostty window (NOT already inside a
// tmux/duck session), the wrapper routes here so the session is a first-class
// duck session — named, @duck_dir stamped, listed by `duck --resume`, and
// evict/revivable — and therefore resumable remotely from the laptop. Inside an
// existing duck session the wrapper runs claude directly, so this never nests a
// session in a session.
package command

import (
	"os"

	"github.com/DigiBugCat/duck/internal/paths"
	"github.com/spf13/cobra"
)

var claudeCmd = &cobra.Command{
	Use:   "claude [args...]",
	Short: "Launch claude in a fresh duck session (so it's resumable remotely)",
	Long: `Create a new duck-managed tmux session in the current directory, run
'claude' (passing through any args, including profile flags like --ben) inside
it, and attach. The session is a first-class duck session — it shows up in
'duck --resume' and is evict/revivable — so a claude you start in a plain
terminal is resumable from your laptop.

This is meant to be driven by the host-side 'claude' shell wrapper, which calls
it ONLY when you are not already inside a tmux/duck session, so it never nests a
duck session inside another.`,
	// Pass every arg straight through to claude (e.g. --resume, --model, --ben)
	// instead of letting cobra interpret them as duck flags.
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
		tildeDir := paths.Contract(cwd)
		// Always a FRESH session: each claude you launch is its own resumable
		// session, mirroring starting a new claude conversation.
		id, _, err := w.flow.EnsureSession(tildeDir, true)
		if err != nil {
			return err
		}
		// The in-pane line runs the BARE WORD `claude` (see managerline.go): the
		// pane's interactive shell defines a `claude` FUNCTION that owns profile
		// flags (--ben/--will → CLAUDE_CONFIG_DIR + token) and otherwise forwards to
		// `command claude`. Inside a tmux/duck pane that function never routes back
		// to `duck claude`, so there is no alias loop. Channel flags are appended by
		// managerLine unless already wired. The duck-agents MCP registration
		// self-installs via the hub-side PersistentPreRun hook (duck panel runs it).
		// One batched remote command: the launch line + the @duck_manager pane
		// stamp (see managerLaunchCmd) — a single ssh roundtrip.
		if _, err := w.client.Run(managerLaunchCmd(id, managerLine(args))); err != nil {
			return err
		}
		// Stamp the durable record so the workspace is channel-aware. Best-effort:
		// a ledger write must never block launching claude.
		stampManagerLaunched(w, tildeDir, id, args)
		// Join the background name/ledger bookkeeping EnsureSession started before
		// handing the terminal over — the writes must land before duck exits.
		w.flow.WaitBackground()
		// Hand off to the interactive attach (reconnect loop), same as bare `duck`.
		runAttachLoop(w.sessions, id, "", w.tsshAttach)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(claudeCmd)
}
