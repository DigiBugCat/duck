// Package sync is duck's Mutagen-backed multi-machine path-sync command bundle,
// ported from flok's cmd/* bundle commands. Where flok registered each
// subcommand on its rootCmd, duck nests them under a `sync` parent command:
// each subcommand's init() registers on the package-level syncCmd var, and the
// parent is exported as Cmd so package command can attach it to the root with
// rootCmd.AddCommand(sync.Cmd) (attaching from here would cause an import
// cycle).
package sync

import "github.com/spf13/cobra"

// syncCmd is the parent of all bundle subcommands. Subcommands self-register on
// it via their init() functions.
var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Mutagen-backed multi-machine path sync",
	Long: `sync is a thin wrapper around Mutagen that lets you sync named bundles
of arbitrary paths between machines via the duck hub.

A bundle is a named set of paths. Each path is synced bidirectionally between
every machine that has the bundle and the hub.`,
}

// Cmd is the exported `sync` parent command. package command attaches it to the
// root via rootCmd.AddCommand(sync.Cmd).
var Cmd = syncCmd

// plural returns "s" for counts other than one. Shared by several subcommands.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
