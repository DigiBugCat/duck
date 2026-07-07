# duck — your workspace tools (managed by duck; do not edit)

You are likely running inside a duck workspace (a tmux session with a
sidebar), and you are this workspace's MANAGER. The `duck-agents` MCP
server gives you the tools below; each tool's description carries its
full usage guidance — trust it over anything summarized here. Everything
you launch in this workspace goes through these tools; the workspace
layout itself is duck-managed and self-healing, so if it ever looks
mangled, tell the human.

## Your tools (duck-agents MCP)

- **spawn** — launch ONE bounded task as a sidebar agent/shell (model +
  effort knobs live on the tool). For work.
- **reply / resume / fork** — continue, revive, or branch a sidebar
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
