// Package mutagen wraps the `mutagen` CLI for duck's user-facing `duck sync`
// feature. Ported from flok/internal/mutagen/mutagen.go with the milestone's
// key fixes:
//
//   - Create uses `-m two-way-resolved` (the modern flag) NOT the removed
//     `--sync-mode` flag, which mutagen 0.18.1 rejects.
//   - `mutagen daemon start` is invoked idempotently at the top of Create, so a
//     fresh machine (no running daemon) succeeds on the first sync.
//   - The session-name prefix and List() filter are duck- (matching
//     paths.SessionName) so duck and flok sessions never collide on a shared
//     daemon.
//   - A package-level runner var (runVar/outputVar) is the test seam: unit
//     tests record the constructed argv (and call order) and assert the flag
//     fix + daemon-start ordering without invoking the real mutagen binary.
//   - +Monitor and +Flush are added for later milestones.
package mutagen

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// DefaultIgnores are appended to every duck-managed sync session. They cover
// the noisy build/cache directories that show up in most projects. Note we do
// NOT enable Mutagen's VCS-ignore mode: duck deliberately syncs .git so a
// repo's history follows the folder to the hub (and back), instead of the hub
// copy silently losing its git state.
var DefaultIgnores = []string{
	"node_modules",
	".venv",
	"venv",
	"__pycache__",
	"dist",
	"build",
	"target",
	".DS_Store",
	".idea",
	".vscode",
}

// listTemplate is a pipe-delimited per-session template. The order of fields
// here is the public contract between Mutagen and our parser.
// Fields: name | status | alpha-spec | beta-spec
// where each spec is "<protocol>|<user>|<host>|<path>".
const listTemplate = `{{range .}}{{.Name}}|{{.Status}}|{{.Alpha.Protocol}}|{{.Alpha.User}}|{{.Alpha.Host}}|{{.Alpha.Path}}|{{.Beta.Protocol}}|{{.Beta.User}}|{{.Beta.Host}}|{{.Beta.Path}}` + "\n" + `{{end}}`

// Session is a minimal view of a Mutagen sync session.
type Session struct {
	Name   string
	Status string
	Alpha  Endpoint
	Beta   Endpoint
}

// Endpoint describes one side of a sync session.
type Endpoint struct {
	Protocol string // "Local", "SSH", etc.
	User     string
	Host     string
	Path     string
}

// Display returns a human-readable form (path for local, user@host:path for SSH).
func (e Endpoint) Display() string {
	if e.Host == "" {
		return e.Path
	}
	if e.User != "" {
		return fmt.Sprintf("%s@%s:%s", e.User, e.Host, e.Path)
	}
	return fmt.Sprintf("%s:%s", e.Host, e.Path)
}

// MatchesHub reports whether this endpoint points at the hub addressed by addr
// (a duck hub address: "user@host", a bare "host", or an ssh alias). It is how
// duck tells a live sync — one whose beta IS the current hub — apart from a
// STALE session left pointing at a previous hub after a migration. The compare
// is host-primary: the user must match only when addr names one (so a bare
// "pelican" still matches a beta of andrew@pelican), which is enough to
// distinguish two different hubs while tolerating how the address was written.
func (e Endpoint) MatchesHub(addr string) bool {
	user, host := "", addr
	if at := strings.LastIndex(addr, "@"); at >= 0 {
		user, host = addr[:at], addr[at+1:]
	}
	if e.Host != host {
		return false
	}
	return user == "" || e.User == user
}

// runVar / outputVar are the test seam. Tests swap them to record argv and call
// order and to inject failures; production delegates to realRun / realOutput.
var (
	runVar    = realRun
	outputVar = realOutput
)

// SetRunner swaps the mutagen run seam for tests in other internal packages
// (e.g. actions) that need to drive Create/Terminate without invoking the real
// mutagen binary. It returns a restore func. mutagen is an internal package, so
// exporting this seam costs no public API surface.
func SetRunner(f func(args ...string) error) func() {
	orig := runVar
	runVar = f
	return func() { runVar = orig }
}

// SetOutputForTest swaps the mutagen output seam (used by all()/List()/Exists)
// for tests in other internal packages. Returns a restore func.
func SetOutputForTest(f func(args ...string) (string, error)) func() {
	orig := outputVar
	outputVar = f
	return func() { outputVar = orig }
}

// EnsureDaemon idempotently starts the mutagen daemon. `mutagen daemon start`
// is a no-op (exit 0) if the daemon is already running, so this is safe to call
// before every Create. It is the fix for a fresh machine where the daemon has
// never been started and the first `sync create` would otherwise fail.
func EnsureDaemon() error {
	return runVar("daemon", "start")
}

// Create starts a new bidirectional sync session.
// alpha and beta are Mutagen endpoint strings (local path or user@host:path).
//
// The daemon is started first (idempotently), then the session is created with
// the modern `-m two-way-resolved` mode flag (NOT the removed `--sync-mode`).
func Create(name, alpha, beta string, extraIgnores []string) error {
	if err := EnsureDaemon(); err != nil {
		return err
	}
	args := []string{
		"sync", "create",
		"--name", name,
		"-m", "two-way-resolved",
	}
	for _, ig := range append(append([]string{}, DefaultIgnores...), extraIgnores...) {
		args = append(args, "--ignore", ig)
	}
	args = append(args, alpha, beta)
	return runVar(args...)
}

// Terminate stops and removes a sync session by name. Missing sessions are not an error.
func Terminate(name string) error {
	exists, err := Exists(name)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	return runVar("sync", "terminate", name)
}

// Flush forces an immediate synchronization cycle for a session and waits for
// it to complete.
func Flush(name string) error { return runVar("sync", "flush", name) }

// Monitor returns a one-shot status line for a session (mutagen monitor
// --no-color renders a single update and exits when given a session name and a
// terminating flag is not available across versions, so we read `sync list`).
func Monitor(name string) (Session, error) {
	sessions, err := all()
	if err != nil {
		return Session{}, err
	}
	for _, s := range sessions {
		if s.Name == name {
			return s, nil
		}
	}
	return Session{}, fmt.Errorf("session %q not found", name)
}

// Exists reports whether a session with the given name exists.
func Exists(name string) (bool, error) {
	sessions, err := all()
	if err != nil {
		return false, err
	}
	for _, s := range sessions {
		if s.Name == name {
			return true, nil
		}
	}
	return false, nil
}

// List returns sessions whose names start with "duck-".
func List() ([]Session, error) {
	sessions, err := all()
	if err != nil {
		return nil, err
	}
	var out []Session
	for _, s := range sessions {
		if strings.HasPrefix(s.Name, "duck-") {
			out = append(out, s)
		}
	}
	return out, nil
}

// all returns every session known to the local mutagen daemon.
func all() ([]Session, error) {
	out, err := outputVar("sync", "list", "--template", listTemplate)
	if err != nil {
		return nil, err
	}
	var sessions []Session
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "|")
		if len(fields) < 10 {
			continue
		}
		sessions = append(sessions, Session{
			Name:   fields[0],
			Status: fields[1],
			Alpha:  Endpoint{Protocol: fields[2], User: fields[3], Host: fields[4], Path: fields[5]},
			Beta:   Endpoint{Protocol: fields[6], User: fields[7], Host: fields[8], Path: fields[9]},
		})
	}
	return sessions, nil
}

func realRun(args ...string) error {
	cmd := exec.Command("mutagen", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("mutagen %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func realOutput(args ...string) (string, error) {
	cmd := exec.Command("mutagen", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("mutagen %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
