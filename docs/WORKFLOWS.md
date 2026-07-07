# duck workflows — deterministic fan-out over headless codex fleets

Status: IMPLEMENTED (2026-07-05, internal/workflow + command/workflows.go +
sidecar `workflow` tool + roster section).
Deltas from the original design, discovered while building:
  - codex exec's native `--output-schema` is SILENTLY IGNORED by
    cross-provider profiles (validated empirically) — so the schema
    is also inlined into the prompt and the reply is parsed here, with up to
    two `codex exec resume` nudges on mismatch. The flag is still passed
    (enforced on gpt tiers).
  - No progress.html artifact (yet): the run's progress surface is the
    roster's ↵ on a workflow row, which fills the viewport with a live
    `duck workflows tail` view. An html artifact can layer on later.
  - Per-agent live lines: status.json carries `agents` — the RUNNING workers
    only (bounded by the concurrency cap), each with label, elapsed, tokens,
    and the last thing it did (message snippet or `$ command`, from the
    worker's --json item events). tail (and thus the roster progress pane)
    renders them. Finished workers leave the list; their history is the
    journal + per-agent .jsonl streams.
  - Web search: gpt-tier workers can (codex's native web_search tool is on
    by default and works even in the read-only sandbox — validated live).
    DeepSeek workers CANNOT: the Moon Bridge proxy doesn't map the tool, and
    the model's attempt leaks raw DSML tool-call markup into its reply. A
    research stage that needs the web must set {model: 'gpt-5.4-mini'} (or
    higher) on that agent() call. Live agent lines show searches as 🔎.
  - Channel lifecycle: a run publishes workflow_started, workflow_phase (one
    per phase() transition), and workflow_complete (with the result summary)
    through the workspace's publish spool — the manager hears all three as
    channel events. Per-agent progress deliberately does NOT go to the
    channel; that's the progress pane's job.
  - Run dirs are flat (<duck-home>/workflows/<run-id>/, workspace recorded
    in status.json) rather than per-workspace subdirs.
  - Journal replay keys on content hashes of (prompt, opts) with FIFO
    per-key queues — not call order — so nondeterministic scheduling
    degrades to cache misses, never wrong results. Date.now()/Math.random()
    are therefore NOT banned in scripts; nondeterminism just costs cache.
Companion to the spawn tool (one durable executor). The two routing buckets:

| verb      | shape                                   | driven by |
|-----------|-----------------------------------------|-----------|
| spawn     | one durable, human-watchable executor   | events    |
| workflow  | deterministic fan-out, disposable fleet | a script  |

Modeled on Claude Code's ultracode/Workflow tool: the manager writes a small
JS script whose control flow (loops, pipelines, barriers) is deterministic
code, and only the inside of each `agent()` call is a model. Intended use is
work one context can't hold or shouldn't trust to one pass: audits,
migrations, adversarial-verify review, judge panels, loop-until-dry
discovery.

## The tool

One new MCP tool on the duck-agents sidecar: `workflow`. Like spawn it is
**synchronous to start, async to completion** — it validates + launches the
run, returns a handle (`wf_...`) in a couple seconds, and the RESULT arrives
later as a `<channel source="duck-agents" type="workflow_complete">` event. The manager
never polls.

Input: `{script, args?, resume_from?}`. The script is persisted under
`~/.duck/workflows/<ws>/<run-id>/script.js` alongside its journal.

Opt-in mirrors ultracode: the manager only reaches for `workflow` when the
human asked for that scale in their own words ("run a workflow", "audit this
thoroughly", "fan out"), or a skill says to. This guidance rides the tool
description, per the guidance-rides-tools principle — not AGENT.md.

## Script surface (goja-embedded JS)

Plain JS, no TS. Must open with a pure-literal `meta = {name, description,
phases?}`. Globals:

- `agent(prompt, opts?) -> Promise<any>` — run one headless worker.
  opts: `{label, phase, schema, model, effort, cwd}`. With `schema`, the
  worker runs with `--output-schema` and `agent()` returns the parsed,
  validated object; without, it returns the final message text. Returns
  `null` on worker death after retries (callers `.filter(Boolean)`).
- `pipeline(items, ...stages)` — per-item stage chains, no barrier between
  stages. The default for multi-stage work.
- `parallel(thunks)` — barrier; use only when a stage needs ALL prior
  results (dedup, early-exit, cross-item comparison).
- `phase(title)`, `log(msg)` — progress narration (feeds the status surface).
- `args`, `budget` — run inputs; `budget.spent()/remaining()` in tokens,
  summed from worker token_count events.
- `Date.now()/Math.random()/new Date()` throw (they'd break resume).

Runner: a new `internal/workflow` package. goja for the script VM;
`agent()` bridges to a Go scheduler. Concurrency: workers are light
headless processes, not contexts on the local box, so the cap is NOT
ultracode's min(16, cores) — the real ceiling is provider throughput
(OpenAI rate limits for gpt tiers). Start at
~64 with a per-run `concurrency` knob, measure Moon Bridge under load,
raise from there. Lifetime cap (~1000) stays as a runaway backstop.

## Workers: headless codex exec, NOT panes

Each `agent()` call runs:

```
codex exec --json -o <dir>/last-message.json \
  [--output-schema <dir>/schema.json] \
  [-m <alias>] -C <cwd> --skip-git-repo-check <prompt>
```

- `--json` JSONL on stdout is the per-worker journal: token counts, tool
  calls, timing. Captured to `<run-dir>/agents/<n>.jsonl`.
- `--output-schema` gives native StructuredOutput. Fallback for tiers that
  fumble it: validate, then `codex exec resume <id>` with a "return ONLY
  valid JSON for this schema" nudge, twice, then `null`.
- Default model tier: **gpt-5.5** (the codex config default). Per-call
  `model`/`effort` opt-down to gpt-5.4-mini low-effort for finders,
  transforms, and mechanical stages. Same alias resolution as spawn
  (`internal/agent/model.go`).
- Sandbox: `-s read-only` unless the script marks a stage `write: true`
  (then `workspace-write` in an isolated worktree — mirror of ultracode's
  worktree isolation; design later, read-only covers the audit/review/
  research cases first).
- Workers do NOT enter the sidebar lot, the ledger, or the org chart.
  They're disposable: no @duck_name, no channel edges. The WORKFLOW is the
  addressable thing.

## Visibility (the three numbers: elapsed, agents, tokens)

The run itself gets exactly one presence in the workspace: a **dedicated
workflows section inside the agents tab** — a divider under the real
(pane-backed) agents listing active runs:

```
  claude        manager
  scout         codex
  ── workflows ──
  ⚙ audit-duck   12m   agents 41/128   3.2M tok
```

1. **Section rows** are synthetic (no pane behind them), driven by the
   runner's status file (`<run-dir>/status.json`, rewritten every few
   seconds). Selecting
   a row swaps the run's progress pane (below) into the viewport; `x` on
   it stops the run.
2. **Progress pane**: selecting a run swaps a live `duck workflows tail
   <run-id>` view into the viewport — the status header (phases, elapsed/
   agents/tokens) plus the journal of per-agent results and log() lines.
3. **`duck workflows` CLI**: `list` (running + recent, with the three
   numbers), `tail <run>` (journal), `stop <run>`, `resume <run>`.

Completion digest to the manager includes the script's return value
(truncated + path to full JSON), the three totals, and the run-dir path.

## Journal & resume

`<run-dir>/journal.jsonl`: one line per completed `agent()` call —
`{seq, prompt-hash, opts-hash, result, tokens, session_id}`. Resume
(`workflow` with `resume_from`) replays the longest unchanged prefix from
the journal; first changed/new call runs live. Codex session ids in the
journal let a human post-mortem any worker via the normal rollout files
(`~/.codex/sessions/...`) — unless `--ephemeral` is chosen for bulk runs
(tradeoff: disk vs. debuggability; default to persisting).

## Build order

1. `internal/workflow`: runner + goja bindings + journal + status.json.
   Worker = codex exec wrapper reusing model-alias resolution.
2. Sidecar `workflow` tool (serve.go tool table) + completion event on the
   channel spool.
3. Progress artifact + roster row + `duck workflows` CLI.
4. Later: worktree-isolated write stages.

## Open questions

- Budget source: ultracode has a user "+500k" directive; duck's nearest
  analog is a `--budget` arg on the tool call. Default uncapped-with-report
  or a soft default (e.g. 2M tokens) that the digest flags when hit?
- Named/saved workflows (`~/.duck/workflows/lib/*.js`) — probably wanted
  the first time a workflow gets reused; punt until then.
