# Brief: window follows the attached client + `window` MCP tool

Repo: ~/Obsidian/aviary/duck (Go CLI). Work on a branch off main, e.g.
`window-follows-client`. Keep `go build ./... && go test ./...` green.
Read docs/WINDOW.md, command/window.go, command/openfwd.go,
internal/openfwd/, internal/sshx/sshx.go, internal/channel/serve.go,
command/channel.go first.

## Problem

`duck window` today resolves its host via static config
(`window_host = studio:7334`). Wrong model: the window must pop on
WHICHEVER client machine is currently attached to the session — exactly
like the open-interceptor already does for `duck render`/opens.

## Part 1 — per-session window forwarding (copy the openfwd pattern)

The open-interceptor pattern (command/openfwd.go) is the template:
attach reverse-forwards a per-session hub unix socket
(`~/.duck/run/open-<session>.sock`) to a laptop-local listener and
stamps `DUCK_OPEN_SOCK` into the tmux SESSION environment; teardown
cancels the forward, rm's the socket, unstamps the env var. Everything
best-effort — never fail an attach over it.

Do the same for the window:

1. On attach (alongside newOpenForwarding), the client:
   - runs `ensureWindowHost("127.0.0.1:7334")` locally (it already
     exists in command/window.go — detached-singleton `duck window
     serve`, logs to ~/.duck/window.log). If it fails, degrade to no
     window forwarding with a stderr note, exactly like the
     open-interceptor's failure notes.
   - reverse-forwards hub socket `~/.duck/run/window-<session>.sock`
     → local port 7334 (`client.RemoteForwardSocket`).
   - stamps `DUCK_WINDOW_SOCK` into the tmux session env.
   - teardown mirrors openfwd teardown (cancel forward, rm socket,
     unstamp).
2. Hub-side resolution in `windowHost()` / the window command becomes,
   in order:
   - `--host` flag
   - `$DUCK_WINDOW_HOST`
   - the session's `DUCK_WINDOW_SOCK` from tmux session env — CRITICAL:
     read it anchored to `$TMUX_PANE` / the target session, NEVER the
     most-recent client (see CLAUDE.md golden rule 3, displayArgs in
     internal/panel). When set, talk HTTP over the unix socket
     (http.Transport with a DialContext that dials the socket; URL host
     is a dummy).
   - config `window_host` (kept as a fallback for headless setups)
   - 127.0.0.1:7334 local (with ensureWindowHost, current behavior).
3. `duck window marks` uses the same resolution.

Structure the socket-vs-tcp difference behind one small helper (e.g.
`windowClient() (*http.Client, baseURL string)`) so open/marks/health
share it. Unit-test the resolution order and the unix-socket client
with a fake listener; follow existing test style (the repo injects
seams — see windowHostHealthy/startWindowHost vars).

## Part 2 — `window` MCP tool

internal/channel/serve.go has the tool table (preview/render/routines);
command/channel.go's `mcpHost` implements the Host interface. Add:

- Host interface: `Window(workspace, target string) (string, error)`.
- mcpHost impl: publish the target (same publishArtifact path the CLI
  uses) and POST /open to the resolved host for that workspace's
  session (resolution from Part 1 — the session is known, so read its
  DUCK_WINDOW_SOCK directly).
- Tool `window`, description in the style of the existing ones
  (guidance rides the tool): roughly — "Open a DYNAMIC artifact
  (animation, interactive page, realtime dashboard, anything the human
  should mark up) in the duck-owned window on the human's current
  client machine. duck keeps custody: the human can highlight/comment,
  and those marks arrive back to you as <channel source=\"duck-window\"
  type=\"mark\"> events — do not poll. Routing: static content →
  preview/render; dynamic or annotatable → window."
  Schema: `target` (file path or URL), required.
- Update the fakeHost in channel_test.go and add a dispatch test like
  the neighbors.

## Also

- Update docs/WINDOW.md: status line is stale ("not yet built") — the
  window shipped v0.24–0.25; document the new discovery model
  (per-session socket follows the attached client; window_host config
  demoted to headless fallback).
- Touch ~/.duck/AGENT.md is NOT yours to edit; skip agent-notes.
- Do NOT touch live tmux workspaces; headless test server only
  (see duck/CLAUDE.md "Headless testing").
- Commit in logical pieces on the branch; do not tag/release/push.

Report back: branch name, commit list, test status, and any design
deviations you had to make.
