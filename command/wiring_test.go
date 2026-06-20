package command

import (
	"os/exec"
	"strings"
	"testing"
)

// TestEffectiveMoshAttach pins the effective-transport resolution that gates the
// mosh interactive attach across all three configured values: auto (mosh-if-
// present, silent fallback), explicit ssh (never mosh), and explicit mosh (mosh,
// or ssh + a warning when the client is absent).
func TestEffectiveMoshAttach(t *testing.T) {
	present := func(string) (string, error) { return "/opt/homebrew/bin/mosh", nil }
	absent := func(string) (string, error) { return "", exec.ErrNotFound }

	// auto + client present → use mosh, no warning (opt-in by install).
	if useMosh, warn := effectiveMoshAttach("auto", present); !useMosh || warn != "" {
		t.Errorf("auto+present: got (useMosh=%v, warn=%q), want (true, \"\")", useMosh, warn)
	}

	// auto + client absent → ssh, SILENTLY (no warning: auto-detect, not a request).
	if useMosh, warn := effectiveMoshAttach("auto", absent); useMosh || warn != "" {
		t.Errorf("auto+absent: got (useMosh=%v, warn=%q), want (false, \"\")", useMosh, warn)
	}

	// explicit ssh never uses mosh and never warns, regardless of local client.
	if useMosh, warn := effectiveMoshAttach("ssh", present); useMosh || warn != "" {
		t.Errorf("ssh: got (useMosh=%v, warn=%q), want (false, \"\")", useMosh, warn)
	}

	// explicit mosh + client present → use mosh, no warning.
	if useMosh, warn := effectiveMoshAttach("mosh", present); !useMosh || warn != "" {
		t.Errorf("mosh+present: got (useMosh=%v, warn=%q), want (true, \"\")", useMosh, warn)
	}

	// explicit mosh + client absent → fall back to ssh WITH a warning.
	useMosh, warn := effectiveMoshAttach("mosh", absent)
	if useMosh {
		t.Errorf("mosh+absent: useMosh = true, want false (fall back to ssh)")
	}
	if !strings.Contains(warn, "mosh") || warn == "" {
		t.Errorf("mosh+absent: want a non-empty fallback warning mentioning mosh, got %q", warn)
	}
}
