// `duck panel` — the agent sidebar. Run from INSIDE a tmux/duck session, it
// splits off a right-hand column: the top pane is a live, interactive view of
// the currently selected agent (a nested tmux client on the hidden
// "<session>-agents" companion), the bottom pane lists every launched agent
// (click / ↵ to view). `duck panel close` removes the panes; the agents keep
// running. `duck panel watch` is the hidden entrypoint the list pane runs.
//
// Everything talks to the LOCAL tmux server: the panel runs where the session
// lives (the hub when attached through duck), so no SSH wiring is needed.
package command

import (
	"fmt"
	"os"

	"github.com/DigiBugCat/duck/internal/channel"
	"github.com/DigiBugCat/duck/internal/panel"
	"github.com/spf13/cobra"
)

var panelCmd = &cobra.Command{
	Use:   "panel",
	Short: "Open the agent sidebar in the current tmux session",
	Long: `Split the current tmux window into a main pane plus a right-hand sidebar:
a live view of the selected agent on top, and a clickable list of launched
agents below. Agents are spawned with 'duck spawn <cmd>'. Must be run from
inside a tmux (duck) session.`,
	RunE: func(c *cobra.Command, args []string) error {
		run := panel.ExecRunner
		outer, dir, err := panelContext(run)
		if err != nil {
			return err
		}
		comp, err := panel.EnsureCompanion(run, outer, dir)
		if err != nil {
			return err
		}
		bin, err := os.Executable()
		if err != nil {
			bin = "duck"
		}
		return panel.Open(run, outer, comp, bin)
	},
}

var panelCloseCmd = &cobra.Command{
	Use:   "close",
	Short: "Close the sidebar (agents keep running)",
	RunE: func(c *cobra.Command, args []string) error {
		run := panel.ExecRunner
		outer, _, err := panelContext(run)
		if err != nil {
			return err
		}
		return panel.Close(run, outer)
	},
}

// panelWatchCmd is the program the list pane runs; not for humans.
var panelWatchCmd = &cobra.Command{
	Use:    "watch <session>",
	Hidden: true,
	Args:   cobra.ExactArgs(1),
	RunE: func(c *cobra.Command, args []string) error {
		// One Resolver for the TUI's lifetime: pairing is memoized, failed
		// pairings retry on a throttle, and status scans skip unchanged files —
		// without it the 2s poll would re-scan the rollout tree per agent forever.
		res := channel.NewResolver(panel.ExecRunner)
		return panel.Watch(panel.ExecRunner, args[0], res.Status)
	},
}

// panelContext resolves the enclosing tmux session and pane cwd, erroring
// with a hint when not inside tmux.
func panelContext(run panel.Runner) (outer, dir string, err error) {
	if !panel.InsideTmux() {
		return "", "", fmt.Errorf("duck panel only works inside a tmux session — run `duck` first, then `duck panel`")
	}
	if outer, err = panel.CurrentSession(run); err != nil {
		return "", "", err
	}
	dir, err = panel.CurrentPanePath(run)
	return outer, dir, err
}

func init() {
	panelCmd.AddCommand(panelCloseCmd, panelWatchCmd)
	rootCmd.AddCommand(panelCmd)
}
