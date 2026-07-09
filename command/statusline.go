// `duck statusline <session> <line-no>` — renders ONE agent line of the
// dynamic status bar. tmux execs it via the #() in status-format[i] on every
// status-interval tick (see tmuxdb.SyncStatusHeight), so it must be fast (one
// list-panes call) and silent on ANY failure: a broken status command would
// otherwise smear an error string across the workspace's status bar. Hidden —
// it is tmux plumbing, not a user verb.
package command

import (
	"fmt"
	"strconv"

	"github.com/DigiBugCat/duck/internal/tmuxdb"
	"github.com/spf13/cobra"
)

var statuslineCmd = &cobra.Command{
	Use:    "statusline <session> <line-no>",
	Short:  "Render one status-bar agent line (tmux #() plumbing)",
	Hidden: true,
	Args:   cobra.ExactArgs(2),
	Run: func(c *cobra.Command, args []string) {
		lineNo, err := strconv.Atoi(args[1])
		if err != nil {
			return // print nothing, exit 0 — see package comment
		}
		line, err := tmuxdb.StatusLine(tmuxdb.ExecRunner, args[0], lineNo)
		if err != nil || line == "" {
			return
		}
		fmt.Println(line)
	},
}

func init() {
	rootCmd.AddCommand(statuslineCmd)
}
