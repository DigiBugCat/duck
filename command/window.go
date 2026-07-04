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
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/DigiBugCat/duck/internal/window"
	"github.com/spf13/cobra"
)

var windowHostFlag string

// windowHost resolves the window host's address: --host flag,
// $DUCK_WINDOW_HOST, then the default loopback port.
func windowHost() string {
	if windowHostFlag != "" {
		return windowHostFlag
	}
	if h := os.Getenv("DUCK_WINDOW_HOST"); h != "" {
		return h
	}
	return fmt.Sprintf("127.0.0.1:%d", window.DefaultPort)
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
			fmt.Printf("%q\n", m.Text)
			if m.Comment != "" {
				fmt.Printf("  %s\n", m.Comment)
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
