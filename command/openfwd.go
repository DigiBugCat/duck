// openfwd.go is the command-layer glue for the open-interceptor: it stands up
// the laptop-side opener listener and the reverse forward that lets the hub's
// duck-open shim reach it, for the lifetime of an interactive attach. The pure
// decision logic lives in internal/openfwd; the ssh forward/fetch primitives in
// internal/sshx. Here we only assemble the production Deps (open via the OS
// opener, tunnel via ssh -L, fetch via ssh cat) and manage start/stop.
package command

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/DigiBugCat/duck/internal/openfwd"
	"github.com/DigiBugCat/duck/internal/sshx"
)

// startOpenForwarding is the package hook the attach loop calls to begin
// routing the hub's open attempts to this laptop. build() sets it from the live
// client; it stays nil in tests and non-attach paths (a no-op). It returns a
// stop func the attach loop defers. A setup failure degrades to a no-op stop —
// the open-interceptor is a convenience, never a reason an attach fails.
var startOpenForwarding func() (stop func())

// newOpenForwarding builds the production starter for the given client: on
// start it launches the listener, reverse-forwards HubPort to it, and returns a
// teardown that cancels the forward and stops the listener.
func newOpenForwarding(client *sshx.Client) func() (stop func()) {
	return func() (stop func()) {
		noop := func() {}
		ln, err := openfwd.Start(productionOpenDeps(client))
		if err != nil {
			fmt.Fprintf(os.Stderr, "duck: open-interceptor disabled (listener: %v); hub opens will run on the hub\n", err)
			return noop
		}
		if err := client.RemoteForward(openfwd.HubPort, ln.LocalPort()); err != nil {
			// A "remote port forwarding failed for listen port" collision means a
			// PRIOR attach left an orphaned forward bound to HubPort on the hub (its
			// control master died without tearing it down, so our cancel — which
			// runs through a different master — can't reach it). Reclaim the port by
			// killing whatever still listens on it, then retry the forward once.
			if isForwardPortCollision(err) && reclaimHubForwardPort(client, openfwd.HubPort) {
				err = client.RemoteForward(openfwd.HubPort, ln.LocalPort())
			}
			if err != nil {
				fmt.Fprintf(os.Stderr, "duck: open-interceptor disabled (reverse forward: %v); hub opens will run on the hub\n", err)
				_ = ln.Close()
				return noop
			}
		}
		return func() {
			_ = client.CancelRemoteForward(openfwd.HubPort, ln.LocalPort())
			_ = ln.Close()
		}
	}
}

// isForwardPortCollision reports whether an ssh -R failure was the hub refusing
// the listen port because something already holds it — the orphaned-forward case
// worth reclaiming. Other failures (auth, no route) are not retried this way.
func isForwardPortCollision(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "remote port forwarding failed for listen port") ||
		strings.Contains(msg, "forwarding request failed")
}

// reclaimHubForwardPort best-effort kills whatever still listens on the hub's
// forward port (an orphaned sshd-session from a prior attach holding the stale
// -R forward). It reports whether the port looks free afterward so the caller
// knows a retry is worth attempting. Login-wrapped so lsof resolves on the hub's
// PATH; all failures are swallowed (the worst case is the original error stands).
func reclaimHubForwardPort(client *sshx.Client, port int) bool {
	script := fmt.Sprintf(
		`pids=$(lsof -nP -iTCP:%d -sTCP:LISTEN -t 2>/dev/null); `+
			`[ -n "$pids" ] && kill $pids 2>/dev/null; sleep 1; `+
			`lsof -nP -iTCP:%d -sTCP:LISTEN -t 2>/dev/null | head -1`,
		port, port)
	out, err := client.Run(script)
	if err != nil {
		return false
	}
	// Empty output means nothing listens on the port now — safe to retry.
	return strings.TrimSpace(out) == ""
}

// productionOpenDeps wires the real seams: open through the laptop's OS opener,
// tunnel a hub-loopback port with ssh -L (reusing the same local port number so
// the rewrite is predictable), and fetch an unsynced file via ssh cat into a
// per-run temp dir.
func productionOpenDeps(client *sshx.Client) openfwd.Deps {
	home, _ := os.UserHomeDir()
	return openfwd.Deps{
		LocalHome: home,
		Open:      osOpen,
		LocalForward: func(hubPort int) (int, error) {
			// Mirror the hub port locally so the rewritten URL is the same port the
			// user saw the hub print (least surprising). LocalForward cancels any
			// stale identical forward first, so re-opening the same dev server is fine.
			if err := client.LocalForward(hubPort, hubPort); err != nil {
				return 0, err
			}
			return hubPort, nil
		},
		Exists: func(p string) bool { _, err := os.Stat(p); return err == nil },
		Fetch: func(hubAbsPath string) (string, error) {
			data, err := client.ReadFile(hubAbsPath)
			if err != nil {
				return "", err
			}
			dir := filepath.Join(os.TempDir(), "duck-open")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return "", err
			}
			dest := filepath.Join(dir, filepath.Base(hubAbsPath))
			if err := os.WriteFile(dest, data, 0o600); err != nil {
				return "", err
			}
			return dest, nil
		},
	}
}

// osOpen opens a URL or file with the laptop's default handler: `open` on
// macOS, `xdg-open` elsewhere. It detaches the child so it outlives duck.
func osOpen(target string) error {
	bin := "xdg-open"
	if runtime.GOOS == "darwin" {
		bin = "open"
	}
	cmd := exec.Command(bin, target)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%s %s: %w", bin, target, err)
	}
	return cmd.Process.Release()
}

// withOpenForwarding runs fn with the open-interceptor active when the hook is
// set, tearing it down afterward. When the hook is nil (tests, or before
// build() wired it) it just runs fn. Centralizing it here keeps runAttachLoop's
// call site a one-liner.
func withOpenForwarding(fn func() Outcome) Outcome {
	if startOpenForwarding == nil {
		return fn()
	}
	stop := startOpenForwarding()
	defer stop()
	return fn()
}
