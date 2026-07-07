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
	"github.com/DigiBugCat/duck/internal/folder"
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
	addr string
	// machineAddr, when non-empty, switches this syncer to HUB-OWNED mode: the
	// hub's mutagen daemon owns the sessions (created/inspected via `duck
	// hubsync ...` over SSH) and machineAddr is how the hub dials back to this
	// machine. Empty keeps the classic laptop-owned mode.
	machineAddr string
	progress    Progress
	monitor     func(name string) (mutagen.Session, error)
	flush       func(name string) error
	poll        time.Duration
	failsafe    time.Duration
}

// hubOwned reports whether this syncer runs in hub-owned mode.
func (s realSyncer) hubOwned() bool { return s.machineAddr != "" }

// failsafeTimeout is the GENEROUS last-resort bound on the initial sync wait. It
// replaces the old 60s cap, which could cut off a large first sync: a real sync
// reaches steady well within it, so it only ever fires if mutagen wedges.
const failsafeTimeout = 30 * time.Minute

// newRealSyncer builds the production realSyncer for hub addr with the live
// mutagen.Monitor seam, a 500ms poll, and the 30-minute failsafe. progress is
// the visible sync-wait reporter (caller passes the no-op when none is wired).
func newRealSyncer(addr, machineAddr string, progress Progress) realSyncer {
	if progress == nil {
		progress = nilProgress{}
	}
	s := realSyncer{
		addr:        addr,
		machineAddr: machineAddr,
		progress:    progress,
		monitor:     mutagen.Monitor,
		flush:       mutagen.Flush,
		poll:        500 * time.Millisecond,
		failsafe:    failsafeTimeout,
	}
	if s.hubOwned() {
		// The sessions live on the hub's daemon, so status/flush are remote:
		// `duck hubsync ...` over SSH (multiplexed via the control master, so a
		// 1s poll is one cheap channel open, not a new connection).
		h := hub.New(addr)
		s.monitor = func(name string) (mutagen.Session, error) {
			out, err := h.Run("duck hubsync status --name " + paths.Quote(name))
			if err != nil {
				return mutagen.Session{}, err
			}
			return mutagen.Session{Name: name, Status: strings.TrimSpace(out)}, nil
		}
		s.flush = func(name string) error {
			_, err := h.Run("duck hubsync flush --name " + paths.Quote(name))
			return err
		}
		s.poll = time.Second
	}
	return s
}

// hubLedger lists the hub-owned sessions that belong to THIS machine in
// laptop perspective (see actions.HubOwnedSessions, the shared fetcher behind
// this and the `duck sync` commands).
func (s realSyncer) hubLedger() ([]mutagen.Session, error) {
	return actions.HubOwnedSessions(s.addr, s.machineAddr)
}

// IsSynced reports whether tildeDir is already covered by a running mutagen
// session TO THE CURRENT HUB — either because a session's alpha endpoint IS the
// local expansion of tildeDir, or because it is an ANCESTOR of it. A session
// syncing ~/dev already keeps ~/dev/foo current, so adding a second session for
// the child would be redundant (and would have mutagen syncing the same files
// twice): when a parent is synced, no action is needed. It matches across
// bundles so an existing `duck sync` session counts.
//
// The beta endpoint MUST match the current hub (s.addr): a session left pointing
// at a PREVIOUS hub after a migration does not keep this hub current, so it is
// NOT treated as synced — that is what makes duck re-mirror the folder to the new
// hub (via AddAndWait's retireForeignHub) instead of silently opening a session
// in a dir the new hub never received.
func (s realSyncer) IsSynced(tildeDir string) (bool, error) {
	local, err := paths.Expand(tildeDir)
	if err != nil {
		return false, err
	}
	if s.hubOwned() {
		// The ledger is already scoped to this machine and mapped to laptop
		// perspective, and hub-owned sessions live ON the current hub by
		// construction — coverage is the only check left.
		sessions, err := s.hubLedger()
		if err != nil {
			return false, err
		}
		for _, ms := range sessions {
			if pathCoveredBy(local, ms.Alpha.Path) {
				return true, nil
			}
		}
		return false, nil
	}
	sessions, err := mutagen.List()
	if err != nil {
		return false, err
	}
	for _, ms := range sessions {
		if pathCoveredBy(local, ms.Alpha.Path) && ms.Beta.MatchesHub(s.addr) {
			return true, nil
		}
	}
	return false, nil
}

// retireForeignHub terminates any running mutagen session whose alpha IS the
// local expansion of tildeDir but whose beta points at a DIFFERENT hub than the
// current one (s.addr) — a leftover from a previous hub. It runs just before
// AddAndWait recreates the session against the current hub: without it the stale
// session (same deterministic name, wrong hub) would collide with the new one.
// This is how a half-finished hub migration self-heals — the anchor-provided hub
// address is the single source of truth, and a folder still pointed at the old
// hub is re-mirrored to the new one the next time it is ducked into.
func (s realSyncer) retireForeignHub(tildeDir string) error {
	local, err := paths.Expand(tildeDir)
	if err != nil {
		return err
	}
	sessions, err := mutagen.List()
	if err != nil {
		return err
	}
	for _, ms := range sessions {
		if ms.Alpha.Path == local && !ms.Beta.MatchesHub(s.addr) {
			if err := mutagen.Terminate(ms.Name); err != nil {
				return fmt.Errorf("retiring stale sync for %s (pointed at %s): %w", tildeDir, ms.Beta.Display(), err)
			}
		}
	}
	return nil
}

// retireLocalSessions terminates every LOCAL-daemon duck session whose alpha
// is (or covers) the local expansion of tildeDir, regardless of which hub its
// beta points at. Hub-owned mode's dual-daemon guard: once the hub's daemon
// owns a directory, no local session may keep syncing it.
func (s realSyncer) retireLocalSessions(tildeDir string) error {
	local, err := paths.Expand(tildeDir)
	if err != nil {
		return err
	}
	sessions, err := mutagen.List()
	if err != nil {
		// No local daemon/mutagen at all is FINE in hub-owned mode — a thin
		// client without mutagen installed has nothing to retire.
		return nil
	}
	for _, ms := range sessions {
		if pathCoveredBy(local, ms.Alpha.Path) || pathCoveredBy(ms.Alpha.Path, local) {
			if err := mutagen.Terminate(ms.Name); err != nil {
				return fmt.Errorf("retiring laptop-owned sync %s (hub now owns %s): %w", ms.Name, tildeDir, err)
			}
		}
	}
	return nil
}

// pathCoveredBy reports whether dir is the same as, or nested under, ancestor —
// i.e. a mutagen session rooted at ancestor already keeps dir in sync. It is a
// path-segment comparison (not a raw string prefix) so "/a/foobar" is NOT treated
// as covered by "/a/foo".
func pathCoveredBy(dir, ancestor string) bool {
	if ancestor == dir {
		return true
	}
	return strings.HasPrefix(dir, ancestor+"/")
}

// CheckContainment lists the active duck Mutagen sessions and classifies
// localAbs against their local (Alpha) paths via folder.CheckContainment. In
// hub-owned mode the ledger already presents sessions in laptop perspective
// (Alpha = this machine's path), so the same classification applies.
func (s realSyncer) CheckContainment(localAbs string) (folder.Containment, error) {
	sessions, err := s.listOwned()
	if err != nil {
		return folder.Containment{}, err
	}
	return folder.CheckContainment(localAbs, sessions), nil
}

// listOwned returns this machine's duck sessions from whichever daemon owns
// them: the hub ledger in hub-owned mode, the local daemon otherwise.
func (s realSyncer) listOwned() ([]mutagen.Session, error) {
	if s.hubOwned() {
		return s.hubLedger()
	}
	return mutagen.List()
}

// Terminate stops and removes a Mutagen session by name; a missing session is
// not an error. In hub-owned mode the session lives on the hub's daemon, so
// the terminate runs there.
func (s realSyncer) Terminate(sessionName string) error {
	if s.hubOwned() {
		_, err := hub.New(s.addr).Run("duck hubsync terminate --name " + paths.Quote(sessionName))
		return err
	}
	return mutagen.Terminate(sessionName)
}

// reconcileDir maps the flow-level Direction to the reconcile package's. They
// are distinct enums so flow does not leak the reconcile type through its Syncer
// seam; DirNone never reaches here (EnsureSynced only reconciles when set).
func reconcileDir(d Direction) reconcile.Direction {
	switch d {
	case DirPush:
		return reconcile.Push
	case DirPull:
		return reconcile.Pull
	default:
		return reconcile.Merge
	}
}

// Reconcile seeds this machine and the hub for tildeDir in the chosen direction
// (Push: local clobbers hub; Pull: hub clobbers local; Merge: newest-wins union)
// BEFORE AddAndWait, so the force-add's mutagen two-way-resolved session finds
// both sides already coherent and has no conflicts to resolve. A failure aborts
// (the caller does NOT proceed to AddAndWait).
func (s realSyncer) Reconcile(tildeDir string, dir Direction) (err error) {
	// Make the rsync seed VISIBLE: Start the spinner (idempotent — if AddAndWait
	// later runs on the same realSyncer it shares this one line) and emit a
	// status. We do NOT Stop on success: waitSteady owns the final ✓ so
	// reconcile→add reads as one continuous line. But on a seed FAILURE (caller
	// aborts before AddAndWait/waitSteady) we MUST clear the line here, else
	// Execute's "error:" note appends to a dangling spinner line. Stop is
	// safe-once, so this never double-fires.
	s.progress.Start("syncing", tildeDir)
	s.progress.Update("reconciling")
	defer func() {
		if err != nil {
			s.progress.Stop(false)
		}
	}()
	// Stream the rsync seed's live progress into the SAME spinner line so a big
	// first seed shows what is transferring. The callback runs on rsync's stdout
	// copier goroutine; ttyProgress.Update is mutex-guarded for exactly this.
	return reconcile.Reconcile(s.addr, tildeDir, reconcileDir(dir), func(line string) {
		s.progress.Update(line)
	})
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
	if s.hubOwned() {
		// Dual-daemon guard: a leftover LAPTOP-owned session for this dir (any
		// hub) must die before the hub-owned one starts, or two daemons sync
		// the same directory and conflict-ping-pong. This is also the per-dir
		// incremental migration: the first duck into a dir after enabling
		// hub-owned sync retires the old local session and hands ownership over.
		if err := s.retireLocalSessions(tildeDir); err != nil {
			return err
		}
	} else {
		// Self-heal a hub migration: if a stale session for this exact path still
		// points at a previous hub, retire it first so the recreate below binds to the
		// current hub instead of colliding with (or being masked by) the old one.
		if err := s.retireForeignHub(tildeDir); err != nil {
			return err
		}
	}
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
	var sessionName string
	if s.hubOwned() {
		_, sessionName, err = actions.AddPathHubOwned(s.addr, s.machineAddr, defaultBundle, tildeDir, force)
	} else {
		_, sessionName, err = actions.AddPath(s.addr, defaultBundle, tildeDir, force)
	}
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

	flush := s.flush
	if flush == nil {
		// A directly-constructed realSyncer (unit tests) may leave the seam
		// unset; production always wires it in newRealSyncer.
		flush = mutagen.Flush
	}
	if ferr := flush(sessionName); ferr != nil {
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
