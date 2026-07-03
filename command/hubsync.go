// hubsync.go is the HUB-side half of hub-owned sync sessions. It is plumbing,
// not porcelain: a laptop invokes `duck hubsync ...` on the hub over SSH (duck
// is installed there by `duck hub setup`), and the hub's own mutagen daemon
// owns the session — alpha is the hub-local path, beta is the calling laptop
// (ssh). Centralizing session ownership on the hub is what makes sync config
// drift impossible by construction: there is exactly one daemon, one duck
// binary, and one DefaultIgnores list creating sessions, and `mutagen sync
// list` on the hub is the whole fleet's sync ledger.
//
// Output is deliberately machine-parseable single lines (the caller is a duck
// binary reading over SSH, not a human): `add` prints the session name, and
// `status` prints the raw mutagen status string.
package command

import (
	"fmt"
	"strings"

	"github.com/DigiBugCat/duck/internal/mutagen"
	"github.com/DigiBugCat/duck/internal/paths"
	"github.com/spf13/cobra"
)

var hubsyncCmd = &cobra.Command{
	Use:    "hubsync",
	Short:  "Hub-side sync-session plumbing (invoked by laptops over SSH)",
	Hidden: true,
}

var (
	hubsyncName     string
	hubsyncHubPath  string
	hubsyncPeer     string
	hubsyncPeerPath string
	hubsyncIgnores  []string
)

// hubsyncAddCmd declaratively ensures a session exists matching the CURRENT
// spec. If a session with the same name already runs and its duck-spec label
// matches the fingerprint of the requested spec, it is left alone (cheap
// no-op, no rescan). Any mismatch — different peer, different path, an ignore
// list from an older duck, a pre-labeling session — terminates and recreates
// it, so a stale session self-heals instead of silently keeping its
// creation-time config forever.
var hubsyncAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Ensure a hub-owned session exists matching the current spec",
	Args:  cobra.NoArgs,
	RunE: func(c *cobra.Command, _ []string) error {
		if hubsyncName == "" || hubsyncHubPath == "" || hubsyncPeer == "" || hubsyncPeerPath == "" {
			return fmt.Errorf("hubsync add requires --name, --hub-path, --peer and --peer-path")
		}
		local, err := paths.Expand(hubsyncHubPath)
		if err != nil {
			return err
		}
		beta := fmt.Sprintf("%s:%s", hubsyncPeer, hubsyncPeerPath)
		want := mutagen.SpecFingerprint(local, beta, hubsyncIgnores)

		sessions, err := mutagen.List()
		if err != nil {
			return err
		}
		for _, s := range sessions {
			if s.Name != hubsyncName {
				continue
			}
			if s.Spec == want {
				fmt.Fprintln(c.OutOrStdout(), hubsyncName)
				return nil
			}
			// Same name, stale spec: recreate against the current one.
			if err := mutagen.Terminate(hubsyncName); err != nil {
				return fmt.Errorf("retiring stale session %s: %w", hubsyncName, err)
			}
			break
		}
		if err := mutagen.Create(hubsyncName, local, beta, hubsyncIgnores); err != nil {
			return err
		}
		fmt.Fprintln(c.OutOrStdout(), hubsyncName)
		return nil
	},
}

// hubsyncStatusCmd prints the raw mutagen status string for one session — the
// laptop's waitSteady polls this over SSH until it reads watching/idle.
var hubsyncStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Print a session's mutagen status",
	Args:  cobra.NoArgs,
	RunE: func(c *cobra.Command, _ []string) error {
		if hubsyncName == "" {
			return fmt.Errorf("hubsync status requires --name")
		}
		s, err := mutagen.Monitor(hubsyncName)
		if err != nil {
			return err
		}
		fmt.Fprintln(c.OutOrStdout(), s.Status)
		return nil
	},
}

var hubsyncFlushCmd = &cobra.Command{
	Use:   "flush",
	Short: "Force an immediate sync cycle for a session",
	Args:  cobra.NoArgs,
	RunE: func(c *cobra.Command, _ []string) error {
		if hubsyncName == "" {
			return fmt.Errorf("hubsync flush requires --name")
		}
		return mutagen.Flush(hubsyncName)
	},
}

var hubsyncTerminateCmd = &cobra.Command{
	Use:   "terminate",
	Short: "Terminate a hub-owned session (missing is a no-op)",
	Args:  cobra.NoArgs,
	RunE: func(c *cobra.Command, _ []string) error {
		if hubsyncName == "" {
			return fmt.Errorf("hubsync terminate requires --name")
		}
		return mutagen.Terminate(hubsyncName)
	},
}

// hubsyncListCmd prints the hub's duck-managed sessions, one per line, in the
// same pipe-delimited shape the mutagen package parses — the fleet ledger.
var hubsyncListCmd = &cobra.Command{
	Use:   "list",
	Short: "List hub-owned duck sessions (name|status|alpha|beta|spec)",
	Args:  cobra.NoArgs,
	RunE: func(c *cobra.Command, _ []string) error {
		sessions, err := mutagen.List()
		if err != nil {
			return err
		}
		for _, s := range sessions {
			fmt.Fprintln(c.OutOrStdout(), strings.Join([]string{
				s.Name, s.Status, s.Alpha.Display(), s.Beta.Display(), s.Spec,
			}, "|"))
		}
		return nil
	},
}

func init() {
	for _, sc := range []*cobra.Command{hubsyncAddCmd, hubsyncStatusCmd, hubsyncFlushCmd, hubsyncTerminateCmd} {
		sc.Flags().StringVar(&hubsyncName, "name", "", "session name")
	}
	hubsyncAddCmd.Flags().StringVar(&hubsyncHubPath, "hub-path", "", "hub-local directory (tilde form ok)")
	hubsyncAddCmd.Flags().StringVar(&hubsyncPeer, "peer", "", "laptop endpoint (user@host)")
	hubsyncAddCmd.Flags().StringVar(&hubsyncPeerPath, "peer-path", "", "absolute directory on the peer")
	hubsyncAddCmd.Flags().StringArrayVar(&hubsyncIgnores, "ignore", nil, "extra mutagen ignore pattern (repeatable)")
	hubsyncCmd.AddCommand(hubsyncAddCmd, hubsyncStatusCmd, hubsyncFlushCmd, hubsyncTerminateCmd, hubsyncListCmd)
	rootCmd.AddCommand(hubsyncCmd)
}
