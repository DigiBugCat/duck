package claude

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// registryEntry is the minimal ~/.claude.json projects[<path>] entry duck writes
// to make a synced-in project discoverable by `claude --resume`. Empirically, a
// project must have an entry in this map for its sessions to be found (a bare
// transcript in the right slug dir is NOT enough); trust + onboarding flags let
// Claude open it without an interactive dialog. duck only ever ADDS an entry for
// a path that has none — it never edits or removes an existing one.
var registryEntry = map[string]any{
	"hasTrustDialogAccepted":                  true,
	"hasCompletedProjectOnboarding":           true,
	"projectOnboardingSeenCount":              1,
	"allowedTools":                            []any{},
	"hasClaudeMdExternalIncludesApproved":     false,
	"hasClaudeMdExternalIncludesWarningShown": false,
}

// Registry is a non-destructive editor for ~/.claude.json's "projects" map. It
// preserves every other key (including auth-bearing ones like mcpServers) and
// every other project entry verbatim: it decodes the document into raw JSON
// fragments, mutates only the "projects" sub-map, and writes it back atomically
// (temp file + rename). It NEVER deletes a key or overwrites an existing project
// entry — the only mutation is adding a fresh entry for a path that has none.
//
// Concurrency: Claude Code itself writes this file. duck runs as a separate
// process and does a fresh read immediately before each write with an atomic
// rename, so a reader never sees a partial file. A concurrent Claude write can
// still lose duck's just-added entry (last-writer-wins) — acceptable because the
// next reconcile re-adds it; duck writes rarely and only outside a live session.
type Registry struct {
	path string // absolute ~/.claude.json
}

// NewRegistry returns a Registry over ~/.claude.json under the given absolute
// home directory. Kept home-parameterized (not os.UserHomeDir) so tests point it
// at a temp HOME.
func NewRegistry(home string) *Registry {
	return &Registry{path: filepath.Join(home, ".claude.json")}
}

// load reads and splits ~/.claude.json into (top-level raw map, projects map).
// A missing file yields empty maps (first-run safe). The projects sub-map is
// decoded to raw fragments so untouched entries round-trip byte-for-byte.
func (r *Registry) load() (top map[string]json.RawMessage, projects map[string]json.RawMessage, err error) {
	top = map[string]json.RawMessage{}
	projects = map[string]json.RawMessage{}
	data, err := os.ReadFile(r.path)
	if err != nil {
		if os.IsNotExist(err) {
			return top, projects, nil
		}
		return nil, nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return top, projects, nil
	}
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, nil, fmt.Errorf("~/.claude.json is not valid JSON: %w", err)
	}
	if raw, ok := top["projects"]; ok && len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &projects); err != nil {
			return nil, nil, fmt.Errorf("~/.claude.json projects is not an object: %w", err)
		}
	}
	return top, projects, nil
}

// Registered reports whether absPath already has a projects entry.
func (r *Registry) Registered(absPath string) (bool, error) {
	_, projects, err := r.load()
	if err != nil {
		return false, err
	}
	_, ok := projects[absPath]
	return ok, nil
}

// Register adds a minimal projects entry for each absPath that has none, writing
// the file back atomically. It returns the paths it actually added (already-known
// paths are skipped, so a fully-covered call writes nothing and returns nil). The
// entry is only ever ADDED — an existing entry is left exactly as Claude wrote it.
func (r *Registry) Register(absPaths ...string) (added []string, err error) {
	top, projects, err := r.load()
	if err != nil {
		return nil, err
	}
	entryJSON, err := json.Marshal(registryEntry)
	if err != nil {
		return nil, err
	}
	for _, p := range absPaths {
		if _, ok := projects[p]; ok {
			continue // never overwrite what Claude already tracks
		}
		projects[p] = json.RawMessage(entryJSON)
		added = append(added, p)
	}
	if len(added) == 0 {
		return nil, nil // nothing to do → don't rewrite the file at all
	}
	projRaw, err := json.Marshal(projects)
	if err != nil {
		return nil, err
	}
	top["projects"] = projRaw
	out, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := r.atomicWrite(out); err != nil {
		return nil, err
	}
	return added, nil
}

// atomicWrite streams to a temp sibling then renames over the target, so a
// reader never sees a partial file. Mode 0600 matches Claude's own (the file
// holds auth material).
func (r *Registry) atomicWrite(data []byte) error {
	dir := filepath.Dir(r.path)
	tmp, err := os.CreateTemp(dir, ".claude.json.duck-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, r.path)
}
