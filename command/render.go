// `duck render <file|url>` — the dedicated-renderer tier: content that
// outgrows the terminal (animation, heavy interactivity, pixel-perfect
// reading) opens in the LAPTOP's browser instead of a pane. Composition of
// two things duck already owns: the hub is tailscale-reachable (a tiny
// static server makes any hub file a URL) and the open-interceptor (the
// hub-side `open` shim → laptop :4774) puts that URL on the client's screen.
//
// The server serves ONLY ~/.duck/render, into which each rendered file's
// parent directory is symlinked (so relative assets resolve) under a
// path-hash name — not the whole home dir. Tailnet-only exposure, user's own
// machines. The server is a detached singleton, started on demand.
package command

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

// renderPort is duck's render-server port (fleet 73xx block convention).
const renderPort = 7327

func renderRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".duck", "render"), nil
}

var renderCmd = &cobra.Command{
	Use:   "render <file|url>",
	Short: "Open a page/artifact in the laptop's browser (full fidelity)",
	Long: `The escape hatch above the sidebar: serve a hub file over the tailnet and
open it in your laptop's browser via duck's open-forwarding — native-speed
rendering for animation, heavy dashboards, or pixel-perfect reading. URLs
are forwarded directly. The sidebar (duck preview) stays the in-terminal
tier; render is for content that outgrows it.`,
	Args: cobra.ExactArgs(1),
	RunE: func(c *cobra.Command, args []string) error {
		u, err := publishArtifact(args[0])
		if err != nil {
			return err
		}
		return openOnClient(u)
	},
}

// publishArtifact turns a target into a URL: http(s) passes through; a file
// is exposed via the render server (parent dir symlinked under a stable hash
// so relative assets resolve; re-linking is idempotent) and addressed by the
// hub's tailnet hostname. Shared by `duck render` and `duck window`.
func publishArtifact(target string) (string, error) {
	if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
		return target, nil
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(abs); err != nil {
		return "", fmt.Errorf("no such file: %s", target)
	}
	root, err := renderRoot()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	dir := filepath.Dir(abs)
	sum := sha256.Sum256([]byte(dir))
	slug := hex.EncodeToString(sum[:6])
	link := filepath.Join(root, slug)
	_ = os.Remove(link)
	if err := os.Symlink(dir, link); err != nil {
		return "", err
	}
	if err := ensureRenderServer(root); err != nil {
		return "", err
	}
	host, err := os.Hostname()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("http://%s:%d/%s/%s", host, renderPort, slug, url.PathEscape(filepath.Base(abs))), nil
}

// renderServeCmd is the detached singleton's entrypoint; not for humans.
var renderServeCmd = &cobra.Command{
	Use:    "serve",
	Hidden: true,
	RunE: func(c *cobra.Command, args []string) error {
		root, err := renderRoot()
		if err != nil {
			return err
		}
		return http.ListenAndServe(fmt.Sprintf(":%d", renderPort), http.FileServer(http.Dir(root)))
	},
}

// ensureRenderServer starts the detached file server if nothing is listening
// on the render port yet, and waits until it answers.
func ensureRenderServer(root string) error {
	if renderServerUp() {
		return nil
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	logf, err := os.OpenFile(filepath.Join(filepath.Dir(root), "render.log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd := exec.Command(self, "render", "serve")
	cmd.Stdout, cmd.Stderr = logf, logf
	// Detach: the server outlives this invocation (same pattern as the
	// background auto-updater).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }() // reap if it dies while we still exist
	for i := 0; i < 20; i++ {
		if renderServerUp() {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("render server did not come up on :%d (see ~/.duck/render.log)", renderPort)
}

func renderServerUp() bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", renderPort), 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// openOnClient routes a URL to the user's laptop browser through the
// hub-side open shim (the open-interceptor). Falls back to printing the URL
// when no shim / no attached client forwarding is available.
func openOnClient(u string) error {
	if _, err := exec.LookPath("open"); err == nil {
		if err := exec.Command("open", u).Run(); err == nil {
			fmt.Printf("opened on your laptop: %s\n", u)
			return nil
		}
	}
	fmt.Printf("open manually: %s\n", u)
	return nil
}

func init() {
	renderCmd.AddCommand(renderServeCmd)
	rootCmd.AddCommand(renderCmd)
}
