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
	"strings"

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

// AddPathHubOwned is AddPath for HUB-OWNED sync: the hub-side registry
// bookkeeping (and its rollback) is identical, but instead of creating a
// session on the local mutagen daemon it asks the duck binary on the hub
// (`duck hubsync add`, installed by `duck hub setup`) to create it on the
// HUB's daemon — alpha the hub-local path, beta this machine at machineAddr.
// The hub-side data location is the same natural location AddPath uses, so a
// folder migrated between ownership modes lands in the same place. The
// session name embeds the machine (paths.HubSessionName) so two laptops
// syncing the same tilde path never collide on the hub's daemon.
func AddPathHubOwned(addr, machineAddr, bundle, rawLocalPath string, force bool, ignores ...string) (hub.PathEntry, string, error) {
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

	sessionName := paths.HubSessionName(bundle, tildePath, machineAddr)
	if _, err := h.Run(HubsyncAddCmd(sessionName, tildePath, machineAddr, expanded, ignores)); err != nil {
		// Best-effort rollback so the hub doesn't accumulate orphaned records.
		_ = h.RemovePath(bundle, tildePath)
		return hub.PathEntry{}, "", fmt.Errorf("starting hub-owned mutagen session: %w", err)
	}
	return entry, sessionName, nil
}

// HubsyncAddCmd builds the remote `duck hubsync add` invocation. Every
// user-influenced value is single-quoted (paths.Quote) — the string runs under
// the hub's login shell.
func HubsyncAddCmd(sessionName, tildePath, machineAddr, localAbs string, ignores []string) string {
	cmd := fmt.Sprintf("duck hubsync add --name %s --hub-path %s --peer %s --peer-path %s",
		paths.Quote(sessionName), paths.Quote(tildePath), paths.Quote(machineAddr), paths.Quote(localAbs))
	for _, ig := range ignores {
		cmd += " --ignore " + paths.Quote(ig)
	}
	return cmd
}

// HubOwnedSessions lists the HUB-owned sessions that belong to the machine at
// machineAddr, mapped into that machine's perspective: each returned session
// carries the MACHINE-local path in Alpha (mirroring what a laptop-owned
// mutagen.List entry looks like, so coverage/containment/status code works
// unchanged on either ownership mode) and the true hub-side session name in
// Name. Beta holds the peer identity and the HUB path. Sessions for other
// machines are filtered out — every caller only ever cares about its own.
func HubOwnedSessions(addr, machineAddr string) ([]mutagen.Session, error) {
	out, err := hub.New(addr).Run("duck hubsync list")
	if err != nil {
		return nil, err
	}
	user, host := "", machineAddr
	if at := strings.LastIndex(machineAddr, "@"); at >= 0 {
		user, host = machineAddr[:at], machineAddr[at+1:]
	}
	var sessions []mutagen.Session
	for _, line := range strings.Split(out, "\n") {
		// name|status|alphaDisplay|betaDisplay|spec, beta as user@host:path.
		fields := strings.Split(strings.TrimSpace(line), "|")
		if len(fields) < 5 {
			continue
		}
		beta := fields[3]
		colon := strings.Index(beta, ":")
		if colon < 0 {
			continue // local beta — not a hub-owned duck session
		}
		peer, peerPath := beta[:colon], beta[colon+1:]
		peerUser, peerHost := "", peer
		if at := strings.LastIndex(peer, "@"); at >= 0 {
			peerUser, peerHost = peer[:at], peer[at+1:]
		}
		if peerHost != host || (user != "" && peerUser != "" && peerUser != user) {
			continue
		}
		sessions = append(sessions, mutagen.Session{
			Name:   fields[0],
			Status: fields[1],
			Alpha:  mutagen.Endpoint{Protocol: "Local", Path: peerPath},
			Beta:   mutagen.Endpoint{Protocol: "SSH", User: peerUser, Host: peerHost, Path: fields[2]},
		})
	}
	return sessions, nil
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

// GetBundleHubOwned is GetBundle for hub-owned sync: every created session
// lives on the HUB's daemon (beta = this machine at machineAddr).
func GetBundleHubOwned(addr, machineAddr, bundle string, force bool) ([]SyncResult, error) {
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
	// One ledger fetch up front (not per path): it is an SSH round-trip.
	owned, err := HubOwnedSessions(addr, machineAddr)
	if err != nil {
		return nil, err
	}
	have := make(map[string]bool, len(owned))
	for _, s := range owned {
		have[s.Name] = true
	}
	results := make([]SyncResult, 0, len(entries))
	for _, e := range entries {
		sessionName := paths.HubSessionName(bundle, e.TildePath, machineAddr)
		if have[sessionName] {
			results = append(results, SyncResult{Tilde: e.TildePath, Session: sessionName, Status: SyncAlready})
			continue
		}
		if err := syncPathHubOwned(addr, machineAddr, sessionName, e.TildePath, force); err != nil {
			return results, err
		}
		results = append(results, SyncResult{Tilde: e.TildePath, Session: sessionName, Status: SyncCreated})
	}
	return results, nil
}

// syncPathHubOwned mirrors SyncPath's local-side guards (non-empty refusal,
// mkdir) and then creates the session on the hub's daemon. The hub-side `add`
// is declarative, so re-running it against an existing matching session is a
// cheap no-op — callers pre-check the ledger only to report Already correctly.
func syncPathHubOwned(addr, machineAddr, sessionName, tildePath string, force bool) error {
	local, err := paths.Expand(tildePath)
	if err != nil {
		return err
	}
	empty, err := paths.IsEmptyDir(local)
	if err != nil {
		return err
	}
	if !empty && !force {
		return ErrLocalNonEmpty{Local: local}
	}
	if err := os.MkdirAll(local, 0o755); err != nil {
		return err
	}
	if _, err := hub.New(addr).Run(HubsyncAddCmd(sessionName, tildePath, machineAddr, local, nil)); err != nil {
		return fmt.Errorf("syncing %s: %w", tildePath, err)
	}
	return nil
}

// RemovePathHubOwned is RemovePath for hub-owned sync: this machine's session
// lives on the hub's daemon, so the terminate runs there (before the registry
// removal, mirroring RemovePath's ordering).
func RemovePathHubOwned(addr, machineAddr, bundle, tildePath string) (warn string, err error) {
	if err := hub.ValidateBundleName(bundle); err != nil {
		return "", err
	}
	h := hub.New(addr)
	if _, terr := h.Run(hubsyncTerminateCmd(paths.HubSessionName(bundle, tildePath, machineAddr))); terr != nil {
		warn = fmt.Sprintf("terminating hub-owned session: %v", terr)
	}
	return warn, h.RemovePath(bundle, tildePath)
}

// DropPathHubOwned terminates this machine's HUB-owned session for a single
// path (a missing session is a no-op on the hub side).
func DropPathHubOwned(addr, machineAddr, bundle, tildePath string) error {
	_, err := hub.New(addr).Run(hubsyncTerminateCmd(paths.HubSessionName(bundle, tildePath, machineAddr)))
	return err
}

// hubsyncTerminateCmd builds the remote `duck hubsync terminate` invocation.
func hubsyncTerminateCmd(sessionName string) string {
	return "duck hubsync terminate --name " + paths.Quote(sessionName)
}

// DropPath terminates this machine's Mutagen session for a single path.
// Terminate is a no-op if no such session exists.
func DropPath(addr, bundle, tildePath string) error {
	return mutagen.Terminate(paths.SessionName(bundle, tildePath))
}
