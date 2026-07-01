// Package anchor owns the anchor host's ~/.duck/anchor.json file: a small
// JSON document a small SUBSET of a laptop's config (the hub address and a
// few genuinely user-level preferences) is mirrored to, so multiple laptops
// belonging to one user stay in sync without a new service.
//
// It is modeled directly on internal/names — same Runner seam, same
// cat-fallback read, same temp-file + rename atomic write — because the
// anchor is the same shape of problem (one small JSON file, one writer at a
// time, no daemon). The anchor host is independently configurable from the
// hub (config.AnchorHost): it can BE the hub (in which case this degrades to
// exactly today's names.json situation) or a separate always-on box, in
// which case a hub move becomes zero-touch on every other laptop. Auth is
// whatever SSH access already reaches that host — no token, no new secret.
package anchor

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

// remotePath is the anchor-host location of the state file. Reads cat it;
// writes stream to a temp sibling and rename over it.
const remotePath = "~/.duck/anchor.json"

// tmpPath is the temp sibling JSON is streamed to before the atomic rename.
const tmpPath = "~/.duck/anchor.json.tmp"

// State is the whole anchor.json document.
type State struct {
	Hub     string            `json:"hub,omitempty"`
	HubName string            `json:"hubName,omitempty"`
	Config  map[string]string `json:"config,omitempty"` // shared config subset, string-valued
}

// Runner is the injectable SSH seam (subset of *hub.Hub) anchor uses to read
// and write the anchor-host file. Tests swap a fake asserting the cat /
// temp+rename command strings; production passes a real *hub.Hub.
type Runner interface {
	Run(remoteCmd string) (string, error)
	RunInput(remoteCmd string, stdin io.Reader) (string, error)
}

// Store reads and writes anchor.json on the anchor host over an injected
// Runner. The zero value is not usable; construct with NewStore.
type Store struct {
	run Runner
}

// NewStore returns a Store backed by the given Runner.
func NewStore(run Runner) *Store {
	return &Store{run: run}
}

// Load reads and parses ~/.duck/anchor.json from the anchor host. A missing
// file is not an error: the cat falls back to `{}` (so the first run works)
// and a blank/empty document yields a zero-value State.
func (s *Store) Load() (State, error) {
	out, err := s.run.Run("cat " + remotePath + " 2>/dev/null || echo '{}'")
	if err != nil {
		return State{}, err
	}
	trimmed := strings.TrimSpace(out)
	if trimmed == "" || trimmed == "{}" {
		return State{}, nil
	}
	var st State
	if err := json.Unmarshal([]byte(trimmed), &st); err != nil {
		return State{}, err
	}
	return st, nil
}

// Save serializes st and writes it to the anchor host atomically: stream
// JSON to a temp sibling over SSH, then `mv` it over anchor.json in the SAME
// ssh call so a partial write never leaves a corrupt file. Like
// names.Store.Save, this does NOT prevent a lost update between two laptops
// racing a load-modify-save cycle — acceptable for the same reason it's
// acceptable there (see internal/names package doc): a single user driving
// one terminal at a time, rare writes.
func (s *Store) Save(st State) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	cmd := "mkdir -p ~/.duck && cat > " + tmpPath + " && mv " + tmpPath + " " + remotePath
	_, err = s.run.RunInput(cmd, bytes.NewReader(data))
	return err
}
