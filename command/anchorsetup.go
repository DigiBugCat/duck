package command

import (
	"fmt"

	"github.com/DigiBugCat/duck/internal/config"
	"github.com/DigiBugCat/duck/internal/hub"
	"github.com/spf13/cobra"
)

// anchorsetup.go mirrors hubsetup.go's set/show pair for the anchor host — the
// independently-configurable SSH host that holds ~/.duck/anchor.json (see
// internal/anchor). Unlike hub setup there is no provisioning step: the
// anchor only ever needs ~/.duck/ to be writable, which any plain SSH host
// (duck-provisioned or not) already has, so `anchor set` is just a
// connectivity check + config.Save.

var anchorCmd = &cobra.Command{
	Use:   "anchor",
	Short: "Configure the anchor host (shares the hub address + a few settings across laptops)",
}

var anchorSetCmd = &cobra.Command{
	Use:   "set <user@host>",
	Short: "Set the anchor host and verify SSH connectivity",
	Long: `set verifies SSH connectivity to <user@host> and saves it as the anchor.

The anchor is a small JSON file (~/.duck/anchor.json) that mirrors the hub
address and a few user-level settings across every laptop pointed at it — see
"duck hub set" for what pushes to it. It can be the same host as the hub, or a
separate always-on box; pointing it at a separate box makes hub moves
zero-touch on every other laptop. No token or extra auth: whatever SSH access
already reaches <user@host> is sufficient.`,
	Args: cobra.ExactArgs(1),
	RunE: func(c *cobra.Command, args []string) error {
		addr := args[0]
		if err := hub.ValidateAddr(addr); err != nil {
			return err
		}
		h := hub.New(addr)
		if err := h.Ping(); err != nil {
			return fmt.Errorf("anchor host unreachable: %w", err)
		}

		cfg, err := config.Load()
		if err != nil {
			return err
		}
		cfg.AnchorHost = addr
		if err := config.Save(cfg); err != nil {
			return err
		}
		fmt.Fprintf(c.OutOrStdout(), "anchor registered: %s\n", addr)
		// Seed the anchor with whatever this laptop already has, so the very
		// first `anchor set` doesn't leave the file empty for the next reader.
		if err := config.PushAnchor(cfg); err != nil {
			fmt.Fprintf(c.ErrOrStderr(), "warning: could not seed the anchor: %v\n", err)
		}
		return nil
	},
}

var anchorShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Print the configured anchor host",
	Args:  cobra.NoArgs,
	RunE: func(c *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if cfg.AnchorHost == "" {
			fmt.Fprintln(c.OutOrStdout(), "(no anchor configured)")
			return nil
		}
		fmt.Fprintln(c.OutOrStdout(), cfg.AnchorHost)
		return nil
	},
}

func init() {
	anchorCmd.AddCommand(anchorSetCmd, anchorShowCmd)
	rootCmd.AddCommand(anchorCmd)
}
