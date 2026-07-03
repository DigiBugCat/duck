// Package flow composes the idempotent bare-`duck` path (DESIGN §2): ensure cwd
// is synced to the hub, ensure a remote tmux session exists in that synced dir,
// then attach. Each step short-circuits if already done, so a second bare
// `duck` makes a second session (N-per-dir) rather than erroring.
//
// flow is the seam between the laptop CLI and the M1 sync engine + the new
// session manager: EnsureSynced drives actions.AddPath (which auto-starts
// mutagen) and waits for a steady state before a session opens in the dir;
// EnsureSession mints a tmux-legal id and creates the session; Attach hands off
// to ssh -t tmux attach. Run is the composition bare `duck` calls.
package flow

import (
	"fmt"
	"os"
	"time"

	"github.com/DigiBugCat/duck/internal/claude"
	"github.com/DigiBugCat/duck/internal/folder"
	"github.com/DigiBugCat/duck/internal/names"
	"github.com/DigiBugCat/duck/internal/paths"
	"github.com/DigiBugCat/duck/internal/session"
)

// Syncer is the seam onto the M1 sync engine. IsSynced reports whether cwd
// (tilde-form) already has a running mutagen session to the hub; Reconcile runs
// the per-file NEWEST-WINS rsync seed (used only on the force/merge path, before
// AddAndWait, so the subsequent mutagen session has no conflicts to resolve);
// AddAndWait bundles + adds the path (auto-starting mutagen) and blocks until
// the initial sync reaches a steady state. It is injectable so the bare-`duck`
// flow is unit-testable without touching real rsync/mutagen/ssh; production is
// realSyncer.
type Syncer interface {
	IsSynced(tildeDir string) (bool, error)
	Reconcile(tildeDir string, dir Direction) error
	AddAndWait(tildeDir string, force bool) error

	// CheckContainment classifies localAbs (an absolute local path) against the
	// local paths of currently active duck Mutagen sessions — see
	// folder.CheckContainment. IsSynced already handles the "localAbs is
	// covered by an existing session" case (it short-circuits EnsureSynced
	// before this is ever consulted); this seam exists for the opposite case,
	// ContainmentEncloses: localAbs is a PARENT of one or more already-synced
	// folders, which IsSynced cannot see.
	CheckContainment(localAbs string) (folder.Containment, error)

	// Terminate stops a named Mutagen session — used to retire a child session
	// made redundant by a newly-consolidated parent sync.
	Terminate(sessionName string) error
}

// Direction is how a conflicting folder (one the hub already has with files) is
// seeded before the mutagen merge. DirNone means no seed (the non-force path).
type Direction int

const (
	DirNone  Direction = iota // no reconcile; AddAndWait(force=false)
	DirPush                   // local clobbers hub (mirror local→hub, --delete)
	DirPull                   // hub clobbers local (mirror hub→local, --delete)
	DirMerge                  // newest-of-each-file wins, union, no deletions
)

// PolicyStore is the per-folder sync-policy seam: Get returns the remembered
// "sync"/"never" policy for a tilde-form dir (ok=false when unknown); Set
// records one. It is injectable so the decision tree is unit-testable without a
// real config file; production is *folder.Store.
type PolicyStore interface {
	Get(dir string) (policy string, ok bool)
	Set(dir, policy string) error
}

// Classifier judges whether a local (absolute) dir is risky to auto-mirror —
// the home dir, an ancestor of home, or a tree past the size/count caps. The
// decision tree only consults it for folders with no remembered policy.
// Production is the folder.Classify function; tests inject a fake.
type Classifier interface {
	IsRisky(localAbs string) (risky bool, reason string)
}

// Choice is the user's answer to a sync prompt for an unknown, risky folder.
type Choice int

const (
	ChoiceNo    Choice = iota // don't sync, don't remember (ask again next time)
	ChoiceYes                 // sync and remember "sync"
	ChoiceNever               // don't sync and remember "never"
)

// Prompter asks the user whether to sync a risky, unknown folder. The
// production impl reads a TTY; a non-interactive/no-TTY impl returns ChoiceNo
// without prompting. Injected so flow stays unit-testable.
type Prompter interface {
	AskSync(dir, reason string) (Choice, error)

	// AskConsolidate asks whether to terminate the already-synced child
	// session(s) (enclosedDisplay, a human-readable tilde-form list) now that
	// parentDir is about to sync as their enclosing parent. true means
	// terminate the children; false (including a non-interactive context)
	// leaves them running alongside the new parent sync.
	AskConsolidate(parentDir, enclosedDisplay string) (bool, error)
}

// Progress is the seam onto a live sync-wait UI. waitSteady (and the reconcile
// seed) drive it so a big first sync is VISIBLE instead of looking frozen:
// Start(action, target) begins a redrawing line, Update(status) is called on
// every poll with the live mutagen phase (e.g. "Staging files on beta"), and
// Stop(ok) clears the line and prints a final ✓/error note. The default is a
// no-op (nilProgress) so flow stays usable with no UI wired and tests need not
// supply one. Injected onto realSyncer via flow.New, mirroring Prompter; the
// production impl (a redrawing TTY spinner) lives in command/.
type Progress interface {
	Start(action, target string)
	Update(status string)
	Stop(ok bool)
}

// nilProgress is the default no-op Progress used when none is wired (tests, a
// non-TTY context that injects nothing). Every method is a no-op.
type nilProgress struct{}

func (nilProgress) Start(string, string) {}
func (nilProgress) Update(string)        {}
func (nilProgress) Stop(bool)            {}

// Override is the bare-`duck` --sync/--no-sync flag, forcing the sync decision
// and bypassing the prompt. OverrideNone leaves the decision tree in charge.
type Override int

const (
	OverrideNone Override = iota
	// OverrideSync (--sync) forces doSync, remembers "sync", AND carries force=true
	// into the sync path: a folder the hub already has with files is merged
	// NEWEST-WINS PER FILE (a Reconcile rsync seed, then mutagen on the now-identical
	// sides) instead of refusing with ErrHubNonEmpty. It is also the override the
	// command layer re-runs the flow with after the user confirms the merge at the
	// hub-conflict prompt — so `duck --sync` resolves a conflict newest-wins
	// non-interactively, with no this-machine-wins data clobber.
	OverrideSync
	// OverrideNoSync (--no-sync) forces a no-sync session and PERSISTS "never" so
	// the folder stays sync-free until the user changes it.
	OverrideNoSync
	// OverrideNoSyncOnce is a ONE-TIME no-sync: it opens a session in the hub's
	// existing copy WITHOUT syncing and does NOT persist any policy, so duck asks
	// again next time. It is what the hub-conflict prompt's [n] choice routes to —
	// declining the merge once must not silently disable sync forever.
	OverrideNoSyncOnce
	// OverridePush / OverridePull force a DIRECTIONAL clobber-merge into a non-empty
	// hub dir and remember "sync": Push mirrors local→hub (local wins, --delete),
	// Pull mirrors hub→local (hub wins, --delete). Like OverrideSync (which is the
	// newest-wins Merge) they carry force=true into the sync path; only the
	// reconcile DIRECTION differs. The command layer routes the conflict prompt's
	// [p]/[u] choices and the --push/--pull flags here.
	OverridePush
	OverridePull
)

// directionFor maps a sync override to the reconcile Direction EnsureSynced uses.
// The three force/merge overrides each pick a direction; everything else is
// DirNone (no seed — the non-force path).
func directionFor(o Override) Direction {
	switch o {
	case OverrideSync:
		return DirMerge
	case OverridePush:
		return DirPush
	case OverridePull:
		return DirPull
	default:
		return DirNone
	}
}

// InteractiveAttach performs ONE interactive attach to tmuxName and reports
// whether the user left CLEANLY (detached or exited normally) — the single bit
// RunWithOverride needs to decide the fresh-untouched cleanup. created tells the
// impl whether the session was freshly minted this run (the bare-`duck` reused
// path ignores cleanLeave, but the seam still takes it so one impl serves both).
//
// The command layer injects a reconnect-loop implementation (so a transport drop
// reconnects and a ^c-give-up returns cleanLeave=false, KEEPING the session for
// `duck -c`); flow's default (defaultAttach) preserves the original behavior so
// the existing flow tests stay green without a command-layer dependency.
type InteractiveAttach func(tmuxName string, created bool) (cleanLeave bool, err error)

// Flow holds the collaborators the bare-`duck` path composes: the hub address,
// the remote tmux manager, the names store, the sync seam, and the new
// sync-awareness collaborators (per-folder policy store, riskiness classifier,
// and the prompt seam). The zero value is not usable; construct with New.
type Flow struct {
	addr       string
	sessions   *session.Manager
	names      *names.Store
	sync       Syncer
	policy     PolicyStore
	classifier Classifier
	prompter   Prompter
	attachInt  InteractiveAttach
	syncClaude bool // global opt-in: co-sync this folder's ~/.claude/projects/<slug>
	local      bool // duck is running ON the hub: skip sync entirely (source == dest)
	// reconcileClaude runs the best-effort cross-machine Claude-history reconcile
	// at the end of coSyncClaude. Injected as a closure (not an interface) because
	// the real one needs config/hub/anchor/claude — which flow must not import; a
	// no-op by default so unit tests need no wiring. See SetClaudeReconciler.
	reconcileClaude func()
}

// SetLocal marks the flow as running ON the hub itself. When set, the bare-`duck`
// sync-awareness gate is bypassed entirely (decideSync always returns no-sync):
// mirroring a folder to the machine it already lives on is a no-op, so `duck` on
// the hub just opens a session in cwd without ever prompting to sync. Wired from
// the command layer off the same hostname match that puts the ssh client into
// local mode.
func (f *Flow) SetLocal(on bool) { f.local = on }

// SetClaudeHistory toggles the per-folder Claude history co-sync (OFF by
// default). When on, a bare `duck` that mirrors a folder ALSO co-syncs that
// folder's ~/.claude/projects/<slug> corpus (transcripts + memory) to the hub.
// Wired from the command layer off config.SyncClaudeHistory so flow keeps no
// config import.
func (f *Flow) SetClaudeHistory(on bool) { f.syncClaude = on }

// SetClaudeReconciler wires the best-effort cross-machine Claude-history
// reconcile step that runs at the end of coSyncClaude on every gated duck
// invocation with history co-sync on. It is a closure because the real
// implementation needs config/hub/anchor/claude — which flow must not import.
// The closure is expected to throttle and detach itself; coSyncClaude calls it
// unconditionally. A nil fn resets to a no-op so flow's tests need no wiring.
func (f *Flow) SetClaudeReconciler(fn func()) {
	if fn == nil {
		fn = func() {}
	}
	f.reconcileClaude = fn
}

// SetInteractiveAttach overrides the interactive-attach seam (the command layer
// wires in its reconnect loop). A nil arg restores the default. Called by the
// command wiring after construction so flow keeps no command-layer import.
func (f *Flow) SetInteractiveAttach(a InteractiveAttach) {
	if a == nil {
		a = f.defaultAttach
	}
	f.attachInt = a
}

// defaultAttach is flow's built-in interactive-attach seam, preserving the
// ORIGINAL behavior: a freshly-created session attaches as a SUBPROCESS
// (AttachAndWait → control returns so cleanup can run) and a normal exit is a
// clean leave; a reused session hands off via the exec Attach (which does not
// return on success). It keeps the existing flow tests green with no command
// dependency.
func (f *Flow) defaultAttach(tmuxName string, created bool) (bool, error) {
	if !created {
		return false, f.sessions.Attach(tmuxName)
	}
	if err := f.sessions.AttachAndWait(tmuxName); err != nil {
		return false, err
	}
	return true, nil
}

// classifyFunc adapts folder.Classify to the Classifier interface.
type classifyFunc struct{}

func (classifyFunc) IsRisky(localAbs string) (bool, string) {
	r := folder.Classify(localAbs)
	return r.Risky, r.Reason
}

// New wires a Flow for hub addr from the session manager and names store, using
// the production Syncer, policy store, and classifier. The Prompter must be
// supplied by the caller (command/ owns the TTY); a nil prompter is treated as
// always-No so the decision tree never blocks in a context without one. The
// Progress drives the visible sync-wait spinner (command/ owns the TTY); a nil
// progress is treated as the no-op reporter. NewWithDeps (used by the
// decision-tree unit tests) injects a Syncer wholesale and so builds no
// realSyncer — progress is a realSyncer concern only, so it is a New param.
// machineAddr, when non-empty, switches the syncer to hub-owned sessions (see
// realSyncer.machineAddr); pass cfg.MachineAddr.
func New(addr, machineAddr string, sessions *session.Manager, store *names.Store, prompter Prompter, progress Progress) *Flow {
	if progress == nil {
		progress = nilProgress{}
	}
	f := &Flow{
		addr:       addr,
		sessions:   sessions,
		names:      store,
		sync:       newRealSyncer(addr, machineAddr, progress),
		policy:     folder.NewStore(),
		classifier: classifyFunc{},
		prompter:   prompter,
	}
	f.attachInt = f.defaultAttach
	f.reconcileClaude = func() {}
	return f
}

// NewWithDeps is New with every sync-awareness collaborator injected, for the
// decision-tree unit tests. Mirrors the New constructor pattern.
func NewWithDeps(addr string, sessions *session.Manager, store *names.Store, sync Syncer, policy PolicyStore, classifier Classifier, prompter Prompter) *Flow {
	f := &Flow{
		addr:       addr,
		sessions:   sessions,
		names:      store,
		sync:       sync,
		policy:     policy,
		classifier: classifier,
		prompter:   prompter,
	}
	f.attachInt = f.defaultAttach
	f.reconcileClaude = func() {}
	return f
}

// noPrompter is the always-No prompter used when no real TTY prompter is wired.
type noPrompter struct{}

func (noPrompter) AskSync(string, string) (Choice, error) { return ChoiceNo, nil }

func (noPrompter) AskConsolidate(string, string) (bool, error) { return false, nil }

// EnsureSynced makes sure cwd is syncing to the hub. If it is not yet synced it
// adds the path (which auto-starts mutagen) and waits for the initial sync to
// reach a steady state so files exist on the hub before a session opens in
// them. Returns the tilde-form dir. Idempotent: an already-synced dir
// short-circuits.
//
// dir is the directional MERGE path: when not DirNone (the merge/push/pull
// choice / `duck --merge|--push|--pull` on a conflict), EnsureSynced first runs
// Reconcile (the rsync seed in that direction) and ONLY THEN AddAndWait(force=
// true). The order is load-bearing — the seed makes both sides coherent so the
// subsequent mutagen two-way-resolved session has no conflicts to resolve. A
// Reconcile failure returns immediately and does NOT proceed to the force-add (a
// partial seed must never be treated as a finished merge). When dir is DirNone no
// Reconcile runs and a non-empty hub dir surfaces as actions.ErrHubNonEmpty for
// the caller to resolve.
func (f *Flow) EnsureSynced(cwd string, dir Direction) (tildeDir string, err error) {
	tildeDir = paths.Contract(cwd)
	synced, err := f.sync.IsSynced(tildeDir)
	if err != nil {
		return "", err
	}
	if synced {
		return tildeDir, nil // short-circuit: already syncing
	}
	f.consolidateEnclosed(cwd, tildeDir)
	if dir != DirNone {
		// Reconcile BEFORE AddAndWait: seed both sides coherent (per direction) so
		// the force-add's mutagen session has no conflicts left.
		if err := f.sync.Reconcile(tildeDir, dir); err != nil {
			return "", err
		}
	}
	if err := f.sync.AddAndWait(tildeDir, dir != DirNone); err != nil {
		return "", err
	}
	return tildeDir, nil
}

// consolidateEnclosed checks whether the about-to-sync cwd would ENCLOSE one
// or more already-synced child sessions (e.g. syncing ~/dev after ~/dev/proj
// is already syncing on its own) and, on consent, terminates those now-
// redundant child sessions so the new parent sync is the only one covering
// them — otherwise the same files would be mirrored by two overlapping
// sessions. Best-effort throughout: a containment-check or terminate failure
// is logged, never propagated, since it must not block the sync the user came
// for.
func (f *Flow) consolidateEnclosed(localAbs, tildeDir string) {
	c, err := f.sync.CheckContainment(localAbs)
	if err != nil || c.Kind != folder.ContainmentEncloses {
		return
	}
	ok, err := f.prompter.AskConsolidate(tildeDir, c.Display())
	if err != nil || !ok {
		return
	}
	for _, s := range c.Enclosed {
		if terr := f.sync.Terminate(s.Name); terr != nil {
			fmt.Fprintf(os.Stderr, "duck: could not terminate redundant session %s: %v\n", s.Name, terr)
		}
	}
}

// coSyncClaude co-syncs the WHOLE Claude history corpus — ~/.claude/projects —
// when the global opt-in is on. It mirrors one directory (not per-slug), so all
// conversation history reaches the hub in a single session regardless of which
// folders you duck into. It is best-effort and returns no error: a failure to
// mirror history must never block or fail the session the user came for (it is
// reported, not propagated).
//
// Gates, in order:
//   - opt-in off → nothing.
//   - ~/.claude/projects does not exist locally → nothing (Claude has never run
//     on this machine; Mutagen will pick it up next time, once it exists).
//   - already syncing → nothing (idempotent; the long-lived mutagen session — or
//     an ancestor of it — keeps it current).
//
// Otherwise it runs EnsureSynced with force=true: the corpus is a multi-machine
// artifact (history may already exist on the hub from another laptop), so the
// NEWEST-WINS reconcile-then-merge is the correct seed — newest copy of each
// transcript wins, union of both sides, nothing deleted.
func (f *Flow) coSyncClaude() {
	if !f.syncClaude {
		return
	}
	claudeTilde := claude.ProjectsRoot()
	local, err := paths.Expand(claudeTilde)
	if err != nil {
		return
	}
	if info, err := os.Stat(local); err != nil || !info.IsDir() {
		return // ~/.claude/projects doesn't exist yet — nothing to seed or reconcile.
	}
	if synced, err := f.sync.IsSynced(claudeTilde); err != nil || !synced {
		// Not yet syncing (or the check failed): seed it now. force=true → newest-
		// wins reconcile then merge (safe for a corpus that may already live on the
		// hub). Best-effort: on failure don't reconcile against a corpus we just
		// failed to seed.
		if _, err := f.EnsureSynced(local, DirMerge); err != nil {
			fmt.Fprintf(os.Stderr, "duck: claude history co-sync skipped: %v\n", err)
			return
		}
	}
	// Map any foreign-machine transcripts the corpus mirror has brought down onto
	// this machine's slug/path form and register them, so `claude --resume`/`-c`
	// finds hub and other-laptop sessions automatically — the auto-wired
	// equivalent of `duck claude-history reconcile`. The injected closure throttles
	// and detaches itself (default no-op in tests / when unset).
	f.reconcileClaude()
}

// EnsureSyncedGated is EnsureSynced behind the same sync-awareness gate as bare
// `duck` (decideSync): it only mirrors cwd to the hub when the decision tree
// allows it (remembered "sync", already-synced, an unknown-but-safe folder, or
// an explicit yes via the prompt). For an unknown risky/home folder with no
// remembered "sync" and no consent it does NOT call AddAndWait — so `duck -c`
// and `duck --resume` can never silently start a multi-GB / home mirror. It
// returns the tilde-form dir in BOTH cases (identical to EnsureSynced's return),
// so the caller's downstream Recent/picker sees a byte-identical dir. This is
// the gate the continue/resume paths use in place of EnsureSynced.
func (f *Flow) EnsureSyncedGated(cwd string) (tildeDir string, err error) {
	d := paths.Contract(cwd)
	doSync, err := f.decideSync(cwd, d, OverrideNone)
	if err != nil {
		return "", err
	}
	if !doSync {
		return d, nil // gated off: no mirror starts for an unconsented risky dir.
	}
	// decideSync said sync; EnsureSynced short-circuits if already synced. The
	// gated path never forces a merge into a non-empty hub dir (force=false), so a
	// hub conflict here surfaces as actions.ErrHubNonEmpty for the caller to
	// resolve — it does not silently merge.
	td, err := f.EnsureSynced(cwd, DirNone)
	if err != nil {
		return "", err
	}
	// Co-sync the whole Claude corpus here too (best-effort) so `duck -c` /
	// `--resume` seed it as well, not just bare `duck` — the user's history follows
	// them regardless of how they re-enter. Idempotent once seeded.
	f.coSyncClaude()
	return td, nil
}

// EnsureSession returns a session for tildeDir, creating a new one when forceNew
// is set or when none exists. It mints a tmux-legal id (session.DeriveID +
// `-<n>` on collision against live sessions), calls session.New (which sets
// @duck_dir), and registers the dir on the names entry. Returns the tmux name
// to attach.
// created reports whether a brand-new tmux session was minted this call (true)
// versus an existing one being reused (false). The bare-`duck` fresh path uses
// it to decide whether the post-attach untouched-cleanup applies: only a session
// FRESHLY created this run may be cleaned up; a reused/reattached one never is.
func (f *Flow) EnsureSession(tildeDir string, forceNew bool) (tmuxName string, created bool, err error) {
	live, err := f.sessions.List()
	if err != nil {
		return "", false, err
	}
	if !forceNew {
		// Reuse the most-recent session in this dir if one exists.
		if s, ok, rerr := f.sessions.Recent(tildeDir); rerr == nil && ok {
			return s.Name, false, nil
		}
	}
	id := mintID(tildeDir, live)
	if err := f.sessions.New(id, tildeDir); err != nil {
		return "", false, err
	}
	if err := f.registerDir(id, tildeDir); err != nil {
		// Naming metadata is best-effort; a failure here must not block attach.
		_ = err
	}
	return id, true, nil
}

// forgetName drops session id's names.json entry, mirroring App.Kill's
// load/delete/save. Used by the fresh-untouched-session cleanup so a session
// duck kills on bail leaves no stale `duck ls` entry. Best-effort by caller.
func (f *Flow) forgetName(id string) error {
	n, err := f.names.Load()
	if err != nil {
		return err
	}
	if _, ok := n.Names[id]; ok {
		delete(n.Names, id)
		return f.names.Save(n)
	}
	return nil
}

// registerDir records the dir on the session's names entry so the picker can
// derive a floor name even before codex runs. Best-effort.
func (f *Flow) registerDir(id, tildeDir string) error {
	n, err := f.names.Load()
	if err != nil {
		return err
	}
	e := n.Names[id]
	e.Dir = tildeDir
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now()
	}
	n.Names[id] = e
	return f.names.Save(n)
}

// mintID derives a tmux-legal id from tildeDir and appends -<n> until it does
// not collide with a live session name.
func mintID(tildeDir string, live []session.Sess) string {
	base := session.DeriveID(tildeDir)
	taken := map[string]bool{}
	for _, s := range live {
		taken[s.Name] = true
	}
	if !taken[base] {
		return base
	}
	for n := 2; ; n++ {
		cand := fmt.Sprintf("%s-%d", base, n)
		if !taken[cand] {
			return cand
		}
	}
}

// Attach tears down nothing itself (the caller has no TUI on the bare path) and
// hands the process off to session.Attach for tmuxName. Returns only on a
// failure to exec.
func (f *Flow) Attach(tmuxName string) error {
	return f.sessions.Attach(tmuxName)
}

// Run is the bare-`duck` composition with no flag override. It delegates to
// RunWithOverride so existing callers stay source-compatible.
func (f *Flow) Run(cwd string) error {
	return f.RunWithOverride(cwd, OverrideNone)
}

// RunWithOverride is the sync-aware bare-`duck` composition. It decides whether
// to mirror cwd to the hub (so running `duck` in a multi-GB tree like ~ never
// auto-starts a mirror), then opens a session in a valid hub dir and attaches.
//
// Decision tree (D = tilde-form cwd, local = absolute cwd):
//  1. --sync/--no-sync override the decision and remember the policy.
//  2. policy=="sync" OR sync.IsSynced(D) → doSync.
//     policy=="never" → no-sync.
//     unknown: risky → prompt (default No; non-TTY → No):
//     yes→doSync+remember sync; no→no-sync (not remembered); never→no-sync+remember never.
//     unknown: safe → doSync + remember sync (no prompt).
//  3. doSync → EnsureSynced(local), sessionDir = returned tildeDir.
//     else → sessionDir = D if it exists on the hub, else "~".
//  4. EnsureSession(sessionDir, true) → Attach.
func (f *Flow) RunWithOverride(cwd string, override Override) error {
	d := paths.Contract(cwd)

	doSync, err := f.decideSync(cwd, d, override)
	if err != nil {
		return err
	}

	var sessionDir string
	if doSync {
		// EnsureSynced takes the ABSOLUTE cwd (it contracts internally) and returns
		// the tilde-form dir; using its result keeps the path form consistent.
		// Only the Sync/Push/Pull overrides force a directional merge into a
		// non-empty hub dir (reconcile then force-add); every other sync path is
		// DirNone (so a hub conflict surfaces as actions.ErrHubNonEmpty for the
		// command layer to resolve interactively).
		sessionDir, err = f.EnsureSynced(cwd, directionFor(override))
		if err != nil {
			return err
		}
		// We are mirroring a folder anyway: if enabled, ALSO co-sync the whole
		// Claude history corpus (transcripts + memory). Best-effort — it must never
		// block or fail the session the user actually came for.
		f.coSyncClaude()
	} else {
		// A no-sync session must still open in a valid hub dir: use D if it exists
		// on the hub, otherwise fall back to home.
		exists, derr := f.sessions.DirExists(d)
		if derr != nil {
			return derr
		}
		if exists {
			sessionDir = d
		} else {
			sessionDir = "~"
		}
	}

	tmuxName, created, err := f.EnsureSession(sessionDir, true)
	if err != nil {
		return err
	}
	// Route EVERY interactive attach through the injected seam (the command layer's
	// reconnect loop). cleanLeave reports whether the user left CLEANLY (detached /
	// exited) versus a give-up (^c during a reconnect backoff) or a vanished
	// session — the bit that gates the fresh-untouched cleanup.
	cleanLeave, err := f.attachInt(tmuxName, created)
	if err != nil {
		return err
	}
	if !created || !cleanLeave {
		// A reused/reattached session is NEVER cleaned up. A freshly-created session
		// the user ABANDONED via ^c (cleanLeave=false) must be KEPT so `duck -c` can
		// resume it. Either way: no kill.
		return nil
	}
	// A FRESHLY created session the user left CLEANLY: if it is untouched (detached
	// immediately, never ran anything) kill it + forget its names entry so it does
	// not clutter `duck ls`. The mutagen sync and folder policy are KEPT. Cleanup
	// is best-effort: the user has already left, so an IsUntouched/Kill/forget
	// error must not fail the run.
	untouched, err := f.sessions.IsUntouched(tmuxName)
	if err != nil || !untouched {
		return nil
	}
	_ = f.sessions.Kill(tmuxName)
	_ = f.forgetName(tmuxName)
	return nil
}

// decideSync resolves whether bare `duck` should mirror cwd, applying the
// --sync/--no-sync override, the remembered per-folder policy, the riskiness
// classifier, and the prompt. d is the tilde-form cwd; cwd is the absolute
// local path the classifier walks. Side effects (remembering a policy) are
// best-effort: a store write failure does not block the session.
func (f *Flow) decideSync(cwd, d string, override Override) (bool, error) {
	if f.local {
		// On the hub itself there is no remote to mirror to (source == destination),
		// so syncing — and the prompt that gates it — is meaningless. Always no-sync;
		// the session opens directly in the local cwd.
		return false, nil
	}
	switch override {
	case OverrideSync, OverridePush, OverridePull:
		// Force doSync and remember "sync". The reconcile DIRECTION these carry
		// (merge / local-wins / hub-wins) is consumed by EnsureSynced via
		// directionFor, not by this decision.
		_ = f.policy.Set(d, folder.PolicySync)
		return true, nil
	case OverrideNoSync:
		_ = f.policy.Set(d, folder.PolicyNever)
		return false, nil
	case OverrideNoSyncOnce:
		// One-time no-sync: do NOT persist any policy, so duck asks again next time.
		return false, nil
	}

	policy, known := f.policy.Get(d)
	if policy == folder.PolicySync {
		return true, nil
	}
	synced, err := f.sync.IsSynced(d)
	if err != nil {
		return false, err
	}
	if synced {
		return true, nil
	}
	if known && policy == folder.PolicyNever {
		return false, nil
	}

	// Unknown policy: classify and (only if risky) prompt.
	risky, reason := f.classifier.IsRisky(cwd)
	if !risky {
		// Safe folder: auto-sync and remember it (no prompt).
		_ = f.policy.Set(d, folder.PolicySync)
		return true, nil
	}
	choice, err := f.prompter.AskSync(d, reason)
	if err != nil {
		return false, err
	}
	switch choice {
	case ChoiceYes:
		_ = f.policy.Set(d, folder.PolicySync)
		return true, nil
	case ChoiceNever:
		_ = f.policy.Set(d, folder.PolicyNever)
		return false, nil
	default: // ChoiceNo: don't sync, don't remember (ask again next time).
		return false, nil
	}
}
