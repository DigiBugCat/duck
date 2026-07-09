# autoDream / dream memory-consolidation — reverse-engineered

Source: `/home/andrew/.local/share/claude/versions/2.1.202` (Claude Code CLI,
~262 MB bundled JS/ELF). Extracted via `grep -abo` byte offsets + windowed
`tail -c | head -c`; no execution. All prompt text below is verbatim from the
binary (unicode escapes like `—` rendered as their characters: `—`).

The feature: while a Claude Code session is running, an idle-triggered
background forked agent ("a dream") periodically consolidates the user's
auto-memory files. It is the write-heavy sibling of *extractMemories* (which
runs inline after turns). Internal names: `autoDream`, task type `"dream"`,
prompt builder `N6l`, scheduler `q6l`/`W6l` (exported as `V6l`).

---

## 1. The full dream prompt

Built by `N6l(e, t, r, n)` where:

- `e` = memory directory (`autoMemoryDirectory`, `$m()`)
- `t` = session-transcripts directory (`Sy(sn())`)
- `r` = additional context string (tool constraints + session list — see §2)
- `n` = boolean: team-memory enabled (`UL()`), toggles the `team/` block

Interpolated constants: `GA="MEMORY.md"`, `PJ=200` (index line cap),
`gEe` = the "directory already exists" note, `U9f` = the team-memory block,
`j9f` = the CLAUDE.md-reconciliation block, `M6l()`/`$6l()` = currently-empty
extension hooks (both gated by functions that `return!1`, so they emit `""`).

```markdown
# Dream: Memory Consolidation

You are performing a dream — a reflective pass over your memory files. Synthesize what you've learned recently into durable, well-organized memories so that future sessions can orient quickly.

Memory directory: `[${memoryDir}]`
This directory already exists — write to it directly with the Write tool (do not run mkdir or check for its existence).

Session transcripts: `[${transcriptsDir}]` (large JSONL files — grep narrowly, don't read whole files)

[${teamMemoryBlock — present only when team memory enabled, see below}]

---

## Phase 1 — Orient

- `ls` the memory directory to see what already exists
- Read `MEMORY.md` to understand the current index
- Skim existing topic files so you improve them rather than creating duplicates
- `ls -R logs/` — recent activity logs (one file per session under `YYYY/MM/DD/`). If a `sessions/` subdirectory also exists, review recent entries there too

## Phase 2 — Gather recent signal

Look for new information worth persisting. Sources in rough priority order:

1. **Session logs** (`logs/YYYY/MM/DD/<id>-<title>.md`) — the append-only activity stream, one file per session. Read the most recent 1–3 days of sessions (the filename title tells you what each was about); each line is prefix-coded (`>` user, `<` assistant, `.` tool call)
2. **Existing memories that drifted** — facts that contradict something you see in the codebase now
3. **Transcript search** — if you need specific context (e.g., "what was the error message from yesterday's build failure?"), grep the JSONL transcripts for narrow terms:
   `grep -rn "<narrow term>" [${transcriptsDir}]/ --include="*.jsonl" | tail -50`

Don't exhaustively read transcripts. Look only for things you already suspect matter.
[${M6l() — empty extension hook}]
## Phase 3 — Consolidate

For each thing worth remembering, write or update a memory file at the top level of the memory directory. Use the memory file format and type conventions from your system prompt's auto-memory section — it's the source of truth for what to save, how to structure it, and what NOT to save.

Focus on:
- Merging new signal into existing topic files rather than creating near-duplicates
- Converting relative dates ("yesterday", "last week") to absolute dates so they remain interpretable after time passes
- Deleting contradicted facts — if today's investigation disproves an old memory, fix it at the source

## Phase 4 — Prune and index

Update `MEMORY.md` so it stays under 200 lines AND under ~25KB. It's an **index**, not a dump — each entry should be one line under ~150 characters: `- [Title](file.md) — one-line hook`. Never write memory content directly into it.

- Remove pointers to memories that are now stale, wrong, or superseded
- Demote verbose entries: if an index line is over ~200 chars, it's carrying content that belongs in the topic file — shorten the line, move the detail
- Add pointers to newly important memories
- Resolve contradictions — if two files disagree, fix the wrong one

[${j9f — CLAUDE.md reconciliation block, always present, see below}]
[${$6l() — empty extension hook}]
---

Return a brief summary of what you consolidated, updated, or pruned. If nothing changed (memories are already tight), say so.[${r ? "\n\n## Additional context\n\n" + r : ""}]
```

### The `[${teamMemoryBlock}]` (constant `U9f`, inserted after the transcripts line when `n`/`UL()` is true)

```markdown
## Team memory (`team/` subdirectory)

The `team/` subdirectory holds memories shared across everyone working in this repo. Other teammates' Claude sessions write here too — treat it differently from your personal files:

- **Phase 1:** `ls team/` and skim it alongside your personal files. A teammate may have already captured something you'd otherwise duplicate.
- **Phase 3:** Merge near-duplicates *within* `team/` the same way you would personal memories. If a personal memory restates a team memory, delete the personal one.
- **Phase 4 — be conservative pruning `team/`:**
  - DO delete or fix a team memory that is clearly contradicted by the current code, or that a newer team memory marks as superseded.
  - DO NOT delete a team memory just because you don't recognize it or it isn't relevant to *your* recent sessions — a teammate may rely on it.
  - When unsure, leave it. A stale team memory costs little; deleting a teammate's load-bearing note costs a lot.

Do not promote personal memories into `team/` during a dream — that's a deliberate choice the user makes via `/remember`, not something to do reflexively.
```

### The CLAUDE.md-reconciliation block (constant `j9f`, always in Phase 4)

```markdown
### Reconcile memories against CLAUDE.md

Project CLAUDE.md instructions are loaded in your system prompt. For each `feedback` or `project` memory, check whether it contradicts a CLAUDE.md instruction on the same topic:

- **Memory is stale** — CLAUDE.md and the memory describe different procedures for the same task: CLAUDE.md is the maintained, checked-in source. Delete the memory, or rewrite it to agree if it carries context worth keeping (the *why* is still useful but the *how* is wrong).
- **CLAUDE.md may be stale** — the memory is clearly dated after CLAUDE.md and explicitly corrects it: do NOT edit CLAUDE.md during a dream. Annotate the memory with "contradicts CLAUDE.md — verify which is current" and list it in your summary so the user can update CLAUDE.md.
- **Not a conflict** — the memory adds detail CLAUDE.md doesn't cover, or narrows a CLAUDE.md rule with a stated reason. Leave it.

A `feedback` memory's "Why: the user corrected me" framing is not evidence it's newer than CLAUDE.md — CLAUDE.md may have been updated since.
```

### The `r` / "Additional context" tail actually passed at runtime

The scheduler always passes a non-empty `r` (so `## Additional context`
always renders). Verbatim template:

```markdown
**Tool constraints for this run:** Shell access is restricted to read-only commands (`ls`, `find`, `grep`, `cat`, `stat`, `wc`, `head`, `tail`, and similar) plus deleting `.md` paths inside the memory directory. Anything else that writes, redirects to a file, or modifies state will be denied. Plan your exploration with this in mind — no need to probe.

Sessions since last consolidation ([${count}]):
- [${sessionId}]
- [${sessionId}]
…
```

### The memory-file *format* the prompt defers to (from the system-prompt auto-memory section, constant near `bzs`)

The dream deliberately does NOT restate the file format — it points to the
system prompt. That format is:

```markdown
---
name: {{short-kebab-case-slug}}
description: {{one-line summary — used to decide relevance in future conversations, so be specific}}
metadata:
  type: {{feedback, project, …}}
---

{{memory content — for feedback/project types, structure as: rule/fact, then **Why:** and **How to apply:** lines. Link related memories with [[their-name]].}}
```

### Related prompt: extractMemories (inline, NOT the dream)

There is a separate, *inline* memory pass — `extractMemories` (`L9f`) — that
runs after turns with `querySource:"extract_memories"`, `maxTurns:5`,
`forkLabel:"extract_memories"`. Its prompt is assembled by `H6l(...)` (not
recovered verbatim here — it's built from parts, not one literal). Telemetry:
`tengu_extract_memories_extraction`, `_skipped_direct_write`,
`_skipped_no_prose`, `_coalesced`, `_error`. Gate: `tengu_passport_quail`
feature flag + `jd()` + no active agentId + not already `Fl()` (remote).
This is worth knowing so you don't conflate the two: **extract = per-turn,
write-append; dream = idle, whole-store reorganize + prune.**

---

## 2. Trigger conditions

Scheduler `q6l` sets `W6l`; called via `V6l(e,t)` from the main loop. Logic:

1. **Gate `q9f()`** — dream is eligible only if ALL hold:
   - `Fl() === null` — i.e. **not a remote/bridge session** (remote-control /
     `ccr` sessions never dream locally).
   - `jd()` — auto-memory globally enabled (see §5 for its full chain).
   - `fer()` — the autoDream resolver (see §5): user setting OR server gate.
   - `V9f()` hard-codes `return!1` (a "force-on" override that ships off), so
     eligibility is entirely `q9f()`.
2. **Time gate** — read last-consolidation timestamp = mtime of the lock file
   (`_sn()` → `stat(.consolidate-lock).mtimeMs`, 0 if absent). Require
   `hoursSince ≥ minHours` (default **24 h**).
3. **Scan throttle** — even if the time gate passes, refuse if the last *scan*
   was < `G9f = 600000 ms` (**10 min**) ago. In-process throttle only.
4. **Session-count gate** — list sessions whose transcript mtime is newer than
   last consolidation (`s2l`), drop the current session, require
   `count ≥ minSessions` (default **5**). Else emit
   `tengu_auto_dream_skipped {reason:"sessions", session_count, min_required}`.
5. **Lock** — acquire `.consolidate-lock` (§3). On failure/held →
   `tengu_auto_dream_skipped {reason:"lock"}`.
6. **Fire** — `tengu_auto_dream_fired {hours_since, sessions_since,
   team_memory_enabled}`, register a `type:"dream"` task (label "dreaming",
   `forkLabel:"auto_dream"`, `querySource:"auto_dream"`, `skipTranscript:true`),
   run the prompt as a forked agent.

`minHours` / `minSessions` are overridable by the server feature flag
`tengu_onyx_plover` (`{minHours, minSessions}`); defaults `U6l={minHours:24,
minSessions:5}`.

### Can it run under `claude -p` (headless)?

**Not directly, and not as a fresh headless invocation.** The dream is fired
from inside a *running interactive main loop* (it needs `toolUseContext`,
`taskRegistry`, `setAppState`, the React app state, and an idle main loop to
fork from). There is:

- **No CLI flag** and **no slash command** that triggers a dream. `"dream"`
  appears in the binary only as: the task `type`/label ("dreaming"), the
  `forkLabel`, telemetry event names, and an unrelated word in a random
  name-word list. No command registration (`name:"…dream…"`) exists.
- The `Fl()!==null` gate additionally *disables* dreaming in remote/bridge
  sessions.

So a `-p` one-shot will not itself dream. To replicate consolidation under
`claude -p`, you drive the *prompt* yourself (see §7) — you are not invoking
the internal trigger, you are reproducing its payload.

### Telemetry (lifecycle)

`tengu_auto_dream_toggled` (settings UI, `{enabled, is_first_enable}`),
`_skipped` (`{reason, …}`; reasons seen: `"sessions"`, `"lock"`),
`_fired` (`{hours_since, sessions_since, team_memory_enabled}`),
`_completed` (`{cache_read, cache_created, output, sessions_reviewed,
daily_logs_found, files_touched_count, team_memory_enabled}`),
`_failed` (`{phase, error_class}`; phase ∈ `"fork"|"completion"`).

---

## 3. The lock protocol

File: `<memoryDir>/.consolidate-lock` (constant `K3f=".consolidate-lock"`,
joined to `$m()` = the memory dir). Staleness window `Y3f = 3600000 ms`
(**1 hour**).

- **Contents:** a single line — the **PID** (`String(process.pid)`), written
  with `writeFile`. mtime is the semantic "last consolidated at" timestamp.
- **Acquire (`i2l`)**:
  1. `stat` + `read` the lock. Parse mtime and the PID integer.
  2. If mtime exists AND `now - mtime < 1h` (fresh) AND the recorded PID is
     **alive** (`_v(pid)` = `process.kill(pid,0)` succeeds, pid>1) → held by a
     live process → return `null` (bail). A fresh lock whose PID is dead is
     treated as acquirable (crash recovery).
  3. Else `mkdir -p` the memory dir, `writeFile` lock = own PID, re-read, and
     confirm the file still contains own PID (last-writer-wins race guard).
     Returns the prior mtime (used as `priorMtime` and as the rollback value).
- **Read last-consolidated (`_sn`)**: `stat(lock).mtimeMs`, else `0`.
- **Release / rollback (`bsn(priorMtime)`)** — on failure only:
  - if `priorMtime === 0` (no prior lock) → `unlink` the lock.
  - else `writeFile(lock, "")` then `utimes` the lock back to `priorMtime`
    (seconds) — i.e. restore the previous "last consolidated at" so the next
    trigger isn't wrongly deferred.
- **On success there is no explicit release/rewrite** in the recovered path —
  the lock file's mtime naturally advances to "now" from the write during
  acquire, which becomes the new last-consolidated timestamp. (Rollback only
  runs in the `catch` when the fork phase failed.)

---

## 4. Input / output contract

**Memory directory** (`$m()` = `autoMemoryDirectory` or default
`~/.claude/projects/<sanitized-cwd>/memory/`). Layout the dream assumes:

- `MEMORY.md` — the index (constant `GA`). Capped: **≤ 200 lines** (`PJ`) AND
  **≤ ~25 KB** (`fde=25000`). Index lines ≤ ~150 chars ideal, ~200 char hard
  nudge. One-line-per-entry `- [Title](file.md) — hook`; never content.
- `<slug>.md` topic files at top level — the actual memories (frontmatter
  format per §1).
- `logs/YYYY/MM/DD/<id>-<title>.md` — append-only per-session activity stream,
  prefix-coded lines (`>` user, `<` assistant, `.` tool call). Primary signal
  source. Optional `sessions/` subdir also reviewed.
- `team/` subdir (when team memory enabled) — shared, pruned conservatively.
- `.consolidate-lock` — the lock (§3).

**Session transcripts** (`Sy(sn())`): the large `*.jsonl` rollout files. Read
only via narrow grep, never whole-file.

**Writes:** in-place, directly with the Write tool (the prompt explicitly says
the dir exists — no staging, no mkdir). Deletions allowed only for `.md` paths
inside the memory dir. `filesTouched` is tracked by `z9f`/`l2l` by scanning the
forked agent's Write/Edit `file_path`s and any `rm`/`del`-style bash commands
targeting `*.md`. On completion these surface as app-state
`pendingMemoryUpdates {source:"dream", summary, paths}` with verb "Improved".

**Tools the dream agent may use** (enforced by `canUseTool: Qan(memoryDir)`,
and reinforced in-prompt): **read-only shell** — `ls find grep cat stat wc
head tail` and similar — **plus deleting `.md` files inside the memory dir**,
plus the Write/Edit tools for memory files. Any other write / redirect /
state-mutation is denied. This is the "read-only-ish pass" the public reporting
described; it is not literally read-only (it must write memories).

Run params: `maxTurns` unset here (extractMemories uses 5); `skipTranscript:
true`, `skipCacheWrite: wXe()`, own `AbortController` (user can abort → logs
"aborted by user", no rollback).

---

## 5. Config surface & precedence

Settings keys (zod schema, user/project settings):

- **`autoDreamEnabled`** (boolean, optional) — *"Enable background memory
  consolidation (auto-dream). When set, overrides the server-side default."*
- **`autoMemoryEnabled`** (boolean, optional) — master switch for reading
  from / writing to the auto-memory dir. When false, no dream.
- **`autoMemoryDirectory`** (string, optional, supports `~/`) — where memory
  lives. *Ignored if set in checked-in projectSettings* (security). Default
  `~/.claude/projects/<sanitized-cwd>/memory/`.

Env vars in the gate chain:

- `CLAUDE_CODE_DISABLE_AUTO_MEMORY` — truthy disables, an explicit "enable"
  value forces on (checked before the settings toggle).
- `CLAUDE_CODE_SIMPLE` — disables auto-memory.
- `CLAUDE_CODE_REMOTE` (+ `CLAUDE_CODE_REMOTE_MEMORY_DIR` /
  `CLAUDE_COWORK_MEMORY_PATH_OVERRIDE`) — remote without a memory-dir override
  disables.
- `CLAUDE_MEMORY_STORES` — enables team/multi-store memory (`UL()`/`jGe()`).

**Precedence — `fer()` (does auto-dream run?):**

```
fer():
  if !Wan()  -> false                       // server/gate not available
  e = settings.autoDreamEnabled
  if e !== undefined -> return e             // 1. USER SETTING WINS
  if A6l()?.enabled === true -> return true  // 2. server flag explicit-on
  return zno()                               // 3. server default heuristic
```

- `Wan()` = availability: server flag `tengu_onyx_plover`
  (`A6l()`) has `enabled===true` OR `available===true`, OR `zno()` true.
- `A6l()` = `nt("tengu_onyx_plover", null)` — the server-side feature-flag
  object (also carries `minHours`/`minSessions` overrides, §2).
- `zno()` = server *default* heuristic: requires team/multi-store memory on
  (`UL()`) and store state `ARt()==="has-content"` (i.e. there's memory
  content to consolidate). This is the "server-side default" the setting
  overrides.

**And `jd()` (auto-memory enabled at all)** — gates everything upstream:

```
jd(): false if lD() or ec() (safe mode);
      env CLAUDE_CODE_DISABLE_AUTO_MEMORY truthy -> false / explicit-on -> true;
      CLAUDE_CODE_SIMPLE -> false;
      CLAUDE_CODE_REMOTE (no mem-dir override) -> false;
      qMr() (model/allowlist gate tengu_sepia_cormorant) -> false;
      settings.autoMemoryEnabled if defined;
      else true.
```

So full precedence to actually dream: `jd()` (auto-memory on) AND `Fl()===null`
(local, not remote) AND `fer()` (user `autoDreamEnabled` › server flag ›
`zno()` default). The settings UI writes `autoDreamEnabled` via
`ti("userSettings", {autoDreamEnabled})` and only lets you toggle it when
`$ = g && k` (auto-memory on AND availability confirmed).

---

## 6. Anything invocable on demand

**No.** Searched command/flag registration for `dream` — none. There is no
slash command, no CLI flag, no internal entry point that fires a dream on
demand. `"dream"` in the binary is only: the background-task `type`/label
("dreaming"), the `forkLabel`, telemetry event names, and an unrelated
random-name-word. The settings UI (`IZl`) exposes an **Auto-dream: on/off**
toggle and shows "running" / "last ran …" / "never" status, but toggling only
flips `autoDreamEnabled`; it does not fire a run. The only trigger is the idle
scheduler `V6l` inside a live interactive session.

---

## 7. Replicating this via `claude -p` for ducklings

Since nothing internal is invocable and `-p` won't self-dream, the duckling
scheduler reproduces the *payload*: point `claude -p` at a memory dir with the
dream prompt, restricted to read-only + memory-dir writes. This is a faithful
reconstruction of `N6l`'s output with our own paths.

**Scheduler responsibilities (mirror the internal gates yourself):**

- Hold your own lock analogous to `.consolidate-lock` (write PID, honor a 1 h
  freshness + PID-liveness check) so two ducklings don't consolidate at once.
- Enforce your own cadence (their defaults: ≥24 h since last, ≥5 new sessions,
  10 min scan throttle). Track "last consolidated" as the lock mtime.
- Restrict the run to read-only shell + memory-dir `.md` writes/deletes. With
  `claude -p`, use `--allowedTools` / a settings profile granting only
  Read/Write/Edit/Grep/Glob and read-only Bash, or run with a permission mode
  that denies non-memory writes. (The internal run denies anything that writes
  outside the memory dir — replicate that boundary or the agent can wander.)

**Dream-wake prompt template** (drop your paths in; keep the four phases —
they carry the behavior). Omit the `team/` block if you have no shared store;
keep the CLAUDE.md-reconciliation block if project CLAUDE.md is in-context.

```markdown
# Dream: Memory Consolidation

You are performing a dream — a reflective pass over your memory files. Synthesize what you've learned recently into durable, well-organized memories so that future sessions can orient quickly.

Memory directory: `<MEMORY_DIR>`
This directory already exists — write to it directly with the Write tool (do not run mkdir or check for its existence).

Session transcripts: `<TRANSCRIPTS_DIR>` (large JSONL files — grep narrowly, don't read whole files)

---

## Phase 1 — Orient
- `ls` the memory directory to see what already exists
- Read `MEMORY.md` to understand the current index
- Skim existing topic files so you improve them rather than creating duplicates
- `ls -R logs/` — recent activity logs (one file per session under `YYYY/MM/DD/`)

## Phase 2 — Gather recent signal
Look for new information worth persisting, in rough priority order:
1. Session logs (`logs/YYYY/MM/DD/<id>-<title>.md`) — read the most recent 1–3 days; lines are prefix-coded (`>` user, `<` assistant, `.` tool call)
2. Existing memories that drifted from current reality
3. Narrow transcript grep only when you need a specific detail:
   `grep -rn "<narrow term>" <TRANSCRIPTS_DIR>/ --include="*.jsonl" | tail -50`
Don't exhaustively read transcripts.

## Phase 3 — Consolidate
Write or update memory files at the top level of the memory directory. For each memory use frontmatter:
---
name: {short-kebab-slug}
description: {specific one-line summary}
metadata:
  type: {feedback|project|...}
---
{content; for feedback/project: rule/fact, then **Why:** and **How to apply:**. Link related memories with [[name]].}
Focus on: merging into existing files over duplicating; converting relative dates to absolute; deleting contradicted facts at the source.

## Phase 4 — Prune and index
Update `MEMORY.md`: keep it under 200 lines AND under ~25KB. It is an index, not a dump — each entry one line under ~150 chars: `- [Title](file.md) — one-line hook`. Never write content into it. Remove stale/superseded pointers, demote overlong lines by moving detail into the topic file, add pointers to newly important memories, resolve contradictions.

---

## Tool constraints for this run
Shell is restricted to read-only commands (`ls`, `find`, `grep`, `cat`, `stat`, `wc`, `head`, `tail`, and similar) plus deleting `.md` paths inside the memory directory. Anything that writes elsewhere, redirects to a file, or modifies state will be denied.

Return a brief summary of what you consolidated, updated, or pruned. If nothing changed, say so.

## Additional context
Sessions since last consolidation (N):
- <session-id>
- ...
```

---

## 8. What could NOT be determined from strings alone

- **The extractMemories prompt verbatim** — it's assembled by `H6l(...)` from
  parts, not one literal, so only its scaffolding (telemetry, params, gate
  `tengu_passport_quail`) was recovered, not the full text.
- **Exact idle detection** — *when* the main loop calls `V6l` (what counts as
  "idle": keystroke gap? turn boundary?) is in the caller, not in the recovered
  window. Confirmed only that it's driven from a live interactive main loop,
  throttled to ≥10 min between scans.
- **`maxTurns` for the dream fork** — not present in the recovered call (extract
  uses 5; dream's may be unset/default).
- **Whether `MEMORY.md`/25KB caps are enforced anywhere but the prompt** — the
  200-line/25KB numbers are used elsewhere in the binary (`fde` has other
  refs) but I did not confirm a hard post-write truncation of `MEMORY.md`; the
  prompt is the only place the cap is *instructed*.
- **Precise server-flag semantics** — `tengu_onyx_plover`, `tengu_sepia_
  cormorant`, `zno()`/`ARt()` states are read from a remote gate service; their
  live values for a given account can't be read from the binary.
