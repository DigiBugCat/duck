# ROUTINES — the flock of ducks (design, settled 2026-07-04)

Decision record + implementation spec. Flock (the never-deployed Mac
automation design) is superseded; duck absorbs the idea. Andrew's calls:
hub-side execution; pane-per-run over app-server threads (visible, killable,
channel-able); runs land in each project workspace's `runs` tab; no
remote-control surface — the hub runs everything, so claude-in-a-workspace
with channels IS remote control.

## The organizational model (the actual point)

A workspace is a MANAGER with a team: **claude in the main pane is the
manager; the sidebar is its flock of executors** (codex runs, shells,
artifacts), pads are team memory, channels are how the manager tasks and
reads its reports, and routines are what the employees DO — the team's
standing duties, owned by the WORKSPACE (v5 correction: not by the project
dir — several workspaces on one repo each keep their own duties, and fires
and reports land exactly where they were scheduled). Each duck manages its
flock and reports up; the ⌂ workspaces tab is the org chart; the human (or
one day a chief-of-staff workspace consuming `duck channel serve`) sits at
the top. This was implicit in the original main-pane-is-claude design —
routines just make it official.

Consequence (v3, Andrew's correction): **schedules drive EXECUTORS; events
drive the MANAGER.** Routines launch/continue codex runs on the clock; the
manager claude is woken only by their REPORTS — completion events delivered
up the channel — never polled by a timer. A scheduled manager beat remains
possible (`target = "manager"`): event-driven is a trusting manager,
scheduled check-ins are rigor — real management MIXES both, so a routine
set composes them freely; trust is merely the default.

## The org chart (v4)

- **The channel fabric is ONE mechanism** — an endpoint is any pane you can
  send-keys into plus a transcript you can tail. Codex executors have
  rollouts; claude managers have their own session transcripts
  (~/.claude/projects/…jsonl) — structural twins. Three EDGES, not three
  systems: down (manager → its lot), up (manager → parent), lateral
  (manager ↔ manager). Addressing: `duck channel send --workspace <ws>
  <pane>`, with **`manager` reserved** for a workspace's main claude pane.
- **@duck_parent** (session option) forms the tree. FLEXIBLE: any workspace
  may parent others (middle managers are just workspaces whose children are
  workspaces — "manager of managers" falls out for free).
- **motherduck is the INVARIANT global root** — one workspace, always
  exists, default parent for everything. Its roster flips scope: children
  (workspaces) render as its agents — name, title, last report, status —
  Enter walks in. The pane of glass is the ⌂ view grown into an org view.
- **The secretary**: motherduck's standing routine #1 — a heartbeat executor
  that diffs the org (workspaces born/died, parents changed, routines
  edited, activity levels), keeps the org pad current, and reports notable
  changes upward as ordinary completion digests. The org's bookkeeping is
  expressed entirely in the system's own primitives.

## The duckisms this is built from

- Scheduler = systemd timer → `duck routines tick` (the evict-sweep pattern;
  no daemon). Hub-side only; laptops never involved.
- A run = a pane in the project workspace's lot, kind `runs` (dynamic tabs
  already render it). Status dots / kill / channel send all work today.
- Run history = codex rollouts (~/.codex/sessions) — codex already persists
  every thread; duck reads, never writes a second ledger.
- Definitions are FILES that are PROJECT CONTENT, owned by the workspace
  (<project-sync-root>/.duck/routines/<workspace>/ — synced + versioned
  alongside pads, self-modifiable by agents via `duck routines add`). The hub
  keeps only last-fire state and a project index (both under ~/.duck), never
  the defs. tmux + files stay the only databases. `add` marks the workspace
  Persistent in the ledger, so its schedule (and the workspace itself) survives
  hub reboots.

## Routine format — duck-native (flock never shipped; no compat debt)

```
<project-sync-root>/.duck/routines/<workspace>/<name>.toml   ← trigger + target + overrides
<project-sync-root>/.duck/routines/<workspace>/<name>.md     ← the prompt (the job description)
```

Created with `duck routines add <name> [--cron "…" | --every 15m | --manual]
[--manager] [--report none] [--model <alias>] [--effort low|medium|high]
<prompt…>` from inside the workspace (or by writing the files directly).

Fields (v1): `trigger = "cron" | "heartbeat" | "manual"`,
`schedule`/`interval`, `target = "run" (default) | "manager"` (run = codex
executor; manager = the rare scheduled turn to claude, e.g. daily digest),
`report = "digest" (default) | "none"`, `model`/`effort` (executor model
alias + reasoning effort; target=run only, and CODEX-NATIVE aliases only —
gpt-5.4-mini etc. Cross-provider models (deepseek via Moon Bridge) are a
deliberate per-spawn choice, never a standing unattended duty).

Clock semantics: cron schedules are evaluated in **America/Los_Angeles**
regardless of hub TZ (override per-def with a `CRON_TZ=` prefix in the
schedule). Missed fires while a workspace is dormant are SKIPPED, never
replayed; a beat landing while the previous run is still working is dropped. The flock repo
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

1. Heal: every Persistent ledger record whose session is gone is recreated
   headless under its own name (manager relaunched) — so scheduled
   workspaces survive reboots.
2. Workspaces: enumerated via the hub-local project index
   (~/.duck/routines-projects.json) → for each project root, the workspace
   subdirs of <root>/.duck/routines/. One not live after the heal (no
   Persistent record) is DORMANT — logged, skipped, never fired.
3. Per live workspace: parse its *.toml; compute due-ness from last-fire
   state in ~/.duck/routines-state.json (keyed root+workspace+name); fire due
   routines per semantics above, INTO that workspace.

## Verbs

- `duck routines` — list this workspace's routines (`--all`: every workspace)
- `duck routines add <name> …` — create one here (writes files, marks the
  workspace persistent); `rm <name>` deletes
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
