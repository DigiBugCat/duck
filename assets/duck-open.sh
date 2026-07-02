#!/bin/sh
# duck-open — hub-side open interceptor (installed by `duck hub setup`).
#
# Installed at ~/.duck/bin/duck-open with `open` and `xdg-open` symlinked to
# it, and exported as $BROWSER, so both Claude Code's internal URL opener and
# any `open <thing>` run from a shell land here. When a duck client is attached,
# its laptop runs a listener that is reverse-forwarded to a PER-SESSION unix
# socket on the hub (~/.duck/run/open-<session>.sock). Each duck session has its
# own socket — no shared port — so several attached laptops never collide and an
# open lands on the laptop THIS session came from. Qualifying targets (http(s)
# URLs and existing regular files) are sent to that socket and open ON THE
# LAPTOP. Everything else — flags, directories, multiple args, no session, no
# listener — falls through to the real platform opener, so the shim is never in
# the way.
#
# Finding the socket: duck stamps the path into the tmux session environment as
# DUCK_OPEN_SOCK at attach. We prefer an inherited $DUCK_OPEN_SOCK, else ask tmux
# (via $TMUX) for the current session's value. No socket resolvable ⇒ this is not
# an attached duck session ⇒ passthrough to the hub's own opener.

passthrough() {
  me="$(basename "$0")"
  for real in "/usr/bin/$me" "/opt/homebrew/bin/$me" "/bin/$me"; do
    [ -x "$real" ] && exec "$real" "$@"
  done
  echo "duck-open: no duck client listening and no system $me found; target was: $*" >&2
  exit 1
}

# Only the simple one-target form is intercepted; anything fancier (flags like
# `open -a`, multi-file opens, bare dirs) goes to the real opener untouched.
[ $# -eq 1 ] || passthrough "$@"
case "$1" in
-*) passthrough "$@" ;;
http://* | https://*) : ;;
*) [ -f "$1" ] || passthrough "$@" ;;
esac

# Resolve this session's opener socket: inherited env first, else tmux session
# env. cut -d= -f2- keeps '=' in the value (paths won't have them, but be exact).
SOCK="$DUCK_OPEN_SOCK"
if [ -z "$SOCK" ] && [ -n "$TMUX" ]; then
  sess="$(tmux display-message -p '#S' 2>/dev/null)"
  if [ -n "$sess" ]; then
    SOCK="$(tmux show-environment -t "$sess" DUCK_OPEN_SOCK 2>/dev/null | cut -d= -f2-)"
  fi
fi
# No socket → not an attached duck session → let the hub opener handle it.
[ -n "$SOCK" ] || passthrough "$@"

# POST to the per-session unix socket. The URL host is a placeholder (curl
# --unix-socket routes by socket, not host). On success the shim echoes so
# Claude's Bash-tool output reflects that the open happened on the laptop.
if curl -fsS -m 5 -o /dev/null --unix-socket "$SOCK" \
  --data-urlencode "target=$1" \
  --data-urlencode "cwd=$PWD" \
  --data-urlencode "home=$HOME" \
  "http://duck-open/open" 2>/dev/null; then
  echo "duck: opened on your laptop: $1"
  exit 0
fi

# The POST failed though a socket path was set. If the socket file is gone the
# client detached cleanly (stay quiet); if it exists but won't answer the tunnel
# is stale — surface it so a broken opener is visible instead of a silent no-op.
# Either way fall through to the hub opener so behaviour degrades, not breaks.
if [ -S "$SOCK" ]; then
  echo "duck: opener socket $SOCK is not responding (stale tunnel — reattach the duck client for this session); running the hub opener instead" >&2
fi
passthrough "$@"
