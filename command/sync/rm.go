// Ported from flok/cmd/rm.go; registers on syncCmd instead of rootCmd.
package sync

import (
	"fmt"

	"github.com/DigiBugCat/duck/internal/actions"
	"github.com/DigiBugCat/duck/internal/config"
	"github.com/DigiBugCat/duck/internal/hub"
	"github.com/DigiBugCat/duck/internal/paths"
	"github.com/spf13/cobra"
)

var rmCmd = &cobra.Command{
	Use:   "rm <name> <path>",
	Short: "Remove a path from a bundle (stops sync everywhere)",
	Long: `rm removes <path> from the bundle on the hub and terminates the local
mutagen sync session for it. Other machines will see their local sync sessions
fail on next contact and should be cleaned up with 'duck sync drop' or
'duck sync status'.

The local files on this machine are NOT deleted; only the sync is stopped.`,
	Args: cobra.ExactArgs(2),
	RunE: func(c *cobra.Command, args []string) error {
		name, rawPath := args[0], args[1]
		if err := hub.ValidateBundleName(name); err != nil {
			return err
		}
		tildePath := normalizeToTilde(rawPath)

		cfg, err := config.RequireHub()
		if err != nil {
			return err
		}
		warn, err := actions.RemovePath(cfg.Hub, name, tildePath)
		if warn != "" {
			fmt.Printf("warning: %s\n", warn)
		}
		if err != nil {
			return err
		}
		fmt.Printf("removed %s from %s\n", tildePath, name)
		return nil
	},
}

// normalizeToTilde turns an absolute or relative-to-cwd path into tilde-form
// when it sits under $HOME. Used by commands that operate on existing entries
// where we can't necessarily stat the path (it may have been deleted already).
func normalizeToTilde(p string) string {
	exp, err := paths.Expand(p)
	if err != nil {
		return p
	}
	return paths.Contract(exp)
}

func init() { syncCmd.AddCommand(rmCmd) }
