#!/usr/bin/env bash
# Install duck — make a remote hub feel local (auto-sync cwd + remote tmux).
#
# Public repo:  curl -sSL https://raw.githubusercontent.com/DigiBugCat/duck/main/install.sh | sh
# Or just:      gh release download --repo DigiBugCat/duck --pattern 'duck-*' --dir ~/.local/bin
#
# Installs a raw binary to ~/.local/bin/duck (NOT Homebrew); `duck update` then
# self-updates from the GitHub release. Mirrors the sibling `cass` tool.

set -euo pipefail

REPO="DigiBugCat/duck"
INSTALL_DIR="${DUCK_INSTALL_DIR:-$HOME/.local/bin}"

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) echo "Unsupported architecture: $ARCH" >&2; exit 1 ;;
esac

TARGET="${OS}-${ARCH}"
ASSET="duck-${TARGET}"

mkdir -p "$INSTALL_DIR"

# Prefer the gh CLI (works for private repos and reuses your auth); fall back to
# the public releases API via curl.
if command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1; then
  echo "Fetching latest duck release via gh…"
  gh release download --repo "$REPO" --pattern "$ASSET" --dir "$INSTALL_DIR" --clobber
  mv -f "${INSTALL_DIR}/${ASSET}" "${INSTALL_DIR}/duck"
  VERSION=$(gh release view --repo "$REPO" --json tagName --jq '.tagName')
else
  echo "Fetching latest duck release…"
  RELEASE=$(curl -sL "https://api.github.com/repos/${REPO}/releases/latest")
  VERSION=$(echo "$RELEASE" | grep -o '"tag_name": *"[^"]*"' | head -1 | cut -d'"' -f4)
  URL=$(echo "$RELEASE" | grep -o '"browser_download_url": *"[^"]*'"${ASSET}"'[^"]*"' | head -1 | cut -d'"' -f4)
  if [ -z "$URL" ]; then
    echo "No binary found for ${TARGET}." >&2
    echo "For a private repo: install the gh CLI, run 'gh auth login', and re-run this script." >&2
    exit 1
  fi
  echo "Downloading duck ${VERSION} for ${TARGET}…"
  curl -sL "$URL" -o "${INSTALL_DIR}/duck"
fi

chmod +x "${INSTALL_DIR}/duck"
echo "Installed duck ${VERSION} to ${INSTALL_DIR}/duck"

# macOS: a brew cask may shadow this install. Warn if duck still resolves elsewhere.
RESOLVED="$(command -v duck || true)"
if [ -n "$RESOLVED" ] && [ "$RESOLVED" != "${INSTALL_DIR}/duck" ]; then
  echo ""
  echo "⚠  'duck' currently resolves to ${RESOLVED}, not ${INSTALL_DIR}/duck."
  echo "   If that's a Homebrew cask, run: brew uninstall --cask duck"
  echo "   Or put ${INSTALL_DIR} ahead of it on your PATH."
fi

if ! echo "$PATH" | tr ':' '\n' | grep -qx "$INSTALL_DIR"; then
  echo ""
  echo "Add ${INSTALL_DIR} to your PATH (e.g. in ~/.zshrc):"
  echo "  export PATH=\"${INSTALL_DIR}:\$PATH\""
fi

echo ""
echo "Get started:"
echo "  duck --resume     # pick a session to resume"
echo "  duck update       # self-update to the latest release"
