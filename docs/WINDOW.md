# duck window — a duck-owned local render surface

Status: settled design, not yet built (2026-07-04).

## The idea

`duck preview` renders into the terminal (kitty pixels / cells) and is meant
for lightweight one-offs: glanceable, in-flow, disposable. Anything heavier —
animation (the escape-stream is capped at ~2-3MB/s, full-frame video will
never fit), interaction, or content you want to *mark up* — needs a real
window. Today that's `duck render`, which flings a URL at the laptop's
default browser and loses custody.

`duck window` replaces the fling with ownership: duck runs on the client
machine (e.g. studio) anyway, so it can host a chromium window it fully
controls — navigate it, position it, close it, intercept its traffic, and
inject an annotation layer so the human can highlight/comment/draw on the
page and the agent can query those marks back as structured data.

The division of labor:

| verb    | surface            | for                                      |
|---------|--------------------|------------------------------------------|
| preview | terminal (sidebar) | lightweight ideas, one-offs, glances      |
| window  | chromium on client | heavy lifts: animation, interaction, markup |

## Architecture

```
pelican (hub)                          studio (client)
─────────────                          ───────────────
duck render publish                    duck window host (detached singleton,
  ~/.duck/render/<hash>/ symlink         same Setsid pattern as auto-updater)
  :7327 static file server        ──►    │
  (existing, unchanged)                  ├─ CDP session → chromium --app=...
                                         ├─ Fetch interception (all traffic)
open shim (existing hub→client    ──►    ├─ annotation store (keyed by URL)
channel) routes window-bound             └─ local publish endpoint
targets to the host                         (~/.duck/render/ trick, for the
                                            rare studio-local file)
```

### The window

- Chromium in **app mode** (`--app=<url>`): chromeless, looks like a duck
  surface, not a browser. Driven over **CDP** — that's how duck keeps custody
  (navigate, resize, close, liveness) with no extension machinery.
- The host is a detached singleton (`duck window serve` re-exec, Setsid,
  port poll, log to `~/.duck/window.log`) — third instance of the proven
  render-server / auto-updater pattern.

### All traffic flows through duck — but not via a proxy

An OS-level proxy was considered and rejected: MITM'ing HTTPS needs a duck CA
trusted on the machine, and `file://` bypasses an HTTP proxy anyway. Instead:

- **CDP `Fetch.enable`** pauses every request (page loads, subresources,
  XHR) in duck's hands: allow / deny / rewrite / log. Proxy semantics inside
  the browser session, TLS already terminated, unbypassable from the page.
  Gives a full audit trail and a policy point (hub ok, tailnet ok,
  arbitrary internet → policy).
- **No `file://`, ever.** Chromium's default (file access off) is kept.
  Anything local is published through a duck HTTP endpoint (the hash-scoped
  symlink-dir trick, served by the host). This makes the isolation boundary
  legible: "what can this window see" = exactly what a duck server on either
  machine explicitly published — inspectable with `ls ~/.duck/render` on each
  machine. Note the machine split does most of the work already: artifacts
  live on pelican, the window runs on studio, so hub content *must* arrive
  as a URL regardless; studio-local files are the rare case.
- Fetch interception also hardens annotation injection: the runtime can be
  injected into the HTML response itself and hostile CSP headers relaxed at
  the interception point (belt and suspenders with
  `Page.addScriptToEvaluateOnNewDocument`).

### Annotation layer (the actual point)

A small JS runtime injected into every page:

- **highlights** (text selection), **comment pins** (anchored to elements),
  **freehand drawing** (canvas overlay), maybe "point at element" mode.
- Marks flow page→host over `Runtime.addBinding` (native CDP callback, no
  websocket needed) and are stored keyed by URL.
- Query side: `duck window marks` returns structured annotations — for a
  highlight: selected text + CSS selector + surrounding context; for a pin:
  comment + anchor element; for a drawing: strokes **plus a screenshot crop
  of the region** so the agent literally sees what was circled.

This turns "human points at the thing" into a first-class agent channel:
"user highlighted `revenue_q3`, commented 'off by 10x', circled the top-right
of the chart <image>" instead of prose archaeology.

**Anchor drift**: annotations key on stable URLs but content evolves under
them (agents rewrite artifacts in place). Selectors can dangle. Each mark
stores surrounding text + a screenshot crop so stale anchors degrade
gracefully rather than silently mis-pointing.

## Hub-side hosting: mostly fine as-is

The existing `:7327` server (`command/render.go`) is a serviceable publish
mechanism; the new intelligence all lives in the client-side host. Facts and
consequences:

- **URL stability is accidentally already right**: the slug hashes the *dir
  path*, not content, so in-place rewrites keep the URL and serve fresh
  bytes — exactly what annotation keying wants.
- **Live updates need a nudge, not a new host**: bytes are always fresh; the
  window just needs "reload". v1: the host polls/ETags the current URL every
  couple seconds. Later: a push over the open-shim channel.
- **Auth is "know the hash"** — acceptable on a single-user tailnet; easy to
  tighten later (per-publish token attached by the host at the interception
  point). Not v1.
- **Nothing ever unpublishes** — symlinks accumulate. Wants
  `duck render ls|rm` + expiry eventually. Not blocking.

## Integration points (from the code survey)

- `internal/openfwd/openfwd.go:41` — `Deps.Open func(target) error` is the
  seam: the client-side handler gains a branch preferring the window host
  over `osOpen` for window-bound targets. Shim protocol unchanged.
- `command/render.go` — publish + `:7327` singleton reused as-is;
  `ensureRenderServer` (Setsid re-exec + port poll) is the template for
  `duck window serve`.
- `command/preview.go:117` — the renderer ladder; preview does NOT grow a
  window tier (the verbs stay separate), but `render` learns to route to the
  window when a host is up.
- Net-new in-repo: CDP driving from Go (nothing in go.mod today; gosling et
  al are external PATH binaries). Either chromedp-style dependency or a
  gosling-mold external binary.
- Caveat: the openfwd listener is attach-scoped (torn down on detach); the
  window host deliberately is NOT — it's a lazy singleton that outlives
  attaches. Lifecycle question (linger forever vs idle timeout) still open.

## Verb sketch

```
duck window <file|url>     publish (if file) and show in the duck window
duck window marks [url]    structured annotations for current/given page
duck window ls             host status, open page, published dirs
duck window close          close the window (host may linger)
```

## Spike plan

Prove the spine end-to-end: `duck window serve` host + CDP-launched
app-mode chromium + load a hub `:7327` artifact + one highlight round-trip
(select text in the window → `duck window marks` shows it on the hub).
