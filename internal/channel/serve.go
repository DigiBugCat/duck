// serve.go is the Claude Code channel sidecar (`duck channel serve`): a
// minimal MCP stdio server that multiplexes a workspace's sidebar agents
// into ONE Claude channel (unscoped = every workspace on the machine, for
// motherduck). Claude launches it via .mcp.json +
// `claude --channels server:duck-agents --dangerously-load-development-channels`
// (the feature is a research preview, hence the flag).
//
// One big channel, metadata does the routing: each pushed event carries
// {session, agent, type} in _meta, and the single `reply` tool takes the
// agent (and session) to route the answer back via send-keys. Agents appear
// and disappear at runtime freely — the launch-time channel never changes.
//
// The MCP surface is small enough to hand-roll (NDJSON JSON-RPC over stdio:
// initialize, tools/list, tools/call, plus outbound
// notifications/claude/channel), which keeps duck dependency-free.
package channel

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/DigiBugCat/duck/internal/panel"
)

// sweepEvery is the cadence for discovering agents and draining their
// rollouts. Local file reads + one tmux list — cheap. A var (not const) so
// tests can shorten it.
var sweepEvery = 2 * time.Second

// maxPush caps a pushed event's content so a huge final message can't blow
// up the supervisor's context.
const maxPush = 2000

// Serve runs the channel sidecar until stdin closes (Claude exiting kills
// us). run drives the local tmux server; in production it is
// panel.ExecRunner and rw is stdin/stdout.
//
// workspace scopes the sweep to ONE duck session's agents — the down edge of
// the org chart: a manager hears its own lot, not every workspace on the
// machine. Empty means machine-wide (motherduck / explicit --all).
func Serve(run panel.Runner, workspace string, in io.Reader, out io.Writer) error {
	s := &server{run: run, workspace: workspace, out: out, offsets: map[string]int64{}, resolver: NewResolver(run), stop: make(chan struct{}), started: time.Now().Truncate(time.Second)}
	watchDone := make(chan struct{})
	go func() { defer close(watchDone); s.watch() }()
	// Stop the watcher when stdin closes (Claude exited) and wait for it to
	// actually exit before returning, so no goroutine outlives Serve.
	defer func() { close(s.stop); <-watchDone }()
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		s.handle(line)
	}
	return sc.Err()
}

type server struct {
	run       panel.Runner
	workspace string // sweep only this duck session's agents ("" = machine-wide)
	out       io.Writer
	resolver  *Resolver     // memoized pairing/status — watch goroutine only
	stop      chan struct{} // closed when Serve returns; stops the watch goroutine
	started   time.Time     // sidecar start; panes spawned after this drain from 0

	mu      sync.Mutex       // guards out and ready (watcher + handler both touch them)
	offsets map[string]int64 // rollout → drained byte offset; watch goroutine only
	ready   bool             // initialize handshake done — notifications may flow
}

func (s *server) write(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Fprintln(s.out, string(b))
}

// instructions is the system-prompt blurb Claude gets at launch: what the
// events mean and how to reply — scoped to the manager's own workspace when
// the sidecar is.
func (s *server) instructions() string {
	scope := "duck sidebar agents (codex etc.)"
	if s.workspace != "" {
		scope = "YOUR sidebar agents (workspace " + s.workspace + " — you are its manager)"
	}
	return "Events from " + scope + " arrive as <channel source=\"duck-agents\"> " +
		"with meta {session, agent, type}. task_complete means the agent finished a turn. " +
		"To answer or give the agent its next instruction, call the reply tool with that session+agent."
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func (s *server) reply(id json.RawMessage, result any) {
	s.write(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (s *server) replyErr(id json.RawMessage, code int, msg string) {
	s.write(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": msg}})
}

func (s *server) handle(line []byte) {
	var req request
	if json.Unmarshal(line, &req) != nil {
		return
	}
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		if p.ProtocolVersion == "" {
			p.ProtocolVersion = "2025-06-18"
		}
		s.reply(req.ID, map[string]any{
			"protocolVersion": p.ProtocolVersion,
			"capabilities": map[string]any{
				"experimental": map[string]any{"claude/channel": map[string]any{}},
				"tools":        map[string]any{},
			},
			"serverInfo":   map[string]any{"name": "duck-agents", "version": "0.1.0"},
			"instructions": s.instructions(),
		})
	case "notifications/initialized":
		s.mu.Lock()
		s.ready = true
		s.mu.Unlock()
	case "tools/list":
		required := []string{"session", "agent", "message"}
		sessionDesc := "duck session owning the agent (from event meta)"
		if s.workspace != "" {
			required = []string{"agent", "message"}
			sessionDesc = "duck session owning the agent (default: your workspace, " + s.workspace + ")"
		}
		s.reply(req.ID, map[string]any{"tools": []any{map[string]any{
			"name":        "reply",
			"description": "Send a message to a duck sidebar agent (typed into its TUI, visible in the viewport).",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"session": map[string]any{"type": "string", "description": sessionDesc},
					"agent":   map[string]any{"type": "string", "description": "agent name (from event meta)"},
					"message": map[string]any{"type": "string"},
				},
				"required": required,
			},
		}}})
	case "tools/call":
		var p struct {
			Name string `json:"name"`
			Args struct {
				Session, Agent, Message string
			} `json:"arguments"`
		}
		if json.Unmarshal(req.Params, &p) != nil || p.Name != "reply" {
			s.replyErr(req.ID, -32602, "unknown tool")
			return
		}
		if p.Args.Session == "" {
			p.Args.Session = s.workspace
		}
		ref, err := FindAgent(s.run, p.Args.Session, p.Args.Agent)
		if err == nil {
			err = Send(s.run, ref, p.Args.Message)
		}
		if err != nil {
			s.reply(req.ID, map[string]any{"content": []any{map[string]any{"type": "text", "text": "send failed: " + err.Error()}}, "isError": true})
			return
		}
		s.reply(req.ID, map[string]any{"content": []any{map[string]any{"type": "text", "text": "delivered to " + p.Args.Agent}}})
	case "ping":
		s.reply(req.ID, map[string]any{})
	default:
		if req.ID != nil {
			s.replyErr(req.ID, -32601, "method not found: "+req.Method)
		}
	}
}

// watch sweeps agents and pushes fresh signal events as channel
// notifications. A rollout first seen for a pane that predates the sidecar
// starts at its current end (history is the transcript's business, not the
// channel's); one paired to a pane spawned under this sidecar drains from the
// start, so a first turn that finishes before pairing still notifies.
// task_started / task_complete push, per-step agent commentary does not.
func (s *server) watch() {
	for {
		select {
		case <-s.stop:
			return
		case <-time.After(sweepEvery):
		}
		s.mu.Lock()
		ready := s.ready
		s.mu.Unlock()
		if !ready {
			continue
		}
		// Publish lane (scoped): mark ourselves alive and drain the spool for
		// our one workspace. Done BEFORE the tmux listing below so a spooled
		// event is delivered even when the lot is empty or a list errors —
		// publishes are independent of whether any agent exists.
		if s.workspace != "" {
			s.drainPublish(s.workspace)
		}
		owners, err := Companions(s.run)
		if err != nil {
			continue
		}
		// Publish lane (--all): a publisher always targets a specific
		// workspace, so touch+drain every workspace that exists.
		if s.workspace == "" {
			for _, outer := range owners {
				s.drainPublish(outer)
			}
		}
		keep := map[string]bool{}
		for _, outer := range owners {
			if s.workspace != "" && outer != s.workspace {
				continue // another workspace's lot — not this manager's to hear
			}
			agents, err := panel.Agents(s.run, outer)
			if err != nil {
				continue // raced away — next sweep
			}
			for _, a := range agents {
				keep[a.PaneID] = true
				rollout := s.resolver.Rollout(a.PaneID)
				if rollout == "" {
					continue
				}
				s.drain(AgentRef{Session: outer, Name: a.Name, WindowID: a.PaneID, Rollout: rollout})
			}
		}
		s.resolver.Forget(keep)
	}
}

// drainPublish marks the workspace's sidecar alive and flushes its publish
// spool, emitting each spooled event as a channel notification. Called only
// from watch() after ready, so events published before the handshake stay
// spooled (the rename in DrainSpool only fires here) and are delivered on the
// first post-ready sweep — never lost.
func (s *server) drainPublish(workspace string) {
	if workspace == "" {
		return
	}
	_ = TouchAlive(workspace)
	events, err := DrainSpool(workspace)
	if err != nil || len(events) == 0 {
		return
	}
	for _, ev := range events {
		meta := map[string]any{"session": workspace}
		typ := "publish"
		for k, v := range ev.Meta {
			meta[k] = v
			if k == "type" {
				typ = v
			}
		}
		meta["session"] = workspace // authoritative — a publisher can't spoof it
		meta["type"] = typ
		s.write(map[string]any{
			"jsonrpc": "2.0",
			"method":  "notifications/claude/channel",
			"params": map[string]any{
				"content": ev.Content,
				"meta":    meta,
			},
		})
	}
}

// pushTypes are the events worth interrupting a supervisor for.
var pushTypes = map[string]bool{"task_started": true, "task_complete": true}

func (s *server) drain(ref AgentRef) {
	off, seen := s.offsets[ref.Rollout]
	if !seen {
		// First sight of this rollout. A pane spawned AFTER the sidecar
		// started is a fresh agent whose pairing simply lagged its first turn
		// (codex creates the rollout lazily; the resolver backs off between
		// pairing attempts) — its whole file is signal, drain from the start
		// so a fast first turn's task_complete isn't swallowed as "history".
		// A pane older than us really does carry history: start at the end.
		if at, err := windowSpawnedAt(s.run, ref.WindowID); err == nil && !at.Before(s.started) {
			off = 0
		} else if info, err := os.Stat(ref.Rollout); err == nil {
			off = info.Size()
		} else {
			return
		}
	}
	var buf strings.Builder
	newOff, _ := Tail(&buf, ref.Rollout, off, false, false)
	s.offsets[ref.Rollout] = newOff
	var events []Event
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var ev Event
		if json.Unmarshal([]byte(line), &ev) != nil || !pushTypes[ev.Type] {
			continue
		}
		events = append(events, ev)
	}
	for i, ev := range events {
		// A task_started whose task_complete drained in the same sweep is
		// noise — the turn is already over; push only the completion.
		if ev.Type == "task_started" {
			superseded := false
			for _, later := range events[i+1:] {
				if later.Type == "task_complete" {
					superseded = true
					break
				}
			}
			if superseded {
				continue
			}
		}
		msg := ev.Message
		if len(msg) > maxPush {
			// Cut on bytes, then drop any rune the cut split — invalid UTF-8 must
			// never reach the MCP JSON payload.
			msg = strings.ToValidUTF8(msg[:maxPush], "") + " …[truncated]"
		}
		content := fmt.Sprintf("[%s/%s] %s", ref.Session, ref.Name, ev.Type)
		if msg != "" {
			content += ": " + msg
		}
		s.write(map[string]any{
			"jsonrpc": "2.0",
			"method":  "notifications/claude/channel",
			"params": map[string]any{
				"content": content,
				"meta":    map[string]any{"session": ref.Session, "agent": ref.Name, "type": ev.Type},
			},
		})
	}
}
