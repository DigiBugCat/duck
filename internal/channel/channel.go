// Package channel is the bidirectional integration for sidebar agents
// (internal/panel): structured events OUT, prompts IN — with the minimum
// moving parts. There is no daemon and no registry: tmux window options carry
// all state, and the event stream is the rollout file codex already writes
// for every session (~/.codex/sessions/…/rollout-<ts>-<uuid>.jsonl).
//
//	out: Tail streams an agent's rollout JSONL (what the human typed, what a
//	     supervisor injected, everything codex did — one merged transcript).
//	in:  Send types into the agent's TUI via send-keys, so an injected prompt
//	     is VISIBLE in the viewport: one conversation, two participants.
//
// Pairing a window with its rollout is heuristic-once-then-pinned: at spawn
// panel stamps @duck_spawned_at; Resolve scans recent rollouts for a
// session_meta whose cwd matches the window's pane path and whose timestamp
// is at/after the spawn instant, then caches the winner in @duck_rollout.
package channel

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DigiBugCat/duck/internal/panel"
	"github.com/DigiBugCat/duck/internal/session"
)

// spawnSlack tolerates clock skew between the spawn stamp and the rollout's
// session_meta timestamp.
const spawnSlack = 10 * time.Second

// SessionsDir returns the codex rollout root (~/.codex/sessions), overridable
// for tests via DUCK_CODEX_SESSIONS.
func SessionsDir() string {
	if d := os.Getenv("DUCK_CODEX_SESSIONS"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex", "sessions")
}

// AgentRef identifies one sidebar agent for channel operations.
type AgentRef struct {
	Session  string // outer duck session (companion owner)
	Name     string // window name (agent label)
	WindowID string // tmux window id, e.g. "@7"
	Rollout  string // resolved rollout path; empty if not (yet) paired
}

// FindAgent locates the named agent window in outer's companion session.
func FindAgent(run panel.Runner, outer, name string) (AgentRef, error) {
	agents, err := panel.Agents(run, outer)
	if err != nil {
		return AgentRef{}, err
	}
	for _, a := range agents {
		if a.Name == name {
			return AgentRef{Session: outer, Name: a.Name, WindowID: a.PaneID}, nil
		}
	}
	return AgentRef{}, fmt.Errorf("no agent %q in session %s (see: duck channel ls)", name, outer)
}

// Resolve pairs ref's window with its codex rollout file, caching the result
// in the window's @duck_rollout option. Windows that never ran codex (plain
// shells, builds) resolve to empty with no error — they have send-keys but no
// structured stream.
func Resolve(run panel.Runner, ref *AgentRef) error {
	if out, err := run("show-options", "-p", "-t", ref.WindowID, "-v", panel.RolloutOption); err == nil {
		if v := strings.TrimSpace(out); v != "" {
			ref.Rollout = v
			return nil
		}
	}
	spawnedAt, err := windowSpawnedAt(run, ref.WindowID)
	if err != nil {
		return err
	}
	dir, err := run("display-message", "-p", "-t", ref.WindowID, "#{pane_current_path}")
	if err != nil {
		return err
	}
	path, err := matchRollout(SessionsDir(), strings.TrimSpace(dir), spawnedAt)
	if err != nil || path == "" {
		return err
	}
	ref.Rollout = path
	_, _ = run("set-option", "-p", "-t", ref.WindowID, panel.RolloutOption, path)
	return nil
}

func windowSpawnedAt(run panel.Runner, windowID string) (time.Time, error) {
	out, err := run("show-options", "-p", "-t", windowID, "-v", panel.SpawnedAtOption)
	if err != nil {
		return time.Time{}, err
	}
	secs, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("window %s has no spawn stamp (spawned before duck channel existed?)", windowID)
	}
	return time.Unix(secs, 0), nil
}

// matchRollout scans rollout files modified after spawnedAt whose
// session_meta cwd equals dir, returning the one whose meta timestamp is
// closest after (spawnedAt - slack). Empty when none match yet (codex still
// starting) — callers retry.
func matchRollout(root, dir string, spawnedAt time.Time) (string, error) {
	if root == "" {
		return "", nil
	}
	var best string
	var bestTS time.Time
	// Rollouts are date-partitioned (root/YYYY/MM/DD, LOCAL date); a day-dir
	// wholly before the spawn day can't contain our rollout, so skip the
	// subtree instead of statting months of history on every scan. One extra
	// day of margin absorbs any local-vs-UTC partitioning drift.
	lt := spawnedAt.Add(-spawnSlack).Local()
	minDay := time.Date(lt.Year(), lt.Month(), lt.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if rel, rerr := filepath.Rel(root, path); rerr == nil {
				if day, perr := time.Parse("2006/01/02", filepath.ToSlash(rel)); perr == nil && day.Before(minDay) {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.ModTime().Before(spawnedAt.Add(-spawnSlack)) {
			return nil
		}
		meta, ok := readSessionMeta(path)
		if !ok || meta.cwd != dir || meta.ts.Before(spawnedAt.Add(-spawnSlack)) {
			return nil
		}
		if best == "" || meta.ts.Before(bestTS) {
			best, bestTS = path, meta.ts
		}
		return nil
	})
	return best, err
}

type sessionMeta struct {
	cwd string
	ts  time.Time
}

// readSessionMeta parses the first line of a rollout (the session_meta event).
func readSessionMeta(path string) (sessionMeta, bool) {
	f, err := os.Open(path)
	if err != nil {
		return sessionMeta{}, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	if !sc.Scan() {
		return sessionMeta{}, false
	}
	var line struct {
		Timestamp string `json:"timestamp"`
		Type      string `json:"type"`
		Payload   struct {
			Cwd string `json:"cwd"`
		} `json:"payload"`
	}
	if json.Unmarshal(sc.Bytes(), &line) != nil || line.Type != "session_meta" {
		return sessionMeta{}, false
	}
	ts, err := time.Parse(time.RFC3339Nano, line.Timestamp)
	if err != nil {
		return sessionMeta{}, false
	}
	return sessionMeta{cwd: line.Payload.Cwd, ts: ts}, true
}

// Event is one filtered rollout event — the subset a supervisor cares about.
type Event struct {
	Time    string `json:"time"`
	Type    string `json:"type"`    // user_message | agent_message | task_started | task_complete
	Message string `json:"message"` // the text (last_agent_message for task_complete)
}

// signalTypes are the event_msg payload types worth surfacing; everything
// else (token counts, patch internals, reasoning) is noise for a supervisor.
var signalTypes = map[string]bool{
	"user_message": true, "agent_message": true,
	"task_started": true, "task_complete": true,
}

// ParseEvent filters one rollout JSONL line down to an Event (ok=false for
// noise lines).
func ParseEvent(line []byte) (Event, bool) {
	var raw struct {
		Timestamp string `json:"timestamp"`
		Type      string `json:"type"`
		Payload   struct {
			Type             string `json:"type"`
			Message          string `json:"message"`
			LastAgentMessage string `json:"last_agent_message"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &raw) != nil || raw.Type != "event_msg" || !signalTypes[raw.Payload.Type] {
		return Event{}, false
	}
	msg := raw.Payload.Message
	if raw.Payload.Type == "task_complete" {
		msg = raw.Payload.LastAgentMessage
	}
	return Event{Time: raw.Timestamp, Type: raw.Payload.Type, Message: msg}, true
}

// Tail streams an agent's rollout to w. From skips everything before the
// given byte offset (use 0 for the full history). With follow, it polls for
// growth every 500ms until the file disappears or w errors; without, it
// returns at EOF. raw passes lines through unfiltered; otherwise only signal
// Events are written (as compact JSON). Returns the final offset.
func Tail(w io.Writer, path string, from int64, follow, raw bool) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return from, err
	}
	defer f.Close()
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return from, err
	}
	off := from
	rd := bufio.NewReaderSize(f, 1<<20)
	for {
		line, err := rd.ReadBytes('\n')
		if len(line) > 0 && err == nil {
			off += int64(len(line))
			if raw {
				if _, werr := w.Write(line); werr != nil {
					return off, werr
				}
			} else if ev, ok := ParseEvent(line); ok {
				b, _ := json.Marshal(ev)
				if _, werr := fmt.Fprintln(w, string(b)); werr != nil {
					return off, werr
				}
			}
			continue
		}
		// Partial line or EOF: stop (once) or wait for growth (follow).
		if !follow {
			return off, nil
		}
		time.Sleep(500 * time.Millisecond)
		if _, serr := os.Stat(path); serr != nil {
			return off, nil // rolled away / deleted — stream over
		}
		if _, serr := f.Seek(off, io.SeekStart); serr != nil {
			return off, serr
		}
		rd.Reset(f)
	}
}

// Send types message into the agent's pane and presses Enter — visible in the
// viewport, queued by codex's composer if a turn is mid-flight.
func Send(run panel.Runner, ref AgentRef, message string) error {
	// "--" ends flag parsing: a message starting with "-" must reach the pane
	// as literal text, not be eaten as a send-keys flag.
	if _, err := run("send-keys", "-t", ref.WindowID, "-l", "--", message); err != nil {
		return err
	}
	// A TUI receiving text and Enter in one burst treats it as a paste and
	// leaves the message sitting in the composer (verified against codex).
	// A short beat makes the Enter a distinct submit keystroke. Long messages
	// arrive as a bracketed paste the TUI takes longer to swallow, so the
	// beat scales with size; then VERIFY the composer emptied and retry the
	// Enter — a large paste eats the first Enter as part of the paste
	// (observed live, twice, with ~1KB prompts).
	beat := 250 * time.Millisecond
	if len(message) > 256 {
		beat = 750 * time.Millisecond
	}
	time.Sleep(beat)
	for attempt := 0; ; attempt++ {
		if _, err := run("send-keys", "-t", ref.WindowID, "Enter"); err != nil {
			return err
		}
		if attempt >= 2 {
			return nil // three Enters sent; stop guessing
		}
		time.Sleep(500 * time.Millisecond)
		if submitted(run, ref.WindowID, message) {
			return nil
		}
	}
}

// submitted reports whether the composer no longer holds the message: it
// captures the pane and looks for a composer line ("› …") still carrying the
// message tail or a pending-paste marker. The submitted prompt also appears
// in the transcript above, so only composer lines are inspected. Capture
// errors count as submitted — never loop on a broken pane.
func submitted(run panel.Runner, windowID, message string) bool {
	out, err := run("capture-pane", "-p", "-t", windowID)
	if err != nil {
		return true
	}
	tail := message
	if len(tail) > 24 {
		tail = tail[len(tail)-24:]
	}
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "›") {
			continue
		}
		if strings.Contains(trimmed, "Pasted Content") || strings.Contains(trimmed, tail) {
			return false
		}
	}
	return true
}

// Companions lists every outer session that currently has a companion, so
// serve/ls can sweep all agents on this machine. Implemented over the local
// tmux server: sessions whose @duck_panel_of is set map companion → outer.
func Companions(run panel.Runner) (map[string]string, error) {
	out, err := run("list-sessions", "-F", "#{session_name}\t#{@duck_panel_of}")
	if err != nil {
		// Both empty-server signatures (session.IsNoServer): no tmux at all is a
		// normal state for channel commands — quiet no-op, not an error.
		if session.IsNoServer(out) || session.IsNoServer(err.Error()) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	owners := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.SplitN(line, "\t", 2)
		if len(f) == 2 && strings.TrimSpace(f[1]) != "" {
			owners[f[0]] = strings.TrimSpace(f[1])
		}
	}
	return owners, nil
}

// AllAgents sweeps every companion for its agents, sorted by session then
// name, resolving rollouts best-effort.
func AllAgents(run panel.Runner) ([]AgentRef, error) {
	owners, err := Companions(run)
	if err != nil {
		return nil, err
	}
	var refs []AgentRef
	for _, outer := range owners {
		agents, err := panel.Agents(run, outer)
		if err != nil {
			continue // raced away — skip
		}
		for _, a := range agents {
			ref := AgentRef{Session: outer, Name: a.Name, WindowID: a.PaneID}
			_ = Resolve(run, &ref)
			refs = append(refs, ref)
		}
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Session != refs[j].Session {
			return refs[i].Session < refs[j].Session
		}
		return refs[i].Name < refs[j].Name
	})
	return refs, nil
}

// StatusByWindow classifies an agent's current state for the roster UI from
// the tail of its rollout:
//
//	working — a turn is open (task_started after the last task_complete)
//	done    — last turn finished (its final message is waiting to be read)
//	idle    — no codex stream, or no turns yet (fresh TUI at the composer)
//
// Reads at most the last statusWindow bytes, so polling stays cheap even on
// long sessions.
func StatusByWindow(run panel.Runner, windowID string) string {
	ref := AgentRef{WindowID: windowID}
	if err := Resolve(run, &ref); err != nil || ref.Rollout == "" {
		return "idle"
	}
	return statusFromFile(ref.Rollout)
}

// statusFromFile is the scan body of StatusByWindow, shared with
// Resolver.Status (which adds the size-unchanged shortcut).
func statusFromFile(rollout string) string {
	f, err := os.Open(rollout)
	if err != nil {
		return "idle"
	}
	defer f.Close()
	const statusWindow = 256 << 10
	start := int64(0)
	if info, err := f.Stat(); err == nil && info.Size() > statusWindow {
		start = info.Size() - statusWindow
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return "idle"
	}
	status := "idle"
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	if start > 0 {
		sc.Scan() // drop the partial line the seek landed in
	}
	for sc.Scan() {
		switch ev, ok := ParseEvent(sc.Bytes()); {
		case !ok:
		case ev.Type == "task_started":
			status = "working"
		case ev.Type == "task_complete":
			status = "done"
		}
	}
	return status
}

// Resolver memoizes window→rollout pairing and status scans for the two
// long-lived pollers (the roster TUI and `duck channel serve`), which
// otherwise re-run tmux subprocesses and rollout-tree scans every 2s per
// agent forever:
//
//   - a successful pairing is remembered for the process lifetime (it is also
//     pinned in the window option, but the memo skips even the show-options
//     subprocess);
//   - a FAILED pairing (shell agents; codex before its first turn, since the
//     TUI creates the rollout lazily) is retried at most every retryEvery
//     instead of every tick;
//   - a status scan is skipped entirely while the rollout's size is unchanged.
//
// One-shot commands (ls, tail, send) keep using the plain functions.
type Resolver struct {
	run panel.Runner
	mu  sync.Mutex
	m   map[string]*rstate
}

type rstate struct {
	rollout string
	nextTry time.Time // when an unresolved window may re-scan
	size    int64     // rollout size at last status scan
	status  string    // status as of size
}

// retryEvery throttles re-pairing attempts for windows with no rollout yet.
const retryEvery = 15 * time.Second

// NewResolver returns a Resolver over the local tmux runner.
func NewResolver(run panel.Runner) *Resolver {
	return &Resolver{run: run, m: map[string]*rstate{}}
}

// Rollout returns the window's rollout path ("" while unpaired), memoized.
func (r *Resolver) Rollout(windowID string) string {
	r.mu.Lock()
	st, ok := r.m[windowID]
	if !ok {
		st = &rstate{}
		r.m[windowID] = st
	}
	r.mu.Unlock()
	if st.rollout != "" {
		return st.rollout
	}
	if time.Now().Before(st.nextTry) {
		return ""
	}
	ref := AgentRef{WindowID: windowID}
	_ = Resolve(r.run, &ref)
	st.rollout = ref.Rollout
	if st.rollout == "" {
		st.nextTry = time.Now().Add(retryEvery)
	}
	return st.rollout
}

// Status classifies the window (working/done/idle), rescanning the rollout
// only when it has grown since the last scan.
func (r *Resolver) Status(windowID string) string {
	rollout := r.Rollout(windowID)
	if rollout == "" {
		return "idle"
	}
	r.mu.Lock()
	st := r.m[windowID]
	r.mu.Unlock()
	info, err := os.Stat(rollout)
	if err != nil {
		return "idle"
	}
	if st.status != "" && info.Size() == st.size {
		return st.status
	}
	st.status = statusFromFile(rollout)
	st.size = info.Size()
	return st.status
}

// Forget drops windows not in keep, so dead agents don't accumulate state.
func (r *Resolver) Forget(keep map[string]bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id := range r.m {
		if !keep[id] {
			delete(r.m, id)
		}
	}
}
