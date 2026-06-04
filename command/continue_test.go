package command

import (
	"testing"

	"github.com/DigiBugCat/duck/internal/session"
)

// TestRunContinueTTYHitResumesRemembered: a terminal with a remembered session
// that STILL EXISTS on the hub resumes THAT session — Recent and the dir-sync
// gate are NOT consulted.
func TestRunContinueTTYHitResumesRemembered(t *testing.T) {
	var attached string
	var ensuredDir, calledRecent bool
	d := continueDeps{
		currentTTY: func() string { return "tty:42" },
		memGet:     func(string) (string, bool) { return "remembered", true },
		memPrune:   func(string) error { t.Fatalf("must not prune on a live hit"); return nil },
		hasSession: func(name string) (bool, error) { return name == "remembered", nil },
		ensureDir:  func() (string, error) { ensuredDir = true; return "~/dev/foo", nil },
		recent:     func(string) (session.Sess, bool, error) { calledRecent = true; return session.Sess{}, false, nil },
		attach:     func(name string) { attached = name },
	}
	if err := runContinueWith(d); err != nil {
		t.Fatalf("runContinueWith: %v", err)
	}
	if attached != "remembered" {
		t.Fatalf("tty hit must attach the remembered session, attached=%q", attached)
	}
	if ensuredDir || calledRecent {
		t.Fatalf("tty hit must NOT consult the dir gate/Recent (ensuredDir=%v recent=%v)", ensuredDir, calledRecent)
	}
}

// TestRunContinueTTYStaleFallsBackToRecent: a remembered session that no longer
// exists on the hub is pruned, and -c falls back to Recent(dir).
func TestRunContinueTTYStaleFallsBackToRecent(t *testing.T) {
	var attached string
	var pruned bool
	d := continueDeps{
		currentTTY: func() string { return "tty:42" },
		memGet:     func(string) (string, bool) { return "ghost", true },
		memPrune:   func(string) error { pruned = true; return nil },
		hasSession: func(string) (bool, error) { return false, nil }, // gone
		ensureDir:  func() (string, error) { return "~/dev/foo", nil },
		recent:     func(string) (session.Sess, bool, error) { return session.Sess{Name: "recent"}, true, nil },
		attach:     func(name string) { attached = name },
	}
	if err := runContinueWith(d); err != nil {
		t.Fatalf("runContinueWith: %v", err)
	}
	if !pruned {
		t.Fatalf("a stale remembered entry must be pruned")
	}
	if attached != "recent" {
		t.Fatalf("stale tty entry must fall back to Recent, attached=%q", attached)
	}
}

// TestRunContinueNoTTYUsesRecent: no controlling terminal → memory is skipped
// entirely and -c uses Recent(dir).
func TestRunContinueNoTTYUsesRecent(t *testing.T) {
	var attached string
	d := continueDeps{
		currentTTY: func() string { return "" },
		memGet:     func(string) (string, bool) { t.Fatalf("must not read memory with no TTY"); return "", false },
		memPrune:   func(string) error { return nil },
		hasSession: func(string) (bool, error) { return false, nil },
		ensureDir:  func() (string, error) { return "~/dev/foo", nil },
		recent:     func(string) (session.Sess, bool, error) { return session.Sess{Name: "recent"}, true, nil },
		attach:     func(name string) { attached = name },
	}
	if err := runContinueWith(d); err != nil {
		t.Fatalf("runContinueWith: %v", err)
	}
	if attached != "recent" {
		t.Fatalf("no-TTY -c must use Recent, attached=%q", attached)
	}
}

// TestRunContinueNoSessionErrors: no tty hit and no Recent session → the
// instructive error.
func TestRunContinueNoSessionErrors(t *testing.T) {
	d := continueDeps{
		currentTTY: func() string { return "" },
		memGet:     func(string) (string, bool) { return "", false },
		memPrune:   func(string) error { return nil },
		hasSession: func(string) (bool, error) { return false, nil },
		ensureDir:  func() (string, error) { return "~/dev/foo", nil },
		recent:     func(string) (session.Sess, bool, error) { return session.Sess{}, false, nil },
		attach:     func(string) { t.Fatalf("must not attach when no session exists") },
	}
	if err := runContinueWith(d); err == nil {
		t.Fatalf("expected an instructive error when no session exists")
	}
}
