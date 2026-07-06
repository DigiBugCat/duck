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

Completions arrive to you automatically as `<channel source="duck-agents"
type="digest">` events — never poll your executors. Detail on demand:
`duck channel tail <name>`.

## Workspace hygiene

- Never do raw tmux pane surgery (kill-pane/move-pane/respawn-pane) on a
  live workspace; use duck verbs. A mangled layout is fixed by
  `duck panel --session <s>`, never by hand.
