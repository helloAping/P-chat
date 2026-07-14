#!/usr/bin/env bash
# P-Chat macOS uninstaller
#
# Inverse of install-macos.sh. Removes the .app bundle, bin
# symlinks, and CLI shim. Does NOT touch user data under
# ~/.p-chat/ — that lives across upgrades by design.
#
# Usage:
#   ./uninstall.sh                         # remove from ~/Applications + ~/.local/bin
#   ./uninstall.sh --system                # remove from /Applications + /usr/local/bin
#   SUDO_FORCE=1 ./uninstall.sh --system   # force sudo even when EUID=0 guess fails

set -euo pipefail

SYSTEM=false
SUDO=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --system) SYSTEM=true; shift ;;
    *) echo "Unknown option: $1"; exit 1 ;;
  esac
done

if $SYSTEM; then
  APP_DIR="/Applications"
  BIN_DIR="/usr/local/bin"
  if [[ $EUID -ne 0 || -n "${SUDO_FORCE:-}" ]]; then SUDO="sudo"; fi
else
  APP_DIR="$HOME/Applications"
  BIN_DIR="$HOME/.local/bin"
fi

echo "[uninstall] app dir: $APP_DIR"
echo "[uninstall] bin dir: $BIN_DIR"

removed=0
if [[ -d "$APP_DIR/pchat-gui.app" ]]; then
  $SUDO rm -rf "$APP_DIR/pchat-gui.app"
  echo "[uninstall] removed $APP_DIR/pchat-gui.app"
  removed=1
fi
for f in pchat pchat-server; do
  link="$BIN_DIR/$f"
  # Only remove if it points into our install — never blow away
  # a user's pre-existing binary with the same name.
  if [[ -L "$link" ]] && [[ "$(readlink "$link" || true)" == *"pchat-gui.app"* || "$(readlink "$link" || true)" == *"/pchat"* ]]; then
    $SUDO rm -f "$link"
    echo "[uninstall] removed symlink $link"
    removed=1
  fi
done

if [[ $removed -eq 0 ]]; then
  echo "[uninstall] nothing to remove (already clean)"
fi

echo ""
echo "[uninstall] user data (~/.p-chat/) is preserved. To wipe:"
echo "  rm -rf \"$HOME/.p-chat\""
