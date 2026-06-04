// `duck clean`: kill all detached sessions (DESIGN §1). Attached sessions are
// left alone; each killed session also forgets its names.json entry.
package command

import (
	"fmt"

	"github.com/spf13/cobra"
)

var cleanCmd = &cobra.Command{
	Use:   "clean",
	Short: "Kill all detached sessions (attached ones are left alone)",
	Args:  cobra.NoArgs,
	RunE: func(c *cobra.Command, args []string) error {
		w, err := build()
		if err != nil {
			return err
		}
		// CleanDetached batches the work: ONE names.json load + ONE save around the
		// kill loop (vs K round-trips per-session), skips attached sessions (the
		// safety contract), prints "killed <name>" per success, and PROPAGATES a
		// hub/List error so a dead hub never masquerades as "nothing to clean".
		killed, err := w.app.CleanDetached()
		if err != nil {
			return err
		}
		if killed == 0 {
			fmt.Println("no detached sessions to clean")
		}
		return nil
	},
}
