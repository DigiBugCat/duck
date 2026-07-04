// `duck window <file|url>` — show a dynamic artifact in the duck-owned
// client window (a CDP-driven chromium the host fully controls; see
// docs/WINDOW.md). Unlike `duck render` (fling at the laptop's default
// browser) and `duck preview` (terminal cells), the window supports
// highlight/comment annotations queryable back as structured marks
// (`duck window marks`). `duck window serve` runs the host itself.
package command

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/DigiBugCat/duck/internal/config"
	"github.com/DigiBugCat/duck/internal/window"
	"github.com/spf13/cobra"
)

var windowHostFlag string

// windowHost resolves the window host's address: --host flag,
// $DUCK_WINDOW_HOST, config window_host, then the default loopback port.
func windowHost() string {
	if windowHostFlag != "" {
		return windowHostFlag
	}
	if h := os.Getenv("DUCK_WINDOW_HOST"); h != "" {
		return h
	}
	if cfg, err := config.Load(); err == nil && cfg.WindowHost != "" {
		return cfg.WindowHost
	}
	return fmt.Sprintf("127.0.0.1:%d", window.DefaultPort)
}

var (
	windowHostHealthy = defaultWindowHostHealthy
	startWindowHost   = defaultStartWindowHost
	windowEnsureSleep = time.Sleep
)

// ensureWindowHost starts the local window host as a detached singleton when
// the selected host is loopback and does not answer /health yet. Remote
// configured hosts are hub-side discovery targets; they must not cause a local
// spawn on the hub.
func ensureWindowHost(host string) error {
	if !isLocalWindowHost(host) || windowHostHealthy(host) {
		return nil
	}
	if err := startWindowHost(); err != nil {
		return err
	}
	for i := 0; i < 50; i++ {
		if windowHostHealthy(host) {
			return nil
		}
		windowEnsureSleep(100 * time.Millisecond)
	}
	return fmt.Errorf("window host did not come up at %s (see ~/.duck/window.log)", host)
}

func isLocalWindowHost(host string) bool {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		return false
	}
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func defaultWindowHostHealthy(host string) bool {
	client := &http.Client{Timeout: 300 * time.Millisecond}
	resp, err := client.Get("http://" + host + "/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func defaultStartWindowHost() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	duckDir := filepath.Join(home, ".duck")
	if err := os.MkdirAll(duckDir, 0o755); err != nil {
		return err
	}
	logf, err := os.OpenFile(filepath.Join(duckDir, "window.log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer logf.Close()
	self, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(self, "window", "serve")
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

var windowCmd = &cobra.Command{
	Use:   "window <file|url>",
	Short: "Show a dynamic artifact in the duck-owned client window (see docs/WINDOW.md)",
	Long: `Publishes the target (a local file goes through the same render-server
symlink trick as duck render; a URL passes through unchanged) and tells the
duck window host to navigate to it. Unlike duck render, the window is a
CDP-driven chromium duck keeps custody of: it supports highlight/comment
annotations you can query back with "duck window marks". Unlike duck
preview, the window is a real browser window on the client machine, not
terminal cells.`,
	Args: cobra.ExactArgs(1),
	RunE: func(c *cobra.Command, args []string) error {
		u, err := publishArtifact(args[0])
		if err != nil {
			return err
		}
		host := windowHost()
		if err := ensureWindowHost(host); err != nil {
			return err
		}
		form := url.Values{"url": {u}}
		resp, err := http.PostForm("http://"+host+"/open", form)
		if err != nil {
			return fmt.Errorf("window host at %s: %w", host, err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		fmt.Printf("shown: %s\n", u)
		fmt.Printf("%s: %s\n", host, strings.TrimSpace(string(body)))
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("window host returned %s", resp.Status)
		}
		return nil
	},
}

var (
	windowServeAddr     string
	windowServeHeadless bool
)

var windowServeCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run the duck window host (foreground)",
	Long: `Starts the window host: owns a chromium session over CDP, injects the
annotation runtime into every page it loads, and serves the control API
(GET /health, POST /open, GET /marks) that "duck window" and "duck window
marks" talk to. Runs in the foreground; Ctrl-C to stop.`,
	RunE: func(c *cobra.Command, args []string) error {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		store, err := window.NewStore(filepath.Join(home, ".duck", "window-marks.json"))
		if err != nil {
			return err
		}
		h := &window.Host{Store: store, Headless: windowServeHeadless}
		ln, err := net.Listen("tcp", windowServeAddr)
		if err != nil {
			return err
		}
		defer ln.Close()
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		fmt.Printf("duck window host listening on %s\n", windowServeAddr)
		return h.Serve(ctx, ln)
	},
}

var windowMarksJSON bool

var windowMarksCmd = &cobra.Command{
	Use:   "marks [url]",
	Short: "List annotations from the duck window (current page, or a given url)",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(c *cobra.Command, args []string) error {
		host := windowHost()
		if err := ensureWindowHost(host); err != nil {
			return err
		}
		q := ""
		if len(args) == 1 {
			q = "?url=" + url.QueryEscape(args[0])
		}
		resp, err := http.Get("http://" + host + "/marks" + q)
		if err != nil {
			return fmt.Errorf("window host at %s: %w", host, err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("window host returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
		}
		if windowMarksJSON {
			fmt.Println(strings.TrimSpace(string(body)))
			return nil
		}
		var marks []window.Mark
		if err := json.Unmarshal(body, &marks); err != nil {
			return err
		}
		if len(marks) == 0 {
			fmt.Println("(no marks)")
			return nil
		}
		const dim, reset = "\x1b[2m", "\x1b[0m"
		for _, m := range marks {
			typ := m.Type
			if typ == "" {
				typ = "highlight"
			}
			if m.Text != "" {
				fmt.Printf("%s: %q\n", typ, m.Text)
			} else {
				fmt.Printf("%s\n", typ)
			}
			if m.Comment != "" {
				fmt.Printf("  note: %s\n", m.Comment)
			}
			if m.Shot != "" {
				fmt.Printf("  shot: %s\n", m.Shot)
			}
			if m.Before != "" || m.After != "" {
				fmt.Printf("  %s...%s%s%s...%s\n", dim, m.Before, reset, dim, m.After+reset)
			}
			fmt.Printf("  %s%s (%s)%s\n\n", dim, m.Stamp, m.URL, reset)
		}
		return nil
	},
}

func init() {
	windowCmd.PersistentFlags().StringVar(&windowHostFlag, "host", "", "window host address (default: $DUCK_WINDOW_HOST or 127.0.0.1:"+fmt.Sprint(window.DefaultPort)+")")
	windowServeCmd.Flags().StringVar(&windowServeAddr, "addr", fmt.Sprintf(":%d", window.DefaultPort), "listen address")
	windowServeCmd.Flags().BoolVar(&windowServeHeadless, "headless", false, "run chrome headless (tests / no display)")
	windowMarksCmd.Flags().BoolVar(&windowMarksJSON, "json", false, "dump raw JSON")
	windowCmd.AddCommand(windowServeCmd)
	windowCmd.AddCommand(windowMarksCmd)
	rootCmd.AddCommand(windowCmd)
}
