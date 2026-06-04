// Ported from flok/cmd/get.go; registers on syncCmd instead of rootCmd. The
// shared plural() helper lives in sync.go.
package sync

import (
	"fmt"

	"github.com/DigiBugCat/duck/internal/actions"
	"github.com/DigiBugCat/duck/internal/config"
	"github.com/DigiBugCat/duck/internal/hub"
	"github.com/spf13/cobra"
)

var getCmd = &cobra.Command{
	Use:   "get <name>",
	Short: "Pull a bundle onto this machine",
	Long: `get fetches the list of paths in a bundle from the hub and starts a
Mutagen sync session for each on this machine.

For each path: if it does not exist locally it is created and populated from the
hub. If it exists and is non-empty, get refuses unless --force, which starts a
two-way merge in which this machine wins conflicts.`,
	Args: cobra.ExactArgs(1),
	RunE: func(c *cobra.Command, args []string) error {
		name := args[0]
		if err := hub.ValidateBundleName(name); err != nil {
			return err
		}
		force, _ := c.Flags().GetBool("force")

		cfg, err := config.RequireHub()
		if err != nil {
			return err
		}

		results, err := actions.GetBundle(cfg.Hub, name, force)
		// Print whatever succeeded before reporting any error, mirroring the
		// previous incremental output.
		for _, r := range results {
			if r.Status == actions.SyncAlready {
				fmt.Printf("  %s already synced (%s)\n", r.Tilde, r.Session)
			} else {
				fmt.Printf("  + %s  (%s)\n", r.Tilde, r.Session)
			}
		}
		if err != nil {
			return err
		}
		if len(results) == 0 {
			fmt.Printf("bundle %q has no paths yet (empty bundle).\n", name)
			return nil
		}
		fmt.Printf("got bundle %s (%d path%s)\n", name, len(results), plural(len(results)))
		return nil
	},
}

func init() {
	getCmd.Flags().Bool("force", false, "sync even if the local path is non-empty (two-way merge; this machine wins conflicts)")
	syncCmd.AddCommand(getCmd)
}
