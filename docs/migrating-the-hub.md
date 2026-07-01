# Migrating the hub

Moving duck to a new hub machine. The hub is just an always-on box reachable
over SSH — there's no database and no daemon, so a migration is mostly
*re-pointing* your laptop and letting Mutagen re-mirror. This walks through it
end to end and calls out the one piece of state worth carrying by hand.

## What actually moves

Most of duck's "state" isn't on the hub exclusively, so it survives a move on
its own:

| Thing | Where it lives | Survives a hub move? |
|---|---|---|
| **Synced project files** | both laptop *and* hub (Mutagen, bidirectional) | **Yes** — your laptop copy is intact; the new hub re-mirrors from it. |
| **Claude transcripts + memory** (if `claude-sync on`) | both sides, per folder | **Yes** — same bidirectional mirror; `claude --resume` works again once re-synced. |
| **`~/.duck/names.json`** (session display names) | **hub only**, laptop is the single writer | **No** — copy it across, or names reset to codex/dir defaults. |
| **Live tmux sessions** | hub only, ephemeral | **No** — running sessions don't transfer. Claude conversations do (transcripts sync), so you `duck` back in and `claude --resume`. |
| **Evict launchd agent** (`duck evict --install`) | hub only | **No** — re-install on the new hub if you used it. |
| **`duck sync` named bundles** | hub-tracked | re-`get` them on the new hub (see below). |

The rule of thumb: **files and Claude history are safe** (they're mirrored on
your laptop too). **Names, running sessions, and hub-side automation** need a
hand.

## If you have multiple laptops: set up an anchor first

Everything above still requires running `duck hub set <new>` on **every**
laptop you use duck from — there's no way around that with only a hub, since
the hub address is how a laptop finds the hub in the first place.

If you use duck from more than one laptop, point them all at an **anchor**
first (`duck anchor set <user@host>`) — a small JSON file
(`~/.duck/anchor.json`) on any SSH-reachable box that mirrors the hub address
(and a few shared settings) across every laptop pointed at it. Pick a box
*other than* the hub itself, and `duck hub set` on any one laptop is enough:
every other laptop picks up the new hub automatically on its next command (via
`config.ResolveAnchor`, wired into the same `RequireHub` check every
hub-touching command already runs — no separate "pull" step). Point the anchor
at the hub itself instead and it degrades to exactly the manual carry-over
described below (still fine, just no better than today).

No token or extra auth for the anchor — it's plain SSH, so whatever keys
already reach that host are sufficient. See `duck anchor show` to check what's
configured.

## Steps

### 1. Provision the new hub

One command verifies SSH, saves the address, and installs everything the hub
needs (`tmux` + `mutagen` + `tsshd` + TPM, the de-hooked `~/.tmux.conf`, and the
`duck-open` interceptor):

```sh
duck hub setup <user@new-host>      # e.g. duck hub setup andrew@pelican
```

If SSH auth isn't set up yet and you're interactive, duck offers to run
`ssh-copy-id` for you. A Tailscale MagicDNS name (`andrew@pelican`) works as the
address — just make sure that exact name is in `~/.ssh/known_hosts` first (`ssh
<user@new-host> true` once to accept the host key), since duck connects by the
name you give it, not by IP.

Confirm:

```sh
duck hub show        # → new hostname
duck config          # shows what you're now pointed at
```

See [Linux hubs](#linux-hubs) below — duck was built macOS-first, and a few
hub-side features behave differently (or not at all) on Linux.

### 2. Carry over your session names

`~/.duck/names.json` is the only hub-exclusive state. Copy it from the old hub
so the picker keeps your names:

```sh
scp <user@old-host>:'~/.duck/names.json' /tmp/names.json
ssh <user@new-host> 'mkdir -p ~/.duck'
scp /tmp/names.json <user@new-host>:'~/.duck/names.json'
```

(Skip this if you don't mind names falling back to codex-generated / directory
names.)

### 3. Re-mirror your project folders

Sync is per-folder and lazy — nothing auto-moves. For each project you work in,
just `duck` into it again:

```sh
cd ~/dev/myproject && duck
```

Because the new hub starts empty for that folder, duck mirrors your laptop copy
up (it's a one-directional push to a fresh hub, nothing is deleted) and Mutagen
maintains it from there. If the new hub somehow *already* has the folder, duck
offers the usual newest-wins merge (nothing deleted).

For any **named `duck sync` bundles**, re-pull them onto the new hub:

```sh
duck sync ls                 # list bundles the hub knows
duck sync get <bundle>       # pull each one onto this machine via the new hub
```

### 4. Re-install hub-side automation

If you ran the eviction sweep as a launchd agent, it lived on the old hub —
re-install it on the new one:

```sh
duck evict --install --every 30m       # adjust flags to taste
```

### 5. Verify

```sh
duck ls                  # talks to the new hub (empty until you open sessions)
cd <a-project> && duck   # sync + fresh remote session on the new hub
duck --resume            # picker shows your carried-over names
```

If `claude-sync` is on, give Mutagen a few seconds after the first `duck` into a
folder, then `claude --resume` inside the session — your conversations are there.

## Linux hubs

duck was written macOS-first, but a Linux box works as a hub — the whole core
loop plus self-eviction. The summary:

| Feature | On a Linux hub |
|---|---|
| **Sync (Mutagen)** | ✅ `duck hub setup` fetches the mutagen binary from GitHub (no apt package) into `~/.local/bin` + `/usr/local/bin`. |
| **Remote tmux + tssh/tsshd attach** | ✅ `tsshd` is installed from its GitHub release and its path recorded in config (tssh's auto-deploy isn't reliable). |
| **`duck-open` interceptor** | ✅ The shim resolves `xdg-open`; the laptop routes `open`/`xdg-open` per-OS. |
| **`duck evict --install` / `--uninstall`** | ✅ Scheduled as a **systemd `--user` timer** (launchd on macOS), with linger enabled, and installed by default during setup. |
| **`duck snap`** | ➖ A macOS-**laptop** feature; the hub side (file upload) is platform-agnostic, so nothing to do on a Linux hub. |
| **Tools you install yourself** | `claude` / `cass` are not provisioned by duck — install them on the hub (see [docs/linux-hub.md](linux-hub.md)). |

For the full Linux-hub story — what `duck hub setup` auto-installs (mutagen,
tsshd), the `~/.local/bin` PATH gotcha, eviction via systemd, and the
claude/cass tools you install yourself — see **[docs/linux-hub.md](linux-hub.md)**.

## Decommissioning the old hub

Nothing on duck's side pins you to the old box once the config points elsewhere,
but the old hub may still be running background work:

- **Mutagen sessions** from your laptop to the old hub keep running until torn
  down. List and drop them: `duck sync status`, then `duck sync drop <path>` for
  paths you no longer want mirrored to the old hub (the local file stays).
- **The evict launchd agent** (if installed) keeps sweeping on the old hub:
  `duck evict --uninstall` while pointed at it, or remove the launchd plist
  directly on that machine.
- **Detached tmux sessions** on the old hub will sit idle. `duck clean` while
  pointed at it, or just let them age out.

Once those are quiet, the old hub is free to repurpose.

## Quick reference

```sh
duck hub setup <user@host>   # provision + point duck at a new hub
duck hub show                # which hub am I on?
duck hub set <user@host>     # re-point without re-provisioning
duck anchor set <user@host>  # (once) share hub moves across all your laptops
duck anchor show             # which anchor am I on?
# carry names:  scp old:~/.duck/names.json  →  new:~/.duck/names.json
# re-mirror:    cd <project> && duck   (per folder)
# bundles:      duck sync get <bundle>
# automation:   duck evict --install …
```
