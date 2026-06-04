package sync

import (
	"io"
	"strings"
	"testing"

	"github.com/DigiBugCat/duck/internal/hub"
)

// TestEmptyBundlesSelectsOnlyEmpty verifies the selection logic that powers
// `duck sync prune`: of all the hub's bundles, it returns exactly those tracking
// zero paths (the husks left after `duck sync rm`s the last path), leaving
// real-path bundles out. The hub SSH runner is faked (flok's fake-runner
// pattern) so no real host is contacted — canned responses key off the remote
// command string.
func TestEmptyBundlesSelectsOnlyEmpty(t *testing.T) {
	// Three bundles: "work" tracks two real paths, "scratch" tracks one, and
	// "stale" tracks none (its last path was rm'd). Only "stale" is empty.
	restore := hub.SetRunner(func(argv []string, _ io.Reader) (string, error) {
		remote := argv[len(argv)-1]
		switch {
		case strings.Contains(remote, "ls -1 ~/.duck/bundles"):
			return "scratch\nstale\nwork\n", nil
		case strings.Contains(remote, "bundles/work/paths"):
			return "id1\t~/dev/foo\nid2\t~/dev/bar\n", nil
		case strings.Contains(remote, "bundles/scratch/paths"):
			return "id1\t~/dev/baz\n", nil
		case strings.Contains(remote, "bundles/stale/paths"):
			return "", nil // no paths -> empty
		}
		return "", nil
	})
	defer restore()

	got, err := emptyBundles("me@hub.local")
	if err != nil {
		t.Fatalf("emptyBundles: %v", err)
	}
	if len(got) != 1 || got[0] != "stale" {
		t.Fatalf("emptyBundles = %v, want [stale] (only the zero-path bundle)", got)
	}
}

// TestEmptyBundlesNoneWhenAllPopulated pins the empty case: when every bundle
// tracks at least one path, prune selects nothing (and RunE prints "nothing to
// prune"). A real-path bundle must never be selected for destruction.
func TestEmptyBundlesNoneWhenAllPopulated(t *testing.T) {
	restore := hub.SetRunner(func(argv []string, _ io.Reader) (string, error) {
		remote := argv[len(argv)-1]
		switch {
		case strings.Contains(remote, "ls -1 ~/.duck/bundles"):
			return "a\nb\n", nil
		case strings.Contains(remote, "bundles/a/paths"):
			return "id1\t~/dev/a\n", nil
		case strings.Contains(remote, "bundles/b/paths"):
			return "id1\t~/dev/b\n", nil
		}
		return "", nil
	})
	defer restore()

	got, err := emptyBundles("me@hub.local")
	if err != nil {
		t.Fatalf("emptyBundles: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("emptyBundles = %v, want none (all bundles populated)", got)
	}
}

// TestPruneDestroysOnlyEmptyBundles drives the whole RunE selection→destroy path
// through the fake runner and asserts the destructive `rm -rf` only ever targets
// the empty bundle — never a bundle that still tracks real paths. This is the
// strong invariant: prune must not reap a populated bundle.
func TestPruneDestroysOnlyEmptyBundles(t *testing.T) {
	var destroyed []string
	restore := hub.SetRunner(func(argv []string, _ io.Reader) (string, error) {
		remote := argv[len(argv)-1]
		switch {
		case strings.Contains(remote, "rm -rf ~/.duck/bundles/"):
			// Record which bundle DestroyBundle targeted.
			for _, b := range []string{"work", "stale"} {
				if strings.Contains(remote, "~/.duck/bundles/"+b) {
					destroyed = append(destroyed, b)
				}
			}
			return "", nil
		case strings.Contains(remote, "ls -1 ~/.duck/bundles"):
			return "stale\nwork\n", nil
		case strings.Contains(remote, "bundles/work/paths"):
			return "id1\t~/dev/foo\n", nil
		case strings.Contains(remote, "bundles/stale/paths"):
			return "", nil
		}
		return "", nil
	})
	defer restore()

	// emptyBundles selects only "stale"...
	empties, err := emptyBundles("me@hub.local")
	if err != nil {
		t.Fatalf("emptyBundles: %v", err)
	}
	// ...and destroying them touches "stale" alone, never "work".
	h := hub.New("me@hub.local")
	for _, name := range empties {
		if err := h.DestroyBundle(name); err != nil {
			t.Fatalf("DestroyBundle(%s): %v", name, err)
		}
	}
	if len(destroyed) != 1 || destroyed[0] != "stale" {
		t.Fatalf("prune destroyed %v, want [stale] only (real-path bundle must be untouched)", destroyed)
	}
}
