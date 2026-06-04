// Ported from flok/cmd/show.go; registers on syncCmd instead of rootCmd.
package sync

import (
	"fmt"

	"github.com/DigiBugCat/duck/internal/config"
	"github.com/DigiBugCat/duck/internal/hub"
	"github.com/DigiBugCat/duck/internal/mutagen"
	"github.com/DigiBugCat/duck/internal/paths"
	"github.com/spf13/cobra"
)

var showCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show paths in a bundle with this machine's sync state",
	Args:  cobra.ExactArgs(1),
	RunE: func(c *cobra.Command, args []string) error {
		name := args[0]
		if err := hub.ValidateBundleName(name); err != nil {
			return err
		}
		cfg, err := config.RequireHub()
		if err != nil {
			return err
		}
		h := hub.New(cfg.Hub)
		entries, err := h.ListPaths(name)
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			fmt.Printf("bundle %s has no paths\n", name)
			return nil
		}
		for _, e := range entries {
			sessionName := paths.SessionName(name, e.TildePath)
			here, err := mutagen.Exists(sessionName)
			if err != nil {
				return err
			}
			state := "not synced here"
			if here {
				state = "synced"
			}
			fmt.Printf("%s\t%s\n", e.TildePath, state)
		}
		return nil
	},
}

func init() { syncCmd.AddCommand(showCmd) }
