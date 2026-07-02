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

// startOpenForwarding is the package hook the attach loop calls to begin routing
// a session's hub-side open attempts to this laptop. It takes the tmux session
// name so each session forwards its OWN hub socket — no shared rendezvous point,
// so N attached laptops never collide. build() sets it from the live client; it
// stays nil in tests and non-attach paths (a no-op). It returns a stop func the
// attach loop defers. A setup failure degrades to a no-op stop — the
// open-interceptor is a convenience, never a reason an attach fails.
var startOpenForwarding func(session string) (stop func())

// hubOpenSock is the hub-side unix socket a given session's opens rendezvous on:
// an ABSOLUTE path under the hub's home. hubHome must already be resolved to a
// real path (no "$HOME"/"~") — a literal absolute path is the only form that
// survives unchanged through all three consumers (the ssh -R socket spec, the
// tmux set-environment value, and the shim's curl --unix-socket), none of which
// reliably expand shell variables. Per-session (not a shared port) is the whole
// point: the shim in that session's panes reads this exact path from the tmux
// session env (DUCK_OPEN_SOCK), so no two sessions ever contend for one bind.
// Session names are duck-generated (safe for a path segment).
func hubOpenSock(hubHome, session string) string {
	return fmt.Sprintf("%s/.duck/run/open-%s.sock", strings.TrimRight(hubHome, "/"), session)
}

// newOpenForwarding builds the production starter for the given client. On start
// it launches the laptop listener, reverse-forwards this session's hub SOCKET to
// it, and stamps the socket path into the tmux session's environment so the shim
// can find it. The teardown cancels the forward, removes the hub socket, and
// unsets the env var. Everything is best-effort: a failure disables the
// interceptor for this session but never blocks the attach.
func newOpenForwarding(client *sshx.Client) func(session string) (stop func()) {
	return func(session string) (stop func()) {
		noop := func() {}
		if session == "" {
			return noop
		}
		// Resolve the hub's absolute home ONCE so the socket path is a literal
		// absolute path everywhere (see hubOpenSock). A failure here means we can't
		// name the socket unambiguously — disable rather than risk a "$HOME"-named
		// file in someone's cwd.
		hubHome, err := hubHomeDir(client)
		if err != nil {
			fmt.Fprintf(os.Stderr, "duck: open-interceptor disabled (hub home: %v); hub opens will run on the hub\n", err)
			return noop
		}
		ln, err := openfwd.Start(productionOpenDeps(client))
		if err != nil {
			fmt.Fprintf(os.Stderr, "duck: open-interceptor disabled (listener: %v); hub opens will run on the hub\n", err)
			return noop
		}
		sock := hubOpenSock(hubHome, session)
		// Ensure the socket's parent dir exists (hub setup makes it, but a hub set
		// up before this feature would not have it). Best-effort.
		_, _ = client.Run("mkdir -p -- " + shquote(strings.TrimRight(hubHome, "/")+"/.duck/run"))
		if err := client.RemoteForwardSocket(sock, ln.LocalPort()); err != nil {
			fmt.Fprintf(os.Stderr, "duck: open-interceptor disabled (reverse forward: %v); hub opens will run on the hub\n", err)
			_ = ln.Close()
			return noop
		}
		// Stamp the socket into the session env so a duck-open shim running in any
		// of this session's panes resolves DUCK_OPEN_SOCK back to it. Best-effort:
		// if tmux is unavailable the forward is still up, opens just can't find it.
		stampOpenSock(client, session, sock)
		return func() {
			unstampOpenSock(client, session)
			_ = client.CancelRemoteForwardSocket(sock, ln.LocalPort())
			// Remove the hub socket file so it never lingers as a stale path (belt
			// and braces alongside StreamLocalBindUnlink on the next bind).
			_, _ = client.Run("rm -f -- " + shquote(sock))
			_ = ln.Close()
		}
	}
}

// stampOpenSock records this session's opener socket in the tmux session
// environment, so the shim (running in a pane of that session) can read it back
// with `tmux show-environment`. Best-effort; errors are swallowed.
func stampOpenSock(client *sshx.Client, session, sock string) {
	_, _ = client.Run(fmt.Sprintf(
		"tmux set-environment -t %s DUCK_OPEN_SOCK %s",
		shquote(session), shquote(sock)))
}

// unstampOpenSock clears the session's opener socket from the tmux session
// environment on teardown, so a pane opened after detach falls through to the
// hub opener rather than a dead socket. Best-effort.
func unstampOpenSock(client *sshx.Client, session string) {
	_, _ = client.Run(fmt.Sprintf(
		"tmux set-environment -u -t %s DUCK_OPEN_SOCK", shquote(session)))
}

// shquote single-quotes a value for safe embedding in the hub shell command.
func shquote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// hubHomeDir returns the hub's absolute home directory (never "~"/"$HOME"), so
// the per-session socket can be named with a literal path every consumer accepts
// verbatim. It asks the hub shell directly — one cheap round-trip at attach.
func hubHomeDir(client *sshx.Client) (string, error) {
	out, err := client.Run("printf %s \"$HOME\"")
	if err != nil {
		return "", err
	}
	home := strings.TrimSpace(out)
	if home == "" || !strings.HasPrefix(home, "/") {
		return "", fmt.Errorf("hub returned non-absolute home %q", home)
	}
	return home, nil
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

// withOpenForwarding runs fn with the open-interceptor active for the given
// session when the hook is set, tearing it down afterward. When the hook is nil
// (tests, or before build() wired it) it just runs fn. Centralizing it here
// keeps runAttachLoop's call site a one-liner.
func withOpenForwarding(session string, fn func() Outcome) Outcome {
	if startOpenForwarding == nil {
		return fn()
	}
	stop := startOpenForwarding(session)
	defer stop()
	return fn()
}
