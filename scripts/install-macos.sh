#!/usr/bin/env bash
# P-Chat macOS installer
#
# Bundles produced by `task package:gui:macos` look like this at the
# script's location:
#
#   ./pchat-gui.app/                    # Wails .app bundle
#   ./pchat-server                      # server binary (universal)
#   ./pchat                             # CLI binary
#   ./web/                              # embedded SPA assets
#   ./uninstall.sh
#
# Usage:
#   ./install.sh                         # install to ~/Applications + ~/.local/bin
#   ./install.sh --system                # install to /Applications + /usr/local/bin (sudo)
#   ./install.sh --prefix "$HOME/Apps"   # install to a custom prefix (no .app/ install,
#                                        # only the side binaries + CLI)
#   ./install.sh --portable              # treat current dir as the install target,
#                                        # no copies, no symlinks
#
# Uninstall: run uninstall.sh from the install location (or `rm -rf` the
#            .app + bin symlinks if you used --portable).

set -euo pipefail

USER_PREFIX="$HOME"
SYSTEM=false
PREFIX=""
PORTABLE=false
EXEC_DIR="$(cd "$(dirname "$0")" && pwd)"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --system)   SYSTEM=true; shift ;;
    --prefix)   PREFIX="$2"; shift 2 ;;
    --portable) PORTABLE=true; shift ;;
    *) echo "Unknown option: $1"; exit 1 ;;
  esac
done

# --- locate bundle contents ---
SRC_APP="$EXEC_DIR/pchat-gui.app"
SRC_SERVER="$EXEC_DIR/pchat-server"
SRC_CLI="$EXEC_DIR/pchat"
SRC_UNINSTALL="$EXEC_DIR/uninstall.sh"

[[ -d "$SRC_APP" ]] || { echo "ERROR: pchat-gui.app not found at $SRC_APP"; exit 1; }
[[ -f "$SRC_SERVER" ]] || { echo "ERROR: pchat-server not found at $SRC_SERVER"; exit 1; }
HAVE_CLI=false
[[ -f "$SRC_CLI" ]] && HAVE_CLI=true

# --- resolve install targets ---
if $PORTABLE; then
  echo "[install] portable mode — treating $EXEC_DIR as install target"
  echo "  Run:  open $SRC_APP"
  echo "  Uninstall: rm -rf \"$EXEC_DIR\""
  exit 0
fi

if [[ -n "$PREFIX" ]]; then
  APP_DIR="$PREFIX"
  BIN_DIR="$PREFIX/bin"
elif $SYSTEM; then
  APP_DIR="/Applications"
  BIN_DIR="/usr/local/bin"
  if [[ $EUID -ne 0 ]]; then
    SUDO="sudo"
  else
    SUDO=""
  fi
else
  APP_DIR="$USER_PREFIX/Applications"
  BIN_DIR="$USER_PREFIX/.local/bin"
  SUDO=""
fi

echo "[install] app dir: $APP_DIR"
echo "[install] bin dir: $BIN_DIR"

mkdir -p "$APP_DIR" "$BIN_DIR"

# --- install .app bundle ---
# Use `rsync -a --delete` to refresh existing installs without
# leaving stale Mach-O resources around. Fall back to cp -R if
# rsync isn't on PATH.
DST_APP="$APP_DIR/pchat-gui.app"
if command -v rsync &>/dev/null; then
  $SUDO rsync -a --delete "$SRC_APP/" "$DST_APP/"
else
  if [[ -d "$DST_APP" ]]; then $SUDO rm -rf "$DST_APP"; fi
  $SUDO cp -R "$SRC_APP" "$DST_APP"
fi
echo "[install] installed $DST_APP"

# --- install side binaries ---
# pchat-server is colocated with the .app on /Applications (next to
# the Wails MacOS/pchat-gui binary reads it via the same parent dir
# at runtime); the bin/ symlinks below are just for the CLI + manual
# server invocation.
if $HAVE_CLI; then
  ln -sf "$SRC_CLI" "$BIN_DIR/pchat"
  echo "[install] symlink $BIN_DIR/pchat -> $SRC_CLI"
fi
ln -sf "$SRC_SERVER" "$BIN_DIR/pchat-server"
echo "[install] symlink $BIN_DIR/pchat-server -> $SRC_SERVER"

# --- copy uninstall script next to the .app ---
# Same convention as install-linux.sh: the uninstall script is a
# pure data file inside the install target, so users always know
# where to find it.
cp -f "$SRC_UNINSTALL" "$APP_DIR/pchat-uninstall.sh" 2>/dev/null || true
chmod +x "$APP_DIR/pchat-uninstall.sh" 2>/dev/null || true

echo ""
echo "[install] done."
echo "  Launch:  open $DST_APP"
if $HAVE_CLI; then
  echo "  CLI:     $BIN_DIR/pchat"
fi
echo "  Server:  $BIN_DIR/pchat-server"
echo "  Uninstall:  $APP_DIR/pchat-uninstall.sh"
if [[ -n "$SUDO" ]]; then
  echo "  (Re-run with sudo to remove /Applications/pchat-gui.app)"
fi
