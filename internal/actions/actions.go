// Package actions holds the mutating orchestration shared by the CLI commands
// and the interactive TUI: creating bundles, adding/removing paths, and
// starting/stopping the underlying Mutagen sessions.
//
// Ported from flok/internal/actions/actions.go. The AddPath/SyncPath
// auto-start-mutagen-with-rollback behavior is preserved VERBATIM; the only
// changes are the module import paths and the actionable error message
// (`duck sync new` instead of `flok new`).
package actions

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/DigiBugCat/duck/internal/hub"
	"github.com/DigiBugCat/duck/internal/mutagen"
	"github.com/DigiBugCat/duck/internal/paths"
)

// SyncStatus describes the outcome of syncing a single path.
type SyncStatus int

const (
	SyncCreated SyncStatus = iota // a new mutagen session was created
	SyncAlready                   // a session for this path already existed
)

// SyncResult records what happened to one path during a get/sync.
type SyncResult struct {
	Tilde   string
	Session string
	Status  SyncStatus
}

// ErrLocalNonEmpty is returned by SyncPath/GetBundle when the local target
// directory exists and is non-empty and force is false. Callers can detect it
// (errors.As) to offer a "force overlay" retry.
type ErrLocalNonEmpty struct{ Local string }

func (e ErrLocalNonEmpty) Error() string {
	return fmt.Sprintf("local path %s exists and is non-empty; refuse to overlay (use force to override)", e.Local)
}

// ErrHubNonEmpty is returned by AddPath when the path's natural location on the
// hub already exists and is non-empty and force is false. Adding would start a
// two-way sync that merges the local folder into existing hub data, so the
// caller should confirm before proceeding. On the merge path duck first runs a
// per-file NEWEST-WINS rsync seed (see internal/reconcile) and only then
// AddPath(force=true), so the merge keeps the newest copy of each file rather
// than letting either side win wholesale.
type ErrHubNonEmpty struct{ Path string }

func (e ErrHubNonEmpty) Error() string {
	return fmt.Sprintf("hub already has %s with files; adding merges into it — use force to proceed (duck merges newest-wins per file)", e.Path)
}

// NewBundle creates a new empty bundle on the hub.
func NewBundle(addr, name string) error {
	if err := hub.ValidateBundleName(name); err != nil {
		return err
	}
	return hub.New(addr).CreateBundle(name)
}

// AddPath registers a local directory with a bundle on the hub and starts a
// Mutagen sync session for it. In mirror mode the hub keeps the data at the
// path's natural location; if that location already holds files and force is
// false, AddPath returns ErrHubNonEmpty rather than silently merging into it.
// On a mutagen failure it rolls back the hub-side record so the hub does not
// accumulate orphaned entries. Returns the created entry and its session name.
// ignores are extra mutagen ignore patterns applied to this session (e.g. to
// keep a credential file out of the sync).
func AddPath(addr, bundle, rawLocalPath string, force bool, ignores ...string) (hub.PathEntry, string, error) {
	if err := hub.ValidateBundleName(bundle); err != nil {
		return hub.PathEntry{}, "", err
	}

	expanded, err := paths.Expand(rawLocalPath)
	if err != nil {
		return hub.PathEntry{}, "", err
	}
	expanded, err = filepath.Abs(expanded)
	if err != nil {
		return hub.PathEntry{}, "", err
	}
	info, err := os.Stat(expanded)
	if err != nil {
		return hub.PathEntry{}, "", fmt.Errorf("local path: %w", err)
	}
	if !info.IsDir() {
		return hub.PathEntry{}, "", fmt.Errorf("only directory paths are supported (got %s)", expanded)
	}
	tildePath := paths.Contract(expanded)
	if err := hub.ValidatePath(tildePath); err != nil {
		return hub.PathEntry{}, "", err
	}

	h := hub.New(addr)
	exists, err := h.BundleExists(bundle)
	if err != nil {
		return hub.PathEntry{}, "", err
	}
	if !exists {
		return hub.PathEntry{}, "", fmt.Errorf("bundle %q does not exist on hub. run: duck sync new %s", bundle, bundle)
	}

	// Guard against silently merging into existing hub data (symmetric with the
	// non-empty-local refusal in SyncPath/get).
	if !force {
		nonEmpty, err := h.RemoteDirNonEmpty(tildePath)
		if err != nil {
			return hub.PathEntry{}, "", err
		}
		if nonEmpty {
			return hub.PathEntry{}, "", ErrHubNonEmpty{Path: tildePath}
		}
	}

	entry, err := h.AddPath(bundle, tildePath)
	if err != nil {
		return hub.PathEntry{}, "", err
	}

	sessionName := paths.SessionName(bundle, tildePath)
	hubEndpoint := fmt.Sprintf("%s:%s", addr, hub.RemoteSyncPath(tildePath))
	if err := mutagen.Create(sessionName, expanded, hubEndpoint, ignores); err != nil {
		// Best-effort rollback so the hub doesn't accumulate orphaned records.
		_ = h.RemovePath(bundle, tildePath)
		return hub.PathEntry{}, "", fmt.Errorf("starting mutagen session: %w", err)
	}
	return entry, sessionName, nil
}

// RemovePath removes a path from a bundle on the hub and terminates this
// machine's sync session for it. The local files are not deleted. The local
// session is torn down first (best-effort) so a hub-side removal failure leaves
// no dangling session pointing at a path the hub no longer tracks. A non-fatal
// failure to terminate the local session is returned as warn (the removal still
// proceeds), so callers can surface it without treating it as an error.
func RemovePath(addr, bundle, tildePath string) (warn string, err error) {
	if err := hub.ValidateBundleName(bundle); err != nil {
		return "", err
	}
	// Terminate is a no-op for a session that doesn't exist, so a path that was
	// never synced here removes cleanly.
	if terr := mutagen.Terminate(paths.SessionName(bundle, tildePath)); terr != nil {
		warn = fmt.Sprintf("terminating local session: %v", terr)
	}
	return warn, hub.New(addr).RemovePath(bundle, tildePath)
}

// GetBundle pulls every path in a bundle onto this machine, starting a Mutagen
// sync session for each. It returns a per-path result for the paths processed
// before any error. An empty (nil) result with a nil error means the bundle
// exists but has no paths.
func GetBundle(addr, bundle string, force bool) ([]SyncResult, error) {
	if err := hub.ValidateBundleName(bundle); err != nil {
		return nil, err
	}
	h := hub.New(addr)
	exists, err := h.BundleExists(bundle)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("bundle %q does not exist on hub", bundle)
	}
	entries, err := h.ListPaths(bundle)
	if err != nil {
		return nil, err
	}
	results := make([]SyncResult, 0, len(entries))
	for _, e := range entries {
		st, err := SyncPath(addr, bundle, e, force)
		if err != nil {
			return results, err
		}
		results = append(results, SyncResult{Tilde: e.TildePath, Session: paths.SessionName(bundle, e.TildePath), Status: st})
	}
	return results, nil
}

// SyncPath starts (or confirms) a Mutagen sync session for a single path.
// If the local target exists and is non-empty and force is false it returns
// ErrLocalNonEmpty. If a session already exists it returns SyncAlready without
// touching anything.
func SyncPath(addr, bundle string, e hub.PathEntry, force bool) (SyncStatus, error) {
	local, err := paths.Expand(e.TildePath)
	if err != nil {
		return SyncCreated, err
	}
	empty, err := paths.IsEmptyDir(local)
	if err != nil {
		return SyncCreated, err
	}
	if !empty && !force {
		return SyncCreated, ErrLocalNonEmpty{Local: local}
	}
	if err := os.MkdirAll(local, 0o755); err != nil {
		return SyncCreated, err
	}
	sessionName := paths.SessionName(bundle, e.TildePath)
	already, err := mutagen.Exists(sessionName)
	if err != nil {
		return SyncCreated, err
	}
	if already {
		return SyncAlready, nil
	}
	hubEndpoint := fmt.Sprintf("%s:%s", addr, hub.RemoteSyncPath(e.TildePath))
	if err := mutagen.Create(sessionName, local, hubEndpoint, nil); err != nil {
		return SyncCreated, fmt.Errorf("syncing %s: %w", e.TildePath, err)
	}
	return SyncCreated, nil
}

// DropPath terminates this machine's Mutagen session for a single path.
// Terminate is a no-op if no such session exists.
func DropPath(addr, bundle, tildePath string) error {
	return mutagen.Terminate(paths.SessionName(bundle, tildePath))
}
