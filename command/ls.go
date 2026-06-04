// `duck ls`: list remote sessions without attaching (DESIGN §1). It reads live
// sessions + resolved names (user ▸ codex ▸ dir-derived) and prints them. The
// internal tmux name is shown last so `duck kill`/`duck rename` have a handle.
package command

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

var lsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List remote sessions without attaching",
	Args:  cobra.NoArgs,
	RunE: func(c *cobra.Command, args []string) error {
		w, err := build()
		if err != nil {
			return err
		}
		// List() is the read-only path: it resolves rows without auto-naming, so a
		// read-only-sounding `ls` never spends codex quota or writes names.json on
		// first sight of an unnamed session (auto-naming stays on the picker).
		rows, err := w.app.List()
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			fmt.Println("(no sessions)")
			return nil
		}
		tw := tabwriter.NewWriter(c.OutOrStdout(), 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tDIR\tAGE\tWIN\tATTACHED\tTMUX")
		for _, r := range rows {
			attached := ""
			if r.Attached {
				attached = "●"
			}
			dir := r.Dir
			if dir == "" {
				dir = "—" // foreign/legacy session: no @duck_dir recorded
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%dw\t%s\t%s\n", r.Display, dir, r.Age, r.Windows, attached, r.TmuxName)
		}
		return tw.Flush()
	},
}
