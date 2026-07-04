// Package session is duck's remote tmux session manager. It drives the hub's
// tmux server over duck's multiplexed SSH (an injectable Runner satisfied by
// *sshx.Client), turning the high-level operations the flow and TUI need —
// List, New, Attach, Kill, Recent(dir), and per-session option get/set — into
// the `tmux …` command strings that run on the hub.
//
// A duck "session" IS a remote tmux session (DESIGN §3): the tmux name is a
// clean, tmux-legal internal id duck assigns (`foo`, `foo-2`, …); the display
// name is resolved elsewhere (internal/names). Liveness / attached / age /
// window count are read live from `tmux list-sessions -F …` and never stored.
//
// Every method that talks to the hub returns an error so failures propagate
// (matching the hub/actions convention). Attach is the exception: it hands the
// process off via sshx.ExecAttach and returns only on a failure to exec.
package session

import (
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/DigiBugCat/duck/internal/paths"
)

// Runner is the injectable SSH seam: the subset of *sshx.Client that session
// needs. Tests swap a fake that records the constructed tmux command strings;
// production passes a real *sshx.Client.
type Runner interface {
	Run(remoteCmd string) (string, error)
	RunInput(remoteCmd string, stdin io.Reader) (string, error)
}

// Attacher hands the process off to an interactive tmux attach over SSH. It is
// the subset of *sshx.Client used by Attach; split out so the dispatch seam is
// independently fakeable. On success the process image is replaced and the call
// does not return.
type Attacher interface {
	ExecAttach(tmuxSession string) error
	// RunAttach is the subprocess variant of ExecAttach: it inherits the TTY,
	// blocks until the user detaches/exits, and returns control to the caller so
	// post-attach cleanup (the fresh-untouched-session kill) can run.
	RunAttach(tmuxSession string) error
}

// Sess is a live remote tmux session as read from the hub. It is the source
// data internal/names resolves into a display label and internal/model turns
// into a Row. Nothing here is persisted.
type Sess struct {
	Name       string    // internal tmux session id (e.g. "foo", "foo-2")
	Dir        string    // tilde-form working dir from the @duck_dir option
	Attached   bool      // a client is currently attached
	LastActive time.Time // session_activity, for recency ranking
	Windows    int       // window count
	Looped     bool      // the @duck_loop option is set — the session is running a /loop; the picker pins these at the top
	PaneTitle  string    // active pane's #{pane_title} — Claude Code writes a task summary here (with a ✳/⠂ status glyph), which names.Resolve prefers over codex
	PanelOf    string    // @duck_panel_of — set on a `duck panel` companion session; non-empty means "plumbing, hide from picker/ls"
}

// dirOption is the tmux user option duck stamps on each session it creates so
// Recent(dir) and the picker can map a session back to its working directory.
const dirOption = "@duck_dir"

// loopOption is the tmux user option that marks a session as running a /loop. It
// is NOT set by duck itself (duck has no view into Claude Code's loop state); a
// hook on the hub's Claude Code stamps it (`tmux set-option @duck_loop 1`) when a
// loop is active and clears it when the loop ends. duck only READS it, so an
// unset option (the common case) simply means "not looped" and pins nothing —
// the feature degrades to a no-op until the marker is wired.
const loopOption = "@duck_loop"

// Manager performs remote tmux operations over an injected Runner. The zero
// value is not usable; construct with New.
type Manager struct {
	run    Runner
	attach Attacher
}

// NewManager returns a Manager driving the hub through the given Runner and
// Attacher (both satisfied by one *sshx.Client).
func NewManager(run Runner, attach Attacher) *Manager {
	return &Manager{run: run, attach: attach}
}

// listFormat is the tmux list-sessions -F template. The fields are tab-joined
// so a display name (resolved elsewhere) never breaks parsing; @duck_dir may be
// empty for non-duck sessions. The order is the contract with parseList.
//
//	name \t @duck_dir \t attached \t activity-epoch \t windows \t @duck_loop \t @duck_panel_of \t pane_title
//
// pane_title is last because it is free text (Claude Code's task summary) that
// may itself contain odd characters; trailing it keeps the earlier fields
// unambiguous. @duck_loop and @duck_panel_of sit just before it (controlled
// markers — a "1"/empty flag and a tmux session id — so they never carry a
// tab) and the free-text title stays the trailing field. tmux resolves
// #{pane_title} to the active pane of the active window for the session.
const listFormat = "#{session_name}\t#{@duck_dir}\t#{session_attached}\t#{session_activity}\t#{session_windows}\t#{@duck_loop}\t#{@duck_panel_of}\t#{pane_title}"

// List returns every live tmux session on the hub, parsed from a single
// `tmux list-sessions -F …` call (name, dir option, attached, activity,
// windows). An empty server yields an empty slice and a nil error: tmux exits
// non-zero with "no server running" when nothing is up, which is normal.
func (m *Manager) List() ([]Sess, error) {
	out, err := m.run.Run(fmt.Sprintf("tmux list-sessions -F '%s'", listFormat))
	if err != nil {
		// An EMPTY hub is normal: tmux exits non-zero with "no server running"
		// when nothing is up, and we absorb that into an empty slice. But a real
		// transport failure (ssh can't reach the hub, auth rejected, network down)
		// also exits non-zero — absorbing THAT into empty+nil would make `duck
		// clean` print "no detached sessions" on a dead hub (silent false success).
		// So we only swallow the recognized no-server signature and PROPAGATE every
		// other error. sshx folds the remote stderr into the error string, so the
		// signature lands in err.Error(); we also check stdout for belt-and-braces.
		if isNoServer(err.Error()) || isNoServer(out) {
			return []Sess{}, nil
		}
		return nil, err
	}
	return parseList(out), nil
}

// isNoServer reports whether s carries tmux's empty-server signature. tmux emits
// one of two forms when no server is up, depending on build/platform: the usual
// "no server running on <socket>" (or just "no server running"), and — on a box
// where the server has NEVER started, so the socket file is absent — "error
// connecting to <socket> (No such file or directory)". Both mean the same thing:
// nothing is running yet, so duck should treat the hub as empty and start the
// first session. Matched as substrings so the socket path is ignored. The second
// form is anchored on "error connecting to" so it can't be confused with an
// unrelated ENOENT from a real transport failure.
// IsNoServer is the exported form of isNoServer for other packages that read
// tmux directly (internal/channel drives the LOCAL server) and must treat an
// empty/never-started server as a quiet no-op rather than an error.
func IsNoServer(s string) bool { return isNoServer(s) }

func isNoServer(s string) bool {
	return strings.Contains(s, "no server running") ||
		(strings.Contains(s, "error connecting to") && strings.Contains(s, "No such file or directory"))
}

// parseList turns the tab/newline list-sessions output into Sess values.
func parseList(out string) []Sess {
	var sessions []Sess
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 5 {
			continue
		}
		s := Sess{
			Name:     fields[0],
			Dir:      fields[1],
			Attached: fields[2] != "0" && fields[2] != "",
		}
		if secs, err := strconv.ParseInt(strings.TrimSpace(fields[3]), 10, 64); err == nil {
			s.LastActive = time.Unix(secs, 0)
		}
		if w, err := strconv.Atoi(strings.TrimSpace(fields[4])); err == nil {
			s.Windows = w
		}
		// @duck_loop and pane_title are optional (older list output / tests emit only
		// 5 fields); a missing field just leaves them zero so Resolve falls through
		// and nothing is pinned. @duck_loop is a controlled marker: any non-empty,
		// non-"0" value means looped (a hook stamps "1").
		if len(fields) >= 6 {
			loop := strings.TrimSpace(fields[5])
			s.Looped = loop != "" && loop != "0"
		}
		if len(fields) >= 8 {
			s.PanelOf = strings.TrimSpace(fields[6])
			s.PaneTitle = fields[7]
		} else if len(fields) >= 7 {
			// Older 7-field output (no @duck_panel_of): title is the trailing field.
			s.PaneTitle = fields[6]
		}
		sessions = append(sessions, s)
	}
	return sessions
}

// New creates a detached tmux session named id whose working directory is the
// hub path of dir (`tmux new-session -d -s <id> -c <hub-path>`), then stamps
// @duck_dir with the RAW tilde-form dir so the session maps back to it. dir is
// tilde-form; the -c target is the hub-real path ($HOME/...) since tmux's -c
// does not expand a leading ~.
func (m *Manager) New(id, dir string) error {
	if _, err := m.run.Run(fmt.Sprintf("tmux new-session -d -s %s -c %s", paths.Quote(id), hubPath(dir))); err != nil {
		return err
	}
	if err := m.SetOption(id, dirOption, dir); err != nil {
		return err
	}
	return m.enableTitlePassthrough(id)
}

// titlesString is the outer-terminal tab title tmux renders while a duck
// session is attached: the live #{pane_title} (Claude Code's task summary)
// when a program has written one, falling back to the session name when the
// pane title is still the terminal default (a bare shell leaves it at the
// hostname — see names.CleanTitle, which makes the same distinction).
const titlesString = "#{?#{==:#{pane_title},#{host}},#{session_name},#{pane_title}}"

// enableTitlePassthrough turns on tmux's title forwarding for session id so
// escape-coded titles written INSIDE the session (Claude Code's live task
// summary) reach the outer terminal's tab. Without set-titles, tmux captures
// them into #{pane_title} and the tab keeps whatever was last written locally.
func (m *Manager) enableTitlePassthrough(id string) error {
	if _, err := m.run.Run(fmt.Sprintf("tmux set-option -t %s set-titles on", paths.Quote(id))); err != nil {
		return err
	}
	_, err := m.run.Run(fmt.Sprintf("tmux set-option -t %s set-titles-string %s", paths.Quote(id), paths.Quote(titlesString)))
	return err
}

// Send types a command line into session id's active pane and presses Enter
// (`tmux send-keys -t <id> <line> Enter`), exactly as the eviction-revival path
// types `claude --resume <id>`. line is the literal shell command to run in the
// pane; callers shell-quote any arguments inside it. Used by `duck claude` to
// launch claude inside a freshly-minted duck session.
func (m *Manager) Send(id, line string) error {
	_, err := m.run.Run(fmt.Sprintf("tmux send-keys -t %s %s Enter",
		paths.Quote(id), paths.Quote(line)))
	return err
}

// Attach hands the current process off to an interactive `tmux attach -t <id>`
// over the warmed control-master socket. Callers MUST fully tear down any local
// TUI first so ssh/tmux own a clean TTY. Returns only on a failure to exec.
func (m *Manager) Attach(id string) error {
	// Best-effort heal: sessions created before title passthrough existed (or
	// by hand) get it on their next attach. Never blocks the attach itself.
	_ = m.enableTitlePassthrough(id)
	return m.attach.ExecAttach(id)
}

// AttachAndWait hands the current process to an interactive `tmux attach -t
// <id>` as a SUBPROCESS (inheriting the TTY) and BLOCKS until the user detaches
// or exits, returning control to the caller. Unlike Attach (syscall.Exec, never
// returns) it is the variant the bare-`duck` FRESH-session path uses so duck
// can, after the user leaves, check IsUntouched and clean up an untouched
// session. Returns nil on a normal interactive exit.
func (m *Manager) AttachAndWait(id string) error {
	_ = m.enableTitlePassthrough(id) // same best-effort heal as Attach
	return m.attach.RunAttach(id)
}

// untouchedFormat is the single `tmux display-message` template IsUntouched
// queries: window count | pane count | the pane's current command | the pane's
// scrollback history size. The order is the contract with IsUntouched's parse.
const untouchedFormat = "#{session_windows}|#{window_panes}|#{pane_current_command}|#{history_size}"

// loginShells are the interactive login shells a FRESH, never-touched session's
// single pane sits at. A different pane_current_command means the user launched
// a program (claude, vim, …) — i.e. the session was worked in.
var loginShells = map[string]bool{"zsh": true, "bash": true, "sh": true, "fish": true}

// IsUntouched reports whether session id was created and then LEFT without the
// user running anything in it: exactly one window, one pane, the pane still at a
// login shell (zsh/bash/sh/fish), and an empty scrollback (history_size==0, so
// nothing scrolled and no program launched). It issues a SINGLE tmux
// display-message query (untouchedFormat). If the session no longer exists (the
// user exited the shell, so tmux already killed it) there is nothing to clean
// up: that is folded into (false, nil), mirroring List's no-server handling.
func (m *Manager) IsUntouched(id string) (bool, error) {
	out, err := m.run.Run(fmt.Sprintf("tmux display-message -p -t %s %s", paths.Quote(id), paths.Quote(untouchedFormat)))
	if err != nil {
		// A gone session (exited) yields tmux's "can't find session" / no-server
		// signature — nothing to clean up, so untouched is irrelevant: (false, nil).
		if isNoServer(err.Error()) || isNoServer(out) || isNoSession(err.Error()) || isNoSession(out) {
			return false, nil
		}
		return false, err
	}
	fields := strings.Split(strings.TrimRight(out, "\r\n"), "|")
	if len(fields) != 4 {
		return false, nil
	}
	windows := strings.TrimSpace(fields[0])
	panes := strings.TrimSpace(fields[1])
	cmd := filepath.Base(strings.TrimSpace(fields[2]))
	history := strings.TrimSpace(fields[3])
	return windows == "1" && panes == "1" && loginShells[cmd] && history == "0", nil
}

// isNoSession reports whether s carries tmux's "can't find session" signature,
// emitted when display-message targets a session that no longer exists (the
// user exited it). Matched as a substring.
func isNoSession(s string) bool {
	return strings.Contains(s, "can't find session")
}

// DirExists reports whether the hub-real path of dir (tilde-form) exists as a
// directory on the hub. The bare-`duck` no-sync path uses it to decide where to
// open a session: a known dir that is NOT being synced still needs a valid hub
// cwd, so a missing dir falls back to home. Mirrors hub.BundleExists' probe.
func (m *Manager) DirExists(dir string) (bool, error) {
	out, err := m.run.Run(fmt.Sprintf("test -d %s && echo yes || echo no", hubPath(dir)))
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "yes", nil
}

// Kill terminates the tmux session named id (`tmux kill-session -t <id>`).
// Forgetting its names.json entry is the caller's responsibility.
func (m *Manager) Kill(id string) error {
	_, err := m.run.Run(fmt.Sprintf("tmux kill-session -t %s", paths.Quote(id)))
	return err
}

// Recent returns the most-recently-active session whose @duck_dir equals dir
// (tilde-form), or ok=false when this dir has no sessions. It is the engine
// behind `duck -c`. Implemented over List (filter by dir, max LastActive).
func (m *Manager) Recent(dir string) (s Sess, ok bool, err error) {
	sessions, err := m.List()
	if err != nil {
		return Sess{}, false, err
	}
	for _, c := range sessions {
		if c.Dir != dir {
			continue
		}
		if !ok || c.LastActive.After(s.LastActive) {
			s, ok = c, true
		}
	}
	return s, ok, nil
}

// HasSession reports whether a live tmux session named id currently exists on
// the hub. Implemented over List (an empty/dead hub yields false, nil) so the
// per-terminal memory can verify a remembered session is still resumable and
// prune a stale entry. A transport failure propagates as a non-nil error.
func (m *Manager) HasSession(id string) (bool, error) {
	sessions, err := m.List()
	if err != nil {
		return false, err
	}
	for _, s := range sessions {
		if s.Name == id {
			return true, nil
		}
	}
	return false, nil
}

// Option reads a tmux user option (e.g. @duck_dir) for session id
// (`tmux show-options -t <id> -v <name>`). ok=false when the option is unset.
func (m *Manager) Option(id, name string) (val string, ok bool, err error) {
	out, err := m.run.Run(fmt.Sprintf("tmux show-options -t %s -v %s", paths.Quote(id), paths.Quote(name)))
	if err != nil {
		return "", false, err
	}
	val = strings.TrimRight(out, "\n")
	if val == "" {
		return "", false, nil
	}
	return val, true, nil
}

// SetOption sets a tmux user option for session id
// (`tmux set-option -t <id> <name> <value>`).
func (m *Manager) SetOption(id, name, value string) error {
	_, err := m.run.Run(fmt.Sprintf("tmux set-option -t %s %s %s", paths.Quote(id), paths.Quote(name), paths.Quote(value)))
	return err
}

// DeriveID slugifies the base of a tilde-form dir into a tmux-legal session id,
// avoiding `.`/`:` (DESIGN risk #4). The display name stays raw elsewhere; this
// id is internal only. Callers append `-<n>` to disambiguate collisions.
func DeriveID(dir string) string {
	base := dir
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	var b strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			// '.', ':', spaces, and any other tmux-unfriendly char collapse to '-'.
			b.WriteRune('-')
		}
	}
	id := strings.Trim(b.String(), "-")
	if id == "" {
		return "duck"
	}
	return id
}

// hubPath returns a hub-real path expression for a tilde-form dir, suitable as
// a tmux `-c` target. tmux does not expand a leading ~, so a tilde path becomes
// "$HOME/...". Absolute paths pass through. The dir portion is single-quoted to
// keep it out of the remote shell's reach.
func hubPath(dir string) string {
	if dir == "~" {
		return `"$HOME"`
	}
	if strings.HasPrefix(dir, "~/") {
		return `"$HOME"/` + paths.Quote(strings.TrimPrefix(dir, "~/"))
	}
	return paths.Quote(dir)
}
