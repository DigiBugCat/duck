package command

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/DigiBugCat/duck/internal/mutagen"
)

// hubsyncListLine renders one session in the pipe-delimited shape mutagen's
// list template emits (11 fields including the trailing duck-spec label).
func hubsyncListLine(name, status, alphaPath, peerUser, peerHost, peerPath, spec string) string {
	return strings.Join([]string{
		name, status,
		"Local", "", "", alphaPath,
		"SSH", peerUser, peerHost, peerPath,
		spec,
	}, "|") + "\n"
}

// runHubsyncAdd sets the add flags, swaps the mutagen seams, runs the command,
// and returns its stdout plus every mutagen argv it issued.
func runHubsyncAdd(t *testing.T, listOut string) (string, [][]string) {
	t.Helper()
	var calls [][]string
	restoreRun := mutagen.SetRunner(func(args ...string) error {
		calls = append(calls, args)
		return nil
	})
	defer restoreRun()
	restoreOut := mutagen.SetOutputForTest(func(args ...string) (string, error) {
		calls = append(calls, args)
		return listOut, nil
	})
	defer restoreOut()

	var out bytes.Buffer
	hubsyncAddCmd.SetOut(&out)
	if err := hubsyncAddCmd.RunE(hubsyncAddCmd, nil); err != nil {
		t.Fatalf("hubsync add: %v", err)
	}
	return out.String(), calls
}

func setAddFlags(name, hubPath, peer, peerPath string, ignores []string) {
	hubsyncName, hubsyncHubPath, hubsyncPeer, hubsyncPeerPath, hubsyncIgnores =
		name, hubPath, peer, peerPath, ignores
}

func TestHubsyncAddCreatesWhenAbsent(t *testing.T) {
	setAddFlags("duck-x-abc", "/data/proj", "andrew@laptop", "/home/andrew/proj", nil)
	out, calls := runHubsyncAdd(t, "")

	if strings.TrimSpace(out) != "duck-x-abc" {
		t.Fatalf("want session name on stdout, got %q", out)
	}
	var created bool
	for _, c := range calls {
		if len(c) >= 2 && c[0] == "sync" && c[1] == "create" {
			created = true
			argv := strings.Join(c, " ")
			if !strings.Contains(argv, "--label "+mutagen.SpecLabelKey+"=") {
				t.Fatalf("create missing spec label: %v", c)
			}
			if !strings.Contains(argv, "andrew@laptop:/home/andrew/proj") {
				t.Fatalf("create missing peer beta endpoint: %v", c)
			}
		}
	}
	if !created {
		t.Fatal("expected a sync create call")
	}
}

func TestHubsyncAddIsNoOpWhenSpecMatches(t *testing.T) {
	setAddFlags("duck-x-abc", "/data/proj", "andrew@laptop", "/home/andrew/proj", nil)
	spec := mutagen.SpecFingerprint("/data/proj", "andrew@laptop:/home/andrew/proj", nil)
	_, calls := runHubsyncAdd(t,
		hubsyncListLine("duck-x-abc", "Watching for changes", "/data/proj", "andrew", "laptop", "/home/andrew/proj", spec))

	for _, c := range calls {
		if len(c) >= 2 && c[0] == "sync" && (c[1] == "create" || c[1] == "terminate") {
			t.Fatalf("matching spec must be a no-op, got: %v", c)
		}
	}
}

func TestHubsyncAddRecreatesOnStaleSpec(t *testing.T) {
	setAddFlags("duck-x-abc", "/data/proj", "andrew@laptop", "/home/andrew/proj", nil)
	// Same name, but a spec recorded under an older ignore list / peer.
	_, calls := runHubsyncAdd(t,
		hubsyncListLine("duck-x-abc", "Watching for changes", "/data/proj", "andrew", "laptop", "/home/andrew/proj", "stalestale00"))

	var terminated, created bool
	for _, c := range calls {
		if len(c) >= 2 && c[0] == "sync" && c[1] == "terminate" {
			terminated = true
			if created {
				t.Fatal("terminate must precede create")
			}
		}
		if len(c) >= 2 && c[0] == "sync" && c[1] == "create" {
			created = true
		}
	}
	if !terminated || !created {
		t.Fatalf("stale spec must terminate+recreate (terminated=%v created=%v)", terminated, created)
	}
}

func TestSpecFingerprintChangesWithIgnoresAndEndpoints(t *testing.T) {
	base := mutagen.SpecFingerprint("/a", "h:/b", nil)
	if base == mutagen.SpecFingerprint("/a", "h:/b", []string{"secrets.env"}) {
		t.Fatal("extra ignore must change the fingerprint")
	}
	if base == mutagen.SpecFingerprint("/a", "other:/b", nil) {
		t.Fatal("different beta must change the fingerprint")
	}
	if base != mutagen.SpecFingerprint("/a", "h:/b", nil) {
		t.Fatal("fingerprint must be deterministic")
	}
}

func TestHubsyncAddRequiresAllFlags(t *testing.T) {
	setAddFlags("", "", "", "", nil)
	if err := hubsyncAddCmd.RunE(hubsyncAddCmd, nil); err == nil ||
		!strings.Contains(fmt.Sprint(err), "requires") {
		t.Fatalf("want a missing-flags error, got %v", err)
	}
}
