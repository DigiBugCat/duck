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

func init() {
	configCmd.AddCommand(configPathCmd, configEditCmd)
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
