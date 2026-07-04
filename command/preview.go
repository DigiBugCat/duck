// `duck preview <file|url>` — render something visual in the sidebar instead
// of a browser: the preview is just another sidebar window (roster row
// "preview", click to view, x to close), running a CELL-BASED renderer so it
// works inside tmux over SSH where pixel protocols (sixel/kitty) don't:
//
//	html / URLs → carbonyl   (real Chromium rendering into terminal cells)
//	markdown    → glow -p    (paged)
//	images      → chafa      (held open until a key)
//	anything else → lynx
//
// For pixel-perfect fidelity `open <file>` still routes to the laptop's real
// browser via duck's open-forwarding; preview is for staying in-terminal.
package command

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/DigiBugCat/duck/internal/panel"
	"github.com/DigiBugCat/duck/internal/paths"
	"github.com/spf13/cobra"
)

var previewCmd = &cobra.Command{
	Use:   "preview <file|url>",
	Short: "Render a page/doc/image in the sidebar (terminal graphics)",
	Args:  cobra.ExactArgs(1),
	RunE: func(c *cobra.Command, args []string) error {
		run := panel.ExecRunner
		outer, dir, err := panelContext(run)
		if err != nil {
			return err
		}
		line, err := previewLine(args[0])
		if err != nil {
			return err
		}
		comp, err := panel.EnsureCompanion(run, outer, dir)
		if err != nil {
			return err
		}
		if _, err := panel.Spawn(run, comp, "preview", dir, line); err != nil {
			return err
		}
		bin, err := os.Executable()
		if err != nil {
			bin = "duck"
		}
		return panel.Open(run, outer, comp, bin)
	},
}

// previewLine picks the in-window renderer command for a target. Files are
// resolved to absolute paths (the window's cwd is the pane dir, but absolute
// is unambiguous); a missing file is an error here rather than a dead window.
func previewLine(target string) (string, error) {
	if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
		return "carbonyl " + paths.Quote(target), nil
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(abs); err != nil {
		return "", fmt.Errorf("no such file: %s", target)
	}
	q := paths.Quote(abs)
	switch strings.ToLower(filepath.Ext(abs)) {
	case ".html", ".htm":
		return "carbonyl file://" + q, nil
	case ".md", ".markdown":
		return "glow -p " + q, nil
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg", ".bmp":
		// chafa prints and exits; hold the window until a keypress so the
		// image doesn't vanish with the process.
		return fmt.Sprintf(`sh -c 'chafa %s; printf "\n  [enter to close] "; read -r _'`, q), nil
	default:
		return "lynx " + q, nil
	}
}

func init() {
	rootCmd.AddCommand(previewCmd)
}
