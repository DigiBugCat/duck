# CLAUDE.md — duck

Agent-facing notes for working on duck. `README.md` is the human overview;
`../CLAUDE.md` (aviary root) has umbrella conventions. This file is the
hard-won operational knowledge: how to build, deploy, and — critically — how
to touch a LIVE workspace without wrecking it.

## What you're touching

duck is a Go CLI; a duck "workspace" is a tmux session on the hub plus a
sidebar ("panel"): a viewport pane (the selected item ITSELF, swapped in via
swap-pane — never a nested client) and a roster pane (a Bubble Tea TUI,
`duck panel watch <session>`). Agents and shells are PANES parked
in a hidden companion session `<session>-agents` ("the lot"); all identity
lives in pane user options (`@duck_name`, `@duck_kind`, …) — tmux is the
database, there is no daemon and no state file beyond `~/.duck/names.json`.

## The golden rules (violations caused every incident so far)

1. **No ad-hoc pane surgery on live workspaces.** Raw `kill-pane`/
   `move-pane`/`respawn-pane` against someone's session WILL mangle layouts
   or kill work. Use duck's verbs (`duck panel`, `duck spawn`, roster `x`),
   which route through EnsureSlot/Heal.
2. **Geometry is asserted, not assumed.** `panel.Heal` runs inside
   `panel.Open`, so ANY `duck panel --session <s>` converges a mangled
   layout (join-pane repositions without touching processes). A broken
   workspace is fixed by `duck panel --session <s>` — never by hand.
3. **Anchor tmux context to the pane, not the client.** `display-message`
   without `-t` answers for the most recently active CLIENT — whatever
   terminal the human last touched, not where your process runs. Always
   target `$TMUX_PANE` (see `displayArgs` in internal/panel). This bug
   cost us a whole evening across duck-8/duck-10.
4. **tmux output parsing:** free-text fields (pane_title) go LAST with a
   bounded SplitN; `TrimSpace` on whole output EATS the last line's trailing
   tab (empty trailing field) — TrimRight("\n") only, and tolerate a missing
   trailing field. Both were live bugs.

## Build & deploy loop

```sh
go build ./... && go test ./...           # suite is fast; keep it green
go build -o /tmp/duck-scratch ./cmd/duck  # scratch binary for live testing
```

**Refreshing the roster pane (bottom-right) with a new build** — the ONE
sanctioned respawn, since the roster is stateless UI (everything it shows
lives in tmux):

```sh
LIST=$(tmux list-panes -s -t <session> -F '#{pane_id} #{@duck_panel_role}' | awk '$2=="list"{print $1}')
tmux respawn-pane -k -t "$LIST" "<binary> panel watch <session>"
```

The viewport/agents are NOT respawned this way — they hold real work.
To apply panel-structure changes to a live workspace: `duck panel
--session <session>` (idempotent open + heal + scratch).

## Headless testing (no human client needed)

Use a throwaway tmux server so nothing touches real workspaces:

```sh
tmux -L ducktest new-session -d -s work -c /tmp -x 200 -y 50
tmux -L ducktest send-keys -t work "PATH=<scratchdir>:$PATH duck panel" Enter
# drive the roster with send-keys, read it with capture-pane:
tmux -L ducktest send-keys -t <listpane> -l "spawn htop" && tmux -L ducktest send-keys -t <listpane> Enter
tmux -L ducktest capture-pane -p -t <listpane>
tmux -L ducktest kill-server   # always clean up
```

Gotchas: capture-pane shows TEXT only. Keys sent in the first ~2s after
respawn can be eaten by TUI startup. To run duck against the test server
from outside a pane: `SOCK=$(tmux -L ducktest display -p '#{socket_path}');
TMUX="$SOCK,0,0" duck <cmd>`.

## Release pipeline (fleet ships in ~2 minutes)

```sh
git tag vX.Y.Z && git push origin main vX.Y.Z
RID=$(gh run list --limit 8 --json databaseId,headBranch --jq '.[]|select(.headBranch=="vX.Y.Z").databaseId' | head -1)
gh run watch $RID --exit-status      # watch by RUN ID — "latest run" races adjacent tags
duck update                          # hub; retry once — the release API lags a few seconds
ssh andrew.sulistio@loki '~/.local/bin/duck update'   # laptops (or wait for hourly auto-update)
```

Prefer shipping releases over hot-patching live sessions (golden rule 1);
the pipeline is fast enough that there is no excuse.

## Driving agents programmatically

Sidebar agents are addressable with three primitives — see internal/channel:
`tmux send-keys -t <pane> -l -- <text>` (input; `--` guards dash-leading
text; 250ms gap before Enter or a TUI treats it as a paste), rollout JSONL
tail (structured output; codex writes ~/.codex/sessions/...), capture-pane
(screen). `duck channel ls|tail|send|serve` wraps these.
