# CLAUDE.md — duck

Agent-facing notes for working on duck. `README.md` is the human overview;
`../CLAUDE.md` (aviary root) has umbrella conventions. This file is the
hard-won operational knowledge: how to build, deploy, and how to touch a
LIVE workspace safely.

## What you're touching

duck is a Go CLI; a duck "workspace" is a plain tmux session on the hub.
Agents and shells are ordinary panes/windows of that same session:
`internal/tmuxdb.Spawn` splits the current window for the first agent
(below the manager, 40%) and gives later agents their own background
windows named after them. Switching is native tmux (`select-window`,
`last-window`, the status bar's window list). All identity lives in pane
user options (`@duck_name`, `@duck_kind`, `@duck_prompt`, …) — tmux is the
database (`internal/tmuxdb`), there is no daemon and no state file beyond
`~/.duck/names.json`. Killing an agent is `kill-pane`: a background window
dies with its pane, a split returns its rows to the manager — the layout
takes care of itself.

`duck palette` and `duck fleet` are one-shot pickers in tmux
`display-popup -E` (palette = session-manager verbs: jump/kill/detach;
fleet = a live tmux roster). The dynamic multi-line status bar (`duck
statusline` rendered via `status-format[i]`) reports pane liveness directly;
duck does not infer model activity from transcript hooks.

Legacy note: pre-teardown workspaces parked agents in a hidden
`<session>-agents` companion session. `tmuxdb.Agents` still reads those so
old live agents stay addressable; `duck clean` reaps stale ones.

## The golden rules

1. **Anchor tmux context to the pane, not the client.** `display-message`
   without `-t` answers for the most recently active CLIENT — whatever
   terminal the human last touched, not where your process runs. Always
   target `$TMUX_PANE` (see `displayArgs` in internal/tmuxdb). This bug
   cost us a whole evening across duck-8/duck-10.
2. **tmux output parsing:** free-text fields (pane_title) go LAST with a
   bounded SplitN; `TrimSpace` on whole output EATS the last line's trailing
   tab (empty trailing field) — TrimRight("\n") only, and tolerate a missing
   trailing field. Both were live bugs.
3. **Ship releases, not hot patches.** Changes reach live workspaces
   through the release pipeline below (~2 minutes); agents spawned after
   `duck update` pick up new behavior, and existing panes keep their work.

## Build & deploy loop

```sh
go build ./... && go test ./...           # suite is fast; keep it green
go build -o /tmp/duck-scratch ./cmd/duck  # scratch binary for live testing
```

## Headless testing (no human client needed)

Use a throwaway tmux server so nothing touches real workspaces:

```sh
tmux -L ducktest new-session -d -s work -c /tmp -x 200 -y 50
tmux -L ducktest send-keys -t work "PATH=<scratchdir>:$PATH duck spawn ..." Enter
tmux -L ducktest list-windows -t work        # spawned agents show up here
tmux -L ducktest capture-pane -p -t <pane>
tmux -L ducktest kill-server   # always clean up
```

Gotchas: capture-pane shows TEXT only. Keys sent in the first ~2s after a
pane starts can be eaten by TUI startup. To run duck against the test
server from outside a pane: `SOCK=$(tmux -L ducktest display -p
'#{socket_path}'); TMUX="$SOCK,0,0" duck <cmd>`.

## Release pipeline (fleet ships in ~2 minutes)

```sh
git tag vX.Y.Z && git push origin main vX.Y.Z
RID=$(gh run list --limit 8 --json databaseId,headBranch --jq '.[]|select(.headBranch=="vX.Y.Z").databaseId' | head -1)
gh run watch $RID --exit-status      # watch by RUN ID — "latest run" races adjacent tags
duck update                          # hub; retry once — the release API lags a few seconds
ssh andrew.sulistio@loki '~/.local/bin/duck update'   # laptops (or wait for hourly auto-update)
```

## Driving durable panes

`duck spawn` creates an operator-visible process and prints its tmux pane ID.
Use tmux directly for later interaction: `send-keys -t <pane> -l -- <text>`
(`--` guards dash-leading text), pause before Enter so a TUI does not treat
both as one paste, and `capture-pane -p -t <pane>` for screen text. Model
delegation belongs to Claude Code's native Agent and Workflow facilities.
