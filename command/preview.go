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
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/DigiBugCat/duck/internal/panel"
	"github.com/DigiBugCat/duck/internal/paths"
	"github.com/spf13/cobra"
)

var previewWatch bool

var previewCmd = &cobra.Command{
	Use:   "preview <file|url> <name>",
	Short: "Render a page/doc/image in the sidebar (terminal graphics)",
	Args:  cobra.ExactArgs(2),
	RunE: func(c *cobra.Command, args []string) error {
		// A name is REQUIRED: every artifact row must be tellable apart in
		// the roster — a tab of panes all called "preview" was the failure.
		name := strings.TrimSpace(args[1])
		if name == "" {
			return fmt.Errorf("artifact name must be non-empty")
		}
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
		isURL := strings.HasPrefix(args[0], "http://") || strings.HasPrefix(args[0], "https://")
		// Local html always live-rerenders: carbonyl caches file:// pages so
		// hard that even its reload button misses edits, and artifacts are a
		// live agent⇄human surface (agents rewrite them in place). The watch
		// wrapper repaints only on real change, so an idle page costs nothing.
		if !isURL {
			switch strings.ToLower(filepath.Ext(args[0])) {
			case ".html", ".htm", ".md", ".markdown":
				// Live agent⇄human surfaces: always watch so a write repaints the
				// pane. html uses the iframe-probe path below; md/markdown fall to
				// the watchWrap default (kill+re-render glow on mtime change).
				previewWatch = true
			}
		}
		if previewWatch {
			if isURL {
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
		wid, err := panel.Spawn(run, outer, name, dir, line, panel.KindArtifact)
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

// mdRenderer is the markdown viewer line for watchWrap: render the file with
// glow (styled, wrapped to the pane width), then HOLD with `sleep infinity` so
// the pane keeps showing it until watchWrap kills the process on an mtime change
// and re-renders. This is what makes the pad's viewer face LIVE — an agent
// write repaints with no buffer to reconcile (disk-as-truth).
//
// Deliberately NOT `glow -p`: the pager holds an alternate screen watchWrap
// can't kill+re-render (verified — the edit never repaints under -p, does under
// plain glow). The render-then-hold shape is the same one watchWrap gives static
// image renderers. Without glow, fall back to `cat` (unstyled but buffer-free).
func mdRenderer(quotedPath string) string {
	body := "cat " + quotedPath
	if _, err := exec.LookPath("glow"); err == nil {
		body = `glow -w "$(tput cols 2>/dev/null || echo 100)" ` + quotedPath
	}
	// Render, then hold so watchWrap keeps the frame until an mtime change kills
	// it. `exec sleep` so the sleep IS the process watchWrap signals.
	return "sh -c " + paths.Quote(body+"; exec sleep infinity")
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
		// Markdown renders with glow — a styled, paged READ-ONLY view (the pad's
		// viewer face). It is disk-as-truth: the caller always watch-wraps it
		// (like local html), so an agent writing the file live re-renders the
		// pane with zero buffer to reconcile. `duck edit` is the EDIT face (micro).
		return mdRenderer(q), false, nil
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
