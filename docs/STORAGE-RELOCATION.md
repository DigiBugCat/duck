# Storage relocation — pads & routine defs to project content

Grounded in two audits (Fable + codex) cross-compared, and the actual code.
Goal: move CONTENT (pads, routine definitions) out of `~/.duck` (machine-local
plumbing) into the synced project dir, and keep RUNTIME (fire-state, reports,
spools, the routines index) hub-local.

## The principle
`~/.duck/*` = machine-local plumbing that must NOT sync. Pads and routine defs
are the opposite — content you want synced + versioned. They belong with the
synced project. Fire-state/reports/index are runtime and stay hub-local.

## Where "project root" is: the LONGEST COVERING MUTAGEN SYNC ROOT
NOT git root (duck has no git awareness; mutagen syncs arbitrary dirs incl `.git`).
NOT blindly `@duck_dir` (a workspace can sit INSIDE an ancestor sync root —
`IsSynced` treats ancestor coverage as synced, syncer.go:110-140 via
`pathCoveredBy`, syncer.go:198). Pads keyed on `@duck_dir` would fragment vs the
real sync boundary.

Resolution (new helper, mirrors IsSynced's scan): iterate mutagen sessions, keep
the LONGEST `Alpha.Path` that `pathCoveredBy(dir, path)`. That's the sync root.
Fall back to `@duck_dir` (the workspace dir) only when NO covering session exists
(un-synced / local-only workspace). The path itself is the key — no more
basename keying (the current `filepath.Base` in ProjectName/PadPath is a live
collision bug: two projects named `web` share pads).

## PADS
- Location: `<sync-root>/.duck/scratchpad/<name>.md`.
- `PadPath(project, name)` → `PadPath(syncRoot, name)` — takes the resolved root
  dir, not a basename. (The dir is the key; same root ⇒ same pads ⇒ workspaces
  within a project share by construction.)
- SPLIT resolve from create: today `PadPath` side-effects (mkdir `.duck/` + write
  a header file) on mere RESOLUTION — bad, it'd mkdir `.duck/` in a repo just by
  resolving a name. New: `PadPath` (pure resolve, no I/O) + `EnsurePad` (create).
  Callers that open a pad call Ensure; callers that just need the path don't.
- Migration (copy, never delete — protects the live working doc): on first
  resolve, if `<sync-root>/.duck/scratchpad/<name>.md` is absent, copy from the
  old candidates in priority order: legacy flat `~/.duck/scratchpad/<name>.md`,
  then legacy `~/.duck/scratchpad/<basename>/<name>.md`.
- `shared.md` is the EXCEPTION: genuinely global, no project home, already in the
  synced vault. Keep the flat-file fallback ONLY for it (and any global pad). Do
  NOT force a global doc into an anointed project.
- Kill the `~/.duck/scratchpad → vault` symlink (a smell). Migration copies the
  real files to project roots; the vault flat file remains the global tier.
- .gitignore: pads are CONTENT — do NOT auto-ignore. Users commit by default;
  a project can locally ignore `.duck/scratchpad/` if it wants private scratch.

## ROUTINES
- DEFINITIONS → `<sync-root>/.duck/routines/<name>.toml` + `.md` (project content,
  synced, alongside pads). Matches docs/routines-design.html's original intent;
  the impl drifted to workspace-owned `~/.duck/routines/<workspace>/`.
- HUB INDEX (new, hub-local): `~/.duck/routines-projects.json` listing each
  project sync-root that has routines (+ preferred manager workspace). The tick
  CANNOT scan the whole filesystem for `.duck/routines`; it reads the index, then
  loads each project's defs. `add`/`rm` update the index.
- FIRE-STATE stays hub-local (`~/.duck/routines-state.json`) but RE-KEYED from
  `workspace+name` to `sync-root+name` (matches the new def location). state.go:47
  `Key()` changes.
- REPORTS/SPOOLS stay hub-local (delivery queues, not content) — unchanged.

## Order (green suite each step)
1. `flow`/`panel`: covering-sync-root resolver (new helper) + fallback.
2. `panel`: PadPath split (resolve vs Ensure), key by sync-root, migration copy,
   shared.md fallback. Update callers (command/edit.go, EnsureScratch).
3. Verify pads open + resolve to new location (headless tmux).
4. routines: defs → project `.duck/routines`, hub index, re-key fire-state,
   update tick + add/rm/list + the MCP routines tool.
5. Remove the vault symlink; migration doc for existing pads.

## Non-negotiables
- Copy, never delete, during migration (shared.md is live).
- PadPath must not side-effect on resolve.
- Content committable (no auto-gitignore).
- Runtime (state/reports/spools/index) stays hub-local.
