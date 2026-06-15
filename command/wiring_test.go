package command

import (
	"os/exec"
	"strings"
	"testing"
)

// TestEffectiveMoshAttach pins the effective-transport resolution that gates the
// mosh interactive attach: mosh only when selected AND the local client exists,
// else ssh — and a warning (with fallback) when mosh is selected but absent.
func TestEffectiveMoshAttach(t *testing.T) {
	present := func(string) (string, error) { return "/opt/homebrew/bin/mosh", nil }
	absent := func(string) (string, error) { return "", exec.ErrNotFound }

	// ssh (default) never uses mosh and never warns, regardless of local client.
	if useMosh, warn := effectiveMoshAttach("ssh", present); useMosh || warn != "" {
		t.Errorf("ssh: got (useMosh=%v, warn=%q), want (false, \"\")", useMosh, warn)
	}

	// mosh selected + client present → use mosh, no warning.
	if useMosh, warn := effectiveMoshAttach("mosh", present); !useMosh || warn != "" {
		t.Errorf("mosh+present: got (useMosh=%v, warn=%q), want (true, \"\")", useMosh, warn)
	}

	// mosh selected + client absent → fall back to ssh WITH a warning.
	useMosh, warn := effectiveMoshAttach("mosh", absent)
	if useMosh {
		t.Errorf("mosh+absent: useMosh = true, want false (fall back to ssh)")
	}
	if !strings.Contains(warn, "mosh") || warn == "" {
		t.Errorf("mosh+absent: want a non-empty fallback warning mentioning mosh, got %q", warn)
	}
}
