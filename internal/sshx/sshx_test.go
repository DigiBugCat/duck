package sshx

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recordRun swaps the package run seam to capture argv (and call order) without
// touching a real host.
func recordRun(t *testing.T) *[][]string {
	t.Helper()
	var calls [][]string
	orig := run
	run = func(argv []string, _ io.Reader) (string, error) {
		calls = append(calls, append([]string(nil), argv...))
		return "", nil
	}
	t.Cleanup(func() { run = orig })
	return &calls
}

func TestOptionsCarryControlMasterFlags(t *testing.T) {
	opts, err := Options()
	if err != nil {
		t.Fatalf("Options: %v", err)
	}
	joined := strings.Join(opts, " ")
	for _, want := range []string{
		"-o BatchMode=yes",
		"-o ConnectTimeout=10",
		"-o ControlMaster=auto",
		"-o ControlPersist=10m",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("Options missing %q\ngot: %v", want, opts)
		}
	}
}

func TestControlPathIsGoExpandedHome(t *testing.T) {
	cp, err := ControlPath()
	if err != nil {
		t.Fatalf("ControlPath: %v", err)
	}
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".duck", "cm", "%r@%h:%p")
	if cp != want {
		t.Errorf("ControlPath() = %q, want %q", cp, want)
	}
	// c2: never a literal "~".
	if strings.HasPrefix(cp, "~") {
		t.Errorf("ControlPath used a literal ~: %q", cp)
	}
	if !filepath.IsAbs(cp) {
		t.Errorf("ControlPath is not absolute: %q", cp)
	}
}

func TestRunArgvShape(t *testing.T) {
	calls := recordRun(t)
	c := New("me@hub.local")
	if _, err := c.Run("tmux list-sessions"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(*calls))
	}
	argv := (*calls)[0]
	if argv[0] != "ssh" {
		t.Errorf("argv[0] = %q, want ssh", argv[0])
	}
	if argv[len(argv)-2] != "me@hub.local" {
		t.Errorf("addr not penultimate: %v", argv)
	}
	// The remote command is wrapped in a login shell so Homebrew tmux/mutagen
	// resolve on the hub's PATH (a plain non-login ssh shell lacks them).
	if argv[len(argv)-1] != "zsh -lc 'tmux list-sessions'" {
		t.Errorf("remote cmd = %q, want login-shell-wrapped \"tmux list-sessions\"", argv[len(argv)-1])
	}
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "ControlPath=") {
		t.Errorf("Run argv missing ControlPath: %v", argv)
	}
}

func TestRemoteForwardSocketArgvShape(t *testing.T) {
	calls := recordRun(t)
	c := New("me@hub.local")
	if err := c.RemoteForwardSocket("/home/me/.duck/run/open-foo.sock", 55123); err != nil {
		t.Fatalf("RemoteForwardSocket: %v", err)
	}
	// Two calls: a best-effort cancel of a stale identical forward, then the add.
	if len(*calls) != 2 {
		t.Fatalf("expected 2 calls (cancel, forward), got %d: %v", len(*calls), *calls)
	}
	add := (*calls)[1]
	joined := strings.Join(add, " ")
	spec := "/home/me/.duck/run/open-foo.sock:127.0.0.1:55123"
	if !strings.Contains(joined, "-O forward") {
		t.Errorf("forward call missing -O forward: %v", add)
	}
	if !strings.Contains(joined, spec) {
		t.Errorf("forward call missing socket spec %q: %v", spec, add)
	}
	// StreamLocalBindUnlink must sit BEFORE -O (ssh rejects connection options
	// placed after the -O action). Assert ordering, not just presence.
	ub := strings.Index(joined, "StreamLocalBindUnlink=yes")
	oIdx := strings.Index(joined, "-O forward")
	if ub < 0 {
		t.Fatalf("missing StreamLocalBindUnlink: %v", add)
	}
	if ub > oIdx {
		t.Errorf("StreamLocalBindUnlink must precede -O forward: %v", add)
	}
}

func TestRunWrapsRemoteCmdInLoginShell(t *testing.T) {
	// Regression guard for the Homebrew-PATH fix: every remote command must run
	// under `zsh -lc` so /opt/homebrew/bin (tmux, mutagen) is on PATH. Embedded
	// single quotes must be escaped so the wrap stays a single shell word.
	calls := recordRun(t)
	c := New("h")
	if _, err := c.Run(`tmux list-sessions -F '#{session_name}'`); err != nil {
		t.Fatalf("Run: %v", err)
	}
	last := (*calls)[0][len((*calls)[0])-1]
	want := `zsh -lc 'tmux list-sessions -F '\''#{session_name}'\'''`
	if last != want {
		t.Errorf("wrapped remote cmd =\n  %q\nwant\n  %q", last, want)
	}
}

func TestEnsureControlDirIs0700(t *testing.T) {
	// Hermetic: redirect HOME so EnsureControlDir writes under a temp dir that
	// the test framework auto-cleans, never the real machine (hard safety rule).
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := EnsureControlDir(); err != nil {
		t.Fatalf("EnsureControlDir: %v", err)
	}
	info, err := os.Stat(filepath.Join(home, ".duck", "cm"))
	if err != nil {
		t.Fatalf("stat cm dir: %v", err)
	}
	// c2: the control-master socket dir is created mode 0700.
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("~/.duck/cm mode = %o, want 0700", perm)
	}
}

func TestWarmUpIssuesSingleTrueHandshake(t *testing.T) {
	// Hermetic: WarmUp calls EnsureControlDir which MkdirAll's ~/.duck/cm.
	// Redirect HOME to a temp dir so nothing is written to the real machine.
	t.Setenv("HOME", t.TempDir())
	calls := recordRun(t)
	c := New("h")
	if err := c.WarmUp(); err != nil {
		t.Fatalf("WarmUp: %v", err)
	}
	// c1: a single serialized `ssh duck true` warm-up.
	if len(*calls) != 1 {
		t.Fatalf("WarmUp made %d calls, want exactly 1", len(*calls))
	}
	argv := (*calls)[0]
	if argv[len(argv)-1] != "zsh -lc 'true'" {
		t.Errorf("warm-up remote cmd = %q, want login-shell-wrapped \"true\"", argv[len(argv)-1])
	}
}

func TestAttachArgvBuildsInteractiveAttach(t *testing.T) {
	c := New("me@hub.local")
	argv, err := c.AttachArgv("cc-1234")
	if err != nil {
		t.Fatalf("AttachArgv: %v", err)
	}
	// ExecAttach uses an absolute ssh path (syscall.Exec does no PATH lookup).
	if !filepath.IsAbs(argv[0]) {
		t.Errorf("AttachArgv[0] = %q, want absolute ssh path", argv[0])
	}
	if argv[1] != "-t" {
		t.Errorf("AttachArgv[1] = %q, want -t (force PTY)", argv[1])
	}
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "ControlPath=") {
		t.Errorf("AttachArgv missing ControlPath (must reuse the master socket): %v", argv)
	}
	// The tail is the login-shell-wrapped tmux attach to the named session
	// (wrapped so tmux resolves on the hub's Homebrew PATH).
	if argv[len(argv)-2] != "me@hub.local" {
		t.Errorf("addr not penultimate: %v", argv)
	}
	want := `zsh -lc 'infocmp "$TERM" >/dev/null 2>&1 || export TERM=xterm-256color; tmux attach-session -t cc-1234'`
	if argv[len(argv)-1] != want {
		t.Errorf("AttachArgv tail = %q, want login-shell-wrapped tmux attach with TERM guard", argv[len(argv)-1])
	}
}

// TestTsshAttachArgvBuildsInteractiveAttach pins the tssh interactive-attach
// argv: the tssh client (absolute), -t, StrictHostKeyChecking=accept-new, --udp,
// then --tsshd-path (when a hub path is known), the addr, and the remote command
// as SEPARATE words `zsh -lc <script>` (tssh runs trailing argv as the command).
// Notably it does NOT pass -F /dev/null or -i: tssh resolves the user's default
// ~/.ssh identities + agent + config, exactly like duck's other ssh calls (a
// -F /dev/null here would drop the attach to a password prompt). Contrast with
// the ssh path (TestAttachArgvBuildsInteractiveAttach), which hands sshd ONE
// quoted command — that test staying green is the proof tssh is opt-in.
func TestTsshAttachArgvBuildsInteractiveAttach(t *testing.T) {
	c := NewWithTransport("me@hub.local", true, "/opt/homebrew/bin/tsshd")
	argv, err := c.AttachArgv("cc-1234")
	if err != nil {
		t.Fatalf("AttachArgv: %v", err)
	}
	// argv[0] is the tssh client, absolute so ExecAttach's syscall.Exec can launch it.
	if !filepath.IsAbs(argv[0]) {
		t.Errorf("AttachArgv[0] = %q, want absolute tssh path", argv[0])
	}
	if filepath.Base(argv[0]) != "tssh" {
		t.Errorf("AttachArgv[0] base = %q, want tssh", filepath.Base(argv[0]))
	}
	// Exact head: -t, accept-new host keys, --udp. No -F/-i (default-key auth).
	wantHead := []string{argv[0], "-t", "-o", "StrictHostKeyChecking=accept-new", "--udp"}
	for i, w := range wantHead {
		if argv[i] != w {
			t.Errorf("argv[%d] = %q, want %q (full: %v)", i, argv[i], w, argv)
		}
	}
	// No -F /dev/null: it would also disable default-identity loading → password prompt.
	for _, a := range argv {
		if a == "-F" {
			t.Fatalf("argv must NOT pass -F (breaks default-key auth): %v", argv)
		}
	}
	// --tsshd-path is present (we passed a hub path) so the hub finds tsshd off its
	// non-login PATH. It must be two argv words: the flag and the absolute path.
	if argv[5] != "--tsshd-path" || argv[6] != "/opt/homebrew/bin/tsshd" {
		t.Errorf("argv[5:7] = %v, want [--tsshd-path /opt/homebrew/bin/tsshd]: %v", argv[5:7], argv)
	}
	// Tail: addr, then `--` (stops tssh parsing -lc as -l c), then the remote
	// command as SEPARATE words zsh -lc <script>.
	if len(argv) != 12 {
		t.Errorf("argv len = %d, want 12 (tssh -t -o … --udp --tsshd-path P addr -- zsh -lc script): %v", len(argv), argv)
	}
	n := len(argv)
	if argv[n-5] != "me@hub.local" {
		t.Errorf("addr must precede the `--` separator: %v", argv)
	}
	if argv[n-4] != "--" {
		t.Errorf("want `--` before the remote command (else tssh reads -lc as -l c): %v", argv)
	}
	if argv[n-3] != "zsh" || argv[n-2] != "-lc" {
		t.Errorf("remote command must be a separate `zsh -lc` invocation: %v", argv)
	}
	want := `infocmp "$TERM" >/dev/null 2>&1 || export TERM=xterm-256color; tmux attach-session -t cc-1234`
	if argv[n-1] != want {
		t.Errorf("tssh remote script = %q, want the termGuard+tmux attach: %q", argv[n-1], want)
	}
}

// TestTsshAttachArgvOmitsTsshdPathWhenEmpty pins that an empty hub tsshd path
// (the Linux auto-deploy case) drops the --tsshd-path flag entirely rather than
// passing an empty value — tssh then self-resolves / auto-installs tsshd.
func TestTsshAttachArgvOmitsTsshdPathWhenEmpty(t *testing.T) {
	c := NewWithTransport("me@hub.local", true, "")
	argv, err := c.AttachArgv("cc-1234")
	if err != nil {
		t.Fatalf("AttachArgv: %v", err)
	}
	for _, a := range argv {
		if a == "--tsshd-path" {
			t.Fatalf("--tsshd-path must be omitted when the hub path is empty: %v", argv)
		}
	}
	// Without the two --tsshd-path words the argv is 10 elements.
	if len(argv) != 10 {
		t.Errorf("argv len = %d, want 10 without --tsshd-path: %v", len(argv), argv)
	}
}

// TestRemoteForwardArgvShape pins the reverse-forward control command: it
// cancels any stale identical forward first, then issues `-O forward -R
// <hub>:127.0.0.1:<local>` carrying the same Control* flags as every other call.
func TestRemoteForwardArgvShape(t *testing.T) {
	calls := recordRun(t)
	c := New("me@hub")
	if err := c.RemoteForward(4774, 55001); err != nil {
		t.Fatalf("RemoteForward: %v", err)
	}
	if len(*calls) != 2 {
		t.Fatalf("want cancel-then-forward (2 calls), got %d: %v", len(*calls), *calls)
	}
	cancel := strings.Join((*calls)[0], " ")
	fwd := strings.Join((*calls)[1], " ")
	if !strings.Contains(cancel, "-O cancel") || !strings.Contains(cancel, "-R 4774:127.0.0.1:55001") {
		t.Errorf("first call should cancel the stale forward: %s", cancel)
	}
	if !strings.Contains(fwd, "-O forward") || !strings.Contains(fwd, "-R 4774:127.0.0.1:55001") {
		t.Errorf("second call should add the forward: %s", fwd)
	}
	if !strings.Contains(fwd, "ControlPath=") {
		t.Errorf("forward must carry Control* flags: %s", fwd)
	}
	if (*calls)[1][len((*calls)[1])-1] != "me@hub" {
		t.Errorf("addr must be the last arg (no remote command): %v", (*calls)[1])
	}
}

// TestLocalForwardArgvShape pins the -L forward used to tunnel a hub dev server
// to the laptop.
func TestLocalForwardArgvShape(t *testing.T) {
	calls := recordRun(t)
	c := New("me@hub")
	if err := c.LocalForward(5173, 5173); err != nil {
		t.Fatalf("LocalForward: %v", err)
	}
	fwd := strings.Join((*calls)[len(*calls)-1], " ")
	if !strings.Contains(fwd, "-O forward") || !strings.Contains(fwd, "-L 5173:127.0.0.1:5173") {
		t.Errorf("local forward argv wrong: %s", fwd)
	}
}

// TestReadFileCats pins that ReadFile streams the file through a login-shell cat
// with the path single-quoted.
func TestReadFileCats(t *testing.T) {
	calls := recordRun(t)
	c := New("h")
	if _, err := c.ReadFile("/tmp/a b.png"); err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	// The cat command is login-shell-wrapped, so the path's own single quotes are
	// escaped as '\'' — the wrapped form below is the correct single shell word.
	last := (*calls)[0][len((*calls)[0])-1]
	want := `zsh -lc 'cat -- '\''/tmp/a b.png'\'''`
	if last != want {
		t.Errorf("ReadFile cmd = %q, want %q", last, want)
	}
}
