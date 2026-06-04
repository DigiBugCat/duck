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

func init() {
	configCmd.AddCommand(configPathCmd, configEditCmd, configClaudeSyncCmd)
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
