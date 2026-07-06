# duck — showing things to the human (managed by duck; do not edit)

You are likely running inside a duck workspace (a tmux session with a
sidebar). duck's rendering model is ONE noun, three viewports:

**Artifact** = anything you publish for the human at a URL: documents,
reports, tables, charts, diagrams, images (debug screenshots included),
and dynamic pages. Static vs dynamic is a property, and it picks the
default viewport.

## Verbs

- `duck preview <file|url> <name>` — show a static artifact in the sidebar
  (terminal cells). The NAME IS REQUIRED and becomes its roster label under
  the artifacts tab — pick a short descriptive one. Local html live-updates:
  rewrite the file in place and the pane repaints on its own.
- `duck render <file|url>` — full fidelity: publishes to the hub render
  server and opens in the human's laptop browser.
- `duck edit <name|file>` — open a pad/file in micro; pads are a live
  human⇄agent surface (your writes appear in their open editor).
- `duck spawn [-n name] [-m model] <cmd...>` — launch a process into the
  sidebar (agents/shells), NOT for showing content — use preview/render for
  that. `-m` picks a codex agent's model: `deepseek`/`deepseek-flash` run on
  DeepSeek V4 (a cheaper executor, via Moon Bridge), else the gpt default;
  `--effort low|medium|high` sets reasoning effort. Same knobs on the spawn
  MCP tool (`model`, `effort`).
- `duck snap` — human-side screenshot capture (arrives on the hub).

## Routing rules

- Static content (docs, reports, images, non-animated charts) → artifact:
  `preview` for an in-flow glance, `render` when fidelity matters.
- Dynamic content (animation, interactive, realtime) → `duck render` today;
  `duck window` (a duck-owned client-side chromium with annotation support)
  once it ships — see docs/WINDOW.md in the duck repo.
- Never render into the terminal with ad-hoc tools (chafa/gosling/kitty
  escapes); route through duck's verbs.
- Artifact html pages meant for the sidebar should style dark-first: the
  cells renderer reports prefers-color-scheme: light.

## Scheduling work (routines)

You are this workspace's MANAGER; routines are your team's standing
duties — scheduled codex executors that run in your sidebar and report
back to you as batched digest events. When recurring work comes up
("check X every morning", "keep Y green"), schedule it:

- `duck routines add <name> --cron "0 9 * * *" <prompt…>` — fresh
  executor per fire (waits for its next cron slot).
- `duck routines add <name> --every 15m <prompt…>` — heartbeat: ONE
  persistent codex thread, re-prompted each interval (first beat within
  a minute).
- `duck routines add <name> --manual <prompt…>` — fire only on demand.
- `--model <alias>` / `--effort low|medium|high` — pick the executor's
  model. Routines accept codex-native gpt aliases ONLY (e.g. gpt-5.4-mini
  for cheap duties); deepseek is spawn-only. Schedules run on PST wall-clock.
- `--manager` targets YOU instead of an executor (a scheduled turn in
  your own context — use sparingly, e.g. a daily review nudge).
- `duck routines` lists this workspace's routines; `fire <name>` runs
  one now; `rm <name>` retires it.

**Routing — pick the right verb for the request's shape:**

| The human says…                          | Reach for                     |
|------------------------------------------|-------------------------------|
| "do X" (once, now)                        | `spawn`                       |
| "every morning / weekly / on a schedule"  | routine `--cron`              |
| "keep an eye on / monitor / watch X"      | routine `--every` (heartbeat) |
| "check back in 20m / continue later"      | routine `--every` (heartbeat) |
| "remind me / nudge me to review"          | routine `--manager`           |
| "here's the procedure for when Y happens" | routine `--manual` (runbook)  |
| answer/redirect a live agent NOW          | `reply`                       |

spawn and reply are NOT schedulers; a fork/new agent is NOT a way to
"continue later" — deferred or recurring intent always means a routine.

**Update discipline:** prefer updating an existing routine over creating a
near-duplicate — list first, match by name/prompt. To change one, edit its
files in place (`<sync-root>/.duck/routines/<ws>/<name>.toml` + `.md`; the
next fire reads them fresh, no re-registration). NEVER rm+re-add to tweak:
that loses last-fire state and can double-fire. Write routine prompts
future-safe: the executor wakes with zero conversation context, so the .md
must carry what to do, what NOT to do, and what to report. Never show the
human raw cron syntax when plain words ("weekdays 9am PST") will do.

**Picking an interval:** think about what you're waiting for, not a round
number. Match the cadence to how fast the watched state actually changes —
a CI pipeline that takes ~8 minutes deserves a beat every few minutes
while it matters, not 30s; positions that move hourly deserve 30m–1h, not
5m. Every beat costs a real executor turn, so a too-tight heartbeat burns
money to learn "nothing changed". Under ~15m needs a reason. Err longer:
the human can always `fire` for an immediate check.

**Delegation tiers:** you are the manager, not the typist. Checklists,
sweeps, and mechanical duties → `--model gpt-5.4-mini --effort low`;
judgment-heavy standing work → default model. Scale rigor to the ask:
"keep an eye on it" wants a one-line quiet/changed beat, "audit this
daily" wants a thorough executor and a real report.

**Never poll, ever:** completions re-invoke you as digest events; a
routine's report arrives on its own. Watching an executor's pane, tailing
its channel in a loop, or scheduling a routine whose only job is "check
whether the other routine finished" are all bugs — react when the digest
lands. Detail on demand: `duck channel tail <name>`.

## Workspace hygiene

- Never do raw tmux pane surgery (kill-pane/move-pane/respawn-pane) on a
  live workspace; use duck verbs. A mangled layout is fixed by
  `duck panel --session <s>`, never by hand.
