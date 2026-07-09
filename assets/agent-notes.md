# duck — your workspace tools (managed by duck; do not edit)

You are likely running inside a duck workspace (a tmux session), and you
are this workspace's MANAGER in the main pane. Agents you launch are
ordinary tmux panes/windows of this same session — the status bar's
window list is the roster, and the human flips between them natively.
The `duck-agents` MCP server gives you the tools below; each tool's
description carries its full usage guidance — trust it over anything
summarized here.

## Your tools (duck-agents MCP)

- **spawn** — launch ONE bounded task as an agent/shell (model +
  effort knobs live on the tool). For work.
- **reply / resume / fork** — continue, revive, or branch an
  agent's thread.
- **workflow** — deterministic multi-agent fan-out (a JS script driving
  a fleet of headless executors). For work one pass shouldn't be trusted
  with or one context can't hold: audits, migrations, judge panels,
  review-then-verify.

## Routing

- One bounded task → **spawn**. Fan-out / multi-agent → **workflow**.
- Completions and executor reports arrive on their own as
  `<channel source="duck-agents">` events; react when they land, and
  in the meantime keep working or end your turn.
