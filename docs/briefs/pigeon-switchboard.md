# Brief: pigeon (Discord switchboard) + motherduck (fleet admin)

Status: design brief, distilled from the 2026-07-05 motherduck session.
Repo context: ~/aviary (umbrella). Pigeon would be a new bird
(clone egg/, 73xx port, register in docs/registry.toml); the spool/room
work lands in the duck repo.

## The idea in one paragraph

Each duck workspace is an EMPLOYEE: its manager is the person, the
sidebar flock its hands, `duck routines` its job description. The fleet
becomes a Discord server — one #channel per workspace ("their desk").
**pigeon** is the switchboard service that routes between Discord and
duck's channel spools. **motherduck** is the admin layer: a global,
project-less duck workspace whose manager is the chief of staff — it
doesn't do the specialists' work, it has cross-cutting reach (read any
workspace, message any manager, hold the fleet-level memory), and it
owns lifecycle (new workspace → new channel + identity + auth,
automatically).

## Why this shape (and not alternatives)

- Hermes-agent-style "one agent that does and learns everything" gets
  muddy; specialists with scoped context are better. What was missing
  is the role ABOVE them: reach, not capability. Motherduck ≈ chief of
  staff, not a super-worker.
- Duck already has the transport, symmetric and bidirectional:
  `duck channel publish --session <s>` appends to any workspace's
  spool; the sidecar (`duck channel serve`) drains it into the
  manager's context without interrupting a turn; offline workspaces
  park messages until a sidecar starts. Manager↔manager request/reply
  needs NO new transport — only convention (reply-to envelope).
- Per-workspace bridges (official channels plugin per session) would
  mean N configs, require live sessions, and give motherduck no view.
  Employees don't run their own comms infra; the org runs the mail.

## Architecture

### pigeon — the switchboard (new bird, a hub daemon)

One Discord application ("duck"), one gateway connection, one process:

- **Inbound**: message in #peacock → `duck channel publish --session
  peacock` with envelope `from: andrew (discord)`. Park-and-drain comes
  free from the spool — messaging an offline employee just works.
- **Outbound**: workspace publishes to its channel → pigeon posts via
  that channel's **webhook** with per-message `username`/`avatar_url`.
  One bot app, one webhook per channel = one distinct-looking identity
  per workspace (webhook username is per-message, so sub-identities
  like `peacock/codex-2` are free). Webhooks are the standard
  multi-agent-identity trick; real extra bot apps can't be minted via
  API (portal-only) and are only worth hand-making if presence dots /
  per-identity slash commands ever matter.
- **Registry**: channel-id ↔ session ↔ webhook URL, maintained by
  pigeon, written at workspace/channel creation.
- Threads ↔ delegated tasks is a natural later mapping.

### motherduck — the admin layer (a duck workspace, not a service)

- A designated global session not tied to a project dir, with a
  chief-of-staff charter (AGENT.md variant): explicitly allowed to use
  `--session` verbs fleet-wide, `duck ls` / `channel ls` / `channel
  tail` for visibility, fleet-level persistent memory (about Andrew and
  the operation, not a project), manager-level routines (daily fleet
  review, "what's rotting" sweep).
- **Lifecycle**: on new workspace spawn, motherduck (or a duck hook it
  owns) creates the Discord channel, mints the webhook
  (`POST /channels/{id}/webhooks`), registers it with pigeon, updates
  the registry. Auth is one bot token held by pigeon; nothing
  per-workspace to configure.

### Envelope convention (doc change, all manager charters)

Messages carry `from:` / `re:` / optional `reply-to: <session>`; a
manager receiving a channel message with reply-to publishes its answer
back to that session's spool. This alone turns spools into a
delegation fabric.

## Phase 2 — mailbox → room

Discord's real lesson: channels are LURKABLE rooms, not inboxes. Duck
evolution: per-workspace channel becomes an append-only shared log
(it's ~JSONL already) with per-subscriber read cursors; `channel serve`
grows a "join" notion. Motherduck joins all rooms (its merged feed IS
the server); multiple ducks can live in one room. Pigeon then becomes
just another subscriber — as would any future Telegram/Signal bridge.

## Prior art (validated, and what to steal)

- **Official channels plugin** (claude-plugins-official,
  external_plugins/discord): per-session MCP server, `claude --channels
  plugin:discord@...`, guild channels opted in per channel snowflake,
  `requireMention` default, pairing→allowlist access model, tools:
  reply/react/edit_message/fetch_messages/download_attachment.
  Requires a live session; no fleet view. USE TACTICALLY per-workspace
  where a rich interactive loop is wanted — composes with pigeon in the
  same channel. Steal: access.json model, ack reactions, typing
  indicator, attachment inbox pattern.
- **claudecord** (ecmulli): channel-config.json → each channel its own
  headless claude subprocess (cwd/model/prompt/sessionId, SQLite,
  cron channels, permission prompts as Discord buttons). Closest to
  the idea, but spawns fresh sessions — duck's differentiator is
  bridging to EXISTING live workspaces. Steal: routing-config shape,
  button-based permission prompts.
- Also: claudecode-discord (chadingTV, channels=workspaces multi-machine
  hub), ccdb (thread=session), cc-connect (multi-platform).

## Platform choice (settled: Discord first, Matrix escape hatch)

Requirement: first-class iOS support (push that just works).

- **Discord**: best client UX incl. iOS, free, buttons/embeds, thick
  prior art. Mismatch = identities: bot apps are portal-only, webhook
  personas aren't @-mentionable, no per-employee presence. Livable
  workarounds: (1) channel IS the address (typing in #peacock = tagging
  peacock, no mention needed); (2) in shared channels, `#peacock <msg>`
  — channel mentions DO autocomplete and are parseable, or plain
  "peacock: …" text triggers; (3) `/duck <workspace> <msg>` slash
  command with registry-served autocomplete; (4) luxury: hand-make a
  small pool of real bot apps for the top employees (mentionable +
  presence).
- **Matrix**: the native fit — an appservice mints unlimited REAL users
  (`@peacock:server`), mentionable, presence, self-hosted. Element X on
  iOS is now genuinely good, but you become operator of your own push
  pipeline (Sygnal). If identity friction bites, Matrix becomes a
  SECOND pigeon backend, not a migration — the duck spool/room layer is
  the source of truth; platforms are renderings.
- Rejected: Telegram (bots can't see other bots' messages in groups —
  employees mutually invisible), Slack (same identity limits as
  Discord + free-tier history caps), Zulip (nice topic threading +
  API-creatable bots, but weakest iOS client).

## Lifecycle (settled)

New manager launch → auto-register with pigeon (hook in the
workspace-boot path): pigeon creates #channel, mints webhook, adds
registry row. Evict/kill → archive channel. No per-workspace auth;
pigeon holds the one bot token.

Server layout: **channel = project** (the bird / synced folder),
**workspace = speaker identity** within it (webhook persona, username
per-message so one webhook serves all of a project's workspaces),
**thread = delegated task**. A project's whole team converses in one
channel. Registration carries the project path; first workspace in a
project creates the channel, later ones just add a persona.
Motherduck gets `#office` (or `#motherduck`) — the sidebar reads as a
live org chart.

Inbound routing (channel no longer implies one workspace), layered:
(1) each project channel has a DEFAULT workspace (the long-lived
manager session) receiving unaddressed messages; (2) addressed
messages route by persona name ("scratch: try X") or #channel/slash
forms; (3) THREADS BIND to a workspace — a task thread opened by/for a
workspace routes all its messages there, making threads the
per-workspace lane inside the project channel.

## Open questions

- Does pigeon subscribe via `channel tail` of every session (today's
  primitives) or wait for phase-2 rooms?
- Where does the channel↔session registry live — pigeon's own state, or
  duck config so `duck ls` can show discord linkage?
- Rate limits: webhook posts are per-webhook rate-limited; fine for
  manager-cadence traffic, batch executor spam into digests.
- Name/port for pigeon in registry.toml (73xx, unused).
