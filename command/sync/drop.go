// drop.go implements `duck sync drop <dir>` — the prune-a-mirror story
// (DESIGN §1 / PLAN M5). Where the other bundle commands operate on a named
// bundle, drop is keyed on a *directory*: it stops this machine's Mutagen
// session (and its filesystem watch) for that one synced dir, leaving the local
// files, the hub copy, and every other mirror untouched. This is how
// accumulated mirrors get cleaned up after `duck` auto-syncs a string of
// throwaway dirs.
//
// A dir can in principle appear in more than one bundle; drop stops the mirror
// for it in every bundle that tracks it, reporting the count.
package sync

import (
	"fmt"

	"github.com/DigiBugCat/duck/internal/actions"
	"github.com/DigiBugCat/duck/internal/config"
	"github.com/DigiBugCat/duck/internal/hub"
	"github.com/spf13/cobra"
)

var dropCmd = &cobra.Command{
	Use:   "drop <dir>",
	Short: "Stop syncing one directory on this machine (the file stays on disk)",
	Long: `drop terminates this machine's Mutagen session (and its filesystem watch)
for <dir>, pruning that one mirror. The local files are NOT deleted, and the hub
copy plus every other synced directory are untouched.

<dir> is matched against the paths tracked in your bundles on the hub; use
tilde-form (~/dev/foo) or an absolute/relative path. If the directory is tracked
by more than one bundle, the mirror is stopped in each of them.`,
	Args: cobra.ExactArgs(1),
	RunE: func(c *cobra.Command, args []string) error {
		tildePath := normalizeToTilde(args[0])

		cfg, err := config.RequireHub()
		if err != nil {
			return err
		}

		bundles, err := bundlesTracking(cfg.Hub, tildePath)
		if err != nil {
			return err
		}
		if len(bundles) == 0 {
			return fmt.Errorf("no synced bundle tracks %s (nothing to drop)", tildePath)
		}

		var dropped int
		for _, bundle := range bundles {
			// Hub-owned mode: this machine's session lives on the hub's daemon.
			drop := func() error { return actions.DropPath(cfg.Hub, bundle, tildePath) }
			if cfg.MachineAddr != "" {
				b := bundle
				drop = func() error { return actions.DropPathHubOwned(cfg.Hub, cfg.MachineAddr, b, tildePath) }
			}
			if err := drop(); err != nil {
				fmt.Printf("warning: %s in %s: %v\n", tildePath, bundle, err)
				continue
			}
			dropped++
		}
		fmt.Printf("dropped %s on this machine (%d mirror%s stopped)\n", tildePath, dropped, plural(dropped))
		return nil
	},
}

// bundlesTracking returns the names of the hub's bundles that track tildePath,
// so drop can stop the mirror in each. It is read-only against the hub (list
// bundles, list each bundle's paths).
func bundlesTracking(addr, tildePath string) ([]string, error) {
	h := hub.New(addr)
	names, err := h.ListBundles()
	if err != nil {
		return nil, err
	}
	var tracking []string
	for _, name := range names {
		entries, err := h.ListPaths(name)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.TildePath == tildePath {
				tracking = append(tracking, name)
				break
			}
		}
	}
	return tracking, nil
}

func init() { syncCmd.AddCommand(dropCmd) }
