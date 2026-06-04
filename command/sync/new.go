// Ported from flok/cmd/new.go; registers on syncCmd instead of rootCmd.
package sync

import (
	"fmt"

	"github.com/DigiBugCat/duck/internal/actions"
	"github.com/DigiBugCat/duck/internal/config"
	"github.com/DigiBugCat/duck/internal/hub"
	"github.com/spf13/cobra"
)

var newCmd = &cobra.Command{
	Use:   "new <name>",
	Short: "Create a new empty bundle on the hub",
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
		if err := actions.NewBundle(cfg.Hub, name); err != nil {
			return err
		}
		fmt.Printf("created bundle: %s\n", name)
		fmt.Printf("next: duck sync add %s <path>\n", name)
		return nil
	},
}

func init() { syncCmd.AddCommand(newCmd) }
