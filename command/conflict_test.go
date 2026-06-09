package command

import (
	"errors"
	"os"
	"testing"

	"github.com/DigiBugCat/duck/internal/actions"
	"github.com/DigiBugCat/duck/internal/flow"
)

// stubRunner records every RunWithOverride call and returns a canned error per
// call, so runWithHubConflict can be driven without real ssh/mutagen/flow.
type stubRunner struct {
	overrides []flow.Override // every override it was called with, in order
	errs      []error         // per-call return; out-of-range index returns nil
}

func (s *stubRunner) RunWithOverride(_ string, override flow.Override) error {
	s.overrides = append(s.overrides, override)
	i := len(s.overrides) - 1
	if i < len(s.errs) {
		return s.errs[i]
	}
	return nil
}

// withDevNullStdin points os.Stdin at /dev/null for the duration of fn. That is
// not a TTY (so isInteractive() is false) and, even if read, yields an immediate
// EOF — so a test can never hang on stdin regardless of the ambient harness,
// mirroring TestAskSyncNoTTYReturnsNo.
func withDevNullStdin(t *testing.T, fn func()) {
	t.Helper()
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer f.Close()
	old := os.Stdin
	os.Stdin = f
	defer func() { os.Stdin = old }()
	fn()
}

// TestRunWithHubConflictNonInteractivePassesThrough is the load-bearing
// non-interactive safety test (invariant b): bare `duck` whose flow returns
// actions.ErrHubNonEmpty in a non-TTY context must return that error UNCHANGED,
// call the flow exactly ONCE (no re-run), and never read stdin. The test
// completing at all proves it does not hang.
func TestRunWithHubConflictNonInteractivePassesThrough(t *testing.T) {
	withDevNullStdin(t, func() {
		conflict := actions.ErrHubNonEmpty{Path: "~/notes/vault"}
		r := &stubRunner{errs: []error{conflict}}

		err := runWithHubConflict(r, "/home/me/notes/vault", flow.OverrideNone)

		var e actions.ErrHubNonEmpty
		if !errors.As(err, &e) {
			t.Fatalf("non-interactive conflict: error = %#v, want ErrHubNonEmpty unchanged", err)
		}
		if e.Path != conflict.Path {
			t.Fatalf("ErrHubNonEmpty.Path = %q, want %q", e.Path, conflict.Path)
		}
		if len(r.overrides) != 1 {
			t.Fatalf("flow must run exactly once in a non-interactive conflict, ran %d times: %v", len(r.overrides), r.overrides)
		}
		if r.overrides[0] != flow.OverrideNone {
			t.Fatalf("first run override = %v, want OverrideNone", r.overrides[0])
		}
	})
}

// TestRunWithHubConflictNoErrorPassesThrough: a clean run (no conflict) returns
// nil and runs the flow exactly once with the caller's override — the helper is
// transparent when there is nothing to resolve.
func TestRunWithHubConflictNoErrorPassesThrough(t *testing.T) {
	withDevNullStdin(t, func() {
		r := &stubRunner{} // no errors → RunWithOverride returns nil
		if err := runWithHubConflict(r, "/home/me/dev/foo", flow.OverrideSync); err != nil {
			t.Fatalf("clean run: unexpected error %v", err)
		}
		if len(r.overrides) != 1 || r.overrides[0] != flow.OverrideSync {
			t.Fatalf("clean run should call the flow once with the caller's override, got %v", r.overrides)
		}
	})
}

// TestConflictOverride pins the direction-prompt answer→override mapping for the
// [p]ush / [u]ll / [m]erge / [n]o prompt: p → OverridePush (local clobbers hub),
// u → OverridePull (hub clobbers local), m and the blank-Enter default →
// OverrideSync (newest-wins merge), n and gibberish → OverrideNoSyncOnce (open in
// the hub's copy, no sync, do NOT persist "never"). The load-bearing default: a
// blank Enter MUST be the non-destructive merge, and declining a ONE-TIME no-sync.
func TestConflictOverride(t *testing.T) {
	cases := []struct {
		in   string
		want flow.Override
	}{
		{"p", flow.OverridePush},
		{"push", flow.OverridePush},
		{" P \n", flow.OverridePush},
		{"u", flow.OverridePull},
		{"pull", flow.OverridePull},
		{"m", flow.OverrideSync},
		{"merge", flow.OverrideSync},
		{"", flow.OverrideSync},
		{"\n", flow.OverrideSync},
		{"  ", flow.OverrideSync},
		{"n", flow.OverrideNoSyncOnce},
		{"no", flow.OverrideNoSyncOnce},
		{"garbage", flow.OverrideNoSyncOnce},
	}
	for _, tc := range cases {
		if got := conflictOverride(tc.in); got != tc.want {
			t.Errorf("conflictOverride(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestRunWithHubConflictUnrelatedErrorPassesThrough: a non-conflict error is
// returned unchanged and never triggers a re-run, even in a non-interactive
// context (it must not be swallowed by the conflict path).
func TestRunWithHubConflictUnrelatedErrorPassesThrough(t *testing.T) {
	withDevNullStdin(t, func() {
		boom := errors.New("ssh blew up")
		r := &stubRunner{errs: []error{boom}}
		err := runWithHubConflict(r, "/home/me/dev/foo", flow.OverrideNone)
		if !errors.Is(err, boom) {
			t.Fatalf("unrelated error should pass through unchanged, got %v", err)
		}
		if len(r.overrides) != 1 {
			t.Fatalf("unrelated error must not re-run the flow, ran %d times", len(r.overrides))
		}
	})
}
