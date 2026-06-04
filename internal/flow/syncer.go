// realSyncer is the production Syncer: it bridges the bare-`duck` flow to the
// M1 sync engine (actions + mutagen). It is kept out of the flow unit tests
// (which inject a fake Syncer) because it shells out to mutagen; its logic is
// thin glue over the already-tested M1 layer.
package flow

import (
	"fmt"
	"strings"
	"time"

	"github.com/DigiBugCat/duck/internal/actions"
	"github.com/DigiBugCat/duck/internal/hub"
	"github.com/DigiBugCat/duck/internal/mutagen"
	"github.com/DigiBugCat/duck/internal/paths"
	"github.com/DigiBugCat/duck/internal/reconcile"
)

// defaultBundle is the bundle bare-`duck` auto-adds cwd to when it is not yet
// synced. A single implicit bundle keeps the bare flow zero-config; explicit
// multi-machine bundles are still managed via `duck sync`.
const defaultBundle = "duck"

// realSyncer drives the M1 sync engine for hub addr. The monitor/poll/failsafe
// fields are the test seam for waitSteady: production wires mutagen.Monitor, a
// 500ms poll, and a generous 30-minute failsafe (newRealSyncer); the unit test
// constructs a realSyncer directly with a fake monitor + tiny poll/failsafe so a
// >60s sync is proven to complete (no old 60s cap) without wall-clock waiting.
// progress is the visible sync-wait reporter (no-op by default).
type realSyncer struct {
	addr     string
	progress Progress
	monitor  func(name string) (mutagen.Session, error)
	poll     time.Duration
	failsafe time.Duration
}

// failsafeTimeout is the GENEROUS last-resort bound on the initial sync wait. It
// replaces the old 60s cap, which could cut off a large first sync: a real sync
// reaches steady well within it, so it only ever fires if mutagen wedges.
const failsafeTimeout = 30 * time.Minute

// newRealSyncer builds the production realSyncer for hub addr with the live
// mutagen.Monitor seam, a 500ms poll, and the 30-minute failsafe. progress is
// the visible sync-wait reporter (caller passes the no-op when none is wired).
func newRealSyncer(addr string, progress Progress) realSyncer {
	if progress == nil {
		progress = nilProgress{}
	}
	return realSyncer{
		addr:     addr,
		progress: progress,
		monitor:  mutagen.Monitor,
		poll:     500 * time.Millisecond,
		failsafe: failsafeTimeout,
	}
}

// IsSynced reports whether tildeDir already has a running mutagen session whose
// alpha endpoint is the local expansion of tildeDir. It matches on the synced
// path so an existing `duck sync` session (any bundle) counts as synced.
func (s realSyncer) IsSynced(tildeDir string) (bool, error) {
	local, err := paths.Expand(tildeDir)
	if err != nil {
		return false, err
	}
	sessions, err := mutagen.List()
	if err != nil {
		return false, err
	}
	for _, ms := range sessions {
		if ms.Alpha.Path == local {
			return true, nil
		}
	}
	return false, nil
}

// Reconcile runs the per-file NEWEST-WINS rsync seed (two `rsync -au` passes,
// PUSH then PULL) between this machine and the hub for tildeDir. It is called by
// Flow.EnsureSynced on the force/merge path BEFORE AddAndWait, so that the
// force-add's mutagen two-way-resolved session finds both sides already
// identical and has no conflicts to resolve. A failure here aborts the merge
// (the caller does NOT proceed to AddAndWait).
func (s realSyncer) Reconcile(tildeDir string) (err error) {
	// Make the rsync seed VISIBLE: Start the spinner (idempotent — if AddAndWait
	// later runs on the same realSyncer it shares this one line) and emit a
	// status. We do NOT Stop on success: on the force/merge path waitSteady owns
	// the final ✓ so reconcile→add reads as one continuous line. But on a seed
	// FAILURE (caller aborts before AddAndWait/waitSteady) we MUST clear the line
	// here, else Execute's "error:" note appends to a dangling spinner line. Stop
	// is safe-once, so this never double-fires. We also do NOT touch
	// reconcile.ReconcileNewest's rsync argv — the status is emitted here only, so
	// the safety-critical rsync commands stay byte-identical.
	s.progress.Start("syncing", tildeDir)
	s.progress.Update("reconciling (newest version of each file wins)")
	defer func() {
		if err != nil {
			s.progress.Stop(false)
		}
	}()
	return reconcile.ReconcileNewest(s.addr, tildeDir)
}

// AddAndWait ensures the default bundle exists, adds tildeDir to it (which
// auto-starts mutagen via actions.AddPath), then flushes the session and waits
// for it to reach a steady state so the files exist on the hub before a tmux
// session opens in the dir (DESIGN risk #2). force is passed to actions.AddPath:
// when false a non-empty hub dir returns actions.ErrHubNonEmpty (the caller
// resolves it); when true the path merges into it. On the force/merge path the
// caller (Flow.EnsureSynced) has already run Reconcile, so both sides are
// identical and the mutagen session has no conflicts — force=true is safe and
// means "the newest copy of each file already won."
func (s realSyncer) AddAndWait(tildeDir string, force bool) (err error) {
	// On the force/merge path Reconcile has already started the spinner line; a
	// pre-waitSteady failure here (bundle/AddPath) must clear it so the "error:"
	// note doesn't append to a dangling line. Stop is safe-once, and on the
	// non-force path no line is active so this is a no-op.
	defer func() {
		if err != nil {
			s.progress.Stop(false)
		}
	}()
	h := hub.New(s.addr)
	exists, err := h.BundleExists(defaultBundle)
	if err != nil {
		return err
	}
	if !exists {
		if err := actions.NewBundle(s.addr, defaultBundle); err != nil {
			return err
		}
	}
	_, sessionName, err := actions.AddPath(s.addr, defaultBundle, tildeDir, force)
	if err != nil {
		return err
	}
	return s.waitSteady(sessionName, tildeDir)
}

// waitSteady flushes the session then polls until it reports a watching/idle
// status, driving the visible progress spinner the whole time. There is NO 60s
// cap (a large first sync could be cut off): it polls every s.poll, calls
// progress.Update(status) on each poll so the live mutagen phase is shown, and
// only the GENEROUS s.failsafe (30 min in production) bounds it as a last
// resort. Start is idempotent (a force/merge Reconcile may already have begun
// the line); Stop(true) fires on steady, Stop(false) on the failsafe / a
// monitor error — so the line is always cleared before the caller hands the
// terminal to the picker/attach. A flush forces an immediate sync cycle.
func (s realSyncer) waitSteady(sessionName, tildeDir string) (err error) {
	s.progress.Start("syncing", tildeDir)
	// Always clear the line before returning: Stop(true) on success, Stop(false)
	// on any error (flush failure, monitor error, failsafe).
	defer func() { s.progress.Stop(err == nil) }()

	if ferr := mutagen.Flush(sessionName); ferr != nil {
		return fmt.Errorf("flushing initial sync: %w", ferr)
	}
	deadline := time.Now().Add(s.failsafe)
	for {
		st, merr := s.monitor(sessionName)
		if merr != nil {
			return merr
		}
		s.progress.Update(st.Status)
		if isSteady(st.Status) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("initial sync did not reach steady state within %s (status: %s)", s.failsafe, st.Status)
		}
		time.Sleep(s.poll)
	}
}

// isSteady reports whether a mutagen status string means the session has
// finished its initial reconciliation and is watching for changes. It uses a
// case-insensitive substring match on "watching"/"idle" so a reworded or
// decorated status (e.g. "Watching for changes (…)") still counts as steady and
// does not burn the 30-min failsafe on a HEALTHY sync.
func isSteady(status string) bool {
	s := strings.ToLower(status)
	return strings.Contains(s, "watching") || strings.Contains(s, "idle")
}
