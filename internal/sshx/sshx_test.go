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

// TestMoshAttachArgvBuildsInteractiveAttach pins the mosh interactive-attach
// argv: the mosh client (absolute) bootstrapping over a single --ssh string that
// carries the DUCKSSH Control* flags, then the remote command as SEPARATE words
// after `--` (mosh execs them directly, no remote shell). Contrast with the ssh
// path (TestAttachArgvBuildsInteractiveAttach), which hands sshd ONE quoted
// command — that test staying green is the proof mosh is opt-in.
func TestMoshAttachArgvBuildsInteractiveAttach(t *testing.T) {
	c := NewWithTransport("me@hub.local", true)
	argv, err := c.AttachArgv("cc-1234")
	if err != nil {
		t.Fatalf("AttachArgv: %v", err)
	}
	// argv[0] is the mosh client, absolute so ExecAttach's syscall.Exec can launch it.
	if !filepath.IsAbs(argv[0]) {
		t.Errorf("AttachArgv[0] = %q, want absolute mosh path", argv[0])
	}
	if filepath.Base(argv[0]) != "mosh" {
		t.Errorf("AttachArgv[0] base = %q, want mosh", filepath.Base(argv[0]))
	}
	// The DUCKSSH options must be ONE argv element (mosh shell-splits the --ssh
	// string itself). Asserting the exact element — not just a substring of the
	// joined argv — catches a regression that appended each `-o` flag as its own
	// argv word (which would still contain "ControlPath="/"ControlMaster=auto" in
	// the join but break mosh, which would parse the loose -o words as ITS options).
	opts, err := Options()
	if err != nil {
		t.Fatalf("Options: %v", err)
	}
	wantSSH := "--ssh=" + strings.Join(append([]string{"ssh"}, opts...), " ")
	if argv[1] != wantSSH {
		t.Errorf("argv[1] must be the single --ssh element\n got %q\nwant %q", argv[1], wantSSH)
	}
	if argv[2] != "--no-init" {
		t.Errorf("argv[2] = %q, want --no-init (tmux owns the alt screen)", argv[2])
	}
	// Exact head + length pins the structure: mosh, --ssh=<one element>, --no-init,
	// addr, --, zsh, -lc, script. A split of opts into extra words fails on length.
	if len(argv) != 8 {
		t.Errorf("argv len = %d, want 8 (mosh, --ssh=, --no-init, addr, --, zsh, -lc, script): %v", len(argv), argv)
	}
	if argv[3] != "me@hub.local" {
		t.Errorf("argv[3] = %q, want addr immediately before the `--` separator: %v", argv[3], argv)
	}
	// Tail: addr, then `--`, then the remote command as SEPARATE words zsh -lc <script>.
	n := len(argv)
	if argv[n-5] != "me@hub.local" {
		t.Errorf("addr must precede the `--` separator: %v", argv)
	}
	if argv[n-4] != "--" {
		t.Errorf("want `--` before the remote command: %v", argv)
	}
	if argv[n-3] != "zsh" || argv[n-2] != "-lc" {
		t.Errorf("remote command must be a separate `zsh -lc` invocation (mosh execs argv directly): %v", argv)
	}
	want := `infocmp "$TERM" >/dev/null 2>&1 || export TERM=xterm-256color; tmux attach-session -t cc-1234`
	if argv[n-1] != want {
		t.Errorf("mosh remote script = %q, want the termGuard+tmux attach (unquoted, one word): %q", argv[n-1], want)
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
