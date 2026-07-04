# ROUTINES — the flock of ducks (design, settled 2026-07-04)

Decision record + implementation spec. Flock (the never-deployed Mac
automation design) is superseded; duck absorbs the idea. Andrew's calls:
hub-side execution; pane-per-run over app-server threads (visible, killable,
channel-able); runs land in each project workspace's `runs` tab; no
remote-control surface — the hub runs everything, so claude-in-a-workspace
with channels IS remote control.

## The organizational model (the actual point)

A workspace is an EMPLOYEE, not furniture: **claude in the main pane is the
manager; the sidebar is its flock of executors** (codex runs, shells,
artifacts), pads are team memory, channels are how the manager tasks and
reads its reports, and routines are the workspace's JOB DESCRIPTION. Each
duck manages its flock and reports up; the ⌂ workspaces tab is the org
chart; the human (or one day a chief-of-staff workspace consuming
`duck channel serve`) sits at the top. This was implicit in the original
main-pane-is-claude design — routines just make it official.

Consequence (v3, Andrew's correction): **schedules drive EXECUTORS; events
drive the MANAGER.** Routines launch/continue codex runs on the clock; the
manager claude is woken only by their REPORTS — completion events delivered
up the channel — never polled by a timer. A scheduled manager beat remains
possible (`target = "manager"`) but is the exception: event-driven is a
trusting manager, scheduled check-ins are a micromanaging one — both are
valid management styles, so both exist, but trust is the default.

## The duckisms this is built from

- Scheduler = systemd timer → `duck routines tick` (the evict-sweep pattern;
  no daemon). Hub-side only; laptops never involved.
- A run = a pane in the project workspace's lot, kind `runs` (dynamic tabs
  already render it). Status dots / kill / channel send all work today.
- Run history = codex rollouts (~/.codex/sessions) — codex already persists
  every thread; duck reads, never writes a second ledger.
- Definitions are FILES in the project (self-modifiable by agents, synced by
  duck, reviewable in git). tmux + files stay the only databases.

## Routine format — duck-native (flock never shipped; no compat debt)

```
<project>/.duck/routines/<name>.toml   ← trigger + target + overrides
<project>/.duck/routines/<name>.md     ← the prompt (the job description)
```

Fields (v1): `trigger = "cron" | "heartbeat" | "manual"`,
`schedule`/`interval`, `target = "run" (default) | "manager"` (run = codex
executor; manager = the rare scheduled turn to claude, e.g. daily digest),
`report = "digest" (default) | "none"`. The flock repo
(~/Obsidian/aviary/flock; never deployed) is reference reading only.

## Semantics per trigger

- **target=run, cron/manual** (default) → a FRESH `codex exec
  --dangerously-bypass-approvals-and-sandbox "$(prompt)"` executor pane per
  fire, cwd = project root, name `<routine>`, kind `runs`. Exit-hold keeps
  failures visible.
- **target=run, heartbeat** → ONE persistent codex TUI pane per routine;
  each beat = `duck channel send` of the prompt — recurring turns in one
  thread, watchable in the viewport.
- **target=manager** (rare) → a scheduled turn sent to the workspace's main
  claude pane (send-keys; ensure claude is running first).

## Reporting upward (the manager's inbox — event-driven, never polled)

The tick doubles as the report courier: per workspace it tracks rollout
offsets (like channel serve) and, when NEW task_completes exist for the
workspace's runs, delivers ONE batched digest turn to the main claude pane
via send-keys: "routine X completed: <last_agent_message firstline> —
`duck channel tail X` for detail." Claude manages from there: reads detail,
redirects executors, writes pads, updates its own pane title (its report to
the org). `report = "none"` opts a routine out. When the main pane runs
`claude --channels server:duck-agents`, the serve path replaces send-keys
delivery — same events, structured; the digest is the dependency-free
default.
- **Concurrency guard**: if the routine's pane exists and channel status is
  `working`, the tick SKIPS (logs, no queue). Missed beats are dropped, not
  replayed — heartbeats are idempotent by design.

## Tick algorithm (`duck routines tick`, hidden verb, runs every minute)

1. Projects: union of (a) live workspaces' @duck_dir, (b) ~/.duck/routines-projects
   (dirs registered by `duck routines enable`, so automation survives all
   workspaces being closed).
2. Per project: parse `.flock/*/routine.toml`; compute due-ness from
   last-fire state in ~/.duck/routines-state.json (one small JSON, like
   names.json).
3. Fire due routines per semantics above. Workspace missing → create it
   headless (session.New + panel arm — same path as attach arming) so runs
   are always inspectable later.

## Verbs

- `duck routines` — list (project, routine, trigger, last fire, live status)
- `duck routines enable|disable` — register/unregister the current project
- `duck routines fire <name>` — manual trigger (also from roster: `new`/fire)
- `duck routines install` — systemd timer, evict-install pattern
- `duck routines tick` — hidden; the timer's entrypoint

## Phases

1. Format parsing + tick + cron/manual runs + `runs` tab. (The meat.)
2. Heartbeats (persistent pane + channel-send beats) + roster affordances
   (ran-history browser over rollouts; "last message" one Enter away).
3. Decommission: stop flock's Mac daemon, unplug its cassandra MCP
   registration, archive the repo with a pointer here.

## Non-goals (explicitly rejected)

- No HTTP/WS API, no MCP remote surface (claude-on-hub is the control plane;
  `duck channel serve` stays stdio for local claude sessions).
- No app-server thread pooling — pane-per-run until scale forces otherwise.
- No second run-ledger — rollouts are the history.
