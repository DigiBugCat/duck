// Package app is the service layer the picker TUI depends on. It defines the
// Service interface — Sessions/Refresh, Attach, Rename, NameNow, Kill — and a
// concrete App that wires the session manager, the names store, and the namer
// together. The TUI holds a Service (not a concrete App) so tests inject a
// fakeService, mirroring flok's actions.Service seam.
//
// App composes, it does not reach around: it imports session/names/namer and
// internal/model (a leaf), and never imports internal/tui — the dependency
// flows tui → app, so there is no cycle.
package app

import (
	"context"
	"fmt"
	"time"

	"github.com/DigiBugCat/duck/internal/model"
	"github.com/DigiBugCat/duck/internal/namer"
	"github.com/DigiBugCat/duck/internal/names"
	"github.com/DigiBugCat/duck/internal/session"
)

// Service is the set of operations the picker TUI calls. It is the seam tests
// fake; the production value is *App. Sessions returns the cached display rows;
// Refresh re-reads the hub (session.List + names) and rebuilds them.
type Service interface {
	// Sessions returns the current display rows from the local cache (instant;
	// the picker renders these without blocking on the hub).
	Sessions() []model.Row
	// Refresh re-reads live sessions + names from the hub and rebuilds the rows,
	// returning the fresh set.
	Refresh() ([]model.Row, error)
	// Attach tears nothing down itself; the TUI quits first, then calls Attach,
	// which hands the process off to ssh -t tmux attach for tmuxName. Returns
	// only on a failure to exec.
	Attach(tmuxName string) error
	// Rename sets the raw user display name for tmuxName (writes names.json).
	// It never touches the tmux name.
	Rename(tmuxName, display string) error
	// NameNow forces a codex re-name of tmuxName now (capture head → codex →
	// freeze), returning the new name.
	NameNow(tmuxName string) (string, error)
	// Kill terminates the tmux session and forgets its names.json entry.
	Kill(tmuxName string) error
}

// App is the production Service. It composes the remote tmux manager, the
// names store, the namer, and a small in-memory row cache for an instant
// picker. The zero value is not usable; construct with New.
type App struct {
	sessions *session.Manager
	names    *names.Store
	namer    namer.Namer
	capture  namer.Capturer    // optional pane-head capturer; nil ⇒ no codex naming
	autoName func(string) bool // per-dir auto-naming toggle; nil ⇒ OFF for every dir
	cache    []model.Row       // throwaway in-memory rows for an instant picker
}

// New wires an App from its collaborators. The TUI receives it as a Service. If
// n also satisfies namer.Capturer (the codex namer does), NameNow can capture
// the pane head; otherwise NameNow degrades to the dir-derived floor.
func New(sessions *session.Manager, store *names.Store, n namer.Namer) *App {
	a := &App{sessions: sessions, names: store, namer: n}
	if c, ok := n.(namer.Capturer); ok {
		a.capture = c
	}
	return a
}

// SetAutoName installs the per-dir auto-naming predicate (config.AutoNameEnabled
// in production). When the predicate reports true for a session's dir AND the
// session has no user name and no fresh cached codex name, Refresh names it
// lazily on first sight and freezes the result. A nil predicate (the default,
// and the test default) keeps auto-naming OFF for every dir so no pane content
// is ever sent to the model unless explicitly enabled.
func (a *App) SetAutoName(enabled func(string) bool) { a.autoName = enabled }

// autoNameEnabled reports whether lazy auto-naming may run for dir. OFF unless a
// predicate was installed and it returns true for this dir.
func (a *App) autoNameEnabled(dir string) bool {
	return a.autoName != nil && a.autoName(dir)
}

// Sessions returns the cached rows.
func (a *App) Sessions() []model.Row {
	return a.cache
}

// Refresh re-reads live sessions + names from the hub, resolves each into a
// display Row (user ▸ codex ▸ dir-derived), ranks them, caches and returns the
// fresh set. Liveness/age/attached are read live and never stored.
//
// Lazy auto-naming (DESIGN §5 / §M3) happens here: the first time Refresh sees a
// session with no user name and no fresh cached codex name — and only when the
// per-dir toggle is on — it captures the pane head, names it via codex, and
// freezes the name on a content hash. namer.CacheHit gates the (quota-costing)
// codex call so a frozen name is reused on every later refresh until the head
// changes materially. Any capture/codex error degrades silently to the existing
// floor (Resolve falls to dir-derived); naming never fails a Refresh.
func (a *App) Refresh() ([]model.Row, error) {
	return a.refresh(true)
}

// List re-reads live sessions + names and resolves them into display Rows
// WITHOUT the auto-naming side effect: it never captures pane content, never
// calls codex, and never writes names.json. It is the read-only path for
// `duck ls`, which is documented as listing without attaching and should not
// spend codex quota or mutate hub state on first sight of an unnamed session.
// Auto-naming stays on the interactive picker's Refresh, where the user is
// present (DESIGN §5).
func (a *App) List() ([]model.Row, error) {
	return a.refresh(false)
}

// refresh is the shared body for Refresh (autoName=true) and List
// (autoName=false): it reads live sessions + names, optionally lazy-names
// unnamed sessions on first sight, resolves each into a display Row, ranks,
// caches, and returns them.
func (a *App) refresh(autoName bool) ([]model.Row, error) {
	live, err := a.sessions.List()
	if err != nil {
		return nil, err
	}
	n, err := a.names.Load()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	dirty := false
	if autoName {
		dirty = a.autoNameOnFirstSight(live, n, now)
	}
	rows := make([]model.Row, 0, len(live))
	for _, s := range live {
		rows = append(rows, model.Row{
			Display:  names.Resolve(n, s.Name, s.Dir, s.PaneTitle),
			Dir:      s.Dir,
			Age:      humanizeAge(now.Sub(s.LastActive)),
			Attached: s.Attached,
			Windows:  s.Windows,
			TmuxName: s.Name,
			LastSeen: s.LastActive,
		})
	}
	if dirty {
		// One atomic write for all freshly-minted names; a persist error is
		// swallowed so a hub hiccup never fails the refresh — the rows already
		// carry the new names in-memory for this session.
		_ = a.names.Save(n)
	}
	ranked := model.Rank(rows)
	a.cache = ranked
	return ranked, nil
}

// autoNameOnFirstSight names any live session that is unnamed (no user name, no
// fresh cached codex name) when its per-dir toggle is on, mutating n in place.
// It reports whether n changed so Refresh can persist once. A session is skipped
// when: there is no capturer, the per-dir toggle is off, the user already named
// it, or namer.CacheHit reports the cached codex name still matches the current
// head. Errors degrade silently — the session simply keeps its dir-derived floor.
func (a *App) autoNameOnFirstSight(live []session.Sess, n names.Names, now time.Time) bool {
	if a.capture == nil {
		return false
	}
	dirty := false
	for _, s := range live {
		e := n.Names[s.Name]
		if e.UserName != "" || !a.autoNameEnabled(s.Dir) {
			continue
		}
		// The running program already wrote a name (Claude Code's pane title); it
		// wins in Resolve, so spending a codex call here would be wasted — skip.
		if names.CleanTitle(s.PaneTitle) != "" {
			continue
		}
		head, err := a.capture.CaptureHead(s.Name)
		if err != nil {
			continue
		}
		if namer.CacheHit(e, head) {
			continue // frozen: reuse the cached codex name, no codex call
		}
		title, err := a.namer.Name(context.Background(), head)
		if err != nil || title == "" {
			continue // never block; Resolve falls to the dir-derived floor
		}
		e.CodexName = title
		e.CodexHash = namer.Hash(head)
		if s.Dir != "" {
			e.Dir = s.Dir
		}
		if e.CreatedAt.IsZero() {
			e.CreatedAt = now
		}
		n.Names[s.Name] = e
		dirty = true
	}
	return dirty
}

// Attach hands the process off to the session manager's interactive attach.
func (a *App) Attach(tmuxName string) error {
	return a.sessions.Attach(tmuxName)
}

// Rename writes a raw user display name into names.json. It never touches the
// tmux name.
func (a *App) Rename(tmuxName, display string) error {
	n, err := a.names.Load()
	if err != nil {
		return err
	}
	e := n.Names[tmuxName]
	e.UserName = display
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now()
	}
	n.Names[tmuxName] = e
	return a.names.Save(n)
}

// NameNow forces a generated name NOW and PINS it: capture the pane head, run
// the namer, then store the result as the USER name in names.json. It writes
// UserName (not CodexName) on purpose — Resolve ranks the live pane title above
// CodexName, so a freshly-generated codex name in the CodexName slot would be
// masked by the running program's pane title and ^n would look like a no-op.
// Pinning it as the user name makes ^n the deliberate override of the live title.
// Falls back to the dir-derived floor on any capture/codex error so naming never
// blocks (and without writing, so the floor is never pinned and a later ^n retries).
func (a *App) NameNow(tmuxName string) (string, error) {
	dir, _, _ := a.sessions.Option(tmuxName, "@duck_dir")

	var snapshot string
	if a.capture != nil {
		snapshot, _ = a.capture.CaptureHead(tmuxName)
	}
	title, err := a.namer.Name(context.Background(), snapshot)
	if err != nil || title == "" {
		// Codex/capture failed: return the dir-derived floor for display WITHOUT
		// writing names.json, so the floor is never pinned and a later ^n retries.
		return names.Derive(dir), nil
	}

	n, lerr := a.names.Load()
	if lerr != nil {
		return "", lerr
	}
	e := n.Names[tmuxName]
	e.UserName = title
	if dir != "" {
		e.Dir = dir
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now()
	}
	n.Names[tmuxName] = e
	if err := a.names.Save(n); err != nil {
		return "", err
	}
	return title, nil
}

// Kill terminates the session and drops its names.json entry.
func (a *App) Kill(tmuxName string) error {
	if err := a.sessions.Kill(tmuxName); err != nil {
		return err
	}
	n, err := a.names.Load()
	if err != nil {
		return err
	}
	if _, ok := n.Names[tmuxName]; ok {
		delete(n.Names, tmuxName)
		return a.names.Save(n)
	}
	return nil
}

// CleanDetached kills every DETACHED tmux session on the hub and forgets each
// killed session's names.json entry, returning how many it killed. It is the
// batched engine behind `duck clean`: where calling App.Kill per session would
// do K×(load+save) of names.json (K extra ssh round-trips), CleanDetached reads
// names.json ONCE, deletes each killed entry in memory, and writes ONCE.
//
// Attached sessions are NEVER killed (the safety contract: never kill a session
// someone is in). A List() error PROPAGATES — a dead/unreachable hub must surface
// as an error, not masquerade as "nothing to clean". A per-session kill failure
// is reported to stdout and skipped (its names.json entry is left intact); the
// loop continues. The "killed <name>" line is printed per success here so the
// command layer only needs the count for its "no detached sessions" message.
func (a *App) CleanDetached() (int, error) {
	live, err := a.sessions.List()
	if err != nil {
		return 0, err
	}
	n, err := a.names.Load()
	if err != nil {
		return 0, err
	}
	killed := 0
	dirty := false
	for _, s := range live {
		if s.Attached {
			continue // never kill a session someone is in
		}
		if err := a.sessions.Kill(s.Name); err != nil {
			fmt.Printf("  %s: %v\n", s.Name, err)
			continue // leave its names.json entry intact; keep going
		}
		fmt.Printf("killed %s\n", s.Name)
		killed++
		if _, ok := n.Names[s.Name]; ok {
			delete(n.Names, s.Name)
			dirty = true
		}
	}
	if dirty {
		if err := a.names.Save(n); err != nil {
			return killed, err
		}
	}
	return killed, nil
}

// humanizeAge renders a duration as the picker's compact age (e.g. "2m", "1h",
// "3d"). Sub-minute is "now".
func humanizeAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return itoa(int(d.Minutes())) + "m"
	case d < 24*time.Hour:
		return itoa(int(d.Hours())) + "h"
	default:
		return itoa(int(d.Hours()/24)) + "d"
	}
}

// itoa is a tiny strconv-free int formatter for the age column.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
