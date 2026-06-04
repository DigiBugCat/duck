// Ported from flok/cmd/status.go; registers on syncCmd instead of rootCmd.
package sync

import (
	"fmt"

	"github.com/DigiBugCat/duck/internal/mutagen"
	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show this machine's duck-managed Mutagen sessions",
	Args:  cobra.NoArgs,
	RunE: func(c *cobra.Command, args []string) error {
		sessions, err := mutagen.List()
		if err != nil {
			return err
		}
		if len(sessions) == 0 {
			fmt.Println("(no duck sessions)")
			return nil
		}
		for _, s := range sessions {
			fmt.Printf("%s\n  alpha:  %s\n  beta:   %s\n  status: %s\n", s.Name, s.Alpha.Display(), s.Beta.Display(), s.Status)
		}
		return nil
	},
}

func init() { syncCmd.AddCommand(statusCmd) }
