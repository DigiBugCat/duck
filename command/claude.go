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

	"github.com/DigiBugCat/duck/internal/paths"
	"github.com/spf13/cobra"
)

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
		tildeDir := paths.Contract(cwd)
		// Always a FRESH session: each claude you launch is its own resumable
		// session, mirroring starting a new claude conversation.
		id, _, err := w.flow.EnsureSession(tildeDir, true)
		if err != nil {
			return err
		}
		// Build the in-pane command line: `cass claude` plus each arg shell-quoted
		// so the pane's shell re-parses them exactly as given.
		line := claudeRunner
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
