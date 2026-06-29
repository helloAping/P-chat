#!/usr/bin/env bash
# P-Chat Linux installer
#
# Usage:
#   ./install.sh                         # install to ~/.local
#   ./install.sh --prefix /usr/local     # system-wide (requires sudo)
#   ./install.sh --no-desktop            # skip .desktop entry
#   ./install.sh --portable              # use current dir as install target
#
# Uninstall: run uninstall.sh from the install dir.

set -euo pipefail

PREFIX="$HOME/.local"
NO_DESKTOP=false
PORTABLE=false
EXEC_DIR="$(cd "$(dirname "$0")" && pwd)"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --prefix)     PREFIX="$2"; shift 2 ;;
    --no-desktop) NO_DESKTOP=true; shift ;;
    --portable)   PORTABLE=true; shift ;;
    *) echo "Unknown option: $1"; exit 1 ;;
  esac
done

# --- locate binaries ---
SRC_GUI="$EXEC_DIR/pchat-gui"
SRC_SERVER="$EXEC_DIR/pchat-server"
SRC_UNINSTALL="$EXEC_DIR/uninstall.sh"

if [[ ! -f "$SRC_GUI" ]]; then
  echo "ERROR: pchat-gui not found next to install.sh ($EXEC_DIR)"
  exit 1
fi
if [[ ! -f "$SRC_SERVER" ]]; then
  echo "ERROR: pchat-server not found next to install.sh ($EXEC_DIR)"
  exit 1
fi

BIN_DIR="$PREFIX/bin"
APP_DIR="$PREFIX/share/pchat"

if $PORTABLE; then
  BIN_DIR="$EXEC_DIR"
  APP_DIR="$EXEC_DIR"
fi

echo "[install] prefix: $PREFIX"
echo "[install] bin dir: $BIN_DIR"
echo "[install] app dir: $APP_DIR"

# --- copy binaries ---
mkdir -p "$BIN_DIR" "$APP_DIR"

install_bin() {
  local src="$1" dst="$2"
  cp -f "$src" "$dst"
  chmod +x "$dst"
  echo "[install] copied $src -> $dst"
}

if ! $PORTABLE; then
  install_bin "$SRC_GUI"    "$BIN_DIR/pchat-gui"
  install_bin "$SRC_SERVER" "$BIN_DIR/pchat-server"
else
  install_bin "$SRC_GUI"    "$EXEC_DIR/pchat-gui"
  install_bin "$SRC_SERVER" "$EXEC_DIR/pchat-server"
fi

# Copy uninstall script
cp -f "$SRC_UNINSTALL" "$APP_DIR/uninstall.sh" 2>/dev/null || true

# Copy web assets (SPA)
if [[ -d "$EXEC_DIR/web" ]]; then
  mkdir -p "$APP_DIR/web"
  cp -rf "$EXEC_DIR/web/" "$APP_DIR/web/"
  echo "[install] copied web assets"
fi

# --- .desktop entry ---
if ! $NO_DESKTOP && ! $PORTABLE; then
  DESKTOP_DIR="$PREFIX/share/applications"
  mkdir -p "$DESKTOP_DIR"

  cat > "$DESKTOP_DIR/pchat.desktop" << EOF
[Desktop Entry]
Type=Application
Name=P-Chat
Comment=P-Chat AI Desktop
Exec=$BIN_DIR/pchat-gui
Icon=$APP_DIR/pchat.png
Terminal=false
Categories=Utility;Development;
StartupWMClass=pchat-gui
EOF
  echo "[install] created desktop entry: $DESKTOP_DIR/pchat.desktop"

  # Update desktop database if available
  if command -v update-desktop-database &>/dev/null; then
    update-desktop-database "$DESKTOP_DIR" 2>/dev/null || true
  fi
fi

echo ""
echo "[install] done."
echo "  Run:  $BIN_DIR/pchat-gui"
echo "  Data: ~/.p-chat/"
if $PORTABLE; then
  echo "  Uninstall: rm -rf $EXEC_DIR"
else
  echo "  Uninstall: $APP_DIR/uninstall.sh"
fi
