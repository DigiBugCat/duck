//go:build darwin

package window

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/chromedp/chromedp"
)

// launchDarwinTab starts (or reuses) the ~/.duck/DuckWindow.app wrapper via
// `open -na`, giving the window its own dock/app-switcher identity ("Duck
// Window") instead of showing up as whatever the underlying chromium
// binary's LaunchServices identity is. It then polls the bundle's fixed CDP
// port for /json/version and hands chromedp the resulting
// webSocketDebuggerUrl via NewRemoteAllocator — same driving surface as the
// direct ExecAllocator path, just fronted by an app bundle.
func launchDarwinTab(parent context.Context, chrome string) (context.Context, []context.CancelFunc, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, nil, err
	}
	bundle, err := ensureDuckWindowBundle(home, chrome, RemoteDebugPort)
	if err != nil {
		return nil, nil, fmt.Errorf("duck window: preparing DuckWindow.app: %w", err)
	}

	cmd := exec.Command("open", "-na", bundle)
	if err := cmd.Run(); err != nil {
		return nil, nil, fmt.Errorf("duck window: open %s: %w", bundle, err)
	}

	wsURL, err := waitForDebuggerURL(RemoteDebugPort, 10*time.Second)
	if err != nil {
		return nil, nil, fmt.Errorf("duck window: DuckWindow.app did not expose CDP on :%d: %w (is this an SSH session without access to the Aqua GUI session? see ~/.claude-ben/CLAUDE.md)", RemoteDebugPort, err)
	}

	actx, acancel := chromedp.NewRemoteAllocator(parent, wsURL)
	tab, tcancel := chromedp.NewContext(actx)
	return tab, []context.CancelFunc{tcancel, acancel}, nil
}

// waitForDebuggerURL polls http://127.0.0.1:<port>/json/version until it
// returns a webSocketDebuggerUrl or timeout elapses.
func waitForDebuggerURL(port int, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	url := fmt.Sprintf("http://127.0.0.1:%d/json/version", port)
	client := &http.Client{Timeout: 1 * time.Second}
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err
			time.Sleep(200 * time.Millisecond)
			continue
		}
		var info struct {
			WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
		}
		decErr := json.NewDecoder(resp.Body).Decode(&info)
		resp.Body.Close()
		if decErr != nil {
			lastErr = decErr
			time.Sleep(200 * time.Millisecond)
			continue
		}
		if info.WebSocketDebuggerURL == "" {
			lastErr = fmt.Errorf("empty webSocketDebuggerUrl in response")
			time.Sleep(200 * time.Millisecond)
			continue
		}
		return info.WebSocketDebuggerURL, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("timed out")
	}
	return "", lastErr
}
