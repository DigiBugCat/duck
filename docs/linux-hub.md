# Linux hub support

duck was written macOS-first, but a **Linux box works as a hub** — the core loop
(continuous sync + remote tmux sessions + roaming attach) runs end to end. The
laptop/client stays macOS; "Linux support" here means *Linux as the always-on
hub you duck into*.

This documents what `duck hub setup` does on Linux, what it can't, and the
gotchas that bit us provisioning the first Linux hub (a Debian box, `pelican`).

## TL;DR

```sh
duck hub setup andrew@your-linux-host
```

That one command brings a fresh Debian/Ubuntu hub up: it installs tmux + rsync
(apt), fetches **mutagen** and **tsshd** from their GitHub releases (neither has
an apt package), writes the de-hooked `~/.tmux.conf` + TPM, installs the
`duck-open` interceptor, and installs the **self-eviction sweep** (a systemd
`--user` timer). Then `duck` into a project as usual.

## What works

| Capability | Linux hub | Notes |
|---|---|---|
| **Sync** (Mutagen, bidirectional) | ✅ | `duck hub setup` installs the mutagen binary from GitHub into `~/.local/bin` and symlinks it onto `/usr/local/bin`. |
| **Remote tmux sessions** | ✅ | tmux from apt. |
| **Attach** (tssh / UDP-QUIC roaming) | ✅ | `tsshd` is installed from its GitHub release. tssh's `--install-tsshd` auto-deploy is **not** reliable, so duck installs tsshd outright — otherwise the attach fails with `tsshd: command not found`. The detected path is stored in config and passed via `--tsshd-path`. |
| **`duck-open`** (open/links route to the laptop) | ✅ | The shim resolves `xdg-open`; the laptop routes per-OS. |
| **Self-eviction** (`duck evict --install`) | ✅ | Scheduled as a **systemd `--user` timer** (`~/.config/systemd/user/duck-evict.{service,timer}`), with **linger enabled** so it fires when no one is logged in. On macOS this is a launchd agent. Installed by default during `duck hub setup`. |
| **Claude history sync** (`claude-sync on`) | ✅ | Same Mutagen mirror; works once `claude` runs on the hub. |
| **`duck snap`** | ➖ | A macOS-**laptop** feature (`screencapture`/`pbcopy`). The hub side is just a file upload and is platform-agnostic — nothing to do on a Linux hub. |
| **`duck` as a client** | ❌ | The released binaries are darwin-only (GoReleaser builds `duck-darwin-*`). The hub never runs `duck`, so this doesn't matter for hub use. |

## Prerequisites on the hub

- **SSH reachable**, key-based auth (duck offers `ssh-copy-id` interactively if not).
- **Debian/Ubuntu** (`apt-get`) — the only Linux package manager duck's installer
  branches on. Other distros: install `tmux` + `rsync` yourself, then run setup
  (mutagen/tsshd still come from GitHub).
- **`curl`, `tar`, `git`, `sudo`** (sudo only for the `/usr/local/bin` symlinks;
  it falls back gracefully if absent).
- Same **`$HOME`/username** convention as macOS hubs (duck mirrors each path at
  its natural location).

## The `~/.local/bin` PATH gotcha

The single thing that caused the most confusion: on Debian, **`~/.local/bin` is
often not on the login shell's PATH** (it's added by `~/.profile`, which zsh
doesn't read). duck runs hub commands over a login shell, so any tool installed
to `~/.local/bin` — mutagen, **and your own `claude`/`cass`** — silently fails to
resolve.

duck handles this two ways now:

1. Its own Linux installs (mutagen, tsshd) are symlinked into `/usr/local/bin`.
2. The `duck-open` rc block prepends **both** `~/.duck/bin` and `~/.local/bin` to
   PATH, so user-installed tools resolve inside duck sessions.

If you provisioned a hub before this and hit `command not found` for `claude` or
`cass`, either re-run `duck hub setup` on a fresh rc, or add
`export PATH="$HOME/.local/bin:$PATH"` to the hub's `~/.zprofile`.

## Tools duck does NOT install (you do)

duck provisions the *hub plumbing*, not your editor stack. If you run Claude on
the hub (e.g. `duck claude`, or a `claude` wrapper that calls `cass claude`),
install these on the hub yourself:

- **Claude Code** — `curl -fsSL https://claude.ai/install.sh | bash` (lands in
  `~/.local/bin/claude`; see the PATH gotcha above).
- **cass** (if your `claude` wrapper routes through it) — `cass`'s own installer,
  or drop the `cass-linux-amd64` release binary into `~/.local/bin` /
  `/usr/local/bin`. cass is pure-Go, so the release binary is static and just
  runs.

A common laptop setup is a `claude()` shell function that runs `cass claude`
in-tmux and `duck claude` otherwise; that in-tmux branch executes **on the hub**,
so it needs `cass` (and the real `claude`) present there.

## Eviction details (systemd)

`duck evict --install` on a Linux hub writes:

- `~/.config/systemd/user/duck-evict.service` — a `oneshot` running
  `~/.duck/evict.sh` with `AGE_SECS`/`RENAME_SECS` and a PATH that finds tmux.
- `~/.config/systemd/user/duck-evict.timer` — `OnUnitActiveSec=<--every>`,
  `Persistent=true`.

It runs `loginctl enable-linger` (best-effort, falls back to sudo) so the timer
fires without an active login session, then `systemctl --user enable --now`.
`duck evict --uninstall` disables the timer and removes the units. Inspect it on
the hub with `systemctl --user list-timers duck-evict.timer`.

> **Why the sweep needed a fix:** tmux ≥3.x on Linux escapes the `\037` field
> separator in `-F` output to the literal string `\037`. The eviction script's
> real-byte `IFS` never matched, so it parsed every session as empty and evicted
> nothing. The script now normalizes the escaped form back to the byte before
> splitting (a no-op on macOS, where tmux emits the raw byte).

## Known limitations

- **No systemd-less fallback** for eviction: a Linux hub without systemd
  `--user` (e.g. a minimal container) can't self-schedule the sweep. Run
  `duck evict` from cron yourself.
- **Non-Debian distros** need tmux/rsync installed by hand before setup.
- The **`duck` client is macOS-only** by release; you can `go build` it for
  Linux (pure-Go), but it's not shipped.

See also [migrating the hub](migrating-the-hub.md) for moving an existing setup
to a new (Linux) hub.
