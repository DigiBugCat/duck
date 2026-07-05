// Hub-local persistence for duck's routines feature: last-fire timestamps.
// This always runs on the hub, so plain local file I/O — no SSH runner
// (contrast internal/names, which reads/writes over sshx.Client because it's
// laptop-driven).
package routines

import (
	"encoding/json"
	"os"
	"path/filepath"
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

// State records last-fire times, persisted at ~/.duck/routines-state.json
// (or $DUCK_HOME/routines-state.json when DUCK_HOME is set, for tests).
type State struct {
	LastFire map[string]time.Time `json:"last_fire"` // key: Key(workspace, name)
}

// Key builds the state map key for a routine: the owning workspace (tmux
// session name) plus a tab plus the routine name. Tabs can't appear in either
// (session names and filenames), so this is collision-free without escaping.
func Key(ws, name string) string {
	return filepath.Clean(ws) + "\t" + name
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
