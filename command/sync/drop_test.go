package sync

import (
	"io"
	"strings"
	"testing"

	"github.com/DigiBugCat/duck/internal/hub"
)

// TestBundlesTrackingMatchesDir verifies the dir→bundle resolution that powers
// `duck sync drop <dir>` (PLAN M5, the prune-a-mirror story): given a tilde-form
// dir, it returns every bundle whose tracked paths include it. The hub SSH
// runner is faked (flok's fake-runner pattern) so no real host is contacted —
// we key the canned responses off the remote command string.
func TestBundlesTrackingMatchesDir(t *testing.T) {
	// Two bundles: "work" tracks ~/dev/foo and ~/dev/bar; "scratch" tracks
	// ~/dev/foo too. So ~/dev/foo lives in both, ~/dev/bar only in "work".
	restore := hub.SetRunner(func(argv []string, _ io.Reader) (string, error) {
		remote := argv[len(argv)-1]
		switch {
		case strings.Contains(remote, "ls -1 ~/.duck/bundles"):
			return "scratch\nwork\n", nil
		case strings.Contains(remote, "bundles/work/paths"):
			return "id1\t~/dev/foo\nid2\t~/dev/bar\n", nil
		case strings.Contains(remote, "bundles/scratch/paths"):
			return "id1\t~/dev/foo\n", nil
		}
		return "", nil
	})
	defer restore()

	got, err := bundlesTracking("me@hub.local", "~/dev/foo")
	if err != nil {
		t.Fatalf("bundlesTracking: %v", err)
	}
	want := map[string]bool{"work": true, "scratch": true}
	if len(got) != len(want) {
		t.Fatalf("bundlesTracking(~/dev/foo) = %v, want both work and scratch", got)
	}
	for _, b := range got {
		if !want[b] {
			t.Errorf("unexpected bundle %q tracking ~/dev/foo", b)
		}
	}

	// A dir tracked by exactly one bundle resolves to just that one.
	got, err = bundlesTracking("me@hub.local", "~/dev/bar")
	if err != nil {
		t.Fatalf("bundlesTracking(bar): %v", err)
	}
	if len(got) != 1 || got[0] != "work" {
		t.Errorf("bundlesTracking(~/dev/bar) = %v, want [work]", got)
	}

	// A dir no bundle tracks resolves to none (drop then errors "nothing to drop").
	got, err = bundlesTracking("me@hub.local", "~/dev/nope")
	if err != nil {
		t.Fatalf("bundlesTracking(nope): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("bundlesTracking(~/dev/nope) = %v, want none", got)
	}
}
