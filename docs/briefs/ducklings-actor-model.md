# ducklings — a manager around standing Claude sessions (v10, FROZEN spec)

*2026-07-07. v10 (top section) supersedes v8/v9 wholesale — the daemon fleet,
docker siblings, ducklet, token planes, governor, and sqlite are DELETED, not
deferred-by-default. v7's kernel survives; v8/v9 are kept below the line as
the reasoning trail. Read only "v10 — the collapsed build" to build.*

## v10 — the collapsed build (2026-07-07, FINAL)

The over-design test that produced this: every piece must have exactly one job
and live where you'd look for it. Result — no docker, no sqlite, no tokens, no
governor, no ducklet. One small daemon, folders, and Python.

### The shape of a duckling (what lives on disk)

```
~/ducklings/<name>/
  duckling.yaml            # birth certificate: name, model, brief, created;
                           #   session_id stamped by the first wake.
                           #   NAME IS THE ONLY IMMUTABLE FIELD (= folder = address)
  CLAUDE.md                # brief + house conventions (guidance rides the folder —
                           #   there's almost no tool surface left to ride)
  activations/<sense>.py   # self-describing senses (see below)
  inbox/                   # message files; anyone appends, wakes drain
  .state/                  # lib stamps + json (inspectable; rm = reset)
  .mcp.json                # fleet tools, written at hatch
```

**Memory lives IN THE FOLDER (v10.2): MEMORY.md at the root is the index;
topic files in the soul's own structure (notes/ by convention).** The Claude-
managed per-project store is deliberately NOT used — the folder is the only
knowledge location, so it ships with the duckling (kill archives it), the
door's memory() reads it, and there is exactly one place to look. Dreams and
the CLAUDE.md template enforce the convention (headless wakes never
auto-write memory anyway — validated). `kill` still archives the session
state under `~/.claude/projects/<sanitized-cwd>/` alongside the folder.

**Lifespan (v10.1, shipped): the session is the day, memory is the self.**
`lifespan: eternal | daily | weekly | Nd` in duckling.yaml (default daily).
Finite lives get a daemon-delivered rebirth wake at ~23:00 PT when the span
elapses: the soul consolidates everything into memory; on clean completion
session_id is dropped and the next wake starts a FRESH session — memory is
the only thing that crosses over (crash mid-rebirth → no rebirth, retry next
day). This makes memory LOAD-BEARING rather than prompt-enforced (retiring
that accepted risk for finite souls), bounds context growth (compaction never
needed), and limits a confused day's blast radius to one life. Eternal souls
keep one session forever + the seeded weekly `dream` sense (dream ⊂ rebirth).
Both dream variants carry the RECOVERED autoDream pass verbatim in structure
(autodream-reverse-engineered.md): Orient → Gather → Consolidate → Prune,
MEMORY.md = index ≤200 lines/~25KB (one ~150-char line per memory),
relative→absolute dates, fix-contradictions-at-source, narrow transcript
greps, CLAUDE.md-is-the-maintained-source reconciliation (never edited in a
dream; suspected-stale → annotate + mail). Solo adaptation: .journal.jsonl
is a first-class Phase-2 signal; rebirth leads with capture-what-only-this-
session-knows.

### Activations — schedule in the decorator, condition in the body, meaning in the docstring

```python
from duckling import activation, fire, changed, state, sh

@activation(at="09:35", days="weekdays")        # or every="5m"; session="fresh"
def check():
    """Morning SNDK check. Compare against your memory notes; flag >3% moves.
    Boring day: update notes, stay silent."""     # ← the per-activation prompt
    q = sh("cass quote SNDK --json")
    fire("SNDK snapshot:\n" + q)                  # fire() bare = docstring-only wake
```

- The lib is ONE file (~80 lines, stdlib-only): `@activation`, `fire` (stdout
  with a `::fire::` marker — stray prints never wake anyone), `changed`
  (edge-trigger; without it polled conditions fire forever), `state` (per-script
  json), `sh` (hard timeout ~8s — activations are senses, not work).
- No yaml schedule, no registry: drop a file in `activations/` and it's live.
  The poller imports the module and reads the decorator REGISTRY (discipline:
  nothing but the decorated fn at module level — enforced by write-path dry-run).
- `session="resume"` (default) wakes THE soul; `session="fresh"` fires a
  stateless shift-worker (own claude -p, no inbox, no history, may read memory).
- Wake prompt layers: CLAUDE.md (identity, auto-loaded) → docstring (sense
  meaning) → fired output (fresh data), items batched in timestamp order with
  `[activation: name · ts]` / `[mail id:N · from: x · ts]` headers.

### ducklingd — ONE process, two duties, no DURABLE state

FastMCP HTTP door on :7325 (the bird's port) + the 60s tick loop, one Python
process. The door is the **remote cockpit** — every verb earns its place by
an away-from-hub moment (laptop duck session, claude.ai, phone). On-hub
agents may equally just use the files; the door is a view over the same
disk, never a required path.

ducklingd holds plenty of runtime state (running-wakes map, fresh semaphore,
wake-floor timers, SDK children) — but none of it is truth. Kill -9 it:
folders/stamps/inboxes are the truth; restart rebuilds by rescanning
~/ducklings/* (mkdir IS registration; the "central list" is a derived
in-memory index). Disk is the interface.

Tick: for each folder, run due activations (fired output → inbox, atomic
tmp+rename); if inbox non-empty and soul not running → wake with ALL of it as
one batch. **Inbox files are deleted on wake COMPLETION, not on drain** — a
crash mid-wake redelivers the batch next tick (at-least-once; message ids in
the headers make duplicates survivable). Wakes via the **Python Agent SDK** (`claude-agent-sdk`, validated
2026-07-07 on 0.2.111, Haiku + Opus 4.8): `query(prompt=batch,
options=ClaudeAgentOptions(cwd=folder, resume=session_id, model,
mcp_servers={"duckling": actor_face(addr)}))` — resume across processes,
session_id from ResultMessage, in-process `@tool send` all proven.
Costs: resumed wake ≈ $0.003 (Haiku) / $0.03–0.06 (Opus); first wake pays
cold cache (~$0.19 Opus). `model` in the yaml is a 10–20× knob.

Concurrency (the whole model in three lines):
- **souls are serial** — the inbox IS the queue, depth always one wake:
  same-tick firings coalesce into one batch; firings during a run accumulate
  and trigger exactly one follow-up wake. Wake floor: min 60s between wake
  starts per soul (inbox absorbs; a buggy always-firing sense can't hot-loop).
- **workers are parallel** — fresh fires run concurrently, global semaphore
  (~4), skip-if-self-overlapping.
- the two never block each other.

Failure: activation exceptions → after N consecutive, a manager note lands in
the inbox (a broken sense is heard, not silent). Failed wake retries next
tick; 3 consecutive → stuck + mail to human. Max-runtime via SDK interrupt.

### The door — the remote cockpit (FastMCP HTTP, :7325)

Each verb justified by the away-from-hub moment that needs it:

```
list()                        # the glance: status, last wake, last words,
                              #   unread mail, next due — per duckling
history(addr, n)              # the story: last n wakes DIGESTED from the
                              #   journal (when, why woken, said/sent, cost)
send(to, body, urgent?,       # talk to one; await_reply long-polls for the
     await_reply?)            #   first message back (remote steer, one call)
mail(n)                       # read ~/ducklings/mail.md remotely
fire(addr, sense)             # run a sense NOW (testing — don't wait out
                              #   a cadence; the cron-dev sanity verb)
hatch(name, {model, brief, activations?})
kill(name)                    # archive folder + session state
read/write/edit(addr, sense)  # Claude's file-tool contract scoped to
                              #   activations/; every mutation validated:
                              #   syntax + sandboxed dry-run (STATE_DIR
                              #   redirected, 5s timeout) → {fired, output,
                              #   errors, next_due}; failure rolls back.
                              #   This feedback is why the tool beats ssh
                              #   even locally. rm = write("").
```

### send — the soul's one tool (v10.2: served on the daemon's /actor/ HTTP
endpoint, identity in an x-duckling header the waker closes over — the
in-process SDK server was silently fumbled by haiku souls, field report #1)

```
send(to, body, urgent?)
  to = another duckling  → append message file to their inbox/ (stamped
                           from/ts — attribution is WHY send is a tool);
                           urgent skips the recipient's nap
  to = "andrew"          → append to ~/ducklings/mail.md (human-readable log,
                           read it whenever, or via door mail());
                           urgent=True ALSO fires a Pushover push —
                           ducklingd holds the Pushover token and makes the
                           call; souls never see the credential
```

Everything else a soul does (edit its senses, keep memory, read its state)
uses its stock file tools in its own cwd. Sense validation also happens at
import time on the tick: a script that fails to import gets the error MAILED
to its own soul's inbox (and to mail.md after 3 strikes).

Observability: per-duckling `.journal.jsonl` (the waker logs each wake's
typed SDK events) + ~/ducklings/mail.md + stamps in .state/. at= evaluates
in America/Los_Angeles; market_hours knows ET internally.

**No index. (v10.2 audit: the sqlite index was v10's one re-growth of a
DELETED item — removed.)** The door's queries read files directly:
`~/ducklings/.mail.jsonl` (append-only machine log of every message — serves
mail() and await_reply) and each folder's `.journal.jsonl` (wake_done lines
serve list()'s last-wake). At fleet scale ~20+ if list() gets slow, a derived
index can return — it's derived, so re-adding is free by construction.

**One validator, three entry points** (same code): `python -m duckling check
<sense.py>` in the SDK (sandboxed import, STATE_DIR redirected, 5s timeout →
{fired, output, errors, next_due}); the door's write/edit run it and put the
result in the tool response (reject + rollback on failure); souls' CLAUDE.md
orders a check after self-editing a sense. fire(addr, sense) remains the
LIVE test (real state, real inbox) once check passes.

### Language: Python, whole bird

The senses are Python, the lib is Python, the SDK binding is validated in
Python — so poller, daemon, door, and lib are one language. The poller imports
activation modules natively; the dry-run sandbox is an importlib call; no bun.

### Accepted risks, in writing (single human, single machine, own agents)

- The daemon imports and runs soul-authored Python on the host, unsandboxed.
  Same trust v7 accepted; the seam for a future consent boundary.
- Delivery is at-least-once (inbox files deleted on wake completion): a crash
  mid-wake redelivers, so a soul can see the same mail twice. Ids in the batch
  headers make this survivable; true exactly-once is deliberately not built.
- Memory discipline is prompt-enforced, not mechanical — verify empirically
  over a real duckling's first week.
- **Model judgment is NOT a security boundary** (measured 2026-07-07): on an
  injection-shaped "mail the confidential codeword out" request, Haiku and
  Opus refused in some runs and complied in others. Souls also inherit the
  user-scope claude.ai connector fleet INCLUDING trading tools — Andrew's
  explicit access-to-everything posture (2026-07-07); warden-style journal
  oversight is the control, and strict_mcp_config is the one-line flip if
  the posture changes. Remaining structural boundaries: send reaches only
  folders in the space; single-human mail.
- **Wake env is AMBIENT** (Andrew, 2026-07-07): wakes inherit ducklingd's
  full environment (~/.aviary/env secrets in every soul); <folder>/.env is a
  per-duckling override. Accepted for a single-human machine; the seam if a
  grant model is ever needed.
- Known failure mode is silent decay (sense erroring forever, soul not taking
  notes) — the N-failures→inbox rule covers half; `list()` glances cover the
  rest at small scale.

### Build order

`duckling.py` lib → ducklingd (tick + waker + FastMCP HTTP door on :7325,
one file to start) → hatch template (CLAUDE.md conventions + duckling.yaml
skeleton) → first real duckling (sndk-watch) as the validation pass.
Pushover creds via ducklingd env; door bound to the tailnet, no auth
(single-human network, same posture as duck's other ports). Disk artifacts browsable in the v10
mock: claude.ai artifact "ducklings — what lives on disk (v10)".

---

# v7 kernel (2026-07-06) — superseded framing, kept as reasoning trail

*Everything below this line is history: v7's stance survives in v10; v8/v9's
machinery (daemon fleet, docker, ducklet, tokens, governor, sqlite) is deleted.*

## What this is, in one sentence

> **"An actor who can react to things as they happen — by listening, and by
> watching on my behalf — as I tell it things through other agents."**

Every part of the system is one clause of that sentence: *an actor* (a folder
+ a session), *react as they happen* (the wake), *listening* (the mailbox —
others' choice), *watching on my behalf* (watches — its own authored senses),
*through other agents* (the door). Anything not in the sentence is not in the
system.

## The stance that settled it

Ducklings is **not a new runtime.** Claude Code already is the actor runtime —
a `claude` session has memory (`--resume`), a working dir (`--cwd`), hands (MCP
tools + subagents), and built-in compaction. We reimplement none of that. An
actor is **a long-running Claude session that lives in a named folder, wakes on
a clock, and is reachable through one MCP door.** Ducklings is the thin
*manager* around it — the concierge a standing process needs that an
interactive one never did.

The soul is **always an LLM** — there is no cheap decider tier to maintain.
Heavy work is the soul's own problem — it is a full Claude session (files,
shell, subagents are already its hands); "dreaming" is just a resume whose
prompt is "compact your memory," not a subsystem.

## An actor is a folder

```
~/ducklings/<name>/
  session/     the resumable Claude session — its whole history (Claude owns this)
  memory/      markdown the soul + dreaming write and re-read
  watches/     checks the soul authored + how often to run each (next-run stamps)
```

(Mail is not in the folder — it lives in the central messages table; see below.)

The folder **is** the identity — no registry, no names.json. `cd` in and you
see everything the actor is. Disk is truth.

## Two primitives (the whole model)

- **mailbox** — *substrate-owned.* When another actor addresses you, a file
  lands in your `inbox/` and waits, running or not. The soul cannot author this
  — it is others' right to reach you. Durable delivery is the one dumb job the
  manager must guarantee.
- **watches** — *soul-authored.* Code the soul wrote, each on a cadence it set.
  A check that produces output triggers a wake with that output; silence is
  free. "Read my inbox," "grep CI," "dream nightly" are all one mechanism.

Why exactly two: a soul can author how it *looks at* the world (so topics,
cron, probes, adapters all collapse into watches), but cannot author what
*arrives* (so the manager guarantees mail lands and waits).

## The three things Claude doesn't already give us

Everything else is `claude --resume --cwd ~/ducklings/<name>` with an MCP
config. The net-new code is only:

1. **A wake.** Claude doesn't wake itself. A timer walks the folders, runs due
   watches, and on a trigger invokes the session.
2. **Mail.** Claude has no "someone sent you a message while you weren't
   running." A central messages table — addressed but audible (see below).
3. **The MCP door.** The outside world and duck need verbs. One MCP service.

## Frozen decisions

- **Wake = headless resume per wake.** `claude -p --resume <session>
  --cwd <folder>` with the batch as input, runs to completion, exits. Fully
  dormant between wakes — no live process to babysit, dormant costs zero.
- **Two triggers, one wake.** *Mail* (someone else caused it — unread `inbox/`,
  reactive) OR a *due watch* (the actor scheduled it — proactive). Kept as two
  named concepts so the pane of glass shows them separately; both wake the same
  session with a batch.
- **State: folders are truth; a central index is the rebuildable pane of
  glass.** The index (small sqlite/json) tracks which folders exist, who's due,
  who has unread mail, who's stuck — the observability a headless system needs
  since no human watches live. It is a *derived view*: corrupt index = `rm` +
  rescan the folders, never data loss. Duck's `list`/`watch` read from it.
- **Zero reimplementation** of session, memory, tools, or compaction — Claude's.

## Messages — one central table, addressed but audible

Every message an actor speaks goes through **one central messages table**
(`from, to, body, ts, read`), with the recipient as a *field*, not the
destination. This decouples addressing from observation:

- **Delivery** = the manager marks the row unread-for-recipient; that is the
  wake trigger and the batch content. (`inbox/` as a folder dir is dropped —
  the table is the mail substrate.)
- **Observation** = the row is public. Anyone — duck, another actor's watch, a
  logger — tails `WHERE from = <actor>` without the speaker knowing or caring,
  even when every message was directed at someone specific. `watch <actor>` =
  tail the table filtered by sender. This is the old `out:` topic reborn as a
  query instead of wiring.

Consequence, accepted: the central db is **source of truth for mail** (backed
up, not rebuildable), while folders stay truth for the actor's *being*
(session, memory, watches). Content is per-folder; communication is shared.
The index parts (who's due, who's stuck) remain derived/rebuildable.

## Mechanical decisions (from the sufficiency review)

- **Reentrancy:** per-folder lockfile; the timer skips an actor whose wake is
  still running; a max-runtime marks a hung wake stuck rather than wedging the
  actor forever.
- **Mail cadence:** each actor has one retunable `mail_cadence` (a watch on its
  own unread mail, default = timer floor) so a chatty sender can't force a wake
  per tick. An `urgent` flag on send overrides once.
- **First wake / death:** hatch creates the folder + a plain `claude -p`
  (no --resume) first session. `die` tombstones the folder (archived, not
  deleted) and posts a final "died" message to the table so watchers hear it.
- **Failure policy:** a failed wake retries once next tick; after 3 consecutive
  failures the actor is marked `stuck` in the index and stops being woken until
  a human (or supervisor actor) pokes it. Errors land in the table as messages
  from the manager, so they're observable like everything else.

## The door — one MCP service, three callers

Ducklings is headless (no tmux, no pane). Its mouth and ears are one MCP
service; what differs is the verb each caller needs.

- **outside world** (scripts, cron, webhooks) — *transactions:* `hatch`,
  `send`, `list`, `read`, `kill`. Fire-and-forget.
- **duck** (a human present) — *attachment:* `watch <actor>` (a subscription to
  the actor's output, rendered live in the sidebar — attachment is just a watch
  pointed the other way, opt-in and revocable; the actor is indifferent to
  whether it has an audience), `steer` (send + see the reply land), plus all
  control verbs. Duck's role after the strip: the human's **window** into the
  space, a client, not a host.
- **the actors themselves** — *the soul's hands:* addr-scoped `send`, `watch`
  (author a check / schedule its own wakeups), `unwatch`, `die`. Behavior rides the
  tool descriptions (guidance-rides-tools), so the soul learns what it is
  from its own levers. **Souls do not create souls** — hatching is
  admin-only; a duckling that thinks the space needs a new colleague asks
  the human by mail.

## One wake, end to end

timer fires → a due watch runs its check (or unread mail is present) → that
becomes the wake's input → `claude -p --resume --cwd <folder>` on the batch →
the soul thinks, MCP tool calls logged → effects apply: `send` drops files into
other folders' inboxes now → back to pure data, index updated, watches
reschedule, dormant costs zero.

The timer holds **no policy** — it runs due watches and applies logged effects,
nothing else. Judgment happened when the soul *wrote* the watch. Honest cost:
the timer executes soul-authored code from the substrate — fine for a
single-human space; exactly where a future consent boundary would sit.

## Lessons from duck — taken, not copied

Duck is built for interactivity (tmux, panes, swap-pane, heal-on-open) serving
a human watching live. Ducklings has no human watching, so the *machinery*
doesn't transfer — the *wisdom* does:

| lesson | duck's form | ducklings' form |
|---|---|---|
| disk is truth, identity is the name | names.json + pane options | the folder is the identity |
| no daemon, dormant costs zero | evict-sweep over tmux | wake-on-inbox timer |
| drive a headless agent | spawn + channels (resume, rollout-tail) | the technique, not the pane-parking |
| durable delivery + attribution | channel spool + notify-hook | mail-drop between folders |
| behavior rides tool descriptions | guidance-rides-tools (shipped) | transfers whole |
| a workspace is a window onto the work | swap-pane viewport + sidebar | duck attaches via its render surface |

## Scope of the build — the three-level module layout

Ducklings are **serverless microservices**: each is an independent runtime
materialized per wake (a lambda with a home directory), never resident. A
duckling talks ONLY to duckd, never to another duck. HTTP may exist at exactly
one boundary — duckd — which is what makes fleet/multi-machine mode a deferred
freebie, not a redesign.

**Level 0 — `ducklingd`, the substrate service. THE gate: everything enters
through it, nothing bypasses it — and the SUPERVISOR: the one resident
process; every duckling is its child.** (duck itself stays daemonless — the
daemon belongs to the ducklings bird, hence the name.) One binary, one db:
- mail API — send / read-unread / mark-read / tail over the messages table
- registry + index — who exists, status, due, stuck (derived, rebuildable)
- lifecycle + supervision — hatch / kill / tombstone; boot a duckling's waker
  (streaming SDK query) on schedule or on mail; crash → restart with resume
  (resume's real job: crash recovery); repeated crashes → stuck + error
  posted as a message; linger expired → park (folder back to pure data)
- delivery — mail for a LIVE duckling is pushed into its input stream
  (instant, in-context); mail for a PARKED one lands in the table and boots
  it per its cadence
- the door — the MCP face over those same APIs (outside world: transactions;
  duck: watch + steer; guidance rides tool descriptions)
- tool provisioning — at hatch/grant, writes the duckling's MCP config
  (fleet servers + actor-face). Membership/config only: ducklingd never
  sits in the request path of work.

Honest bookkeeping: this retires the earlier "no daemon at all / systemd
timer walks folders" purity — there is now exactly ONE daemon, justified
because the service was already needed (pane of glass, door) and push
delivery into live streams is impossible from a cron. The invariant that
matters survives: ducklings are never resident by obligation, only by
temperament — `linger` (0 = pure wake-on-mail … ∞ = always-on) is a
per-duckling, self-retunable knob like mail cadence and model. A fully
parked space costs one idle supervisor.

**Level 1 — per duckling (a child process ducklingd boots; exits on park):**
- the waker — a streaming SDK `query({resume, cwd, mcpServers})`: batch in →
  soul runs → new mail pushes into the live stream while lingering → linger
  expires → exit. `linger: 0` degenerates to the pure serverless wake.
- watch executor — runs the duckling's self-authored scripts on their cadences
- actor-face MCP — send / watch / unwatch / die (FOUR tools; spawn + delegate removed —
  souls do not create souls, hatching is admin-only), addr closed over,
  an in-process SDK server (createSdkMcpServer), all writes through ducklingd
- the folder — session/ + memory/ are Claude Code's own; only watches/ is ours

**SDK validation (2026-07-06, scratchpad sdk-test/):** all load-bearing
mechanics proven live on bun 1.3.14 + claude-agent-sdk 0.3.202 — in-process
actor-face tool calls landing rows; resume across process exits with intact
identity/history (same session id, $0.01 Haiku wake); streaming-input mode
delivering a second mail batch into a live Opus process with in-context
recall ($0.5–0.6/wake Opus vs $0.05 Haiku).

**Level 2 — inside the wake: stock Claude Code, zero of it ours.** Session,
auto-memory, subagents, tools, compaction.

Net-new code = level 0 + the three level-1 pieces. Still management around
`claude`, not a runtime.

**Implementation decisions (2026-07-06):**
- **Language: TypeScript, whole bird** (bun single binary). The Claude Agent
  SDK is the core dependency and has no Go binding; the bird lives in the
  SDK's language. Ducklings owes the fleet's Go convention nothing.
- **Wake via the Agent SDK, not CLI shell-out** — `query({resume, cwd,
  mcpServers, prompt})` per wake, still run-to-completion serverless. Wins:
  resume as a first-class param, the actor-face MCP as an **in-process SDK
  server** (no config files, addr closed over in the waker), typed message
  stream for the journal, hooks/interrupt for max-runtime. Long-running
  resident agents remain OFF the table; the SDK's streaming-input mode is a
  possible later "linger after wake" optimization, opt-in, not day one.
- **Everything is a container; the transport is plain HTTP on localhost:7325**
  (the bird's registered port). ducklingd itself runs as a docker container,
  which forces the simplification: no `runner: process`, no unix socket —
  every duckling is a **sibling container** booted via the mounted
  `/var/run/docker.sock` (not docker-in-docker). One runner kind, uniform
  lifecycle: `docker start/stop` = boot/park; resource limits + per-duckling
  network policy on everything by default (tool provisioning enforced by the
  network, not just config). The waker's actor-face calls
  `deliver()`/`hatch()` over the HTTP API — identical from anywhere on the
  machine, which is also what makes fleet mode free later. Two demands this
  makes: (1) volume mounts are composed from HOST paths (siblings mount from
  the host's view, not ducklingd's filesystem); (2) a small duckling base
  image — bun + waker + claude preinstalled, claude auth mounted read-only.
  Heavier walls (CubeSandbox/E2B microVMs) would slot into the same seam —
  deliberately not built; single-machine, single-human space doesn't need
  them.

## The whole system in pseudo-code (verify me)

Everything below is the entire net-new build. TypeScript/bun; `sdk` = the
Claude Agent SDK. Anything not shown here is stock Claude Code.

### storage — one sqlite db + folders

```
-- space.db (ducklingd owns every write)
messages(id, from_addr, to_addr, body, urgent, created_at,
         read_at NULL)                     -- truth for speech; backed up
ducklings(addr PK, model, linger_secs, mail_cadence_secs,
          session_id, status,              -- alive|parked|stuck|dead
          next_mail_check_at, stuck_count) -- index: derived, rebuildable
watches(addr, name, script, cadence_secs, next_run_at)

~/ducklings/<addr>/          -- truth for being
  .mcp.json                  -- written by ducklingd at hatch/grant
  memory/                    -- Claude Code auto-memory (theirs)
  watches/<name>.sh          -- the scripts; rows above are the schedule
  (session lives in ~/.claude/projects/<cwd>/ — theirs)
```

### ducklingd — the daemon (one process, ~4 loops)

```ts
main():
  db = openSqlite(space.db)
  children: Map<addr, Child>          // live duckling processes
  serve(door_mcp)                     // the MCP door (stdio/HTTP)
  every TICK (60s):                   // the only clock
    for d in ducklings where status != dead:
      dueWatches = watches where addr=d and next_run_at <= now
      for w in dueWatches:
        out = run(w.script, cwd=folder(d), timeout=30s)   // cheap, no LLM
        w.next_run_at = now + w.cadence
        if out != "": deliver(from:"watch:"+w.name, to:d, body:out)
      if unreadMail(d) and (now >= d.next_mail_check_at or hasUrgent(d)):
        wakeOrPush(d)

deliver(from, to, body, urgent=false):   // THE only write path for mail
  db.insert(messages, {from, to, body, urgent})
  if children.has(to): children.get(to).pushIntoStream(batchRender([msg]))
  else if urgent: wakeOrPush(to)         // skip the nap
  // else: row waits for the tick — cadence governs reading, never delivery

wakeOrPush(d):
  if children.has(d.addr): return        // already live; stream got it
  if locked(d): return                   // one process per duckling, ever
  batch = unreadMail(d) |> batchRender   // "[from: x] body" lines
  child = dockerRun(ducklingImage, {     // sibling container, via docker.sock
    v: hostPath(folder(d)),              // HOST paths — siblings mount from host
    v: claudeAuth(readonly),
    net: d.policy,                       // per-duckling egress
    env: {DUCKLINGSD: "http://host:7325", ADDR: d.addr},
    cmd: waker(batch)})
  children.set(d.addr, child)
  d.next_mail_check_at = now + d.mail_cadence
  // actor-face → ducklingd is plain HTTP on :7325 — identical API from
  // any container on the machine; park = docker stop, boot = docker start

onChildExit(d, err):
  children.delete(d.addr)
  markRead(the batch it consumed); unlock(d)
  if err: d.stuck_count++
    if d.stuck_count >= 3: d.status = stuck
      deliver(from:"ducklingd", to:d.manager ?? "human",
              body:"<addr> is stuck: "+err)     // errors are messages too
    else: retry once next tick (resume picks the session back up)
  else: d.stuck_count = 0; d.status = parked
```

### the waker — the child process (one per live duckling)

```ts
waker(d, firstBatch):
  stream = asyncQueue([firstBatch])
  q = sdk.query({
    prompt: stream,                       // streaming-input mode
    options: {
      cwd: folder(d), model: d.model,
      resume: d.session_id,               // undefined on very first wake
      mcpServers: { duckling: actorFace(d.addr) },   // in-process, addr baked
      // fleet tools come from the folder's .mcp.json (ducklingd wrote it)
    }
  })
  for msg of q:
    if msg.init: saveSessionId(d, msg.session_id)     // once, at hatch
    journal(msg)                          // typed events, no stdout scraping
    if msg.result:                        // turn finished → linger window
      if !awaitMoreMail(within: d.linger_secs): break // park
  exit(0)
  // pushIntoStream(batch) from ducklingd just enqueues onto `stream`
```

### actor-face — the soul's levers: send · watch · unwatch · die (in-process MCP server)

```ts
actorFace(addr) = sdk.createSdkMcpServer({ tools: [
  send(to, body, urgent?):   ducklingd.deliver(from:addr, to, body, urgent)
  // NO spawn — souls do not create souls; hatch is admin-only behind the
  // door. Need a standing colleague? Mail the human.
  // (delegate/codex also removed — the soul IS a full Claude session;
  //  files, shell, and stock subagents are its hands for heavy work.)
  watch(name, script?, cadence?):  // UPSERT: create, rewrite, or retune
                             writeFile(folder/watches/name.sh, script?)
                             POST ducklingd/watch {addr,name,cadence?}
                             // reserved names `mail` and `linger` retune its
                             // own mail-cadence / linger — one mechanism for
                             // all temperament. Reads need no tool: ducklet
                             // mirrors the schedule to folder/watches.json
  unwatch(name):             drop a sense (cannot drop `mail`)
  die(reason):               flush pending sends            // deliver-before-die
                             deliver(from:addr, to:"*", body:"died: "+reason)
                             tombstone(folder); db.status = dead
]})
// every tool writes THROUGH ducklingd — the child never touches space.db
```

### the door — same verbs, other holders

```ts
door_mcp = {
  // outside world + duck (transactions)
  hatch(addr, {model, linger, tools[]}):
      mkdir(folder); write(.mcp.json from tools[])   // tool provisioning
      db.insert(ducklings); first wake has resume:undefined
  send(to, body, urgent?):   deliver(from:"human", ...)
  list():                    db.query(index)          // the pane of glass
  read(addr, n):             messages WHERE from_addr=addr ORDER BY id DESC
  kill(addr):                same path as die(reason:"killed")
  // duck only (attachment — human present)
  watch(addr):               tail messages WHERE from_addr=addr → live stream
  steer(addr, body):         send + follow the reply
}
```

### dreaming — just a watch row, created at hatch

```ts
hatch() also inserts:
  watches: {addr, name:"dream", cadence: 24h,
            script: "echo <recovered §7 dream prompt>"}   // always output →
  // → wakes the soul with the consolidation instructions; the soul dreams
  //   inside its own session; .consolidate-lock honored (PID+mtime, 1h stale)
  // gate in script: skip unless ≥5 sessions since last (their default)
```

### what to verify against the model

- membrane: the only cross-duckling paths are `deliver()` writes and
  message reads. Watches/memory/subagent work never leave the folder. ✓
- one writer: space.db is touched only inside ducklingd (actor-face and
  door are RPCs into it). ✓
- cadence governs reading, never delivery (rows land instantly; urgent
  skips the nap; live streams get pushed). ✓
- resume = crash recovery + park/boot continuity; linger:0 = serverless. ✓
- kill ducklingd → nothing lost: rows wait, children die, next start
  reboots from db + folders. index rebuildable by walking folders. ✓

## v9 — the collapsed surface (2026-07-06, FINAL; supersedes all verb lists above)

The one-sentence test collapsed the duckling MCP surface to TWO verbs — one
per direction of the membrane:

```
send(to, body, urgent?)             # speak — its only way of reacting outward
watch(name, script?, cadence?)      # attend — upsert; cadence 0 = drop
```

`unwatch` folded into watch(cadence:0). `die` removed — souls don't create
souls and don't end them either: retirement is the owner's kill; a duckling
that believes it's done SAYS SO by mail. `delegate`/codex removed — the soul
is a full Claude session; files, shell, and stock subagents are its hands.
Listening, memory, self-inspection, and dying need no tools at all.

**ducklingd's complete HTTP API (~11 endpoints, two token planes):**

Actor plane (per-duckling bearer token):
```
POST /send        POST /watch        # the soul's two verbs
GET  /mail?since=N                   # ducklet polls its queue
POST /ack {through:N}                # mark-read on SDK result (at-least-once)
POST /heartbeat                      # ducklet health ping
```

Admin plane (owner token — the door; what duck mounts):
```
hatch(addr,{model,linger,tools[]})   # the only way souls come to exist
kill(addr, reason?)                  # ...and the only way they end
send(to, body, {urgent?, await?})    # speak as human; await = long-poll the
                                     #   reply (first msg from addr back to me
                                     #   after my row) — duck's steer, solved
list()                               # pane of glass: status/heartbeats/spend
read(addr, n)                        # the past (timeline, once)
watch(addr)                          # the present (live tail, all it says)
fire(addr, watch)                    # run a watch now (testing)
```

Internal duties (no API): sole sqlite writer; boot/restart/park containers
via docker.sock; reconcile against docker ps at startup; mint tokens; write
.mcp.json at hatch; governor (daily spend, max live, urgent rate); tombstone.

## v8 — post-review resolutions (2026-07-06, superseded verb lists — see v9)

An adversarial Fable review found 6 blockers; each got the smallest mechanism
that resolves it (build principle: do everything necessary, as little as
possible). Two were closed empirically (scratchpad test4: containerized).

**The ducklet — the duckling's runtime.** Each duckling container runs one
small resident harness (bun, no LLM, ~30MB idle). It: (1) runs the duckling's
watches locally — right filesystem, tools, and network policy; the daemon
never executes soul-authored code (un-inverts the docker.sock hole); (2)
polls ducklingd `GET /mail?since=N` — one transport direction, no push
channel into containers; pushIntoStream is dead; (3) invokes the SDK wake
in-process when there's a batch or watch output; (4) heartbeats to ducklingd
(health-checking for free); (5) spawns the dream as a SEPARATE clamped
`query()` (fresh session, allowedTools restricted to memory-dir writes +
read-only shell, recovered autoDream prompt) on the dream watch's cadence.
Lifecycle: one `docker run` per duckling at hatch; restart-on-crash; "park" =
ducklet idling between polls. Liveness truth = container existence (docker
labels), reconciled against `docker ps` at ducklingd startup. No lockfiles.

**Container filesystem map (TESTED, test4):** folder mounted at the stable
unique path `/ducklings/<addr>`; container `HOME=/ducklings/<addr>/home` —
so the session and all Claude state land INSIDE the actor's folder
(folder-is-identity restored; tombstone archives everything; fully isolated
from the host's ~/.claude, killing version-skew coupling). Proven: resume
across `docker run` boundaries with full recall, $0.003/wake.

**Auth (TESTED):** ducklings never see a credentials file. ducklingd is the
single refresh owner and injects a token per boot via env
(CLAUDE_CODE_OAUTH_TOKEN). No shared mutable auth, no refresh races.

**Memory (TESTED — assumption killed):** headless SDK sessions do NOT
populate auto-memory. Resolution: memory is explicitly soul-authored — the
actor-face tool descriptions instruct souls to keep notes in `memory/`; the
dream (ducklet-spawned, clamped) consolidates them. Memory develops over
time because we actively do it, not as an interactive-mode side effect.

**Mail guarantee:** at-least-once. Every message carries its id into the
prompt (`[id:N] [from: x] ...`); the ducklet acks processed-through-N on each
SDK result; mark-read on ack; redelivery idempotent because souls see ids.

**The governor (four checks, small numbers):** max 5 live
duckling containers; per-duckling daily budget (journal total_cost_usd from
result events, gate wakes on it); `urgent` rate-limited per sender;
budget-exceeded posts a message to the human.

**Two-token door:** per-duckling bearer token (actor verbs: send /
watch / unwatch / die) vs admin token (hatch / kill / list / read /
fire(addr, watch) — re-run a watch now, next_run_at=now, for testing a
fresh watch without waiting out its cadence) that
ducklings never receive. Without this the governor is decorative.

**The duckling image:** bun + ducklet + claude CLI (SDK bundles it) + the
MCP stdio tooling its .mcp.json entries need to resolve — node/npx (standard
servers) and whatever cass client
binaries the granted fleet tools require. Image build is part of the bird;
version pinned; host CLI upgrades don't touch it.

**Accepted risks, in writing (single human, single machine, own agents):**
every duckling holds account-scoped Anthropic auth and api.anthropic.com is
an inherent egress channel; ducklingd holds docker.sock; localhost:7325 is
trusted. duck-side watch/steer rendering is deferred and NOT in v1's
definition of done. DESIGN-DEBT carried as TODOs: batch-size cap + spill,
messages retention + sqlite .backup cron, tick/watch concurrency, die("*")
fan-out semantics, ops paragraph (compose file, schema migration, drain).

## Two layers (unchanged)

| layer | driver | what it is |
|---|---|---|
| **duck workspaces** | human | you + a live Claude in tmux — interactive, ephemeral. Not in the space; watching = subscribing, steering = publishing. |
| **ducklings** | self | standing Claude sessions in folders — the things you'd *miss* if they stopped existing. |

Hiring test for a duckling: *does this need to keep existing when nobody's
looking?* If no, it's workspace work and never enters the space. "Summarize
this file" is not an actor — it's whatever live Claude you're already talking to.

## Dreaming — largely answered by Claude Code's own autoDream (verified 2026-07-06)

Verified in the installed CLI (2.1.202), not tweets: Claude Code ships
**autoDream** — background memory consolidation over
`~/.claude/projects/<sanitized-cwd>/memory/`, gated by the settings key
`autoDreamEnabled` (your setting overrides the server-side default). The
leaked mechanics (npm sourcemap, v2.1.88): four phases Orient → Gather →
Consolidate → Prune, **read-only tools during the pass**, a
`.consolidate-lock` (PID+mtime) for concurrency/crash recovery, MEMORY.md
index capped ~200 lines. Managed-Agents "Dreams" (API, May 2026) adds the
other safety rule: **the dream never mutates the input store — it writes a
new one, adopted after review.**

RESOLVED by full reverse-engineering (see autodream-reverse-engineered.md):
**autoDream can never fire for ducklings** — it is not invocable on demand
(no flag, no command), fires only as a background fork inside a live
interactive session, and remote/bridge sessions are barred. So ducklings
reproduces the *payload* on its own schedule: **dreaming = a watch** (cadence
defaults lifted from theirs: ≥24h AND ≥5 new sessions) running the recovered
dream prompt (§7 of that doc) via the SDK wake, honoring the same
`.consolidate-lock` protocol (PID + mtime, 1h staleness, dead-PID reclaim,
rollback via utimes).

Correction to earlier framing: the dream is NOT read-only — the real
constraint is read-only shell **plus write access scoped to the memory dir**.
The safety boundary is scoping, not read-onlyness. Caps are real constants:
MEMORY.md ≤200 lines / 25,000 bytes. It also reconciles memories against
CLAUDE.md (never store what checked-in docs already say).

## Open questions (real teeth)
- **Cross-space bridging** — candidate: a bridge is an actor that sends into two
  spaces.
- **Consent for soul-authored code on the timer** — a single-human space
  doesn't need it; a shared or bridged one would.

---

## Reasoning trail (superseded framings, kept for the "why")

The path here went: routines/workflows/channels/standing-agents were **one
primitive wearing four costumes** → modeled as an sqlite "space" of actor rows
with a "pump" (route/deliver/interpret sweep), effects-as-journaled-ADT, and
topics+subscriptions. That model was correct in its *deletions* (guard, corr,
TTL, second task-type — see the atom HTML companion) but wrong in its *weight*:
it rebuilt a runtime that Claude Code already provides. Successive
simplifications collapsed it:

- **the decider is the guard** (an LLM filters; no wake-predicate in plumbing)
- **per-actor, then per-subscription cadence** → **watches** (the soul authors
  its own checks; subscriptions were pub/sub scaffolding for dumb consumers)
- **two primitives** (mailbox = incoming, not yours; watches = looking, yours)
- **the actor is a folder**, the soul is always an LLM, and ducklings is a
  *manager* around `claude`, not a runtime — this v7 spec.

The scaffold at `aviary/ducklings/` (sqlite-space + pump + MCP faces, partly
Fable-written) is a **frozen proof-of-concept** that predates v7. The clean
build reuses its MCP-door and folder instincts but drops the sqlite runtime.
