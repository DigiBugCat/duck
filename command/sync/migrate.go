// migrate.go implements `duck sync migrate` — the one-shot bulk handover of
// this machine's LAPTOP-owned mutagen sessions to the HUB's daemon (hub-owned
// sync, enabled by `duck config machine-addr`). Without it migration is lazy:
// each folder hands over the first time you duck into it. migrate does them
// all now, so the local daemon's ledger empties in one pass instead of
// lingering for weeks as a second source of drift.
//
// Per session the order is terminate-local FIRST, then create hub-owned: the
// data already exists on both sides, so the gap between the two is harmless,
// whereas the reverse order would briefly have two daemons syncing the same
// directory (conflict ping-pong — the one genuinely dangerous state).
package sync

import (
	"fmt"
	"strings"

	"github.com/DigiBugCat/duck/internal/actions"
	"github.com/DigiBugCat/duck/internal/config"
	"github.com/DigiBugCat/duck/internal/hub"
	"github.com/DigiBugCat/duck/internal/mutagen"
	"github.com/DigiBugCat/duck/internal/paths"
	"github.com/spf13/cobra"
)

var migrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Hand this machine's laptop-owned sync sessions over to the hub's daemon",
	Long: `migrate terminates every duck sync session on THIS machine's mutagen daemon
that points at the current hub and recreates each one on the HUB's daemon
(alpha = hub path, beta = this machine), so the hub owns the whole fleet's
sync sessions and per-machine config drift can no longer accumulate.

Requires hub-owned sync to be enabled first:

  duck config machine-addr <user@host-the-hub-can-ssh-to>

Sessions pointing at a DIFFERENT hub (stale, pre-migration leftovers) are
terminated but not recreated — the current hub never received that folder, so
recreating would mirror it somewhere it was never seeded. Duck into those
folders once to re-seed them properly. Extra per-session ignore patterns from
the old sessions are not carried over (mutagen does not expose them); the
recreated sessions use duck's current default ignore list.`,
	Args: cobra.NoArgs,
	RunE: func(c *cobra.Command, args []string) error {
		cfg, err := config.RequireHub()
		if err != nil {
			return err
		}
		if cfg.MachineAddr == "" {
			return fmt.Errorf("hub-owned sync is not enabled on this machine.\nRun: duck config machine-addr <user@host-the-hub-can-ssh-to>")
		}

		sessions, err := mutagen.List()
		if err != nil {
			return err
		}
		if len(sessions) == 0 {
			fmt.Println("(no laptop-owned sessions to migrate)")
			return nil
		}

		h := hub.New(cfg.Hub)
		var migrated, retired, failed int
		for _, s := range sessions {
			tilde := paths.Contract(s.Alpha.Path)
			if !s.Beta.MatchesHub(cfg.Hub) {
				// Points at a previous hub: the current hub never received this
				// folder, so only retire it (see Long above).
				if err := mutagen.Terminate(s.Name); err != nil {
					fmt.Printf("warning: could not retire stale session %s (%s): %v\n", s.Name, tilde, err)
					failed++
					continue
				}
				fmt.Printf("retired %s (%s — pointed at old hub %s; duck into it to re-seed)\n", s.Name, tilde, s.Beta.Display())
				retired++
				continue
			}
			bundle := bundleFromSessionName(s.Name)
			if bundle == "" {
				fmt.Printf("warning: skipping %s (%s): not a duck session name\n", s.Name, tilde)
				failed++
				continue
			}
			if err := mutagen.Terminate(s.Name); err != nil {
				fmt.Printf("warning: could not terminate %s (%s): %v\n", s.Name, tilde, err)
				failed++
				continue
			}
			hubName := paths.HubSessionName(bundle, tilde, cfg.MachineAddr)
			if _, err := h.Run(actions.HubsyncAddCmd(hubName, tilde, cfg.MachineAddr, s.Alpha.Path, nil)); err != nil {
				// The local session is already gone; the folder is now unsynced
				// until the user ducks into it. Say so plainly.
				fmt.Printf("error: %s (%s) was terminated locally but the hub-owned recreate failed: %v\n", s.Name, tilde, err)
				fmt.Printf("       duck into %s to re-establish its sync.\n", tilde)
				failed++
				continue
			}
			fmt.Printf("migrated %s → %s (hub-owned)\n", tilde, hubName)
			migrated++
		}
		fmt.Printf("migrated %d, retired %d stale, %d problem%s — the hub's daemon now owns this machine's sessions\n",
			migrated, retired, failed, plural(failed))
		if failed > 0 {
			return fmt.Errorf("%d session%s need attention (see above)", failed, plural(failed))
		}
		return nil
	},
}

// bundleFromSessionName recovers the bundle from a duck session name,
// "duck-<bundle>-<12 hex>". Bundles may themselves contain dashes, so it
// strips the fixed prefix and the fixed-width ID suffix rather than splitting.
// Returns "" when the name doesn't fit the shape.
func bundleFromSessionName(name string) string {
	rest, ok := strings.CutPrefix(name, "duck-")
	if !ok || len(rest) < 14 { // at least a 1-char bundle + "-" + 12 hex
		return ""
	}
	return rest[:len(rest)-13]
}

func init() { syncCmd.AddCommand(migrateCmd) }
