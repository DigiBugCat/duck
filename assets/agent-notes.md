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
- `duck spawn [-n name] <cmd...>` — launch a process into the sidebar
  (agents/shells), NOT for showing content — use preview/render for that.
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

## Workspace hygiene

- Never do raw tmux pane surgery (kill-pane/move-pane/respawn-pane) on a
  live workspace; use duck verbs. A mangled layout is fixed by
  `duck panel --session <s>`, never by hand.
