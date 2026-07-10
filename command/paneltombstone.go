// The panel subcommand is RETIRED (the sidebar was deleted in v0.37.0 —
// agents are native tmux panes/windows now). This tombstone exists because
// the verb is still invoked by legacy callers: pre-teardown zshrc claude
// wrappers ran `duck panel` on every claude start, and old workspace launch
// lines carry it too. Without it, cobra treats "panel" as bare duck's FOLDER
// positional and every legacy call mints a workspace in $HOME — combined
// with the wrapper, a literal session fork bomb (109 runaway workspaces on
// pelican, 2026-07-10). A no-op that exits 0 keeps those callers harmless.
package command

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var panelTombstoneCmd = &cobra.Command{
	Use:    "panel",
	Hidden: true,
	Short:  "retired: the sidebar was replaced by native tmux windows",
	// Swallow legacy flags like --session <name> without erroring.
	DisableFlagParsing: true,
	RunE: func(c *cobra.Command, args []string) error {
		fmt.Fprintln(os.Stderr, "duck panel is retired: agents are native tmux windows now (try `duck palette`, prefix+Space). This no-op keeps old wrappers harmless — remove the call.")
		return nil
	},
}

func init() { rootCmd.AddCommand(panelTombstoneCmd) }
