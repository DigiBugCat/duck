// Package channel is the bidirectional integration for workspace agents
// (internal/tmuxdb): structured events OUT, prompts IN — with the minimum
// moving parts. There is no daemon and no registry: tmux window options carry
// all state, and the event stream is the rollout file codex already writes
// for every session (~/.codex/sessions/…/rollout-<ts>-<uuid>.jsonl).
//
//	out: Tail streams an agent's rollout JSONL (what the human typed, what a
//	     supervisor injected, everything codex did — one merged transcript).
//	in:  Send types into the agent's TUI via send-keys, so an injected prompt
//	     is VISIBLE in the agent's pane: one conversation, two participants.
//
// Pairing a window with its rollout is heuristic-once-then-pinned: at spawn
// tmuxdb stamps @duck_spawned_at; Resolve scans recent rollouts for a
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

	"github.com/DigiBugCat/duck/internal/tmuxdb"
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

// AgentRef identifies one workspace agent for channel operations.
type AgentRef struct {
	Session  string // the workspace session the agent belongs to
	Name     string // window name (agent label)
	WindowID string // tmux window id, e.g. "@7"
	Rollout  string // resolved rollout path; empty if not (yet) paired
	// SpawnedAt is the pane's spawn stamp, set by Resolve when it attempts a
	// fresh pairing (zero for cached hits and pairing-ineligible panes). The
	// Resolver uses it to retry YOUNG codex panes every sweep instead of
	// every retryEvery — a fresh agent's first turn shouldn't wait 15s for
	// its channel to attach.
	SpawnedAt time.Time
}

// FindAgent locates an agent pane in the outer session by REFERENCE,
// which may be (in precedence order): a tmux pane id (%NN or @NN — the stable
// handle `duck spawn` prints, unambiguous even when agents share a name); a
// codex thread id (the rollout's trailing UUID, from an event's meta.thread);
// or the agent's display name. Name resolution is last so an explicit id always
// wins over a colliding label.
func FindAgent(run tmuxdb.Runner, outer, ref string) (AgentRef, error) {
	agents, err := tmuxdb.Agents(run, outer)
	if err != nil {
		return AgentRef{}, err
	}
	// A pane id is exact — match it first, before any name could shadow it.
	if strings.HasPrefix(ref, "%") || strings.HasPrefix(ref, "@") {
		for _, a := range agents {
			if a.PaneID == ref {
				return AgentRef{Session: outer, Name: a.Name, WindowID: a.PaneID}, nil
			}
		}
	}
	// A thread id resolves via each pane's paired rollout — the stream identity,
	// independent of the mutable name label. Gated on the ref LOOKING like a
	// thread id so the common name lookup never pays the per-agent Resolve cost.
	if looksLikeThreadID(ref) {
		for _, a := range agents {
			r := AgentRef{Session: outer, Name: a.Name, WindowID: a.PaneID}
			_ = Resolve(run, &r)
			if r.Rollout != "" && threadID(r.Rollout) == ref {
				return r, nil
			}
		}
	}
	for _, a := range agents {
		if a.Name == ref {
			return AgentRef{Session: outer, Name: a.Name, WindowID: a.PaneID}, nil
		}
	}
	return AgentRef{}, fmt.Errorf("no agent %q in session %s (see: duck channel ls)", ref, outer)
}

// Resolve pairs ref's window with its codex rollout file, caching the result
// in the window's @duck_rollout option. Windows that never ran codex (plain
// shells, builds) resolve to empty with no error — they have send-keys but no
// structured stream.
func Resolve(run tmuxdb.Runner, ref *AgentRef) error {
	if out, err := run("show-options", "-p", "-t", ref.WindowID, "-v", tmuxdb.RolloutOption); err == nil {
		if v := strings.TrimSpace(out); v != "" {
			ref.Rollout = v
			return nil
		}
	}
	// Pairing is cwd+time correlation, so ONLY panes actually launched as
	// codex may attempt it — otherwise a preview/shell/carbonyl pane spawned
	// in the same directory adopts a neighboring agent's rollout (the duck-2
	// misattribution). Panes predating the @duck_cmd stamp read as empty and
	// simply never pair fresh; anything they pinned earlier is honored above.
	if cmd, err := run("show-options", "-p", "-t", ref.WindowID, "-v", tmuxdb.CmdOption); err != nil || !cmdRunsCodex(cmd) {
		return err
	}
	spawnedAt, err := windowSpawnedAt(run, ref.WindowID)
	if err != nil {
		return err
	}
	ref.SpawnedAt = spawnedAt
	dir, err := run("display-message", "-p", "-t", ref.WindowID, "#{pane_current_path}")
	if err != nil {
		return err
	}
	path, err := matchRollout(SessionsDir(), strings.TrimSpace(dir), spawnedAt, claimedRollouts(run))
	if err != nil || path == "" {
		return err
	}
	ref.Rollout = path
	_, _ = run("set-option", "-p", "-t", ref.WindowID, tmuxdb.RolloutOption, path)
	return nil
}

// HandleNotify is `duck channel notify <json>` — codex's notify hook, wired
// in by `duck spawn`. Codex execs it at end of turn, INSIDE the agent's pane
// environment, with a payload carrying the thread id (= the rollout session
// id). That makes attribution exact: decode the thread id, locate the rollout
// file directly, pin it on $TMUX_PANE. No cwd+time correlation, no scanning —
// matchRollout survives only as the fallback for codex started outside duck.
//
// It deliberately pins WITHOUT publishing the event: serve's sweep drains the
// (now-paired) rollout within 2s, so pushing here too would double-deliver.
func HandleNotify(run tmuxdb.Runner, paneID, payload string) error {
	if paneID == "" {
		return nil // notify fired outside tmux — nothing to attribute
	}
	var p struct {
		Type        string `json:"type"`
		ThreadID    string `json:"thread-id"`
		LastMessage string `json:"last-assistant-message"`
	}
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return fmt.Errorf("notify payload: %w", err)
	}
	if p.Type != "agent-turn-complete" || p.ThreadID == "" {
		return nil
	}
	if path := rolloutByThreadID(SessionsDir(), p.ThreadID); path != "" {
		// Unconditional pin: the process itself says so — this also heals a
		// wrong pairing the correlation fallback might have guessed earlier.
		_, _ = run("set-option", "-p", "-t", paneID, tmuxdb.RolloutOption, path)
	}
	return nil
}

// HandleHook is `duck channel hook <json>` — codex's SessionStart hook, wired in
// by duck spawn. codex runs it INSIDE the agent's pane at the FIRST turn (not
// process start — verified), with a payload carrying session_id AND
// transcript_path (the exact rollout file, handed over directly — no
// rolloutByThreadID lookup, no matchRollout guess). Combined with $TMUX_PANE
// (the pane the hook runs in), this pins the EXACT pane↔rollout↔session binding
// RACE-FREE — even for N concurrent same-cwd spawns, each hook fires in its own
// pane's process. This is what makes the fan-out scramble impossible at the
// source; matchRollout survives only as the fallback for non-hooked/legacy panes.
//
// MUST be fast + non-blocking: codex BLOCKS on the hook with a 60s timeout, so
// this does only local option stamps (no rollout scans, no sweeps). It does NOT
// publish an event — serve's sweep drains the now-paired rollout within ~2s.
//
// Only source=="startup" binds. resume/clear/compact re-fire SessionStart with
// the same session_id; re-stamping the same value is an idempotent no-op, so
// they are harmless, but gating on startup keeps the intent clear and avoids a
// compact mid-life re-bind masquerading as a birth.
func HandleHook(run tmuxdb.Runner, paneID, payload string) error {
	if paneID == "" {
		return nil // hook fired outside tmux — nothing to attribute
	}
	var p struct {
		Event          string `json:"hook_event_name"`
		Source         string `json:"source"`
		SessionID      string `json:"session_id"`
		TranscriptPath string `json:"transcript_path"`
		TurnID         string `json:"turn_id"`
	}
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return fmt.Errorf("hook payload: %w", err)
	}
	switch p.Event {
	case "SessionStart":
		if p.SessionID == "" {
			return nil
		}
		// Step 1 of the turn lifecycle: bind pane↔session. Stamp the durable
		// session id (resume/fork handle) and the exact rollout. Idempotent.
		_, _ = run("set-option", "-p", "-t", paneID, tmuxdb.SessionOption, p.SessionID)
		if p.TranscriptPath != "" && p.Source == "startup" {
			_, _ = run("set-option", "-p", "-t", paneID, tmuxdb.RolloutOption, p.TranscriptPath)
		}
	case "UserPromptSubmit":
		// Step 2: a prompt was actually SUBMITTED (a turn began). turn_id is unique
		// per submit, so Send confirms delivery by watching @duck_last_prompt
		// CHANGE — this fires on EVERY turn (spawn's first AND reply/resume's later
		// ones), unlike SessionStart which only fires once. This is the ground-truth
		// "the Enter landed" signal, replacing fragile composer inspection.
		if p.TurnID != "" {
			_, _ = run("set-option", "-p", "-t", paneID, tmuxdb.PromptOption, p.TurnID)
		}
	}
	return nil
}

// looksLikeThreadID reports whether ref has the 8-4-4-4-12 hyphenated UUID shape
// of a codex thread id — a cheap gate so FindAgent only pays per-agent rollout
// resolution when the reference could actually be a thread id.
func looksLikeThreadID(ref string) bool {
	groups := strings.Split(ref, "-")
	if len(groups) != 5 {
		return false
	}
	for _, want := range [5]int{8, 4, 4, 4, 12} {
		g := groups[0]
		groups = groups[1:]
		if len(g) != want {
			return false
		}
		for _, c := range g {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
				return false
			}
		}
	}
	return true
}

// threadID extracts the codex thread id (the trailing UUID of a rollout
// filename `rollout-<ts>-<uuid>.jsonl`) — a stable 1:1 key for the STREAM that
// produced an event, unlike @duck_name (a mutable pane label). Attribution uses
// it so a supervisor keys on the actual conversation, not on whatever name the
// pane happens to carry. Returns "" for a non-rollout path.
func threadID(rolloutPath string) string {
	base := strings.TrimSuffix(filepath.Base(rolloutPath), ".jsonl")
	if !strings.HasPrefix(base, "rollout-") {
		return ""
	}
	// The uuid is the last 5 hyphen-groups (8-4-4-4-12). Splitting and taking
	// the tail is simpler and format-stable than a regex.
	parts := strings.Split(base, "-")
	if len(parts) < 5 {
		return ""
	}
	return strings.Join(parts[len(parts)-5:], "-")
}

// rolloutByThreadID locates root/YYYY/MM/DD/rollout-*-<id>.jsonl without
// walking the tree: codex thread ids are UUIDv7, whose first 48 bits are the
// creation time in unix ms — that names the date partition directly. The
// neighboring days are tried too (UTC-vs-local partitioning drift).
func rolloutByThreadID(root, threadID string) string {
	if root == "" || len(threadID) < 13 {
		return ""
	}
	ms, err := strconv.ParseUint(strings.ReplaceAll(threadID, "-", "")[:12], 16, 64)
	if err != nil {
		return ""
	}
	day := time.UnixMilli(int64(ms)).Local()
	for _, d := range []int{0, -1, 1} {
		dir := filepath.Join(root, day.AddDate(0, 0, d).Format("2006/01/02"))
		if m, _ := filepath.Glob(filepath.Join(dir, "rollout-*-"+threadID+".jsonl")); len(m) > 0 {
			return m[0]
		}
	}
	return ""
}

// cmdRunsCodex reports whether a spawn cmdline launches codex: some token's
// path basename is exactly "codex" (covers "codex", "codex --model x",
// "/usr/local/bin/codex resume"), without false-positiving on incidental
// substrings ("gosling render codex-notes.html"). Tokens are unwrapped from
// shell quoting first — `duck spawn` stamps the paths.Quote'd line, so the
// stored token is 'codex', not codex.
func cmdRunsCodex(cmdline string) bool {
	for _, tok := range strings.Fields(cmdline) {
		if filepath.Base(strings.Trim(tok, `'"`)) == "codex" {
			return true
		}
	}
	return false
}

// claimedRollouts collects every rollout already pinned in some pane's
// @duck_rollout, machine-wide. A claimed rollout is off-limits to fresh
// pairing: two codex agents sharing a cwd must never adopt the same stream.
func claimedRollouts(run tmuxdb.Runner) map[string]bool {
	claimed := map[string]bool{}
	out, err := run("list-panes", "-a", "-F", "#{"+tmuxdb.RolloutOption+"}")
	if err != nil {
		return claimed
	}
	for _, line := range strings.Split(out, "\n") {
		if p := strings.TrimSpace(line); p != "" {
			claimed[p] = true
		}
	}
	return claimed
}

func windowSpawnedAt(run tmuxdb.Runner, windowID string) (time.Time, error) {
	out, err := run("show-options", "-p", "-t", windowID, "-v", tmuxdb.SpawnedAtOption)
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
// closest after (spawnedAt - slack). Rollouts in claimed (already pinned by
// another pane) are skipped. Empty when none match yet (codex still
// starting) — callers retry.
//
// AMBIGUITY: cwd+time correlation cannot tell two codex agents launched in the
// same directory near-simultaneously apart — both see the same unclaimed
// candidates and would adopt the SAME (earliest) stream, cross-attributing each
// other's events (the fan-out scramble). So when 2+ unclaimed candidates match
// the same cwd, matchRollout returns empty and refuses to guess: HandleNotify's
// exact thread-id pin (fired on first-turn-complete) resolves it correctly, and
// serve.drain replays from offset 0 for post-sidecar panes, so nothing is lost —
// pairing is merely delayed until it can be made unambiguously. One candidate is
// unambiguous and pairs immediately as before.
func matchRollout(root, dir string, spawnedAt time.Time, claimed map[string]bool) (string, error) {
	if root == "" {
		return "", nil
	}
	var best string
	var bestTS time.Time
	candidates := 0
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
		if !strings.HasSuffix(path, ".jsonl") || claimed[path] {
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
		candidates++
		if best == "" || meta.ts.Before(bestTS) {
			best, bestTS = path, meta.ts
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	// 2+ unclaimed same-cwd candidates: ambiguous — refuse to guess (see doc).
	if candidates > 1 {
		return "", nil
	}
	return best, nil
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
func Send(run tmuxdb.Runner, ref AgentRef, message string) error {
	// Pair with the rollout up front (best-effort): it is the authoritative
	// submit-confirmation signal below. Callers mostly pass unresolved refs.
	if ref.Rollout == "" {
		_ = Resolve(run, &ref)
	}
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
	// The sanity loop: press Enter until the submit is CONFIRMED. A multi-KB
	// paste can take codex seconds to ingest — during that window Enters are
	// swallowed while the pane looks momentarily clean, so a quick composer
	// glance false-positives (observed live). Extra Enters on an empty composer
	// are no-ops, so repetition is safe.
	//
	// GROUND TRUTH is codex's UserPromptSubmit hook, which fires on EVERY submit
	// (spawn's first turn AND reply/resume's later ones) and stamps a fresh
	// turn_id into @duck_last_prompt. So a genuine submit = @duck_last_prompt
	// CHANGED from its pre-send value. This needs no rollout (works at fresh
	// spawn, unlike task_started), and no composer heuristic. Fallbacks (for a
	// pane whose hook isn't wired — e.g. a user's own codex): the rollout's
	// task_started when paired, else composer inspection.
	prePrompt := paneOpt(run, ref.WindowID, tmuxdb.PromptOption)
	var rolloutFrom int64 = -1
	if ref.Rollout != "" {
		if fi, err := os.Stat(ref.Rollout); err == nil {
			rolloutFrom = fi.Size()
		}
	}
	for attempt := 0; attempt < 6; attempt++ {
		if _, err := run("send-keys", "-t", ref.WindowID, "Enter"); err != nil {
			return err
		}
		time.Sleep(time.Duration(500+attempt*500) * time.Millisecond)
		// Primary: a new UserPromptSubmit stamped a different turn_id.
		if cur := paneOpt(run, ref.WindowID, tmuxdb.PromptOption); cur != "" && cur != prePrompt {
			return nil
		}
		// Fallback A: paired rollout gained a task_started past the pre-send offset.
		if rolloutFrom >= 0 && taskStartedSince(ref.Rollout, rolloutFrom) {
			return nil
		}
		// Fallback B (last resort — hookless, unpaired pane): composer emptied.
		if rolloutFrom < 0 && submitted(run, ref.WindowID, message) {
			time.Sleep(400 * time.Millisecond)
			if submitted(run, ref.WindowID, message) {
				return nil
			}
		}
	}
	return fmt.Errorf("no submit confirmation after retries (pane %s)", ref.WindowID)
}

// paneOpt reads a pane user option, "" on any error or unset.
func paneOpt(run tmuxdb.Runner, windowID, opt string) string {
	out, err := run("show-options", "-p", "-t", windowID, "-v", opt)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// sleepFn is a package var so tests can stub the composer-await wait.
var sleepFn = time.Sleep

// SetSleepFn overrides the composer-await sleep and returns a restore func. For
// tests (in this and dependent packages) that drive AwaitComposer/SendWhenReady
// and must not block on real wall-clock waits.
func SetSleepFn(fn func(time.Duration)) (restore func()) {
	old := sleepFn
	sleepFn = fn
	return func() { sleepFn = old }
}

// AwaitComposer polls the pane until codex's composer prompt (›) is on screen,
// up to timeout. A freshly spawned TUI eats keystrokes during its first seconds
// of startup, so a caller sending the first turn must wait for readiness first.
// Attempt-bounded so a stubbed sleepFn finishes instantly in tests.
func AwaitComposer(run tmuxdb.Runner, paneID string, timeout time.Duration) bool {
	const every = 500 * time.Millisecond
	for i := 0; i < int(timeout/every)+1; i++ {
		if out, err := run("capture-pane", "-p", "-t", paneID); err == nil && strings.Contains(out, "›") {
			return true
		}
		sleepFn(every)
	}
	return false
}

// SendWhenReady awaits the agent's composer, then Sends. It is the correct entry
// point for delivering the FIRST turn to a just-spawned agent (one-call
// spawn+send); Send alone assumes a ready composer. If the composer never
// appears within the window it sends anyway (best-effort — a non-codex or
// slow-booting pane still gets the text rather than silently dropping it).
func SendWhenReady(run tmuxdb.Runner, ref AgentRef, message string) error {
	AwaitComposer(run, ref.WindowID, 15*time.Second)
	return Send(run, ref, message)
}

// taskStartedSince reports whether the rollout gained a task_started event
// past the given byte offset — the authoritative "the prompt submitted"
// signal (codex writes it the instant a turn begins).
func taskStartedSince(path string, from int64) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return false
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if ev, ok := ParseEvent(sc.Bytes()); ok && ev.Type == "task_started" {
			return true
		}
	}
	return false
}

// submitted reports whether the composer no longer holds the message: it
// captures the pane and looks for a composer line ("› …") still carrying the
// message tail or a pending-paste marker. The submitted prompt also appears
// in the transcript above, so only composer lines are inspected. Capture
// errors count as submitted — never loop on a broken pane.
func submitted(run tmuxdb.Runner, windowID, message string) bool {
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

// Workspaces lists every candidate workspace session on the local tmux
// server, so serve/ls can sweep all agents on this machine. Agents are panes
// of the workspace session itself, so every session qualifies except stale
// pre-teardown companions (@duck_panel_of set) — their live agents surface
// via the owning session's legacy sweep in tmuxdb.Agents. Keyed and valued
// by session name (the shape the sweeps iterate).
func Workspaces(run tmuxdb.Runner) (map[string]string, error) {
	out, err := run("list-sessions", "-F", "#{session_name}\t#{"+tmuxdb.PanelOfOption+"}")
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
		if len(f) >= 1 && strings.TrimSpace(f[0]) != "" && (len(f) < 2 || strings.TrimSpace(f[1]) == "") {
			owners[f[0]] = f[0]
		}
	}
	return owners, nil
}

// AllAgents sweeps every workspace for its agents, sorted by session then
// name, resolving rollouts best-effort.
func AllAgents(run tmuxdb.Runner) ([]AgentRef, error) {
	owners, err := Workspaces(run)
	if err != nil {
		return nil, err
	}
	var refs []AgentRef
	for _, outer := range owners {
		agents, err := tmuxdb.Agents(run, outer)
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
func StatusByWindow(run tmuxdb.Runner, windowID string) string {
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
	run tmuxdb.Runner
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

// youngAge is how long after spawn a codex pane retries pairing on EVERY
// sweep instead of every retryEvery: codex creates its rollout lazily, and a
// first turn can start and finish inside one retryEvery window — the channel
// must attach within a sweep or two, not up to 15s late.
const youngAge = 2 * time.Minute

// NewResolver returns a Resolver over the local tmux runner.
func NewResolver(run tmuxdb.Runner) *Resolver {
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
		if !ref.SpawnedAt.IsZero() && time.Since(ref.SpawnedAt) < youngAge {
			st.nextTry = time.Time{} // young codex pane: retry next sweep
		}
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
