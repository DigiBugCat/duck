// serve.go is the Claude Code channel sidecar (`duck channel serve`): a
// minimal MCP stdio server that multiplexes EVERY sidebar agent on this
// machine into ONE Claude channel. Claude launches it via .mcp.json +
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
// rollouts. Local file reads + one tmux list — cheap.
const sweepEvery = 2 * time.Second

// maxPush caps a pushed event's content so a huge final message can't blow
// up the supervisor's context.
const maxPush = 2000

// Serve runs the channel sidecar until stdin closes (Claude exiting kills
// us). run drives the local tmux server; in production it is
// panel.ExecRunner and rw is stdin/stdout.
func Serve(run panel.Runner, in io.Reader, out io.Writer) error {
	s := &server{run: run, out: out, offsets: map[string]int64{}, resolver: NewResolver(run)}
	go s.watch()
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
	run      panel.Runner
	out      io.Writer
	resolver *Resolver // memoized pairing/status — watch goroutine only

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
			"serverInfo": map[string]any{"name": "duck-agents", "version": "0.1.0"},
			"instructions": "Events from duck sidebar agents (codex etc.) arrive as <channel source=\"duck-agents\"> " +
				"with meta {session, agent, type}. task_complete means the agent finished a turn. " +
				"To answer or give the agent its next instruction, call the reply tool with that session+agent.",
		})
	case "notifications/initialized":
		s.mu.Lock()
		s.ready = true
		s.mu.Unlock()
	case "tools/list":
		s.reply(req.ID, map[string]any{"tools": []any{map[string]any{
			"name":        "reply",
			"description": "Send a message to a duck sidebar agent (typed into its TUI, visible in the viewport).",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"session": map[string]any{"type": "string", "description": "duck session owning the agent (from event meta)"},
					"agent":   map[string]any{"type": "string", "description": "agent name (from event meta)"},
					"message": map[string]any{"type": "string"},
				},
				"required": []string{"session", "agent", "message"},
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
// notifications. A rollout seen for the FIRST time starts at its current end
// (history is the transcript's business, not the channel's); task_started /
// task_complete / agent errors push, per-step agent commentary does not.
func (s *server) watch() {
	for {
		time.Sleep(sweepEvery)
		s.mu.Lock()
		ready := s.ready
		s.mu.Unlock()
		if !ready {
			continue
		}
		owners, err := Companions(s.run)
		if err != nil {
			continue
		}
		keep := map[string]bool{}
		for comp, outer := range owners {
			agents, err := panel.Agents(s.run, comp)
			if err != nil {
				continue // companion raced away — next sweep
			}
			for _, a := range agents {
				keep[a.WindowID] = true
				rollout := s.resolver.Rollout(a.WindowID)
				if rollout == "" {
					continue
				}
				s.drain(AgentRef{Session: outer, Name: a.Name, WindowID: a.WindowID, Rollout: rollout})
			}
		}
		s.resolver.Forget(keep)
	}
}

// pushTypes are the events worth interrupting a supervisor for.
var pushTypes = map[string]bool{"task_started": true, "task_complete": true}

func (s *server) drain(ref AgentRef) {
	off, seen := s.offsets[ref.Rollout]
	if !seen {
		if info, err := os.Stat(ref.Rollout); err == nil {
			s.offsets[ref.Rollout] = info.Size()
		}
		return
	}
	var buf strings.Builder
	newOff, _ := Tail(&buf, ref.Rollout, off, false, false)
	s.offsets[ref.Rollout] = newOff
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var ev Event
		if json.Unmarshal([]byte(line), &ev) != nil || !pushTypes[ev.Type] {
			continue
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
