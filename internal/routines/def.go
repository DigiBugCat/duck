// Package routines parses duck's routine definitions — the workspace's job
// description (DESIGN: docs/ROUTINES.md) — and computes due-ness against the
// clock. A routine is a pair of files under <project>/.duck/routines/:
// <name>.toml (trigger + target + overrides) and <name>.md (the prompt, the
// actual job description). Parsing and firing are deliberately split: this
// package only owns Load (files -> Def) and Due (Def + clock -> bool); the
// tick loop, state persistence, and pane creation live elsewhere.
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
)

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
	Name     string // file basename without .toml
	Dir      string // absolute project root it was loaded from
	Trigger  Trigger
	Schedule string        // cron expression, required when Trigger==cron
	Interval time.Duration // required when Trigger==heartbeat (toml value is a Go duration string like "15m")
	Target   Target        // defaults to "run"
	Report   string        // "digest" (default) | "none"
	Prompt   string        // contents of the sibling <name>.md, trimmed

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
}

// routinesSubdir is where routine definitions live under a project root.
const routinesSubdir = ".duck/routines"

// Load parses every *.toml under <projectDir>/.duck/routines. Missing
// routines dir => (nil, nil). A malformed routine returns an error naming the
// file. A .toml without a sibling .md is an error (the prompt is the job
// description; a routine without one is meaningless).
func Load(projectDir string) ([]Def, error) {
	dir := filepath.Join(projectDir, routinesSubdir)
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

		d, err := loadOne(name, projectDir, tomlPath, mdPath)
		if err != nil {
			return nil, fmt.Errorf("routine %s: %w", e.Name(), err)
		}
		defs = append(defs, d)
	}

	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
	return defs, nil
}

func loadOne(name, projectDir, tomlPath, mdPath string) (Def, error) {
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
		Name:     name,
		Dir:      projectDir,
		Trigger:  Trigger(raw.Trigger),
		Schedule: raw.Schedule,
		Target:   Target(raw.Target),
		Report:   raw.Report,
	}

	switch d.Trigger {
	case TriggerCron:
		if d.Schedule == "" {
			return Def{}, fmt.Errorf("trigger=cron requires schedule")
		}
		sched, err := cron.ParseStandard(d.Schedule)
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
		next := d.schedule.Next(last.In(time.Local))
		return !next.After(now.In(time.Local))
	default:
		return false
	}
}
