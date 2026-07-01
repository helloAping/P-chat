#!/usr/bin/env bash
# P-Chat Linux uninstaller
#
# Usage:
#   ./uninstall.sh                 # removes the default install
#   ./uninstall.sh --remove-data   # also delete ~/.p-chat/

set -euo pipefail

PREFIX="$HOME/.local"
REMOVE_DATA=false
EXEC_DIR="$(cd "$(dirname "$0")" && pwd)"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --remove-data) REMOVE_DATA=true; shift ;;
    --prefix)      PREFIX="$2"; shift 2 ;;
    --portable)    PREFIX="$EXEC_DIR"; shift ;;
    *) shift ;;
  esac
done

BIN_DIR="$PREFIX/bin"
APP_DIR="$PREFIX/share/pchat"
DESKTOP_FILE="$PREFIX/share/applications/pchat.desktop"

echo "[uninstall] prefix: $PREFIX"

# --- stop running instances ---
for proc in pchat-gui pchat-server pchat; do
  if pid=$(pgrep -f "$proc" 2>/dev/null || true); then
    echo "[uninstall] stopping $proc (PID: $pid)"
    kill "$pid" 2>/dev/null || true
    sleep 1
  fi
done

# --- remove binaries ---
for bin in pchat-gui pchat-server pchat; do
  if [[ -f "$BIN_DIR/$bin" ]]; then
    rm -f "$BIN_DIR/$bin"
    echo "[uninstall] removed $BIN_DIR/$bin"
  fi
done

# --- remove .desktop entry ---
if [[ -f "$DESKTOP_FILE" ]]; then
  rm -f "$DESKTOP_FILE"
  echo "[uninstall] removed $DESKTOP_FILE"
fi

# --- remove app data dir ---
if [[ -d "$APP_DIR" ]]; then
  rm -rf "$APP_DIR"
  echo "[uninstall] removed $APP_DIR"
fi

# --- remove user data ---
if $REMOVE_DATA; then
  DATA_DIR="$HOME/.p-chat"
  if [[ -d "$DATA_DIR" ]]; then
    rm -rf "$DATA_DIR"
    echo "[uninstall] removed user data: $DATA_DIR"
  fi
else
  echo "[uninstall] keeping user data: ~/.p-chat/ (use --remove-data to also delete)"
fi

echo "[uninstall] done."
