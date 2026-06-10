#!/bin/sh
# duck-open — hub-side open interceptor (installed by `duck hub setup`).
#
# Installed at ~/.duck/bin/duck-open with `open` and `xdg-open` symlinked to
# it, and exported as $BROWSER, so both Claude Code's internal URL opener and
# any `open <thing>` run from a shell land here. When a duck client is
# attached, its laptop runs a listener that is reverse-forwarded to
# localhost:$DUCK_OPEN_PORT on the hub; qualifying targets (http(s) URLs and
# existing regular files) are sent there and open ON THE LAPTOP. Everything
# else — flags, directories, multiple args, no listener — falls through to the
# real platform opener, so the shim is never in the way.

PORT="${DUCK_OPEN_PORT:-4774}"

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

if curl -fsS -m 5 -o /dev/null \
  --data-urlencode "target=$1" \
  --data-urlencode "cwd=$PWD" \
  --data-urlencode "home=$HOME" \
  "http://127.0.0.1:$PORT/open" 2>/dev/null; then
  # Saying so in stdout matters: when Claude runs `open x` through its Bash
  # tool, this line is the tool output that tells it the open SUCCEEDED.
  echo "duck: opened on your laptop: $1"
  exit 0
fi
passthrough "$@"
