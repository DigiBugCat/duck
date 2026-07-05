# Codex spawning rework — verified-facts-driven

Grounds the codex agent system in what we MEASURED live (see aviary scratchpad
"SESSION PRIMITIVES — fully characterized"), not assumptions. Three layers,
built in order, green suite at each.

## Verified facts this is built on
- `codex resume <session_id> "<prompt>"` → SAME session_id, appends the same
  rollout, context intact. session_id = a durable PER-CONVERSATION handle.
- `codex fork <session_id> [prompt]` (interactive only) → NEW id, inherits parent
  context, parent untouched = an independent branch. Cheap fan-out.
- SessionStart hook fires at FIRST-TURN (not process start), delivers
  {session_id, transcript_path, cwd, source} on stdin + TMUX_PANE in env = the
  spawned pane. RACE-FREE binding.
- Hooks fire ONLY with `--dangerously-bypass-hook-trust`; without it, SILENTLY
  skipped. codex BLOCKS on a hook (60s timeout) → the hook cmd must be fast.
- Double-fire comes from multiple registrations (config + ~/.codex/.tmp/plugins
  hooks). Dedup on session_id; gate "born" on source==startup.
- notify/HandleNotify (agent-turn-complete) stays the STABLE FLOOR; the hook is a
  precise fast-path over it, never the sole spine (private codex ABI).

## Layer 1 — Foundation: trust flag + SessionStart binding (task #13)
1. `withCodexHookTrust(args)`: inject `--dangerously-bypass-hook-trust` for codex
   spawns (mirrors withCodexFullAccess at spawn.go:146). Without it hooks no-op.
2. `withCodexSessionHook(args)`: inject `-c hooks...` wiring a SessionStart hook to
   `duck channel hook` (new subcommand, sibling of `duck channel notify`). MUST be
   fast + non-blocking (codex blocks 60s). Just: read stdin JSON + $TMUX_PANE,
   write the binding, exit. NO tmux round-trips in the hook itself.
3. `duck channel hook`: reads the SessionStart payload ({session_id,
   transcript_path, cwd, source}) + $TMUX_PANE from env. If source==startup:
   stamp @duck_rollout = transcript_path on the pane (EXACT, no matchRollout
   guess) and @duck_session = session_id. Dedup: idempotent (re-stamp same pane
   is a no-op). This makes Resolve/matchRollout a FALLBACK, not the mechanism.
4. Keep withCodexNotify — HandleNotify still heals + is the floor.
Result: at first-turn, the pane carries its EXACT rollout + session_id, bound
race-free. The scramble literally cannot happen (no correlation). Today's
refuse-to-guess matchRollout stays as the fallback for non-hooked/legacy panes.

## Layer 2 — resume + fork primitives (task #14)
- session_id becomes a first-class handle: stamped @duck_session (layer 1),
  readable, resumable. Add to AgentRef.
- `resume`: given a session_id, launch `codex resume <id> "<prompt>"` — continue
  an existing conversation (same id). Works for a dead/gone agent (durable).
- `fork`: `codex fork <id> [prompt]` — branch. New id, inherited context. The
  fan-out multiplier: prime one base, fork N.
- Both go through the SAME spawn pipeline (trust flag, hook, full-access) so the
  forked/resumed agent is also bound + attributed.

## Layer 3 — duck mcp tools (task #15)
Per Fable's review (all incorporated):
1. Registry refactor of serve.go: []Tool{name, schema(per-instance), handler};
   tools/list emits table; tools/call dispatches by name IN A GOROUTINE (else a
   blocking spawn freezes the whole server — Fable's biggest catch). reply is the
   first entry, no behavior change.
2. Extract the spawn pipeline OUT of command/spawn.go into internal (e.g.
   internal/agent) so the MCP tool and CLI share it — MCP spawn must NOT bypass
   the notify hook / trust flag / full-access.
3. Tools (receipts DOWN, events UP the channel):
   - spawn(cmd, prompt?, name?, tab?) → {paneId, sessionId?, status}. Blocks
     through spin-up only. paneId primary (instant), sessionId when the first
     turn has fired (nullable). Add `pane` to event meta so receipts↔events
     correlate. Honest latency hint ("returns in seconds; result arrives on the
     channel — do NOT poll").
   - ONE messaging verb: evolve `reply` to take pane-id/session-id/name (FindAgent
     already resolves all three). Don't add `send` beside it (model-choice poison).
   - resume(sessionId, prompt) → continue by id.
   - fork(sessionId, prompt?) → branch, returns the new {paneId, sessionId?}.
4. Descriptions worded WITH the harness grain (Agent/SendMessage register) so
   Claude prefers them over Bash. Update AGENT.md to point at the tools first.

## Non-negotiables (from the whole research arc)
- INTERACTIVE codex TUI in a tmux pane always (human-watchable). Never exec/
  mcp-server/app-server for the agent itself.
- notify stays the floor; hooks are the fast-path.
- Everything keyed by session_id / pane-id — never name — for attribution.
- Hook cmd fast + non-blocking (60s codex timeout).
