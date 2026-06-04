package actions

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DigiBugCat/duck/internal/hub"
	"github.com/DigiBugCat/duck/internal/mutagen"
	"github.com/DigiBugCat/duck/internal/paths"
)

// TestAddPathRollsBackOnMutagenFailure asserts the M1 acceptance criterion:
// a forced mutagen.Create failure leaves NO orphan hub record — AddPath calls
// h.RemovePath (the `rm -f …/paths/<id>` rollback) and returns an error
// wrapping "starting mutagen session".
func TestAddPathRollsBackOnMutagenFailure(t *testing.T) {
	tmp := t.TempDir() // a real local dir so the os.Stat/IsDir check passes.

	// Command-aware hub fake: BundleExists must read "yes" (it checks for the
	// `test -d` probe); every other remote command (the mkdir&&cat record write
	// and the rollback rm) returns empty. No real ssh runs.
	var hubCmds []string
	restoreHub := hub.SetRunner(func(argv []string, _ io.Reader) (string, error) {
		remote := argv[len(argv)-1]
		hubCmds = append(hubCmds, remote)
		if strings.Contains(remote, "test -d") {
			return "yes\n", nil
		}
		return "", nil
	})
	defer restoreHub()

	// mutagen fake: fail ONLY on `sync create`, succeed on `daemon start` (which
	// Create invokes first), so the rollback fires for the right reason.
	restoreMut := mutagen.SetRunner(func(args ...string) error {
		if len(args) > 0 && args[0] == "sync" {
			return fmt.Errorf("simulated mutagen create failure")
		}
		return nil
	})
	defer restoreMut()

	// force=true skips the RemoteDirNonEmpty branch entirely.
	_, _, err := AddPath("me@hub.local", "b1", tmp, true)
	if err == nil {
		t.Fatalf("AddPath succeeded; want a mutagen-create failure")
	}
	if !strings.Contains(err.Error(), "starting mutagen session") {
		t.Errorf("error %q does not wrap \"starting mutagen session\"", err)
	}

	// The rollback must have removed the just-written hub record.
	var sawRollback bool
	for _, c := range hubCmds {
		if strings.Contains(c, "rm -f ~/.duck/bundles/b1/paths/") {
			sawRollback = true
		}
	}
	if !sawRollback {
		t.Errorf("no rollback `rm -f` issued; hub would keep an orphan record\nhub commands: %v", hubCmds)
	}
}

// TestAddPathSuccessAutoStartsSessionAndReturnsName asserts the AC3 happy path:
// when mutagen.Create succeeds, AddPath auto-starts the session (issues a
// `sync create`) and returns the deterministic sessionName
// duck-<bundle>-<id> == paths.SessionName(bundle, tildePath). The previous test
// only covered the FAILURE/rollback path.
func TestAddPathSuccessAutoStartsSessionAndReturnsName(t *testing.T) {
	tmp := t.TempDir() // a real local dir so the os.Stat/IsDir check passes.

	// Command-aware hub fake: BundleExists reads "yes"; the record write returns
	// empty. No rollback should fire on the success path.
	var hubCmds []string
	restoreHub := hub.SetRunner(func(argv []string, _ io.Reader) (string, error) {
		remote := argv[len(argv)-1]
		hubCmds = append(hubCmds, remote)
		if strings.Contains(remote, "test -d") {
			return "yes\n", nil
		}
		return "", nil
	})
	defer restoreHub()

	// mutagen fake: succeed on every call, recording the argv so we can assert a
	// `sync create` was issued (daemon start precedes it).
	var mutCalls [][]string
	restoreMut := mutagen.SetRunner(func(args ...string) error {
		mutCalls = append(mutCalls, append([]string(nil), args...))
		return nil
	})
	defer restoreMut()

	entry, sessionName, err := AddPath("me@hub.local", "b1", tmp, true)
	if err != nil {
		t.Fatalf("AddPath: %v", err)
	}

	// t.TempDir is NOT under $HOME, so Contract leaves it absolute; derive the
	// expected name through the same helpers rather than a hardcoded literal.
	tilde := paths.Contract(tmp)
	want := paths.SessionName("b1", tilde)
	if sessionName != want {
		t.Errorf("sessionName = %q, want %q", sessionName, want)
	}
	if entry.TildePath != tilde {
		t.Errorf("entry.TildePath = %q, want %q", entry.TildePath, tilde)
	}
	if entry.ID != paths.ID(tilde) {
		t.Errorf("entry.ID = %q, want %q", entry.ID, paths.ID(tilde))
	}

	// A `sync create` must have been issued (the auto-start).
	var sawCreate bool
	for _, c := range mutCalls {
		if len(c) >= 2 && c[0] == "sync" && c[1] == "create" {
			sawCreate = true
		}
	}
	if !sawCreate {
		t.Errorf("no `sync create` issued; session was not auto-started\nmutagen calls: %v", mutCalls)
	}

	// No rollback on the success path.
	for _, c := range hubCmds {
		if strings.HasPrefix(c, "rm -f ~/.duck/bundles/") {
			t.Errorf("unexpected rollback `rm -f` on the success path: %q", c)
		}
	}
}

// TestSyncPathReturnsAlreadyWhenSessionExists asserts the AC5 idempotency
// half: when mutagen reports the session already present, SyncPath returns
// SyncAlready and issues NO `sync create`. SyncPath never contacts the hub, so
// only the mutagen seams are needed.
func TestSyncPathReturnsAlreadyWhenSessionExists(t *testing.T) {
	dir := t.TempDir() // empty -> passes IsEmptyDir, reaching the Exists check.
	tilde := paths.Contract(dir)
	sn := paths.SessionName("b1", tilde)

	// Exists()/all() read through outputVar (`sync list`): return one
	// pipe-delimited line (>=10 fields) whose field[0] is our session name so
	// mutagen.Exists reports it present.
	restoreOut := mutagen.SetOutputForTest(func(_ ...string) (string, error) {
		return sn + "|Watching|Local|||/x|SSH||h|dev/x\n", nil
	})
	defer restoreOut()

	// Record any run so we can assert NO `sync create` fired.
	var mutCalls [][]string
	restoreRun := mutagen.SetRunner(func(args ...string) error {
		mutCalls = append(mutCalls, append([]string(nil), args...))
		return nil
	})
	defer restoreRun()

	st, err := SyncPath("me@hub.local", "b1", hub.PathEntry{ID: paths.ID(tilde), TildePath: tilde}, false)
	if err != nil {
		t.Fatalf("SyncPath: %v", err)
	}
	if st != SyncAlready {
		t.Errorf("SyncPath status = %v, want SyncAlready (the session already exists)", st)
	}
	for _, c := range mutCalls {
		if len(c) >= 2 && c[0] == "sync" && c[1] == "create" {
			t.Errorf("SyncPath issued a `sync create` for an already-existing session: %v", c)
		}
	}
}

// TestSyncPathRefusesNonEmptyLocalWithoutForce asserts the AC5 refusal half:
// a non-empty local target with force=false yields ErrLocalNonEmpty BEFORE any
// mutagen/hub call. errors.As must match the typed error so callers can offer a
// force-overlay retry.
func TestSyncPathRefusesNonEmptyLocalWithoutForce(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "occupied"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seeding non-empty dir: %v", err)
	}
	tilde := paths.Contract(dir)

	// Guard: if SyncPath reaches mutagen at all, that's a bug (it must refuse
	// before touching it). Either seam firing is a failure.
	restoreOut := mutagen.SetOutputForTest(func(args ...string) (string, error) {
		t.Errorf("SyncPath reached mutagen output seam (args=%v); want refusal first", args)
		return "", nil
	})
	defer restoreOut()
	restoreRun := mutagen.SetRunner(func(args ...string) error {
		t.Errorf("SyncPath reached mutagen run seam (args=%v); want refusal first", args)
		return nil
	})
	defer restoreRun()

	_, err := SyncPath("me@hub.local", "b1", hub.PathEntry{ID: paths.ID(tilde), TildePath: tilde}, false)
	if err == nil {
		t.Fatalf("SyncPath succeeded on a non-empty local dir with force=false; want ErrLocalNonEmpty")
	}
	var e ErrLocalNonEmpty
	if !errors.As(err, &e) {
		t.Fatalf("error %#v is not ErrLocalNonEmpty", err)
	}
	if e.Local != dir {
		t.Errorf("ErrLocalNonEmpty.Local = %q, want %q", e.Local, dir)
	}
}

// TestNewBundleStartsNoMutagen asserts AC2: `duck sync new` creates the bundle
// via the hub only and never reaches the mutagen package. Both mutagen seams
// are recorders: Create/Terminate go through runVar, but Exists/List go through
// outputVar, so a runVar-only recorder would both miss an Exists call AND let it
// hit the real mutagen binary. Both must record zero calls.
func TestNewBundleStartsNoMutagen(t *testing.T) {
	// Hub fake: BundleExists must read "no" so CreateBundle proceeds to mkdir
	// (a "yes" would short-circuit with an "already exists" error — wrong reason).
	var hubCmds []string
	restoreHub := hub.SetRunner(func(argv []string, _ io.Reader) (string, error) {
		remote := argv[len(argv)-1]
		hubCmds = append(hubCmds, remote)
		if strings.Contains(remote, "test -d") {
			return "no\n", nil
		}
		return "", nil
	})
	defer restoreHub()

	var mutRun, mutOut [][]string
	restoreRun := mutagen.SetRunner(func(args ...string) error {
		mutRun = append(mutRun, append([]string(nil), args...))
		return nil
	})
	defer restoreRun()
	restoreOut := mutagen.SetOutputForTest(func(args ...string) (string, error) {
		mutOut = append(mutOut, append([]string(nil), args...))
		return "", nil
	})
	defer restoreOut()

	if err := NewBundle("me@hub.local", "b1"); err != nil {
		t.Fatalf("NewBundle: %v", err)
	}

	if len(mutRun) != 0 {
		t.Errorf("NewBundle made %d mutagen run calls; want 0 (no mutagen on the new path): %v", len(mutRun), mutRun)
	}
	if len(mutOut) != 0 {
		t.Errorf("NewBundle made %d mutagen output calls; want 0 (no mutagen on the new path): %v", len(mutOut), mutOut)
	}

	// Sanity: the hub WAS asked to create the bundle (so the test exercises the
	// real path, not a no-op).
	var sawMkdir bool
	for _, c := range hubCmds {
		if strings.Contains(c, "mkdir -p ~/.duck/bundles/b1") {
			sawMkdir = true
		}
	}
	if !sawMkdir {
		t.Errorf("NewBundle did not issue the bundle mkdir; hub commands: %v", hubCmds)
	}
}
