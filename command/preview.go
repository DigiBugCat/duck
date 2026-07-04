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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/DigiBugCat/duck/internal/panel"
	"github.com/DigiBugCat/duck/internal/paths"
	"github.com/spf13/cobra"
)

var previewWatch bool

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
		render, hold, err := previewRender(args[0])
		if err != nil {
			return err
		}
		line := render
		if previewWatch {
			if strings.HasPrefix(args[0], "http://") || strings.HasPrefix(args[0], "https://") {
				return fmt.Errorf("--watch needs a local file (URLs have no mtime to watch)")
			}
			abs, _ := filepath.Abs(args[0])
			switch strings.ToLower(filepath.Ext(abs)) {
			case ".html", ".htm":
				// Live HTML without any visible churn: a wrapper page holds a
				// visible iframe of the target plus a HIDDEN probe iframe. The
				// probe reloads on a timer (display:none — never painted) and
				// the visible frame reloads ONLY when the probe's content
				// differs, so an unchanged file repaints exactly nothing. One
				// carbonyl runs for the window's lifetime. Needs
				// --allow-file-access-from-files (forwarded to Chromium) so
				// the wrapper may read its same-file iframe; without it every
				// file:// is its own origin and change-detection is impossible
				// (fetch and DOM reads both blocked — verified).
				wrap, err := writeRefreshWrapper(abs)
				if err != nil {
					return err
				}
				line = "carbonyl --allow-file-access-from-files file://" + paths.Quote(wrap)
			default:
				// Cheap renderers re-run on change; the loop holds the window.
				line = watchWrap(abs, render)
			}
		} else if hold {
			// One-shot renderers print and exit; hold the window for a keypress
			// so the output doesn't vanish with the process.
			line = fmt.Sprintf(`sh -c %s`, paths.Quote(render+`; printf "\n  [enter to close] "; read -r _`))
		}
		comp, err := panel.EnsureCompanion(run, outer, dir)
		if err != nil {
			return err
		}
		bin, err := os.Executable()
		if err != nil {
			bin = "duck"
		}
		if err := panel.Open(run, outer, comp, bin); err != nil {
			return err
		}
		wid, err := panel.Spawn(run, outer, "preview", dir, line, panel.KindArtifact)
		if err != nil {
			return err
		}
		// Stamp the render recipe on non-watch file previews so the roster can
		// re-render on selection when the file has changed (click-to-refresh).
		if !previewWatch && !strings.HasPrefix(args[0], "http") {
			if abs, err := filepath.Abs(args[0]); err == nil {
				if mtime, ok := panel.FileMtime(abs); ok {
					panel.StampPreview(run, wid, line, abs, mtime)
				}
			}
		}
		return nil
	},
}

// previewRender picks the renderer command for a target and reports whether
// it is one-shot (prints and exits → the caller adds a hold). Files are
// resolved to absolute paths (the window's cwd is the pane dir, but absolute
// is unambiguous); a missing file is an error here rather than a dead window.
// htmlRenderer picks the page engine. Carbonyl (cells), full stop: preview
// is the terminal porthole onto static artifacts — legible, a few KB per
// screen, no passthrough machinery. The gosling/casty pixel ladder is
// retired (see docs/WINDOW.md): anything that needs fidelity or motion
// belongs in the browser (duck render) or the planned duck window, not
// squeezed through the escape stream. Two carbonyl gotchas documented in
// WINDOW.md: it reports prefers-color-scheme:light, and it caches file://
// pages hard (reload won't see edits — bump ?v= or respawn).
func htmlRenderer() string {
	return "carbonyl"
}

func previewRender(target string) (render string, hold bool, err error) {
	if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
		return htmlRenderer() + " " + paths.Quote(target), false, nil
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return "", false, err
	}
	if _, err := os.Stat(abs); err != nil {
		return "", false, fmt.Errorf("no such file: %s", target)
	}
	q := paths.Quote(abs)
	switch strings.ToLower(filepath.Ext(abs)) {
	case ".html", ".htm":
		return htmlRenderer() + " file://" + q, false, nil
	case ".md", ".markdown":
		// Markdown opens the way pads do — micro: softwrapped, editable,
		// autosaving, live-reloading — the treatment Andrew standardized on.
		// (glow stays installed for anyone wanting a styled read: `spawn
		// glow -p <file>`.)
		return panel.EditorCmd(abs), false, nil
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg", ".bmp":
		// Pixels first (kitty via tmux passthrough — one layer in the swap
		// design), cells as the universal fallback if the forced mode errors.
		return fmt.Sprintf("chafa --passthrough tmux -f kitty %s || chafa %s", q, q), true, nil
	default:
		return "lynx " + q, false, nil
	}
}

// watchWrap turns a one-shot render command into a live one: run the
// renderer, poll the file's mtime (1s), and on change kill + re-render. If
// the renderer exits on its own (the user quit it) the loop ends too, so the
// window closes normally. Static renderers (chafa's hold-read included) and
// interactive ones (carbonyl, glow -p, lynx) both fit this shape.
func watchWrap(absPath, renderLine string) string {
	f := paths.Quote(absPath)
	// Each cycle scrubs the pane completely (clear + tmux clear-history):
	// leftover scrollback from the previous render otherwise accumulates and
	// the fresh render lands below the visible viewport ("it scrolls down").
	script := fmt.Sprintf(
		`while :; do clear; tmux clear-history -t "$TMUX_PANE" 2>/dev/null; (%s) & p=$!; m=$(stat -c %%Y %s 2>/dev/null); `+
			`while kill -0 $p 2>/dev/null && [ "$(stat -c %%Y %s 2>/dev/null)" = "$m" ]; do sleep 1; done; `+
			`if kill -0 $p 2>/dev/null; then kill $p 2>/dev/null; wait $p 2>/dev/null; else wait $p 2>/dev/null; exit 0; fi; done`,
		renderLine, f, f)
	// Quote the WHOLE script through paths.Quote: renderLine carries its own
	// quoted paths, and hand-rolled outer quotes would collide with them.
	return "sh -c " + paths.Quote(script)
}

// writeRefreshWrapper writes (idempotently, keyed by target hash) the
// self-refreshing iframe wrapper page for a watched HTML preview and returns
// its path. Lives under ~/.duck/previews; tiny, overwrite-safe, no cleanup
// needed.
func writeRefreshWrapper(target string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".duck", "previews")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(target))
	path := filepath.Join(dir, hex.EncodeToString(sum[:8])+".html")
	body := fmt.Sprintf(`<body style="margin:0;background:#111">
<iframe id="view" src="file://%[1]s" style="border:0;width:100vw;height:100vh"></iframe>
<iframe id="probe" src="file://%[1]s" style="display:none"></iframe>
<script>
const probe = document.getElementById("probe"), view = document.getElementById("view");
let last = null;
probe.onload = () => {
  try {
    const cur = probe.contentDocument.documentElement.outerHTML;
    if (last !== null && cur !== last) view.contentWindow.location.reload();
    last = cur;
  } catch (e) {}
};
setInterval(() => probe.contentWindow.location.reload(), 1500);
</script></body>
`, target)
	return path, os.WriteFile(path, []byte(body), 0o644)
}

func init() {
	previewCmd.Flags().BoolVarP(&previewWatch, "watch", "w", false, "re-render whenever the file changes (live view)")
	rootCmd.AddCommand(previewCmd)
}
