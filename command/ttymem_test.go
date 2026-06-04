package command

import (
	"os"
	"testing"
)

// withTempHome points $HOME at a temp dir so the tty-memory store writes under a
// throwaway ~/.duck. os.UserHomeDir honors $HOME on darwin/linux.
func withTempHome(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	old, had := os.LookupEnv("HOME")
	if err := os.Setenv("HOME", dir); err != nil {
		t.Fatalf("setenv HOME: %v", err)
	}
	t.Cleanup(func() {
		if had {
			os.Setenv("HOME", old)
		} else {
			os.Unsetenv("HOME")
		}
	})
}

func TestTTYMemSetGetRoundTrip(t *testing.T) {
	withTempHome(t)
	const tty = "tty:42"
	if err := ttyMemSet(tty, "foo"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, ok := ttyMemGet(tty)
	if !ok || got != "foo" {
		t.Fatalf("Get = %q,%v, want foo,true", got, ok)
	}
	// A second terminal is independent.
	if err := ttyMemSet("tty:7", "bar"); err != nil {
		t.Fatalf("Set 2: %v", err)
	}
	if got, _ := ttyMemGet(tty); got != "foo" {
		t.Fatalf("first terminal entry must survive a second Set, got %q", got)
	}
}

func TestTTYMemGetMissIsFalse(t *testing.T) {
	withTempHome(t)
	if _, ok := ttyMemGet("tty:999"); ok {
		t.Fatalf("Get on an empty store must miss")
	}
}

// TestTTYMemEmptyTTYIsNoOp: a "" tty (stdin not a terminal) must never read or
// write the store — Set is a no-op and Get always misses.
func TestTTYMemEmptyTTYIsNoOp(t *testing.T) {
	withTempHome(t)
	if err := ttyMemSet("", "foo"); err != nil {
		t.Fatalf("Set(\"\") should be a no-op nil, got %v", err)
	}
	if _, err := ttyMemPath(); err != nil {
		t.Fatalf("path: %v", err)
	}
	// No file should have been created by the no-op Set.
	p, _ := ttyMemPath()
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("Set(\"\") must not create the store file (stat err=%v)", err)
	}
	if _, ok := ttyMemGet(""); ok {
		t.Fatalf("Get(\"\") must always miss")
	}
}

func TestTTYMemPrune(t *testing.T) {
	withTempHome(t)
	const tty = "tty:42"
	if err := ttyMemSet(tty, "gone"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := ttyMemPrune(tty); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if _, ok := ttyMemGet(tty); ok {
		t.Fatalf("pruned entry must be gone")
	}
}

// TestCurrentTTYNonTTYIsEmpty: when stdin is not a terminal, CurrentTTY must
// return "" (the skip-memory signal). Pointing stdin at /dev/null makes this
// deterministic regardless of whether the test is run from an interactive shell
// (mirroring conflict_test.go's withDevNullStdin pattern).
func TestCurrentTTYNonTTYIsEmpty(t *testing.T) {
	withDevNullStdin(t, func() {
		if got := CurrentTTY(); got != "" {
			t.Fatalf("CurrentTTY with non-TTY stdin = %q, want \"\"", got)
		}
	})
}
