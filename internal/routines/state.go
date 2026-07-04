// Package routines owns the hub-local persistence for duck's routines
// feature: last-fire timestamps and the registry of projects with routines
// enabled. This always runs on the hub, so plain local file I/O — no SSH
// runner (contrast internal/names, which reads/writes over sshx.Client
// because it's laptop-driven).
package routines

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// dir returns the duck home dir: $DUCK_HOME if set (test seam), else
// ~/.duck.
func dir() (string, error) {
	if d := os.Getenv("DUCK_HOME"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".duck"), nil
}

// statePath returns the state file's path: $DUCK_HOME/routines-state.json
// (or ~/.duck/routines-state.json).
func statePath() (string, error) {
	d, err := dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "routines-state.json"), nil
}

// projectsPath returns the projects registry's path: $DUCK_HOME/
// routines-projects (or ~/.duck/routines-projects).
func projectsPath() (string, error) {
	d, err := dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "routines-projects"), nil
}

// State records last-fire times, persisted at ~/.duck/routines-state.json
// (or $DUCK_HOME/routines-state.json when DUCK_HOME is set, for tests).
type State struct {
	LastFire map[string]time.Time `json:"last_fire"` // key: Key(dir, name)
}

// Key builds the state map key for a routine in a project dir: the cleaned
// dir plus a tab plus the routine name. Tabs can't appear in routine names
// (they're filenames), so this is collision-free without escaping.
func Key(dir, name string) string {
	return filepath.Clean(dir) + "\t" + name
}

// LoadState reads the state file. A missing file is not an error: it
// returns an empty State with a non-nil map.
func LoadState() (State, error) {
	empty := State{LastFire: map[string]time.Time{}}
	p, err := statePath()
	if err != nil {
		return empty, err
	}
	data, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return empty, nil
	}
	if err != nil {
		return empty, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return empty, nil
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return empty, err
	}
	if s.LastFire == nil {
		s.LastFire = map[string]time.Time{}
	}
	return s, nil
}

// SaveState writes s atomically: a temp sibling in the same dir, then
// rename over the target, so a partial write never corrupts the file (the
// same no-corruption-not-no-lost-updates caveat as names.go — this package
// has no cross-process locking either).
func SaveState(s State) error {
	if s.LastFire == nil {
		s.LastFire = map[string]time.Time{}
	}
	p, err := statePath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(p, data)
}

// Projects is the registry of project dirs with routines enabled, persisted
// at ~/.duck/routines-projects (or $DUCK_HOME/routines-projects), one
// absolute path per line, sorted, no duplicates.
func Projects() ([]string, error) {
	p, err := projectsPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out, nil
}

// Enable registers dir in the projects registry (idempotent), storing
// filepath.Clean(dir).
func Enable(dir string) error {
	clean := filepath.Clean(dir)
	existing, err := Projects()
	if err != nil {
		return err
	}
	for _, p := range existing {
		if p == clean {
			return nil
		}
	}
	existing = append(existing, clean)
	return saveProjects(existing)
}

// Disable unregisters dir from the projects registry (idempotent).
func Disable(dir string) error {
	clean := filepath.Clean(dir)
	existing, err := Projects()
	if err != nil {
		return err
	}
	out := existing[:0]
	for _, p := range existing {
		if p != clean {
			out = append(out, p)
		}
	}
	return saveProjects(out)
}

// saveProjects sorts, dedupes, and atomically writes the projects registry.
func saveProjects(projects []string) error {
	sort.Strings(projects)
	deduped := projects[:0]
	var prev string
	for i, p := range projects {
		if i == 0 || p != prev {
			deduped = append(deduped, p)
		}
		prev = p
	}
	p, err := projectsPath()
	if err != nil {
		return err
	}
	var b strings.Builder
	for _, proj := range deduped {
		b.WriteString(proj)
		b.WriteString("\n")
	}
	return atomicWrite(p, []byte(b.String()))
}

// atomicWrite mkdir -p's the target's dir, writes data to a temp sibling,
// then renames it over target so a partial write never corrupts the file.
func atomicWrite(target string, data []byte) error {
	d := filepath.Dir(target)
	if err := os.MkdirAll(d, 0o755); err != nil {
		return err
	}
	// Unique temp per writer: tick vs `fire`/`enable` can overlap, and a
	// shared .tmp path lets one writer rename the other's half-written file.
	// (Rename-over gives no-corruption, not no-lost-updates — same caveat
	// as names.json.)
	f, err := os.CreateTemp(d, filepath.Base(target)+".tmp*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, 0o644); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, target); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
