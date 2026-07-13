// openfwd.go is the command-layer glue for the open-interceptor: it stands up
// the laptop-side opener listener and the reverse forward that lets the hub's
// duck-open shim reach it, for the lifetime of an interactive attach. The pure
// decision logic lives in internal/openfwd; the ssh forward/fetch primitives in
// internal/sshx. Here we only assemble the production Deps (open via the OS
// opener, tunnel via ssh -L, fetch via ssh cat) and manage start/stop.
package command

import (
	"crypto/rand"
	"encoding/hex"
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

// hubOpenSock is the hub-side unix socket for one local attach instance. The
// random owner suffix matters even though the tmux session name is unique: two
// terminals can attach the same workspace, and an old attach must never rebind
// or remove the newer attach's socket. hubHome is already absolute; session is a
// duck-generated tmux-safe slug.
func hubOpenSock(hubHome, session, owner string) string {
	return fmt.Sprintf("%s/.duck/run/open-%s-%s.sock", strings.TrimRight(hubHome, "/"), session, owner)
}

func openForwardOwner() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
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
		owner, err := openForwardOwner()
		if err != nil {
			fmt.Fprintf(os.Stderr, "duck: open-interceptor disabled (owner id: %v); hub opens will run on the hub\n", err)
			return noop
		}
		ln, err := openfwd.Start(productionOpenDeps(client))
		if err != nil {
			fmt.Fprintf(os.Stderr, "duck: open-interceptor disabled (listener: %v); hub opens will run on the hub\n", err)
			return noop
		}
		sock := hubOpenSock(hubHome, session, owner)
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
			unstampOpenSock(client, session, sock)
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

// unstampOpenSock clears the session's opener socket only when this attach still
// owns it. A newer terminal may have stamped its own socket while this one was
// attached; the older teardown must not erase that handoff.
func unstampOpenSock(client *sshx.Client, session, sock string) {
	expected := "DUCK_OPEN_SOCK=" + sock
	_, _ = client.Run(fmt.Sprintf(
		"[ \"$(tmux show-environment -t %s DUCK_OPEN_SOCK 2>/dev/null)\" = %s ] && tmux set-environment -u -t %s DUCK_OPEN_SOCK || true",
		shquote(session), shquote(expected), shquote(session)))
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
	var stops []func()
	if startOpenForwarding != nil {
		stops = append(stops, startOpenForwarding(session))
	}
	for i := len(stops) - 1; i >= 0; i-- {
		defer stops[i]()
	}
	return fn()
}
