# Brief: window artifacts appear in the roster artifacts tab

Repo: ~/aviary/products/duck, branch off main (now v0.32.0, includes the
per-session DUCK_WINDOW_SOCK discovery and the `window` MCP tool). Keep
`go build ./... && go test ./...` green. Read docs/WINDOW.md ("routing
model" + "artifacts tab" sections), internal/panel/ (roster, lot,
showFiller/placeholder machinery from commit 34348bd), command/window.go
(runWindow / windowClient / publishArtifact), command/channel.go
(mcpHost.Window).

## Goal

WINDOW.md's settled routing: the sidebar artifacts tab lists ALL
artifacts, static and dynamic. Selecting a static one previews in the
viewport (works today). A DYNAMIC one (shown via `duck window` / the
window MCP tool) must ALSO get a roster row; selecting it re-opens /
focuses the window on the client (the row is the remote control for a
surface living on the client machine). Today window opens leave no
trace in the roster — fix that.

## Design constraints (duck invariants — do not violate)

- tmux is the database: a roster row is backed by a pane in the lot
  with @duck_* user options. Represent a window artifact as a parked
  placeholder pane (reuse/extend the lot-parked placeholder pattern
  from showFiller, commit 34348bd) carrying:
  @duck_kind=window, @duck_name=<label>, @duck_url=<published url>.
- Viewport behavior when a window row is selected: show a cheap static
  placeholder (e.g. the placeholder pane prints the artifact name/url
  and "open on <client> — ↵ to focus"), NEVER try to render the page
  in cells.
- Enter on a window row = re-POST /open for that url via the session's
  resolved window target (windowClient(session)) — that both navigates
  and raises the window client-side. Give the artifacts tab hint line
  the right keys (hint-line machinery from 0ce4d2d).
- x on a window row = remove the placeholder row only (there is no
  host /close endpoint; do not add one in this brief).
- Same label re-shown = update the existing row's @duck_url, don't
  stack duplicates (match preview's name-reuse behavior if it has one).

## Wiring points

1. `duck window <target>` CLI: after a successful /open, when run
   inside a duck session (panel.CurrentSession), ensure the artifact
   row exists. Window verb should grow an optional name arg
   (`duck window <file|url> [name]`, default: basename or host) to
   label the row — mirror preview's required-name ergonomics WITHOUT
   breaking the existing one-arg form.
2. mcpHost.Window (command/channel.go): same registration, using the
   workspace it already receives; add an optional `name` property to
   the MCP tool schema, defaulting like the CLI.
3. Roster (internal/panel/watch.go): artifacts tab includes
   kind=window rows; Enter dispatches the reopen; hint line updated;
   unit tests in the existing style (fake runner) for row listing,
   enter-dispatch, name-reuse, and x-removal.

## Testing

Headless tmux only (tmux -L ducktest …, see duck/CLAUDE.md). NEVER
touch live workspaces. go test green; add focused unit tests around
the new panel + window code paths.

Report: branch, commits, test status, deviations.
