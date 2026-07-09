// `duck kill <name>`: kill a remote session and forget its names.json entry
// (DESIGN §1). <name> is the internal tmux name (as shown by `duck ls`).
package command

import (
	"fmt"

	"github.com/spf13/cobra"
)

var killCmd = &cobra.Command{
	Use:   "kill <name>",
	Short: "Kill a remote session and forget its name",
	Args:  cobra.ExactArgs(1),
	RunE: func(c *cobra.Command, args []string) error {
		w, err := build()
		if err != nil {
			return err
		}
		if err := w.app.Kill(args[0]); err != nil {
			return err
		}
		fmt.Printf("killed %s\n", args[0])
		return nil
	},
}
