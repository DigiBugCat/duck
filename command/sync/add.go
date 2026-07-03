// Ported from flok/cmd/add.go; registers on syncCmd instead of rootCmd.
package sync

import (
	"fmt"

	"github.com/DigiBugCat/duck/internal/actions"
	"github.com/DigiBugCat/duck/internal/config"
	"github.com/DigiBugCat/duck/internal/hub"
	"github.com/spf13/cobra"
)

var addCmd = &cobra.Command{
	Use:   "add <name> <path>",
	Short: "Add a local path to a bundle and start syncing it",
	Long: `add registers <path> with the named bundle on the hub and starts a
Mutagen sync session between this machine and the hub.

<path> must exist locally; it will be the seed for the bundle on the hub.
Use tilde-form (~/foo) or absolute paths; the path is stored in tilde-form
so it can be replayed on machines with a different home directory.`,
	Args: cobra.ExactArgs(2),
	RunE: func(c *cobra.Command, args []string) error {
		name, rawPath := args[0], args[1]
		if err := hub.ValidateBundleName(name); err != nil {
			return err
		}

		force, _ := c.Flags().GetBool("force")
		ignores, _ := c.Flags().GetStringArray("ignore")

		cfg, err := config.RequireHub()
		if err != nil {
			return err
		}

		// Hub-owned mode: the session must live on the hub's daemon, or this
		// command would quietly reintroduce the laptop-owned drift the mode
		// exists to eliminate (and set up a two-daemons-one-dir fight).
		var entry hub.PathEntry
		var sessionName string
		if cfg.MachineAddr != "" {
			entry, sessionName, err = actions.AddPathHubOwned(cfg.Hub, cfg.MachineAddr, name, rawPath, force, ignores...)
		} else {
			entry, sessionName, err = actions.AddPath(cfg.Hub, name, rawPath, force, ignores...)
		}
		if err != nil {
			return err
		}

		hubEndpoint := fmt.Sprintf("%s:%s", cfg.Hub, hub.RemoteSyncPath(entry.TildePath))
		fmt.Printf("added %s to %s\n", entry.TildePath, name)
		fmt.Printf("  session: %s\n", sessionName)
		fmt.Printf("  hub:     %s\n", hubEndpoint)
		return nil
	},
}

func init() {
	addCmd.Flags().Bool("force", false, "merge into the hub's existing folder if it already has files (this machine wins conflicts)")
	addCmd.Flags().StringArray("ignore", nil, "extra path/glob to exclude from this sync (repeatable; e.g. --ignore .codex)")
	syncCmd.AddCommand(addCmd)
}
