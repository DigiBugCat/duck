// hubsyncer_test.go covers realSyncer's HUB-OWNED mode: the ledger fetched
// over SSH (`duck hubsync list`) is parsed into laptop perspective and drives
// IsSynced/coverage, scoped to THIS machine only.
package flow

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/DigiBugCat/duck/internal/hub"
)

// withHubLedger swaps the hub SSH seam to answer every remote command with the
// given `duck hubsync list` output.
func withHubLedger(t *testing.T, out string) {
	t.Helper()
	restore := hub.SetRunner(func(argv []string, stdin io.Reader) (string, error) {
		return out, nil
	})
	t.Cleanup(restore)
}

func hubSyncerForTest() realSyncer {
	return newRealSyncer("andrew@pelican", "andrew@laptop", nilProgress{})
}

func TestHubOwnedIsSyncedCoversByPeerPath(t *testing.T) {
	home, _ := os.UserHomeDir()
	withHubLedger(t, strings.Join([]string{
		// this machine, covering parent dir
		"duck-duck-abc123-def456|Watching for changes|/data/dev|andrew@laptop:" + home + "/dev|spec1",
		// another machine's session for the exact queried dir — must NOT count
		"duck-duck-abc123-999999|Watching for changes|/data/dev/foo|andrew@otherbox:" + home + "/dev/foo|spec2",
	}, "\n"))

	s := hubSyncerForTest()
	synced, err := s.IsSynced("~/dev/foo")
	if err != nil {
		t.Fatal(err)
	}
	if !synced {
		t.Fatal("parent-dir session for this machine must cover ~/dev/foo")
	}

	synced, err = s.IsSynced("~/elsewhere")
	if err != nil {
		t.Fatal(err)
	}
	if synced {
		t.Fatal("~/elsewhere is not covered by any session")
	}
}

func TestHubOwnedIsSyncedIgnoresOtherMachines(t *testing.T) {
	home, _ := os.UserHomeDir()
	withHubLedger(t,
		"duck-duck-abc123-999999|Watching for changes|/data/proj|andrew@otherbox:"+home+"/proj|spec")

	synced, err := hubSyncerForTest().IsSynced("~/proj")
	if err != nil {
		t.Fatal(err)
	}
	if synced {
		t.Fatal("another machine's session must not make this machine count as synced")
	}
}

// TestHubLedgerIsMemoizedPerInvocation pins the latency fix: one bare `duck`
// consults the hub ledger 2–3 times (IsSynced for the target dir, the Claude
// co-sync, sometimes decideSync) and each fetch is an ssh roundtrip, so
// newRealSyncer memoizes it. Terminate (a ledger write) invalidates the memo
// so the next read refetches.
func TestHubLedgerIsMemoizedPerInvocation(t *testing.T) {
	home, _ := os.UserHomeDir()
	fetches := 0
	restore := hub.SetRunner(func(argv []string, stdin io.Reader) (string, error) {
		cmd := strings.Join(argv, " ")
		if strings.Contains(cmd, "hubsync list") {
			fetches++
		}
		return "duck-duck-x-y|Watching|/data/p|andrew@laptop:" + home + "/p|spec", nil
	})
	t.Cleanup(restore)

	s := hubSyncerForTest()
	for i := 0; i < 3; i++ {
		if _, err := s.IsSynced("~/p"); err != nil {
			t.Fatal(err)
		}
	}
	if fetches != 1 {
		t.Fatalf("3 IsSynced calls must fetch the ledger ONCE (memoized), got %d fetches", fetches)
	}
	// A ledger write invalidates: the next read refetches.
	if err := s.Terminate("duck-duck-x-y"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.IsSynced("~/p"); err != nil {
		t.Fatal(err)
	}
	if fetches != 2 {
		t.Fatalf("a Terminate must invalidate the memo (want 2 fetches), got %d", fetches)
	}
}

func TestHubLedgerSkipsMalformedAndLocalBetaLines(t *testing.T) {
	home, _ := os.UserHomeDir()
	withHubLedger(t, strings.Join([]string{
		"",
		"garbage line",
		"name|status|/a|/local/beta/no/colon|spec", // local beta — not hub-owned
		"duck-duck-x-y|Watching|/data/p|andrew@laptop:" + home + "/p|spec",
	}, "\n"))

	sessions, err := hubSyncerForTest().hubLedger()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("want 1 parsed session, got %d: %+v", len(sessions), sessions)
	}
	if sessions[0].Alpha.Path != home+"/p" {
		t.Fatalf("ledger must map the PEER path into Alpha (laptop perspective), got %q", sessions[0].Alpha.Path)
	}
	if sessions[0].Name != "duck-duck-x-y" {
		t.Fatalf("unexpected name %q", sessions[0].Name)
	}
}
