// Ported from flok/cmd/ls.go; registers on syncCmd instead of rootCmd.
package sync

import (
	"fmt"

	"github.com/DigiBugCat/duck/internal/config"
	"github.com/DigiBugCat/duck/internal/hub"
	"github.com/spf13/cobra"
)

var lsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List bundles on the hub",
	Args:  cobra.NoArgs,
	RunE: func(c *cobra.Command, args []string) error {
		cfg, err := config.RequireHub()
		if err != nil {
			return err
		}
		h := hub.New(cfg.Hub)
		names, err := h.ListBundles()
		if err != nil {
			return err
		}
		if len(names) == 0 {
			fmt.Println("(no bundles)")
			return nil
		}
		for _, n := range names {
			entries, err := h.ListPaths(n)
			if err != nil {
				fmt.Printf("%s\t(error: %v)\n", n, err)
				continue
			}
			fmt.Printf("%s\t%d path%s\n", n, len(entries), plural(len(entries)))
		}
		return nil
	},
}

func init() { syncCmd.AddCommand(lsCmd) }
