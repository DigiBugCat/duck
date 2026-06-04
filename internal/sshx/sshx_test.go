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
	if argv[len(argv)-1] != "zsh -lc 'tmux attach-session -t cc-1234'" {
		t.Errorf("AttachArgv tail = %q, want login-shell-wrapped tmux attach", argv[len(argv)-1])
	}
}
