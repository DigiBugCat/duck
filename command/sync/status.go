// Ported from flok/cmd/status.go; registers on syncCmd instead of rootCmd.
// Hub-owned mode: when this machine has opted into hub-owned sync (machine_addr
// set), its sessions live on the HUB's daemon and the local daemon is empty —
// status reads the hub's ledger for this machine instead, so it keeps telling
// the truth about what is actually syncing.
package sync

import (
	"fmt"

	"github.com/DigiBugCat/duck/internal/actions"
	"github.com/DigiBugCat/duck/internal/config"
	"github.com/DigiBugCat/duck/internal/mutagen"
	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show this machine's duck-managed Mutagen sessions",
	Args:  cobra.NoArgs,
	RunE: func(c *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if cfg.MachineAddr != "" && cfg.Hub != "" {
			sessions, err := actions.HubOwnedSessions(cfg.Hub, cfg.MachineAddr)
			if err != nil {
				return fmt.Errorf("reading the hub's session ledger: %w", err)
			}
			fmt.Printf("hub-owned sync (sessions live on %s's daemon):\n", cfg.Hub)
			if len(sessions) == 0 {
				fmt.Println("(no duck sessions for this machine)")
			}
			for _, s := range sessions {
				// Alpha is this machine's path, Beta carries the hub path (see
				// actions.HubOwnedSessions' perspective mapping).
				fmt.Printf("%s\n  local:  %s\n  hub:    %s\n  status: %s\n", s.Name, s.Alpha.Path, s.Beta.Path, s.Status)
			}
			// A leftover local session alongside hub-owned mode means two daemons
			// could fight over one directory — make it loudly visible.
			if leftovers, lerr := mutagen.List(); lerr == nil && len(leftovers) > 0 {
				fmt.Printf("\nwarning: %d laptop-owned duck session(s) still on the LOCAL daemon (run `duck sync migrate`):\n", len(leftovers))
				for _, s := range leftovers {
					fmt.Printf("  %s (%s)\n", s.Name, s.Alpha.Path)
				}
			}
			return nil
		}
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
