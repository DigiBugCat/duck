// Package hub holds the SSH operations against the canonical hub host. All
// state on the hub lives under ~/.duck/.
//
// Ported from flok/internal/hub/hub.go with three changes, all required by the
// duck design:
//
//  1. Every remote path ~/.flok/... -> ~/.duck/...; the ping token
//     flok-ok -> duck-ok; all error messages reference duck commands.
//  2. sshInput now carries the same Control* multiplexing flags as the rest of
//     duck's SSH (gap#6), so synchronous SSH calls on duck's critical path
//     reuse the warmed master socket instead of opening a fresh connection.
//  3. A package-level runner var (var runSSH) is introduced so unit tests can
//     assert on the constructed argv and inject failures without touching a
//     real host. ALL injection-hardening (shellQuote / ValidatePath /
//     ValidateBundleName / remoteShellPath) is preserved verbatim.
package hub

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/DigiBugCat/duck/internal/paths"
	"github.com/DigiBugCat/duck/internal/sshx"
)

// Hub represents the remote canonical host accessed over SSH.
// All state on the hub lives under ~/.duck/.
type Hub struct {
	Addr string // user@host
}

func New(addr string) *Hub { return &Hub{Addr: addr} }

var safeName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,63}$`)

// ValidateAddr rejects hub addresses that are unsafe as a bare positional in
// ssh / ssh-copy-id / rsync. The addr is used WITHOUT a `--` separator, so a
// leading dash would be parsed as an ssh option (e.g. "-oProxyCommand=…"), and
// embedded whitespace/control chars could split it into multiple args or corrupt
// a remote command. We therefore reject any leading "-" and any whitespace or
// control character, while staying permissive on host characters so user@host,
// a bare host, an ssh alias, and user@ip all pass. The addr is NEVER shell-
// interpolated unquoted elsewhere, so this positional-safety check is sufficient.
func ValidateAddr(addr string) error {
	if addr == "" {
		return fmt.Errorf("hub address is empty")
	}
	if strings.HasPrefix(addr, "-") {
		return fmt.Errorf("invalid hub address %q (must not start with '-'; it would be parsed as an ssh option)", addr)
	}
	for _, r := range addr {
		if r <= 0x20 || r == 0x7f {
			return fmt.Errorf("invalid hub address %q (must not contain whitespace or control characters)", addr)
		}
	}
	return nil
}

// ValidateBundleName rejects names that aren't safe to interpolate into shell
// commands or use as filesystem path components.
func ValidateBundleName(name string) error {
	if !safeName.MatchString(name) {
		return fmt.Errorf("invalid bundle name %q (allowed: letters, digits, _ . -; max 64 chars)", name)
	}
	return nil
}

// ValidatePath rejects tracked paths containing control characters (NUL,
// newlines, tabs, other C0 / DEL). They have no legitimate use in a path here,
// would corrupt the newline/tab-delimited hub records, and must never reach a
// remote shell. This is the path-side counterpart to ValidateBundleName.
func ValidatePath(p string) error {
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("path contains a control character; not supported")
		}
	}
	return nil
}

// runSSH is the seam tests swap to record the constructed argv / stdin and
// inject failures without contacting a real host. Production runs via
// exec.Command. The argv carries the full DUCKSSH flag set (gap#6).
var runSSH = func(argv []string, stdin io.Reader) (string, error) {
	// Ensure ~/.duck/cm exists before the multiplexed ssh binds its socket.
	if err := sshx.EnsureControlDir(); err != nil {
		return "", err
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ssh %s: %w: %s", argv[len(argv)-2], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// SetRunner swaps the SSH runner for tests in other internal packages (e.g.
// actions) that need to drive hub operations without contacting a real host. It
// returns a restore func. hub is an internal package, so exporting this seam
// costs no public API surface.
func SetRunner(f func(argv []string, stdin io.Reader) (string, error)) func() {
	orig := runSSH
	runSSH = f
	return func() { runSSH = orig }
}

// ssh runs a remote command and returns stdout. Stderr is included in the
// returned error message on failure.
func (h *Hub) ssh(remoteCmd string) (string, error) {
	return h.sshInput(remoteCmd, nil)
}

// sshInput runs a remote command, optionally feeding stdin to it, and returns
// stdout. Stderr is included in the returned error on failure. Streaming data
// in via stdin lets callers keep untrusted content (e.g. a user-supplied path)
// out of the remote command text entirely.
//
// The argv carries duck's Control* multiplexing flags (gap#6) so this path —
// including the synchronous bind on duck new — reuses the warmed master socket.
func (h *Hub) sshInput(remoteCmd string, stdin io.Reader) (string, error) {
	opts, err := sshx.Options()
	if err != nil {
		return "", err
	}
	argv := []string{"ssh"}
	argv = append(argv, opts...)
	// Wrap in a login shell so the hub's Homebrew PATH (brew/tmux/mutagen) is
	// present — same fix as sshx; a non-login ssh shell lacks /opt/homebrew/bin.
	argv = append(argv, h.Addr, sshx.LoginShellWrap(remoteCmd))
	return runSSH(argv, stdin)
}

// Run executes an arbitrary remote command over the multiplexed connection and
// returns its stdout. Used by `duck hub setup` for provisioning steps. The
// caller is responsible for any shell-quoting of untrusted content (setup's
// commands are duck-controlled).
func (h *Hub) Run(remoteCmd string) (string, error) {
	return h.ssh(remoteCmd)
}

// RunInput executes an arbitrary remote command, feeding stdin, and returns its
// stdout. Used by `duck hub setup` to stream the tmux.conf into `cat >` without
// escaping its contents.
func (h *Hub) RunInput(remoteCmd string, stdin io.Reader) (string, error) {
	return h.sshInput(remoteCmd, stdin)
}

// Ping verifies connectivity and that the remote shell is usable.
func (h *Hub) Ping() error {
	out, err := h.ssh("echo duck-ok")
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) != "duck-ok" {
		return fmt.Errorf("unexpected response from %s: %q", h.Addr, out)
	}
	return nil
}

// Hostname returns the remote machine's `hostname` output. Used as the
// friendly display name for the hub.
func (h *Hub) Hostname() (string, error) {
	out, err := h.ssh("hostname")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// DisplayName formats a hub for human display. If name is non-empty and
// differs from the host portion of addr, returns "name (host)"; otherwise
// just the host portion of addr.
func DisplayName(addr, name string) string {
	host := addr
	if i := strings.LastIndex(addr, "@"); i >= 0 {
		host = addr[i+1:]
	}
	if name == "" || name == host {
		return host
	}
	return fmt.Sprintf("%s (%s)", name, host)
}

// BundleExists returns true if the bundle directory exists on the hub.
func (h *Hub) BundleExists(name string) (bool, error) {
	if err := ValidateBundleName(name); err != nil {
		return false, err
	}
	out, err := h.ssh(fmt.Sprintf(`test -d ~/.duck/bundles/%s && echo yes || echo no`, name))
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "yes", nil
}

// CreateBundle creates the directory tree for a new bundle. Fails if it exists.
// Only duck's own metadata lives under ~/.duck; the synced data for each path
// lives at the path's natural location on the hub (see RemoteSyncPath).
func (h *Hub) CreateBundle(name string) error {
	if err := ValidateBundleName(name); err != nil {
		return err
	}
	exists, err := h.BundleExists(name)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("bundle %q already exists on hub", name)
	}
	_, err = h.ssh(fmt.Sprintf(`mkdir -p ~/.duck/bundles/%s/paths`, name))
	return err
}

// DestroyBundle removes the bundle directory tree on the hub.
// This does NOT remove paths on any machine; it only deletes the hub copy.
func (h *Hub) DestroyBundle(name string) error {
	if err := ValidateBundleName(name); err != nil {
		return err
	}
	_, err := h.ssh(fmt.Sprintf(`rm -rf ~/.duck/bundles/%s`, name))
	return err
}

// ListBundles returns the names of all bundles on the hub, sorted.
func (h *Hub) ListBundles() ([]string, error) {
	out, err := h.ssh(`[ -d ~/.duck/bundles ] && ls -1 ~/.duck/bundles 2>/dev/null || true`)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			names = append(names, line)
		}
	}
	sort.Strings(names)
	return names, nil
}

// PathEntry represents a single tracked path within a bundle.
type PathEntry struct {
	ID        string // sha256 prefix of the tilde-form path
	TildePath string // e.g. ~/dev/foo
}

// ListPaths returns all paths in a bundle.
func (h *Hub) ListPaths(bundle string) ([]PathEntry, error) {
	if err := ValidateBundleName(bundle); err != nil {
		return nil, err
	}
	// Print "<id>\t<tilde-path>" per entry. `find` handles missing/empty dirs
	// without shell-glob errors (zsh `nomatch` makes a bare `paths/*` fail when
	// the directory is empty, e.g. immediately after removing the last path).
	cmd := fmt.Sprintf(
		`find ~/.duck/bundles/%s/paths -maxdepth 1 -type f 2>/dev/null | while IFS= read -r f; do printf '%%s\t%%s\n' "$(basename "$f")" "$(cat "$f")"; done`,
		bundle,
	)
	out, err := h.ssh(cmd)
	if err != nil {
		return nil, err
	}
	var entries []PathEntry
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		entries = append(entries, PathEntry{ID: parts[0], TildePath: parts[1]})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].TildePath < entries[j].TildePath })
	return entries, nil
}

// AddPath registers a path with the bundle on the hub and ensures its natural
// location exists there. In mirror mode the synced data lives at that natural
// location (the same tilde path resolved against the hub's home), not in a
// duck-private store, so every machine — the hub included — holds the path at
// the same place. The actual sync is started by the caller via Mutagen.
func (h *Hub) AddPath(bundle, tildePath string) (PathEntry, error) {
	if err := ValidateBundleName(bundle); err != nil {
		return PathEntry{}, err
	}
	if err := ValidatePath(tildePath); err != nil {
		return PathEntry{}, err
	}
	if !strings.HasPrefix(tildePath, "~/") && tildePath != "~" && !filepath.IsAbs(tildePath) {
		return PathEntry{}, fmt.Errorf("path must be tilde-form (~/...) or absolute (/...): %s", tildePath)
	}
	id := paths.ID(tildePath)
	// Mutagen creates a sync root but not its parents, so ensure the natural
	// location exists. The mkdir target is single-quoted (remoteShellPath); the
	// bundle name is validated and the id is hex. The path RECORD is written by
	// streaming the path over stdin into `cat`, so the user-controlled path
	// never appears in the remote command text (a heredoc body is NOT safe: a
	// path line equal to the delimiter would terminate it and inject commands).
	cmd := fmt.Sprintf(
		`mkdir -p %s && mkdir -p ~/.duck/bundles/%s/paths && cat > ~/.duck/bundles/%s/paths/%s`,
		remoteShellPath(tildePath), bundle, bundle, id,
	)
	if _, err := h.sshInput(cmd, strings.NewReader(tildePath)); err != nil {
		return PathEntry{}, err
	}
	return PathEntry{ID: id, TildePath: tildePath}, nil
}

// RemovePath deletes the duck path record for a tracked path. The synced data
// lives at the path's natural location — the user's real folder on the hub — so
// it is deliberately NOT deleted; only the record is removed. The sync session
// must be torn down separately via Mutagen.
func (h *Hub) RemovePath(bundle, tildePath string) error {
	if err := ValidateBundleName(bundle); err != nil {
		return err
	}
	id := paths.ID(tildePath)
	_, err := h.ssh(fmt.Sprintf(`rm -f ~/.duck/bundles/%s/paths/%s`, bundle, id))
	return err
}

// RemoteSyncPath returns the Mutagen endpoint path for a tracked path: its
// natural location on the hub. A tilde path becomes a path relative to the
// remote home (Mutagen resolves relative endpoints against it), mirroring tilde
// semantics across machines; an absolute path maps to the same absolute path.
func RemoteSyncPath(tildePath string) string {
	if tildePath == "~" {
		return "."
	}
	if strings.HasPrefix(tildePath, "~/") {
		return strings.TrimPrefix(tildePath, "~/")
	}
	return tildePath
}

// RemoteDirNonEmpty reports whether the natural location of tildePath already
// exists on the hub and contains files. Used to guard against silently merging
// a local folder into existing hub data on add.
func (h *Hub) RemoteDirNonEmpty(tildePath string) (bool, error) {
	p := remoteShellPath(tildePath)
	out, err := h.ssh(fmt.Sprintf(`if [ -d %s ] && [ -n "$(ls -A %s 2>/dev/null)" ]; then echo nonempty; else echo empty; fi`, p, p))
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "nonempty", nil
}

// remoteShellPath returns a shell expression (for the remote login shell) that
// expands to the natural location of tildePath on the hub, with the
// user-controlled portion single-quoted to prevent shell injection.
func remoteShellPath(tildePath string) string {
	if tildePath == "~" {
		return `"$HOME"`
	}
	if strings.HasPrefix(tildePath, "~/") {
		return `"$HOME"/` + paths.Quote(strings.TrimPrefix(tildePath, "~/"))
	}
	return paths.Quote(tildePath)
}
