# ROUTINES — pulling flock into duck (design, settled 2026-07-04)

Decision record + implementation spec. Flock (the Mac automation runtime)
retires; duck absorbs it. Andrew's calls: nothing is Mac-only → execution
moves hub-side; pane-per-run beats app-server threads (visible, killable,
channel-able; our scale is handfuls, not fleets); runs land in each
PROJECT's workspace under a `runs` tab; no remote-control surface — the hub
runs everything, so claude-in-a-workspace with channels IS remote control.

## The duckisms this is built from

- Scheduler = systemd timer → `duck routines tick` (the evict-sweep pattern;
  no daemon). Hub-side only; laptops never involved.
- A run = a pane in the project workspace's lot, kind `runs` (dynamic tabs
  already render it). Status dots / kill / channel send all work today.
- Run history = codex rollouts (~/.codex/sessions) — codex already persists
  every thread; duck reads, never writes a second ledger.
- Definitions are FILES in the project (self-modifiable by agents, synced by
  duck, reviewable in git). tmux + files stay the only databases.

## Routine format — adopt flock's `.flock/` verbatim

```
<project>/.flock/flock.toml        ← project marker + defaults (model, sandbox)
<project>/.flock/<name>/routine.toml   ← trigger (cron | heartbeat | manual), interval/schedule, codex overrides
<project>/.flock/<name>/prompt.md      ← the instructions
```

Zero migration for existing projects (Cassandra-Finance et al). Implementer:
read ~/Obsidian/aviary/flock internal/ for the exact routine.toml schema and
keep field-compatibility; extensions go in ignored-by-flock keys.

## Semantics per trigger

- **cron / manual** → a FRESH `codex exec --dangerously-bypass-approvals-and-sandbox
  "$(prompt.md)"` pane per fire, cwd = project root, name `<routine>`,
  kind `runs`. Exit-hold wrap (already in Spawn) keeps failures visible.
- **heartbeat** → ONE persistent codex TUI pane per routine (name `<routine>`,
  kind `runs`), created on first beat; each beat = `duck channel send` of
  prompt.md into it — recurring turns in one thread, flock's core trick,
  except watchable in the viewport.
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
