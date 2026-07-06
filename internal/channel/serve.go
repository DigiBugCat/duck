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

// Host backs the sidecar's action tools (spawn/resume/fork agents, preview/
// render artifacts, routine control). It is an interface (not direct calls) so
// internal/channel needn't import the packages those actions live in — several
// of which import channel back (a cycle). command wires the concrete host in via
// Serve. A nil host just omits the action tools (the reply-only server works).
//
// Agents: Launch spawns a NEW codex agent (argv), Resume continues a session by
// id, Fork branches one — each returns the pane id (instant handle) and session
// id (bound at first turn, "" if not yet taken). Artifacts: Preview shows a
// file/url in the sidebar (returns the pane id), Render opens it in the human's
// laptop browser. Routines: list/fire the workspace's scheduled executors.
// workspace is the outer duck session the action targets.
type Host interface {
	Launch(workspace string, argv []string, name, tab, prompt, model, effort string) (paneID, sessionID string, err error)
	Resume(workspace, sessionID, prompt string) (paneID, newSessionID string, err error)
	Fork(workspace, sessionID, prompt string) (paneID, newSessionID string, err error)

	Preview(workspace, target, name string) (paneID string, err error)
	Render(workspace, target string) error
	Window(workspace, target, name string) (string, error)

	Routines(workspace string) (string, error)          // human-readable listing
	FireRoutine(workspace, name string) (string, error) // run one now

	// Workflow starts a detached workflow run (docs/WORKFLOWS.md) and returns
	// its run id; completion arrives later as a workflow_complete event.
	Workflow(workspace, script, argsJSON, resumeFrom string, budget int64) (string, error)
}

// Serve runs the channel sidecar until stdin closes (Claude exiting kills
// us). run drives the local tmux server; in production it is
// panel.ExecRunner and rw is stdin/stdout. launcher backs the spawn/resume/fork
// tools (nil = those tools are omitted).
//
// workspace scopes the sweep to ONE duck session's agents — the down edge of
// the org chart: a manager hears its own lot, not every workspace on the
// machine. Empty means machine-wide (motherduck / explicit --all).
func Serve(run panel.Runner, workspace string, host Host, in io.Reader, out io.Writer) error {
	s := &server{run: run, workspace: workspace, host: host, out: out, offsets: map[string]int64{}, resolver: NewResolver(run), stop: make(chan struct{}), started: time.Now().Truncate(time.Second)}
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
	// Wait for in-flight tools/call handlers (spawn can block on spin-up) to write
	// their replies before returning — otherwise a reply is lost when stdin closes
	// mid-call.
	s.inflight.Wait()
	return sc.Err()
}

type server struct {
	run       panel.Runner
	workspace string // sweep only this duck session's agents ("" = machine-wide)
	host      Host   // backs spawn/resume/fork tools; nil omits them
	out       io.Writer
	resolver  *Resolver     // memoized pairing/status — watch goroutine only
	stop      chan struct{} // closed when Serve returns; stops the watch goroutine
	started   time.Time     // sidecar start; panes spawned after this drain from 0

	mu       sync.Mutex       // guards out and ready (watcher + handler both touch them)
	offsets  map[string]int64 // rollout → drained byte offset; watch goroutine only
	ready    bool             // initialize handshake done — notifications may flow
	inflight sync.WaitGroup   // tools/call handlers dispatched in goroutines
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
		"with meta {session, agent, type}. Window annotations arrive as <channel source=\"duck-window\"> " +
		"with meta {session, source, type=mark}; they mean the human pointed at or commented on the current artifact. " +
		"task_complete means the agent finished a turn. " +
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

// tool is one MCP tool the sidecar exposes. schema and handler are built
// per-server-instance (the schema varies with s.workspace), so the table is a
// method, not a package global. handler returns the human-facing result text;
// a non-nil error becomes an isError tool result.
type tool struct {
	name        string
	description string
	schema      map[string]any
	handler     func(args json.RawMessage) (string, error)
}

// tools builds this instance's tool table. reply is first (unchanged behavior);
// spawn/resume/fork append here as they land. Schemas that vary with workspace
// scope are computed here so tools/list and tools/call share one definition.
func (s *server) tools() []tool {
	agentDesc := "agent reference: name, pane id (%NN), or codex session id (from event meta)"
	sessionReq := true
	sessionDesc := "duck session owning the agent (from event meta)"
	if s.workspace != "" {
		sessionReq = false
		sessionDesc = "duck session owning the agent (default: your workspace, " + s.workspace + ")"
	}
	required := []string{"agent", "message"}
	if sessionReq {
		required = []string{"session", "agent", "message"}
	}
	ts := []tool{{
		name:        "reply",
		description: "Send a message to a duck sidebar agent (typed into its TUI, visible in the viewport). The agent's response arrives later as a <channel source=\"duck-agents\"> event — do not poll; react when it lands. ROUTING: reply = continue an EXISTING agent's thread NOW. To continue a thread LATER or on a schedule, that is a heartbeat routine (`duck routines add <name> --every …`), not a reply you sit on.",
		schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session": map[string]any{"type": "string", "description": sessionDesc},
				"agent":   map[string]any{"type": "string", "description": agentDesc},
				"message": map[string]any{"type": "string"},
			},
			"required": required,
		},
		handler: func(raw json.RawMessage) (string, error) {
			var a struct{ Session, Agent, Message string }
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", err
			}
			if a.Session == "" {
				a.Session = s.workspace
			}
			ref, err := FindAgent(s.run, a.Session, a.Agent)
			if err != nil {
				return "", err
			}
			if err := Send(s.run, ref, a.Message); err != nil {
				return "", err
			}
			return "delivered to " + a.Agent, nil
		},
	}}
	if s.host == nil {
		return ts
	}
	// receipt renders a {paneId, sessionId} handle line for the spawn family.
	receipt := func(pane, sess string) string {
		if sess != "" {
			return fmt.Sprintf("spawned — pane %s, session %s. Its output arrives on the duck-agents channel; do NOT poll or tail — react when it lands. Address it later with the reply tool by pane id or session id.", pane, sess)
		}
		return fmt.Sprintf("spawned — pane %s (session id pending its first turn). Output arrives on the duck-agents channel; do NOT poll — react when it lands. Address it by pane id via the reply tool.", pane)
	}
	ws := s.workspace
	ts = append(ts,
		tool{
			name:        "spawn",
			description: "Launch a codex agent into this workspace's sidebar (a durable, human-watchable TUI pane) and optionally give it its first task. Returns in a few seconds with a handle once the agent is up — the RESULT is not in the reply; it arrives later as a <channel source=\"duck-agents\"> event, so do NOT poll or tail. Safe to launch several in parallel. Optionally pick a model (e.g. deepseek for DeepSeek V4, a cheaper executor) and reasoning effort. Prefer this over shelling out to `duck spawn`. ROUTING: spawn = do this ONCE, NOW. It is not a scheduler — for anything recurring or deferred (\"every morning\", \"keep an eye on\", \"check back later\") use a routine (`duck routines add`) instead. Use for bounded/executor work (codex is a strong executor); for open-ended thinking use a native subagent.",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"prompt": map[string]any{"type": "string", "description": "first task to hand the agent (recommended — triggers its first turn so the session id binds)"},
					"name":   map[string]any{"type": "string", "description": "optional roster label (default: a unique codex-N)"},
					"tab":    map[string]any{"type": "string", "description": "optional roster tab"},
					"model": map[string]any{
						"type":        "string",
						"description": "optional model for this agent (default: the codex config default, gpt-5.5). Use deepseek/deepseek-flash to run on DeepSeek V4 via Moon Bridge, or a gpt alias.",
						"enum":        []string{"gpt-5.5", "gpt-5.4", "gpt-5.4-mini", "gpt-5.3-codex-spark", "deepseek", "deepseek-pro", "deepseek-flash"},
					},
					"effort": map[string]any{
						"type":        "string",
						"description": "optional reasoning effort for this agent (default: the codex config default)",
						"enum":        []string{"low", "medium", "high"},
					},
				},
			},
			handler: func(raw json.RawMessage) (string, error) {
				var a struct{ Prompt, Name, Tab, Model, Effort string }
				if err := json.Unmarshal(raw, &a); err != nil {
					return "", err
				}
				pane, sess, err := s.host.Launch(ws, []string{"codex"}, a.Name, a.Tab, a.Prompt, a.Model, a.Effort)
				if err != nil {
					return "", err
				}
				return receipt(pane, sess), nil
			},
		},
		tool{
			name:        "resume",
			description: "Resume a codex session by its id — continue that EXACT conversation (same session id, full context intact). Works even if its pane is gone. Returns a handle; output arrives on the channel. Use to give a prior agent its next turn with all its context.",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"session_id": map[string]any{"type": "string", "description": "the codex session id to resume (from an event's meta or a prior spawn receipt)"},
					"prompt":     map[string]any{"type": "string", "description": "the next turn to deliver"},
				},
				"required": []string{"session_id"},
			},
			handler: func(raw json.RawMessage) (string, error) {
				var a struct {
					SessionID string `json:"session_id"`
					Prompt    string
				}
				if err := json.Unmarshal(raw, &a); err != nil {
					return "", err
				}
				pane, sess, err := s.host.Resume(ws, a.SessionID, a.Prompt)
				if err != nil {
					return "", err
				}
				return receipt(pane, sess), nil
			},
		},
		tool{
			name:        "fork",
			description: "Fork a codex session by its id — branch a NEW session that inherits the parent's context, leaving the parent untouched. The cheap fan-out primitive: prime one base agent with shared setup, then fork it N ways to explore variations in parallel. Returns the new agent's handle; output arrives on the channel.",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"session_id": map[string]any{"type": "string", "description": "the codex session id to fork from"},
					"prompt":     map[string]any{"type": "string", "description": "the task for this branch"},
				},
				"required": []string{"session_id"},
			},
			handler: func(raw json.RawMessage) (string, error) {
				var a struct {
					SessionID string `json:"session_id"`
					Prompt    string
				}
				if err := json.Unmarshal(raw, &a); err != nil {
					return "", err
				}
				pane, sess, err := s.host.Fork(ws, a.SessionID, a.Prompt)
				if err != nil {
					return "", err
				}
				return receipt(pane, sess), nil
			},
		},
		tool{
			name:        "preview",
			description: "Show an artifact (a file or URL — a document, report, table, chart, image, or a rendered .md/.html) to the human IN THIS WORKSPACE's sidebar. Prefer this over shelling out to `duck preview`. Use whenever showing something visually beats describing it in text. Local html/markdown live-update: rewrite the file and the pane repaints itself.",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"target": map[string]any{"type": "string", "description": "path to a local file or an http(s) URL"},
					"name":   map[string]any{"type": "string", "description": "a short descriptive label for the artifact (its roster tab entry)"},
				},
				"required": []string{"target", "name"},
			},
			handler: func(raw json.RawMessage) (string, error) {
				var a struct{ Target, Name string }
				if err := json.Unmarshal(raw, &a); err != nil {
					return "", err
				}
				pane, err := s.host.Preview(ws, a.Target, a.Name)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("previewing %q in the sidebar (pane %s)", a.Name, pane), nil
			},
		},
		tool{
			name:        "render",
			description: "Open an artifact (file or URL) at FULL FIDELITY in the human's laptop browser — for anything dynamic, interactive, or where fidelity matters (the sidebar preview is terminal cells). Prefer this over shelling out to `duck render`.",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"target": map[string]any{"type": "string", "description": "path to a local file or an http(s) URL"},
				},
				"required": []string{"target"},
			},
			handler: func(raw json.RawMessage) (string, error) {
				var a struct{ Target string }
				if err := json.Unmarshal(raw, &a); err != nil {
					return "", err
				}
				if err := s.host.Render(ws, a.Target); err != nil {
					return "", err
				}
				return "opened in the human's laptop browser", nil
			},
		},
		tool{
			name:        "window",
			description: "Open a DYNAMIC artifact (animation, interactive page, realtime dashboard, anything the human should mark up) in the duck-owned window on the human's current client machine. duck keeps custody: the human can highlight/comment, and those marks arrive back to you as <channel source=\"duck-window\" type=\"mark\"> events — do not poll. ROUTING: static content → preview/render; dynamic or annotatable → window.",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"target": map[string]any{"type": "string", "description": "file path or URL"},
					"name":   map[string]any{"type": "string", "description": "optional roster label (default: basename or host)"},
				},
				"required": []string{"target"},
			},
			handler: func(raw json.RawMessage) (string, error) {
				var a struct{ Target, Name string }
				if err := json.Unmarshal(raw, &a); err != nil {
					return "", err
				}
				shown, err := s.host.Window(ws, a.Target, a.Name)
				if err != nil {
					return "", err
				}
				return "opened in the duck window: " + shown, nil
			},
		},
		tool{
			name:        "routines",
			description: "List this workspace's scheduled routines (your standing executor duties), or fire one NOW by name (fresh executor, off-schedule). ROUTING: reach for routines whenever the human asks for a scheduled task, recurring run, monitor, reminder, follow-up, or says to watch something, keep an eye on it, check back later, or keep working later — that is a routine, NOT a spawn. Pick the trigger by shape: --cron for standalone jobs where each run stands alone; --every (heartbeat: ONE persistent thread re-prompted per interval, keeps memory between beats) when continuity matters or the interval is under an hour; --manual for on-demand runbooks; --manager to schedule a turn in YOUR own context (reviews/consolidation). This tool only lists+fires; create with `duck routines add <name> [--cron|--every|--manual] [--manager] [--model gpt-…] [--effort …] <prompt>` via shell. To MODIFY a routine, edit its files in place (<sync-root>/.duck/routines/<ws>/<name>.toml + .md — the next fire reads them fresh); NEVER rm+re-add, which loses last-fire state and can double-fire. Prefer updating an existing routine over creating a near-duplicate: list first, match by name/prompt. Write prompts future-safe: the executor wakes with no conversation context, so the .md must say what to do, what NOT to do, and what to report. Intervals: match cadence to how fast the watched state actually changes — every beat costs an executor turn, so a too-tight heartbeat burns money to learn nothing changed; under ~15m needs a reason; err longer (the human can always fire for an immediate check). Tiering: checklists/sweeps → --model gpt-5.4-mini --effort low; judgment-heavy duties → default model (routines are codex-native only; deepseek is spawn-only). Completions arrive as digest events — never poll, and never build a routine whose only job is checking on another routine.",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"fire": map[string]any{"type": "string", "description": "optional: name of a routine to run NOW (omit to just list)"},
				},
			},
			handler: func(raw json.RawMessage) (string, error) {
				var a struct{ Fire string }
				if err := json.Unmarshal(raw, &a); err != nil {
					return "", err
				}
				if a.Fire != "" {
					return s.host.FireRoutine(ws, a.Fire)
				}
				return s.host.Routines(ws)
			},
		},
		tool{
			name: "workflow",
			description: "Run a deterministic multi-agent workflow: a JS script you write whose control flow (loops, fan-out, barriers) is plain code, where each agent() call runs ONE disposable headless codex executor (DeepSeek V4 flash by default — cheap; opt up per call for judge/verify stages, and REQUIRED for web research: gpt-tier workers have native web search, deepseek workers do NOT — their search tool-calls break at the proxy). Workers are processes, not sidebar panes: the RUN is the one visible thing (roster workflows section + `duck workflows`). Returns a wf_ run id in a couple seconds; the RESULT is not in the reply — the run reports through the channel (workflow_started, workflow_phase per phase() transition, and workflow_complete carrying the result summary), so do NOT poll or tail; react when events land. " +
				"ROUTING: use a workflow for fan-out work one pass shouldn't be trusted with or one context can't hold — audits, migrations, review-then-adversarially-verify, judge panels, loop-until-dry discovery — and only at the human's scale of ask; a single bounded task is a spawn, anything recurring is a routine. " +
				"SCRIPT SURFACE (plain JS, no TS): must begin `export const meta = {name, description}` as a PURE literal. Globals: agent(prompt, opts?) -> Promise (opts: {label, model, effort, cwd, write, schema} — schema forces a validated JSON object return, retried via session-resume on mismatch; workers are sandboxed read-only unless write:true; a failed worker resolves to null, so .filter(Boolean)); pipeline(items, ...stages) (per-item chains, NO barrier between stages — the default for multi-stage work; stages get (prev, item, i)); parallel(thunks) (a BARRIER — only when a stage needs ALL prior results, e.g. dedup); phase(title) + log(msg) (progress narration); args (the args input, verbatim); budget {total, spent(), remaining()} in tokens — agent() throws once total is exhausted. " +
				"The script's return value becomes the run's result (persisted to result.json, summarized in the completion event). Every completed agent() call is journaled; pass resume_from with a prior run id to replay unchanged calls from its journal and only run what changed. Default worker concurrency 64; runaway backstop 1000 agents. To inspect or kill a run: duck workflows tail|stop <run-id>.",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"script": map[string]any{"type": "string", "description": "the workflow script (JS). Must start with `export const meta = {name, description}`"},
					"args":   map[string]any{"type": "string", "description": "optional JSON value exposed to the script as `args` (encode objects/arrays as JSON text)"},
					"budget": map[string]any{"type": "integer", "description": "optional token cap across all workers (0/omit = uncapped)"},
					"resume_from": map[string]any{"type": "string", "description": "optional prior wf_ run id whose journal seeds the replay cache (edit-and-resume)"},
				},
				"required": []string{"script"},
			},
			handler: func(raw json.RawMessage) (string, error) {
				var a struct {
					Script     string `json:"script"`
					Args       string `json:"args"`
					Budget     int64  `json:"budget"`
					ResumeFrom string `json:"resume_from"`
				}
				if err := json.Unmarshal(raw, &a); err != nil {
					return "", err
				}
				id, err := s.host.Workflow(ws, a.Script, a.Args, a.ResumeFrom, a.Budget)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("started %s. The result arrives as a workflow_complete event — do NOT poll; react when it lands. Progress meanwhile: the roster's workflows section or `duck workflows tail %s`; stop it with `duck workflows stop %s`.", id, id, id), nil
			},
		},
	)
	return ts
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
		var list []any
		for _, t := range s.tools() {
			list = append(list, map[string]any{
				"name": t.name, "description": t.description, "inputSchema": t.schema,
			})
		}
		s.reply(req.ID, map[string]any{"tools": list})
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if json.Unmarshal(req.Params, &p) != nil {
			s.replyErr(req.ID, -32602, "bad tools/call params")
			return
		}
		var h func(json.RawMessage) (string, error)
		for _, t := range s.tools() {
			if t.name == p.Name {
				h = t.handler
				break
			}
		}
		if h == nil {
			s.replyErr(req.ID, -32602, "unknown tool: "+p.Name)
			return
		}
		// Dispatch in a goroutine: a handler that BLOCKS (spawn awaits the agent's
		// spin-up) must not freeze the single-threaded stdin loop — no other
		// tools/call, reply, or ping could be serviced while it ran. JSON-RPC
		// permits out-of-order responses; s.reply is mutex-guarded (s.write), and
		// the watch goroutine keeps pushing notifications independently.
		id := req.ID
		s.inflight.Add(1)
		go func() {
			defer s.inflight.Done()
			text, err := h(p.Arguments)
			if err != nil {
				s.reply(id, map[string]any{"content": []any{map[string]any{"type": "text", "text": err.Error()}}, "isError": true})
				return
			}
			s.reply(id, map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}})
		}()
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
			s.drainWindowMarks(s.workspace)
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
				s.drainWindowMarks(outer)
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
				// Routine executors (kind=runs) report through the courier's
				// batched digest — pushing their rollout events here too would
				// double-deliver every completion to the manager.
				if a.Kind == panel.KindRun {
					continue
				}
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
		// agent carries the pane's display label; thread is the STABLE identity of
		// the stream that produced the event (1:1 with the rollout). A supervisor
		// keys on thread so attribution can't scramble when panes share a name or
		// a name gets re-derived — the label is for humans, the thread for routing.
		meta := map[string]any{"session": ref.Session, "agent": ref.Name, "type": ev.Type}
		if tid := threadID(ref.Rollout); tid != "" {
			meta["thread"] = tid
		}
		s.write(map[string]any{
			"jsonrpc": "2.0",
			"method":  "notifications/claude/channel",
			"params": map[string]any{
				"content": content,
				"meta":    meta,
			},
		})
	}
}
