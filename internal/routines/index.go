// index.go: the hub-local routines INDEX and the project-content location for
// routine definitions. Part of the storage relocation (docs/STORAGE-RELOCATION.md):
// routine DEFINITIONS become project content under <sync-root>/.duck/routines/
// (synced, versionable, alongside pads), while the tick still needs a cheap
// GLOBAL view of every project that has routines — it cannot scan the whole
// filesystem. The index provides that: a hub-local list of the project sync
// roots that carry routines, written by `add`/`rm` and read by the tick.
//
// This file is ADDITIVE — it introduces the new location + index without
// changing tick/add/fire behavior yet; the cutover wires callers over in a
// separate, isolated step so the live scheduler is never half-migrated.
package routines

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
)

// ProjectRoutinesDir is where a project's routine defs live: <root>/.duck/
// routines/ (root is tilde-form or absolute; the caller resolves the covering
// sync root). Sibling of the project's pads under the same .duck/.
func ProjectRoutinesDir(root string) string {
	return filepath.Join(root, ".duck", "routines")
}

// indexPath is the hub-local registry of project roots that have routines:
// $DUCK_HOME/routines-projects.json. The tick reads it to know which projects
// to load, instead of scanning the filesystem.
func indexPath() (string, error) {
	d, err := dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "routines-projects.json"), nil
}

// index is the on-disk shape: a set of project roots (tilde-form) that have
// routines. A map for idempotent add/remove; serialized as a sorted list.
type index struct {
	Roots []string `json:"roots"`
}

// LoadIndex reads the routines-projects index. Missing file => empty, no error.
func LoadIndex() ([]string, error) {
	p, err := indexPath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var idx index
	if err := json.Unmarshal(b, &idx); err != nil {
		return nil, err
	}
	return idx.Roots, nil
}

// IndexAdd records that root has routines (idempotent). RegisterAdd/Remove keep
// the index in sync as the sole readers/writers, so the tick's global view stays
// cheap and correct without a filesystem scan.
func IndexAdd(root string) error { return indexMutate(root, true) }

// IndexRemove drops root from the index (e.g. its last routine was removed).
func IndexRemove(root string) error { return indexMutate(root, false) }

func indexMutate(root string, add bool) error {
	roots, err := LoadIndex()
	if err != nil {
		return err
	}
	set := map[string]bool{}
	for _, r := range roots {
		set[r] = true
	}
	if add {
		set[root] = true
	} else {
		delete(set, root)
	}
	out := make([]string, 0, len(set))
	for r := range set {
		out = append(out, r)
	}
	sort.Strings(out)
	p, err := indexPath()
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(index{Roots: out}, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
