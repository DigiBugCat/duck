// `duck config`: show (and optionally open) duck's laptop-side configuration —
// most importantly "where do we duck to?" (the hub). `duck config path` prints
// just the file location; `duck config edit` opens it in $EDITOR/$VISUAL.
package command

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/DigiBugCat/duck/internal/config"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Show duck's configuration (hub destination, codex model, remembered folders)",
	Args:  cobra.NoArgs,
	RunE: func(c *cobra.Command, _ []string) error {
		p, err := config.Path()
		if err != nil {
			return err
		}
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		out := c.OutOrStdout()
		fmt.Fprintf(out, "config  %s\n\n", p)

		tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
		hub := cfg.Hub
		if hub == "" {
			hub = "(none — run `duck hub setup <user@host>`)"
		} else if cfg.HubName != "" {
			hub = fmt.Sprintf("%s  (%s)", hub, cfg.HubName)
		}
		fmt.Fprintf(tw, "hub\t%s\n", hub)
		model := cfg.CodexModel
		if model == "" {
			model = "(default)"
		}
		fmt.Fprintf(tw, "codex model\t%s\n", model)
		claudeSync := "off"
		if cfg.SyncClaudeHistory {
			claudeSync = "on"
		}
		fmt.Fprintf(tw, "claude history sync\t%s  (duck config claude-sync on|off)\n", claudeSync)
		fmt.Fprintf(tw, "attach transport\t%s  (duck config attach-transport auto|ssh|tssh)\n", cfg.Transport())
		autoUpdate := "on"
		if !cfg.AutoUpdateEnabled() {
			autoUpdate = "off"
		}
		fmt.Fprintf(tw, "auto update\t%s  (duck config auto-update on|off)\n", autoUpdate)
		tw.Flush()

		// Per-folder sync memory: what duck remembers about where it auto-mirrors.
		if len(cfg.Folders) > 0 {
			fmt.Fprintln(out, "\nfolders (sync memory):")
			ftw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
			for _, dir := range sortedKeys(cfg.Folders) {
				fmt.Fprintf(ftw, "  %s\t%s\n", dir, cfg.Folders[dir])
			}
			ftw.Flush()
		}
		// Per-folder auto-naming opt-ins (codex naming sends pane content to a model).
		if on := enabledKeys(cfg.AutoName); len(on) > 0 {
			fmt.Fprintln(out, "\nauto-naming ON for:")
			for _, dir := range on {
				fmt.Fprintf(out, "  %s\n", dir)
			}
		}
		return nil
	},
}

var configPathCmd = &cobra.Command{
	Use:   "path",
	Short: "Print the path to duck's config file",
	Args:  cobra.NoArgs,
	RunE: func(c *cobra.Command, _ []string) error {
		p, err := config.Path()
		if err != nil {
			return err
		}
		fmt.Fprintln(c.OutOrStdout(), p)
		return nil
	},
}

var configEditCmd = &cobra.Command{
	Use:   "edit",
	Short: "Open duck's config file in $VISUAL/$EDITOR",
	Args:  cobra.NoArgs,
	RunE: func(c *cobra.Command, _ []string) error {
		p, err := config.Path()
		if err != nil {
			return err
		}
		editor := os.Getenv("VISUAL")
		if editor == "" {
			editor = os.Getenv("EDITOR")
		}
		if editor == "" {
			fmt.Fprintf(c.OutOrStdout(), "no $VISUAL/$EDITOR set; edit it directly:\n  %s\n", p)
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return err
		}
		ed := exec.Command(editor, p)
		ed.Stdin, ed.Stdout, ed.Stderr = os.Stdin, os.Stdout, os.Stderr
		return ed.Run()
	},
}

// configClaudeSyncCmd toggles the per-folder Claude history co-sync. It is a
// global on/off (default off) — when on, a bare `duck` that mirrors a folder
// ALSO co-syncs that folder's ~/.claude/projects/<slug> corpus (transcripts +
// memory) to the hub. Off by default because it ships terminal transcripts off
// your machine.
var configClaudeSyncCmd = &cobra.Command{
	Use:       "claude-sync <on|off>",
	Short:     "Toggle per-folder Claude history sync (transcripts + memory) to the hub",
	Args:      cobra.ExactArgs(1),
	ValidArgs: []string{"on", "off"},
	RunE: func(c *cobra.Command, args []string) error {
		var on bool
		switch args[0] {
		case "on", "true", "yes":
			on = true
		case "off", "false", "no":
			on = false
		default:
			return fmt.Errorf("expected on or off, got %q", args[0])
		}
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		cfg.SyncClaudeHistory = on
		if err := config.Save(cfg); err != nil {
			return err
		}
		state := "off"
		if on {
			state = "on"
		}
		fmt.Fprintf(c.OutOrStdout(), "claude history sync: %s\n", state)
		if on {
			fmt.Fprintln(c.OutOrStdout(), "  duck will co-sync each ducked folder's ~/.claude/projects/<slug> (transcripts + memory) to the hub.")
		}
		return nil
	},
}

// configAutoUpdateCmd toggles the background self-updater (default on). When on,
// every `duck` run spawns a throttled detached check that replaces the binary in
// place when a newer release exists; the next run uses it. Dev (from-source)
// builds ignore this and never auto-update.
var configAutoUpdateCmd = &cobra.Command{
	Use:       "auto-update <on|off>",
	Short:     "Toggle the background self-updater (default on)",
	Args:      cobra.ExactArgs(1),
	ValidArgs: []string{"on", "off"},
	RunE: func(c *cobra.Command, args []string) error {
		var on bool
		switch args[0] {
		case "on", "true", "yes":
			on = true
		case "off", "false", "no":
			on = false
		default:
			return fmt.Errorf("expected on or off, got %q", args[0])
		}
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		cfg.AutoUpdate = &on
		if err := config.Save(cfg); err != nil {
			return err
		}
		state := "off"
		if on {
			state = "on"
		}
		fmt.Fprintf(c.OutOrStdout(), "auto update: %s\n", state)
		if on {
			fmt.Fprintln(c.OutOrStdout(), "  each `duck` run checks for a newer release in the background (throttled) and self-updates; the next run uses it.")
		}
		return nil
	},
}

// configAttachTransportCmd selects the interactive-attach transport. The
// default, auto, uses tssh whenever the local `tssh` client is installed and
// falls back to ssh otherwise — so a client opts in just by installing tssh
// (the hub always supports it; `duck hub setup` installs tsshd). ssh forces ssh
// even with tssh installed; tssh forces tssh (warn + ssh fallback when the
// client is missing). tssh (UDP/QUIC, resilient roaming over Tailscale) only
// replaces the interactive `tmux attach` — the SSH control plane (sync forwards,
// the opener, naming, provisioning) always stays on ssh.
var configAttachTransportCmd = &cobra.Command{
	Use:       "attach-transport <auto|ssh|tssh>",
	Short:     "Select the interactive attach transport (auto default: tssh if installed, else ssh)",
	Args:      cobra.ExactArgs(1),
	ValidArgs: []string{"auto", "ssh", "tssh"},
	RunE: func(c *cobra.Command, args []string) error {
		// ValidArgs only drives shell completion; enforce the allowed set here.
		var stored, shown string
		switch args[0] {
		case "auto":
			stored, shown = "", "auto" // empty == auto default; keeps config.toml clean
		case "ssh":
			stored, shown = "ssh", "ssh"
		case "tssh":
			stored, shown = "tssh", "tssh"
		default:
			return fmt.Errorf("expected auto, ssh, or tssh, got %q", args[0])
		}
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		cfg.AttachTransport = stored
		if err := config.Save(cfg); err != nil {
			return err
		}
		out := c.OutOrStdout()
		fmt.Fprintf(out, "attach transport: %s\n", shown)
		switch shown {
		case "auto":
			fmt.Fprintln(out, "  uses tssh when the `tssh` client is installed locally, else ssh (the hub always supports tssh).")
			fmt.Fprintln(out, "  SSH stays the control plane + fallback; only the interactive attach uses tssh.")
		case "tssh":
			fmt.Fprintln(out, "  needs `tssh` on your laptop and tsshd on the hub (duck hub setup installs it).")
			fmt.Fprintln(out, "  SSH stays the control plane + fallback; only the interactive attach uses tssh.")
		}
		return nil
	},
}

func init() {
	configCmd.AddCommand(configPathCmd, configEditCmd, configClaudeSyncCmd, configAttachTransportCmd, configAutoUpdateCmd)
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func enabledKeys(m map[string]bool) []string {
	var keys []string
	for k, v := range m {
		if v {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}
