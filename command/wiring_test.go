package command

import (
	"os/exec"
	"strings"
	"testing"
)

// TestEffectiveTsshAttach pins the effective-transport resolution that gates the
// tssh interactive attach across all three configured values: auto (tssh-if-
// present, silent fallback), explicit ssh (never tssh), and explicit tssh (tssh,
// or ssh + a warning when the client is absent).
func TestEffectiveTsshAttach(t *testing.T) {
	present := func(string) (string, error) { return "/opt/homebrew/bin/tssh", nil }
	absent := func(string) (string, error) { return "", exec.ErrNotFound }

	// auto + client present → use tssh, no warning (opt-in by install).
	if useTssh, warn := effectiveTsshAttach("auto", present); !useTssh || warn != "" {
		t.Errorf("auto+present: got (useTssh=%v, warn=%q), want (true, \"\")", useTssh, warn)
	}

	// auto + client absent → ssh, SILENTLY (no warning: auto-detect, not a request).
	if useTssh, warn := effectiveTsshAttach("auto", absent); useTssh || warn != "" {
		t.Errorf("auto+absent: got (useTssh=%v, warn=%q), want (false, \"\")", useTssh, warn)
	}

	// explicit ssh never uses tssh and never warns, regardless of local client.
	if useTssh, warn := effectiveTsshAttach("ssh", present); useTssh || warn != "" {
		t.Errorf("ssh: got (useTssh=%v, warn=%q), want (false, \"\")", useTssh, warn)
	}

	// explicit tssh + client present → use tssh, no warning.
	if useTssh, warn := effectiveTsshAttach("tssh", present); !useTssh || warn != "" {
		t.Errorf("tssh+present: got (useTssh=%v, warn=%q), want (true, \"\")", useTssh, warn)
	}

	// explicit tssh + client absent → fall back to ssh WITH a warning.
	useTssh, warn := effectiveTsshAttach("tssh", absent)
	if useTssh {
		t.Errorf("tssh+absent: useTssh = true, want false (fall back to ssh)")
	}
	if !strings.Contains(warn, "tssh") || warn == "" {
		t.Errorf("tssh+absent: want a non-empty fallback warning mentioning tssh, got %q", warn)
	}
}
