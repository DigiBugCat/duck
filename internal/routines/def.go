// Package routines parses duck's routine definitions — the WORKSPACE's job
// description (DESIGN: docs/ROUTINES.md) — and computes due-ness against the
// clock. A workspace is a manager (claude in the main pane) with a flock of
// executors; its routines are what those employees do on a schedule. A
// routine is a pair of files under <project-sync-root>/.duck/routines/<workspace>/:
// <name>.toml (trigger + target + overrides) and <name>.md (the prompt, the
// actual job description). Stored as PROJECT CONTENT (synced + versioned
// alongside pads under the same .duck/), but OWNED by the workspace and fired
// there — so several workspaces on one repo each keep their own duties, and
// fires/reports land exactly where they were scheduled. The hub keeps only the
// last-fire state and a project index (both under ~/.duck), never the defs.
// Parsing and firing are deliberately split: this file owns LoadWorkspace
// (files -> Def) and Due (Def + clock -> bool); the tick loop, state
// persistence, and pane creation live elsewhere.
package routines

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/robfig/cron/v3"

	"github.com/DigiBugCat/duck/internal/agent"
	"github.com/DigiBugCat/duck/internal/panel"
	"github.com/DigiBugCat/duck/internal/paths"
)

// Location is the fleet's wall clock: every cron schedule is evaluated in
// Pacific time regardless of the hub's TZ (the human is in PST; "0 9 * * *"
// must mean 9am for THEM). Falls back to time.Local only if the tzdata lookup
// fails, which on a stock hub it won't.
var Location = func() *time.Location {
	if loc, err := time.LoadLocation("America/Los_Angeles"); err == nil {
		return loc
	}
	return time.Local
}()

// Trigger selects what makes a routine due.
type Trigger string

const (
	TriggerCron      Trigger = "cron"
	TriggerHeartbeat Trigger = "heartbeat"
	TriggerManual    Trigger = "manual"
)

// Target selects who receives the fire.
type Target string

const (
	TargetRun     Target = "run"
	TargetManager Target = "manager"
)

// Def is one parsed routine definition.
type Def struct {
	Name      string // file basename without .toml
	Root      string // covering project sync-root (tilde-form) the def was loaded from
	Workspace string // owning workspace (tmux session name) it was loaded from
	Trigger   Trigger
	Schedule  string        // cron expression, required when Trigger==cron
	Interval  time.Duration // required when Trigger==heartbeat (toml value is a Go duration string like "15m")
	Target    Target        // defaults to "run"
	Report    string        // "digest" (default) | "none"
	Model     string        // optional CODEX-NATIVE model alias for the executor (agent.KnownNativeModels); empty = codex default
	Effort    string        // optional reasoning effort (low|medium|high); empty = codex default
	Prompt    string        // contents of the sibling <name>.md, trimmed

	schedule cron.Schedule // parsed at Load time so bad cron exprs fail early
}

// rawDef mirrors the on-disk TOML shape. Kept private and unmarshaled via
// toml.Decode (not toml.Unmarshal) so we can inspect MetaData.Undecoded and
// reject unknown keys instead of silently ignoring typos.
type rawDef struct {
	Trigger  string `toml:"trigger"`
	Schedule string `toml:"schedule"`
	Interval string `toml:"interval"`
	Target   string `toml:"target"`
	Report   string `toml:"report"`
	Model    string `toml:"model"`
	Effort   string `toml:"effort"`
}

// SyncRootFn resolves the longest mutagen sync root covering a workspace's dir
// (tilde-form) — where its project content (routine defs, pads) belongs.
// Injected by command (flow.CoveringSyncRoot) so this low-level package needn't
// import flow/mutagen. The default returns "" (no sync info) → SyncRoot falls
// back to the workspace's own dir. Mirrors panel.SyncRootFn exactly, so a
// workspace's routine defs and its pads resolve to the identical root.
var SyncRootFn = func(dir string) string { return "" }

// SyncRoot resolves the project sync-root a workspace's routine defs live
// under: the covering sync root when the workspace is synced, else the
// workspace's own dir (@duck_dir). Always tilde-form. Requires a LIVE
// workspace (@duck_dir readable); callers on the tick's global path use the
// index instead. Mirrors panel.PadRoot so defs co-locate with pads.
func SyncRoot(run panel.Runner, ws string) (string, error) {
	dir, err := workspaceCwd(run, ws)
	if err != nil {
		return "", err
	}
	tilde := paths.Contract(dir)
	if root := SyncRootFn(tilde); root != "" {
		return root, nil
	}
	return tilde, nil
}

// WorkspaceDir returns the routines dir for one workspace under a project root:
// <expand(root)>/.duck/routines/<ws>/. root is tilde-form (as returned by
// SyncRoot / stored in the index); it is expanded here so callers always pass
// tilde-form.
func WorkspaceDir(root, ws string) (string, error) {
	absRoot, err := paths.Expand(root)
	if err != nil {
		return "", err
	}
	return filepath.Join(ProjectRoutinesDir(absRoot), ws), nil
}

// WSRef pairs a project sync-root (tilde-form) with an owning workspace. The
// tick and `routines --all` enumerate these — a workspace name alone no longer
// locates a def dir, since two projects can share a workspace name.
type WSRef struct {
	Root      string // tilde-form project sync-root
	Workspace string // tmux session name
}

// AllWorkspaces enumerates every (root, workspace) that has routine
// definitions, driven by the hub-local project index (LoadIndex) — NOT a
// filesystem scan. For each indexed root it reads <root>/.duck/routines/ and
// returns one WSRef per workspace subdirectory. A stale/unreadable root is
// SKIPPED, never fatal (the tick must survive one bad project). Missing index
// => (nil, nil).
func AllWorkspaces() ([]WSRef, error) {
	roots, err := LoadIndex()
	if err != nil {
		return nil, err
	}
	var out []WSRef
	for _, root := range roots {
		absRoot, err := paths.Expand(root)
		if err != nil {
			continue // stale index entry (bad tilde) — skip, don't crash the sweep
		}
		entries, err := os.ReadDir(ProjectRoutinesDir(absRoot))
		if err != nil {
			continue // project dir gone / unreadable — skip
		}
		for _, e := range entries {
			if e.IsDir() {
				out = append(out, WSRef{Root: root, Workspace: e.Name()})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Root != out[j].Root {
			return out[i].Root < out[j].Root
		}
		return out[i].Workspace < out[j].Workspace
	})
	return out, nil
}

// RootHasRoutines reports whether a project root still has ANY routine
// definition across ALL its workspaces (a *.toml under any <root>/.duck/
// routines/<ws>/). rm uses it to decide whether to drop the root from the index:
// the index is keyed by PROJECT, so it must survive until the project's LAST
// routine — across every workspace under it — is gone. root is tilde-form.
func RootHasRoutines(root string) (bool, error) {
	absRoot, err := paths.Expand(root)
	if err != nil {
		return false, err
	}
	wsEntries, err := os.ReadDir(ProjectRoutinesDir(absRoot))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	for _, we := range wsEntries {
		if !we.IsDir() {
			continue
		}
		defEntries, err := os.ReadDir(filepath.Join(ProjectRoutinesDir(absRoot), we.Name()))
		if err != nil {
			continue
		}
		for _, de := range defEntries {
			if !de.IsDir() && filepath.Ext(de.Name()) == ".toml" {
				return true, nil
			}
		}
	}
	return false, nil
}

// LoadWorkspace parses every *.toml under <root>/.duck/routines/<ws>/. root is
// tilde-form. Missing dir => (nil, nil). A malformed routine returns an error
// naming the file. A .toml without a sibling .md is an error (the prompt is the
// job description; a routine without one is meaningless).
func LoadWorkspace(root, ws string) ([]Def, error) {
	dir, err := WorkspaceDir(root, ws)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var defs []Def
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".toml" {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".toml")
		tomlPath := filepath.Join(dir, e.Name())
		mdPath := filepath.Join(dir, name+".md")

		d, err := loadOne(root, name, ws, tomlPath, mdPath)
		if err != nil {
			return nil, fmt.Errorf("routine %s: %w", e.Name(), err)
		}
		defs = append(defs, d)
	}

	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
	return defs, nil
}

func loadOne(root, name, ws, tomlPath, mdPath string) (Def, error) {
	var raw rawDef
	meta, err := toml.DecodeFile(tomlPath, &raw)
	if err != nil {
		return Def{}, err
	}
	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, len(undecoded))
		for i, k := range undecoded {
			keys[i] = k.String()
		}
		return Def{}, fmt.Errorf("unknown field(s): %s", strings.Join(keys, ", "))
	}

	d := Def{
		Name:      name,
		Root:      root,
		Workspace: ws,
		Trigger:   Trigger(raw.Trigger),
		Schedule:  raw.Schedule,
		Target:    Target(raw.Target),
		Report:    raw.Report,
		Model:     raw.Model,
		Effort:    raw.Effort,
	}

	switch d.Trigger {
	case TriggerCron:
		if d.Schedule == "" {
			return Def{}, fmt.Errorf("trigger=cron requires schedule")
		}
		// Pin every schedule to the fleet wall clock (Location) unless the def
		// spells its own TZ. robfig evaluates a spec in its parse-time zone, so
		// the pin must happen HERE, not at Next() time.
		spec := d.Schedule
		if !strings.HasPrefix(spec, "TZ=") && !strings.HasPrefix(spec, "CRON_TZ=") {
			spec = "CRON_TZ=" + Location.String() + " " + spec
		}
		sched, err := cron.ParseStandard(spec)
		if err != nil {
			return Def{}, fmt.Errorf("invalid schedule %q: %w", d.Schedule, err)
		}
		d.schedule = sched
	case TriggerHeartbeat:
		if raw.Interval == "" {
			return Def{}, fmt.Errorf("trigger=heartbeat requires interval")
		}
		interval, err := time.ParseDuration(raw.Interval)
		if err != nil {
			return Def{}, fmt.Errorf("invalid interval %q: %w", raw.Interval, err)
		}
		if interval <= 0 {
			return Def{}, fmt.Errorf("interval must be > 0, got %q", raw.Interval)
		}
		d.Interval = interval
	case TriggerManual:
		// no schedule/interval required
	case "":
		return Def{}, fmt.Errorf("trigger is required")
	default:
		return Def{}, fmt.Errorf("unknown trigger %q", raw.Trigger)
	}

	switch d.Target {
	case "":
		d.Target = TargetRun
	case TargetRun, TargetManager:
		// ok
	default:
		return Def{}, fmt.Errorf("unknown target %q", raw.Target)
	}

	switch d.Report {
	case "":
		d.Report = "digest"
	case "digest", "none":
		// ok
	default:
		return Def{}, fmt.Errorf("unknown report %q", raw.Report)
	}

	// Model/effort must fail at load, not at fire — a routine that silently
	// launches the wrong executor is worse than one that refuses to parse.
	// Same alias table as spawn, but routines are codex-native ONLY: an
	// unattended scheduled executor stays on codex's shown models;
	// cross-provider (DeepSeek et al) is a deliberate per-spawn choice.
	if cross, err := agent.CrossProvider(d.Model); err != nil {
		return Def{}, err
	} else if cross {
		return Def{}, fmt.Errorf("model %q is not codex-native — routines accept only: %s",
			raw.Model, strings.Join(agent.KnownNativeModels(), ", "))
	}
	switch d.Effort {
	case "", "low", "medium", "high":
		// ok
	default:
		return Def{}, fmt.Errorf("unknown effort %q (want low|medium|high)", raw.Effort)
	}

	if d.Target == TargetManager && (d.Model != "" || d.Effort != "") {
		return Def{}, fmt.Errorf("model/effort apply to codex executors, not target=manager")
	}

	promptBytes, err := os.ReadFile(mdPath)
	if err != nil {
		if os.IsNotExist(err) {
			return Def{}, fmt.Errorf("missing sibling prompt %s", filepath.Base(mdPath))
		}
		return Def{}, err
	}
	d.Prompt = strings.TrimSpace(string(promptBytes))

	return d, nil
}

// Due reports whether the routine should fire, given the last fire time
// (zero => never fired) and now.
//   - manual: never due.
//   - heartbeat: due when last is zero or now-last >= Interval.
//   - cron: due when the next scheduled time strictly after last is <= now.
//     A zero last is never due — the tick seeds LastFire=now on first sight,
//     so a freshly registered cron routine waits for its next slot rather
//     than firing immediately. Missed beats collapse to one fire (dropped,
//     not replayed).
func (d Def) Due(last, now time.Time) bool {
	switch d.Trigger {
	case TriggerManual:
		return false
	case TriggerHeartbeat:
		if last.IsZero() {
			return true
		}
		return now.Sub(last) >= d.Interval
	case TriggerCron:
		if d.schedule == nil {
			return false
		}
		if last.IsZero() {
			// Never fired: not due. The tick seeds LastFire=now on first
			// sight so the routine waits for its next slot; without that
			// seed a zero last would recede forever ("next after now" is
			// never <= now).
			return false
		}
		// ParseStandard schedules live in time.Local, and robfig evaluates
		// them in the INPUT time's location — a JSON round-trip pins
		// last_fire to a fixed offset, which would silently turn "0 9 * * *"
		// into 9am UTC. Normalize before computing.
		next := d.schedule.Next(last.In(Location))
		return !next.After(now.In(Location))
	default:
		return false
	}
}

// NextFire reports when the routine will next fire, given the last fire time
// and now — the listing's NEXT column. Zero means "no scheduled fire" (manual
// routines, or an unparsed schedule). A heartbeat whose interval has already
// lapsed (or that has never beaten) reports now: it fires on the next tick.
func (d Def) NextFire(last, now time.Time) time.Time {
	switch d.Trigger {
	case TriggerCron:
		if d.schedule == nil {
			return time.Time{}
		}
		return d.schedule.Next(now.In(Location))
	case TriggerHeartbeat:
		if last.IsZero() {
			return now
		}
		if next := last.Add(d.Interval); next.After(now) {
			return next
		}
		return now
	default:
		return time.Time{}
	}
}
