package flow

import (
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/DigiBugCat/duck/internal/hub"
	"github.com/DigiBugCat/duck/internal/mutagen"
)

// recProgress records the Progress lifecycle so the tests can assert Start /
// per-poll Update / Stop without a real terminal.
type recProgress struct {
	started  int
	updates  []string
	stops    []bool
	startDir string
}

func (p *recProgress) Start(_, target string) { p.started++; p.startDir = target }
func (p *recProgress) Update(status string)   { p.updates = append(p.updates, status) }
func (p *recProgress) Stop(ok bool)           { p.stops = append(p.stops, ok) }

// stubFlush makes mutagen.Flush a no-op (its runVar path) so waitSteady's flush
// succeeds without a real mutagen binary. Returns a restore func.
func stubFlush(t *testing.T) {
	t.Helper()
	restore := mutagen.SetRunner(func(_ ...string) error { return nil })
	t.Cleanup(restore)
}

// TestWaitSteadyCompletesPastOld60sCap proves the old 60s cap is gone: the fake
// monitor reports a non-steady phase 150 times (>120, i.e. >60s at the real
// 500ms poll) before going steady, and waitSteady still completes. The poll is
// injected ~0 so this runs instantly. Update is called on every poll with the
// live status, and Stop(true) fires exactly once.
func TestWaitSteadyCompletesPastOld60sCap(t *testing.T) {
	stubFlush(t)
	const nonSteady = 150
	calls := 0
	prog := &recProgress{}
	s := realSyncer{
		progress: prog,
		monitor: func(name string) (mutagen.Session, error) {
			calls++
			if calls <= nonSteady {
				return mutagen.Session{Name: name, Status: "Staging files on beta"}, nil
			}
			return mutagen.Session{Name: name, Status: "Watching for changes"}, nil
		},
		poll:     0,
		failsafe: 30 * time.Minute, // generous; never reached here.
	}

	if err := s.waitSteady("duck-sess", "~/dev/foo"); err != nil {
		t.Fatalf("waitSteady returned error after %d non-steady polls: %v", nonSteady, err)
	}
	if calls != nonSteady+1 {
		t.Fatalf("monitor calls = %d, want %d", calls, nonSteady+1)
	}
	if len(prog.updates) != nonSteady+1 {
		t.Fatalf("progress.Update calls = %d, want one per poll (%d)", len(prog.updates), nonSteady+1)
	}
	if prog.updates[0] != "Staging files on beta" {
		t.Fatalf("first Update status = %q, want live phase", prog.updates[0])
	}
	if prog.started != 1 || prog.startDir != "~/dev/foo" {
		t.Fatalf("Start = %d for %q, want 1 for ~/dev/foo", prog.started, prog.startDir)
	}
	if len(prog.stops) != 1 || prog.stops[0] != true {
		t.Fatalf("Stop = %v, want exactly [true]", prog.stops)
	}
}

// TestWaitSteadyFailsafe proves a never-steady session fails ONLY at the
// (tiny, injected) failsafe — not the old 60s cap — and Stop(false) fires so
// the line is cleared on failure.
func TestWaitSteadyFailsafe(t *testing.T) {
	stubFlush(t)
	prog := &recProgress{}
	s := realSyncer{
		progress: prog,
		monitor: func(name string) (mutagen.Session, error) {
			return mutagen.Session{Name: name, Status: "Scanning"}, nil
		},
		poll:     0,
		failsafe: 10 * time.Millisecond,
	}

	err := s.waitSteady("duck-sess", "~/dev/foo")
	if err == nil {
		t.Fatal("waitSteady should fail when never steady")
	}
	if !strings.Contains(err.Error(), "steady state") {
		t.Fatalf("error = %v, want a steady-state timeout", err)
	}
	if len(prog.stops) != 1 || prog.stops[0] != false {
		t.Fatalf("Stop = %v, want exactly [false] on failsafe", prog.stops)
	}
}

// TestAddAndWaitClearsLineOnPreWaitFailure proves a failure BEFORE waitSteady
// (here BundleExists errors) still clears the spinner line with Stop(false), so
// Execute's "error:" note never appends to a dangling spinner. Without the
// AddAndWait error-guard this Stop would never fire.
func TestAddAndWaitClearsLineOnPreWaitFailure(t *testing.T) {
	restore := hub.SetRunner(func(_ []string, _ io.Reader) (string, error) {
		return "", fmt.Errorf("hub unreachable")
	})
	defer restore()

	prog := &recProgress{}
	s := realSyncer{addr: "me@hub.local", progress: prog}
	// Simulate the force/merge path: Reconcile started the line, then AddAndWait
	// fails before waitSteady. Drive AddAndWait directly with a started line.
	prog.Start("syncing", "~/dev/foo")
	err := s.AddAndWait("~/dev/foo", true)
	if err == nil {
		t.Fatal("AddAndWait should fail when the hub is unreachable")
	}
	if len(prog.stops) != 1 || prog.stops[0] != false {
		t.Fatalf("Stop = %v, want exactly [false] clearing the line on pre-wait failure", prog.stops)
	}
}

// TestIsSteadyMatchesDecoratedStatuses pins that isSteady uses a case-
// insensitive SUBSTRING match, so a reworded/decorated mutagen status still
// reads as steady (otherwise waitSteady would burn the 30-min failsafe on a
// HEALTHY sync). Exact bare strings and non-steady statuses are also checked.
func TestIsSteadyMatchesDecoratedStatuses(t *testing.T) {
	steady := []string{
		"Watching for changes",
		"Watching for changes (1 file)",
		"watching",
		"Idle",
		"IDLE",
		"Now idle and watching",
	}
	for _, s := range steady {
		if !isSteady(s) {
			t.Errorf("isSteady(%q) = false, want true", s)
		}
	}
	notSteady := []string{"Scanning", "Reconciling", "Staging files", ""}
	for _, s := range notSteady {
		if isSteady(s) {
			t.Errorf("isSteady(%q) = true, want false", s)
		}
	}
}
