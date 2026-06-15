// Package sshx builds and runs duck's multiplexed SSH commands against the hub.
//
// Every duck SSH call uses duck's OWN connection multiplexing — it never reads
// or mutates the user's ~/.ssh/config. The DUCKSSH option set is:
//
//	ssh -o BatchMode=yes -o ConnectTimeout=10 \
//	    -o ControlMaster=auto \
//	    -o ControlPath=<HOME>/.duck/cm/%r@%h:%p \
//	    -o ControlPersist=10m
//
// The ControlPath uses the Go-expanded $HOME (never a literal "~"), and the
// <HOME>/.duck/cm directory is MkdirAll 0700 at startup (design fix c2). The
// warmed master socket is reused by every subsequent call — including the
// ported internal/hub package, which carries the same Control* flags (gap#6) —
// and by the interactive attach via ExecAttach.
//
// This package is NEW for duck (flok had no multiplexing layer).
package sshx

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/DigiBugCat/duck/internal/paths"
)

// sshBinary is the ssh executable. ExecAttach needs an absolute path for
// syscall.Exec; run/output go through exec.Command which does its own lookup.
const sshBinary = "ssh"

// moshBinary is the mosh client wrapper (it bootstraps mosh-server over ssh,
// then hands off to mosh-client over UDP). Only the interactive attach uses it.
const moshBinary = "mosh"

// execAttachPath resolves the ssh binary to an absolute path for syscall.Exec,
// which (unlike exec.Command) does not search $PATH. It prefers the same ssh
// that run/exec.Command would pick via PATH, so the attach reuses the warmed
// ControlMaster socket even on Homebrew-OpenSSH setups; falls back to the
// conventional macOS location.
func execAttachPath() string {
	if p, err := exec.LookPath(sshBinary); err == nil {
		return p
	}
	return "/usr/bin/ssh"
}

// moshClientPath resolves the mosh client wrapper to an absolute path (mirroring
// execAttachPath) so ExecAttach's syscall.Exec — which does no $PATH lookup —
// can launch it. Falls back to the Homebrew location (where `brew install mosh`
// lands it on macOS, the same PATH LoginShellWrap exists to reach). Callers
// resolve local mosh availability separately (command/wiring.go) and only build
// a mosh argv when the client is actually present, falling back to ssh otherwise.
func moshClientPath() string {
	if p, err := exec.LookPath(moshBinary); err == nil {
		return p
	}
	return "/opt/homebrew/bin/mosh"
}

// runFunc is the seam tests swap to record argv / stdin and inject failures
// without touching a real host. Production runs via exec.Command.
type runFunc func(argv []string, stdin io.Reader) (string, error)

// run is the package-level runner. Tests replace it; restore it with defer.
var run runFunc = realRun

func realRun(argv []string, stdin io.Reader) (string, error) {
	// The multiplexed ssh flags reference ~/.duck/cm/<socket>; create that dir
	// before the first ssh or it fails with "cannot bind to path … No such file
	// or directory". Idempotent and cheap.
	if err := EnsureControlDir(); err != nil {
		return "", err
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s: %w: %s", strings.Join(argv, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// Client drives the hub over multiplexed SSH.
type Client struct {
	Addr string // user@host
	// Mosh, when true, makes AttachArgv build a mosh interactive attach instead
	// of `ssh -t`. It affects ONLY AttachArgv — every other call (Run, the
	// opener's RemoteForward/LocalForward, ReadFile, naming, EnsureTerminfo, and
	// mosh's own bootstrap ssh) stays on ssh, because mosh cannot port-forward.
	Mosh bool
}

// New returns a Client for the given hub address (ssh interactive-attach
// transport — the default).
func New(addr string) *Client { return &Client{Addr: addr} }

// NewWithTransport returns a Client whose interactive attach uses mosh when
// mosh is true, ssh otherwise. The control plane is unaffected either way.
func NewWithTransport(addr string, mosh bool) *Client { return &Client{Addr: addr, Mosh: mosh} }

// homeDir returns the Go-expanded $HOME (never a literal "~"). Split out so the
// ControlPath and the cm-dir MkdirAll share one source of truth (c2).
func homeDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if home == "" {
		return "", fmt.Errorf("could not determine home directory")
	}
	return home, nil
}

// controlDir returns <HOME>/.duck/cm, the ssh control-master socket directory.
func controlDir() (string, error) {
	home, err := homeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".duck", "cm"), nil
}

// ControlPath returns the ssh ControlPath template using the Go-expanded $HOME.
// The %r@%h:%p tokens are filled in by ssh per (remote-user, host, port).
func ControlPath() (string, error) {
	dir, err := controlDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "%r@%h:%p"), nil
}

// EnsureControlDir creates <HOME>/.duck/cm with mode 0700 (c2). Idempotent.
// Call once at startup before any multiplexed SSH.
func EnsureControlDir() error {
	dir, err := controlDir()
	if err != nil {
		return err
	}
	return os.MkdirAll(dir, 0o700)
}

// Options returns the DUCKSSH option flags (without the leading "ssh" or the
// trailing addr/command). Exported so the ported hub package can carry the same
// Control* flags (gap#6) and tests can assert on them.
func Options() ([]string, error) {
	cp, err := ControlPath()
	if err != nil {
		return nil, err
	}
	return []string{
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=" + cp,
		"-o", "ControlPersist=10m",
	}, nil
}

// commandArgv builds the full ssh argv for a non-interactive remote command:
//
//	ssh <DUCKSSH opts> <addr> <remoteCmd>
func (c *Client) commandArgv(remoteCmd string) ([]string, error) {
	opts, err := Options()
	if err != nil {
		return nil, err
	}
	argv := []string{sshBinary}
	argv = append(argv, opts...)
	argv = append(argv, c.Addr, LoginShellWrap(remoteCmd))
	return argv, nil
}

// termGuard is a shell prefix that runs before the remote tmux attach. It keeps
// the forwarded TERM when the hub's terminfo db knows it, else falls back to
// xterm-256color so tmux never aborts with "missing or unsuitable terminal".
// `infocmp <term>` exits non-zero when the entry is absent; its output is
// discarded. Lives inside the LoginShellWrap'd command so it shares the same
// login shell as tmux (and thus the Homebrew PATH where infocmp resolves).
const termGuard = `infocmp "$TERM" >/dev/null 2>&1 || export TERM=xterm-256color; `

// LoginShellWrap wraps a remote command so the hub runs it under a LOGIN shell,
// making the user's full PATH available — notably Homebrew's /opt/homebrew/bin,
// where tmux and mutagen live on a macOS hub. A plain `ssh host cmd` runs cmd in
// a non-login, non-interactive shell whose PATH lacks those dirs (verified:
// `ssh duck tmux` → "command not found", `ssh duck "zsh -lc 'tmux -V'"` → ok).
// Mirrors the old duck scripts' `/bin/zsh -lc` pattern.
func LoginShellWrap(remoteCmd string) string {
	return "zsh -lc " + paths.Quote(remoteCmd)
}

// Run executes a remote command over the multiplexed connection and returns its
// stdout. No PTY is allocated.
func (c *Client) Run(remoteCmd string) (string, error) {
	return c.RunInput(remoteCmd, nil)
}

// RunInput executes a remote command, optionally feeding stdin, and returns
// stdout. Streaming untrusted content via stdin keeps it out of the command
// text entirely.
func (c *Client) RunInput(remoteCmd string, stdin io.Reader) (string, error) {
	argv, err := c.commandArgv(remoteCmd)
	if err != nil {
		return "", err
	}
	return run(argv, stdin)
}

// controlArgv builds an `ssh -O <action> [-R/-L spec] addr` control-command
// argv against the existing master socket. `-O` operations talk to the warmed
// ControlMaster (they need it up — call WarmUp first); they print nothing and
// exit 0 on success. The forward/cancel specs reuse the SAME Control* flags so
// they resolve the same socket as every other duck call.
func (c *Client) controlArgv(action string, forwardSpec ...string) ([]string, error) {
	opts, err := Options()
	if err != nil {
		return nil, err
	}
	argv := []string{sshBinary}
	argv = append(argv, opts...)
	argv = append(argv, "-O", action)
	argv = append(argv, forwardSpec...)
	argv = append(argv, c.Addr)
	return argv, nil
}

// RemoteForward asks the master to add a reverse forward: the hub's
// 127.0.0.1:hubPort connects back to the laptop's 127.0.0.1:localPort. This is
// the channel the hub-side `duck-open` shim uses to reach the laptop's opener
// listener. It cancels any pre-existing forward on the same spec first so a
// stale forward (left by a crashed prior attach) does not make the add fail.
func (c *Client) RemoteForward(hubPort, localPort int) error {
	return c.ensureForward("-R", fmt.Sprintf("%d:127.0.0.1:%d", hubPort, localPort))
}

// ensureForward adds a forward (flag -R or -L) on the control master,
// best-effort cancelling a stale identical forward first (left by a crashed
// prior attach) so the add never fails on "already forwarded"; the cancel's
// error is ignored because none-to-cancel is the common case.
func (c *Client) ensureForward(flag, spec string) error {
	if err := EnsureControlDir(); err != nil {
		return err
	}
	if argv, err := c.controlArgv("cancel", flag, spec); err == nil {
		_, _ = run(argv, nil)
	}
	argv, err := c.controlArgv("forward", flag, spec)
	if err != nil {
		return err
	}
	_, err = run(argv, nil)
	return err
}

// CancelRemoteForward tears down the reverse forward added by RemoteForward.
// Best-effort by nature (the master may already be gone); the error is returned
// for the caller to log/ignore.
func (c *Client) CancelRemoteForward(hubPort, localPort int) error {
	argv, err := c.controlArgv("cancel", "-R", fmt.Sprintf("%d:127.0.0.1:%d", hubPort, localPort))
	if err != nil {
		return err
	}
	_, err = run(argv, nil)
	return err
}

// LocalForward adds a forward so the laptop's 127.0.0.1:localPort reaches the
// hub's 127.0.0.1:hubPort. The opener listener uses it to make a hub-local URL
// (a dev server on localhost:<port>) reachable from the laptop browser before
// it opens the rewritten URL.
func (c *Client) LocalForward(localPort, hubPort int) error {
	return c.ensureForward("-L", fmt.Sprintf("%d:127.0.0.1:%d", localPort, hubPort))
}

// ReadFile returns the bytes of a file on the hub by streaming it through `cat`
// over the multiplexed connection. Used by the opener listener to pull a file
// that is NOT in a synced folder (so it has no local twin) into a laptop temp
// dir before opening it. remotePath is single-quoted into the login-shell'd
// command. Whole-file-in-memory is fine for the screenshots/PDFs/HTML this
// serves; it is not meant for large blobs.
func (c *Client) ReadFile(remotePath string) ([]byte, error) {
	out, err := c.Run("cat -- " + paths.Quote(remotePath))
	if err != nil {
		return nil, err
	}
	return []byte(out), nil
}

// WarmUp issues a single serialized handshake (`ssh duck true`) to establish
// the control-master socket before the laptop fans out parallel refresh calls,
// so they reuse the master instead of racing to create it (design fix c1).
// Safe to call repeatedly; ControlMaster=auto reuses an existing master.
func (c *Client) WarmUp() error {
	if err := EnsureControlDir(); err != nil {
		return err
	}
	_, err := c.Run("true")
	return err
}

// EnsureTerminfo makes the hub recognize the given TERM so the interactive
// attach keeps the client's full terminal capabilities instead of falling back
// to xterm-256color (the termGuard safety net in AttachArgv). It pipes the
// LOCAL terminfo source (infocmp -x) into a remote `tic -x -`, but only when the
// hub does not already know the entry — so it is idempotent and a no-op on the
// common case. Best-effort: a missing local entry, absent tic, or any transport
// error returns the error for the caller to ignore; the attach still works via
// the fallback. Skips the universally-present defaults (and empty TERM) entirely.
func (c *Client) EnsureTerminfo(term string) error {
	switch term {
	case "", "xterm", "xterm-256color", "screen", "screen-256color", "tmux-256color", "vt100", "ansi", "dumb":
		return nil
	}
	src, err := exec.Command("infocmp", "-x", term).Output()
	if err != nil {
		// Local db lacks the entry — nothing to push; the hub fallback covers it.
		return err
	}
	// `tic -x -` reads source from stdin. Guard with a remote infocmp so we only
	// compile when the entry is genuinely absent. LoginShellWrap puts tic on the
	// hub's Homebrew PATH (same as tmux).
	remote := "infocmp " + paths.Quote(term) + " >/dev/null 2>&1 || tic -x -"
	_, err = c.RunInput(LoginShellWrap(remote), bytes.NewReader(src))
	return err
}

// AttachArgv builds the argv for an interactive `tmux attach-session` over the
// SAME multiplexed control path. Pure (no side effects) so it is unit-testable;
// ExecAttach wraps it with the actual syscall.Exec.
func (c *Client) AttachArgv(tmuxSession string) ([]string, error) {
	if c.Mosh {
		return c.moshAttachArgv(tmuxSession)
	}
	opts, err := Options()
	if err != nil {
		return nil, err
	}
	// -t forces PTY allocation so tmux/ssh own a real TTY after the bubbletea
	// teardown. The remote command is the literal tmux attach, prefixed with a
	// TERM guard: ssh -t forwards the client's TERM (e.g. xterm-ghostty under
	// Ghostty), but the hub's terminfo db may lack that entry, in which case
	// tmux aborts with "missing or unsuitable terminal" and the connection
	// drops. infocmp probes the hub's db and we downgrade to xterm-256color
	// (universally present) only when the forwarded TERM is unknown there.
	argv := []string{execAttachPath(), "-t"}
	argv = append(argv, opts...)
	// Wrap in a login shell so tmux resolves on the hub's Homebrew PATH; the
	// -t PTY passes through ssh → zsh → tmux. tmuxSession is a tmux-legal slug.
	argv = append(argv, c.Addr, LoginShellWrap(termGuard+"tmux attach-session -t "+tmuxSession))
	return argv, nil
}

// moshAttachArgv builds the argv for an interactive mosh attach. mosh bootstraps
// mosh-server over the SAME multiplexed ssh (so the one-time launch reuses the
// warmed ControlMaster), then carries the session over UDP — which is why duck
// must NOT wrap a mosh attach in the ssh-255 backoff loop (a network drop never
// makes mosh exit; mosh roams). Pure (no side effects), so it is unit-testable.
//
// Two shapes differ from AttachArgv on purpose:
//   - The DUCKSSH options are joined into ONE "--ssh=ssh <opts>" string; mosh
//     splits that on whitespace itself (our -o flags and the ~/.duck/cm
//     ControlPath contain no spaces on a normal $HOME).
//   - After "--", mosh execs the remote argv DIRECTLY (no remote shell), so the
//     login-shell wrap is passed as SEPARATE words ["zsh","-lc",<script>] rather
//     than LoginShellWrap's single quoted string (which the remote sshd would
//     shell-split, but mosh would not). The zsh -lc wrap is still required so
//     tmux resolves on the hub's Homebrew PATH; termGuard degrades TERM the same.
//
// --no-init keeps mosh-client from driving its own alternate-screen init: tmux
// already owns the alt screen on the hub, so this avoids a double switch.
func (c *Client) moshAttachArgv(tmuxSession string) ([]string, error) {
	opts, err := Options()
	if err != nil {
		return nil, err
	}
	sshCmd := strings.Join(append([]string{sshBinary}, opts...), " ")
	remote := termGuard + "tmux attach-session -t " + tmuxSession
	argv := []string{
		moshClientPath(),
		"--ssh=" + sshCmd,
		"--no-init",
		c.Addr,
		"--",
		"zsh", "-lc", remote,
	}
	return argv, nil
}

// ExecAttach replaces the current process with an interactive ssh -t attach to
// the named tmux session, reusing the warmed control-master socket. It returns
// only on failure to exec (on success the process image is replaced). Callers
// must fully tear down bubbletea first so ssh/tmux own a clean TTY; TERM passes
// through via the inherited environment.
func (c *Client) ExecAttach(tmuxSession string) error {
	if err := EnsureControlDir(); err != nil {
		return err
	}
	argv, err := c.AttachArgv(tmuxSession)
	if err != nil {
		return err
	}
	return syscall.Exec(argv[0], argv, os.Environ())
}

// RunAttach is the SUBPROCESS variant of ExecAttach: it runs the same
// interactive `ssh -t tmux attach-session` as a child process (inheriting this
// process's TTY via os.Stdin/Stdout/Stderr) and BLOCKS until the user detaches
// or exits, returning nil on a normal interactive exit. Unlike ExecAttach
// (which replaces the process image and never returns) it hands control BACK to
// the caller, so the fresh-untouched-session cleanup can run after the user
// leaves. The argv — including the -t PTY and the multiplexing Control* flags —
// is the same AttachArgv ExecAttach uses, so the interactive session/picker
// works identically.
func (c *Client) RunAttach(tmuxSession string) error {
	if err := EnsureControlDir(); err != nil {
		return err
	}
	argv, err := c.AttachArgv(tmuxSession)
	if err != nil {
		return err
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
