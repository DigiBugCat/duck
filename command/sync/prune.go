// prune.go implements `duck sync prune` — a user-invoked cleanup that destroys
// EMPTY, duck-owned bundles on the hub (bundles whose tracked-path list is
// empty). It is deliberately NOT a side-effect of `duck sync rm`: removing the
// last path from a bundle leaves the (now-empty) bundle in place, and only this
// explicit command reaps it, so a bundle is never destroyed out from under the
// user as an invisible consequence of removing a path.
//
// Only bundles with 0 paths are destroyed; bundles that still track real paths
// are left untouched. prune prints what it pruned.
package sync

import (
	"fmt"

	"github.com/DigiBugCat/duck/internal/config"
	"github.com/DigiBugCat/duck/internal/hub"
	"github.com/spf13/cobra"
)

var pruneCmd = &cobra.Command{
	Use:   "prune",
	Short: "Destroy empty bundles on the hub (bundles with no tracked paths)",
	Long: `prune destroys every duck-owned bundle on the hub that tracks zero paths.

Removing the last path from a bundle (duck sync rm) leaves the empty bundle in
place; prune is the explicit, user-invoked step that reaps those husks. Bundles
that still track real paths are never touched. No local files are affected — this
only removes empty bundle metadata under ~/.duck/bundles on the hub.`,
	Args: cobra.NoArgs,
	RunE: func(c *cobra.Command, args []string) error {
		cfg, err := config.RequireHub()
		if err != nil {
			return err
		}

		empties, err := emptyBundles(cfg.Hub)
		if err != nil {
			return err
		}
		if len(empties) == 0 {
			fmt.Println("(nothing to prune)")
			return nil
		}

		h := hub.New(cfg.Hub)
		var pruned int
		for _, name := range empties {
			if err := h.DestroyBundle(name); err != nil {
				fmt.Printf("warning: could not prune %s: %v\n", name, err)
				continue
			}
			fmt.Printf("pruned %s\n", name)
			pruned++
		}
		fmt.Printf("pruned %d empty bundle%s\n", pruned, plural(pruned))
		return nil
	},
}

// emptyBundles returns the names of the hub's bundles that track zero paths, so
// prune can destroy exactly those (and only those). It is read-only against the
// hub (list bundles, list each bundle's paths); the destroy happens in RunE.
func emptyBundles(addr string) ([]string, error) {
	h := hub.New(addr)
	names, err := h.ListBundles()
	if err != nil {
		return nil, err
	}
	var empty []string
	for _, name := range names {
		entries, err := h.ListPaths(name)
		if err != nil {
			return nil, err
		}
		if len(entries) == 0 {
			empty = append(empty, name)
		}
	}
	return empty, nil
}

func init() { syncCmd.AddCommand(pruneCmd) }
