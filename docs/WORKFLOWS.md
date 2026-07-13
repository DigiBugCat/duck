# duck workflows — detached, replayable Codex runs

Status: implemented (`internal/workflow`, `command/workflows.go`).

`duck workflows` is an explicit operator CLI for deterministic JavaScript
fan-out over headless `codex exec` workers. It is independent of Claude Code's
native Workflow tool: use the native tool for delegation inside a conversation;
use duck's CLI only when a run must persist outside that conversation and be
inspected or resumed by run ID.

## Commands

- `duck workflows run <script.js>` — validate and start a detached run
- `duck workflows list` — show active and recent runs
- `duck workflows tail <run-id>` — follow status and worker activity
- `duck workflows stop <run-id>` — stop a run

Read a completed run's persisted `result.json` from the run directory shown by
`list` or `tail`.

Runs persist under the duck workflow home with `script.js`, `status.json`,
`result.json`, worker streams, and `journal.jsonl`. Completion is recorded in
those files; it is not injected into a manager conversation.

## Script surface

Scripts are plain JavaScript executed by goja and begin with a literal metadata
export. Available globals:

- `agent(prompt, opts?)` — run one headless worker. Options include `label`,
  `phase`, `schema`, `model`, `effort`, and `cwd`.
- `pipeline(items, ...stages)` — process each item through its stages without a
  barrier between stages.
- `parallel(thunks)` — run a barrier when the next step needs all prior results.
- `phase(title)` and `log(message)` — update persisted progress.
- `args` and `budget` — run inputs and token accounting.

`Date.now()`, `Math.random()`, and argument-less `new Date()` are rejected so a
script can be resumed deterministically.

## Workers

Each `agent()` call is a headless Codex process, not a tmux pane. Workers write
JSONL event streams beneath the run directory. Structured calls validate their
final response against the supplied schema; provider profiles that ignore
Codex's native output-schema flag receive an inline schema and bounded repair
turns.

The default model is the configured Codex default (`gpt-5.6-sol` in the current
catalog). Calls can opt down to `gpt-5.6-terra` or `gpt-5.6-luna`. Concurrency is
bounded by the runner and total calls have a runaway cap.

## Journal and resume

The journal records completed calls by a content key derived from prompt and
options, with FIFO results for repeated identical keys. Resuming an edited
script reuses matching completed calls and executes new or changed calls live.
Worker session IDs remain in the journal for post-mortem inspection.

The run directory and status/result files are authoritative. There is no channel
sidecar, publish spool, manager event, or MCP tool associated with this CLI.
