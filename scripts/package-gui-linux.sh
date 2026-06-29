#!/usr/bin/env bash
# Assemble the P-Chat Linux distribution bundle into build/bin/
# Requires: pchat-gui (Wails build), pchat-server (Go build), web/ (Vite build)
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BIN="$ROOT/build/bin"

echo "[package-linux] assembling bundle -> $BIN"

mkdir -p "$BIN"

# --- pchat-gui (Wails desktop app) ---
GUI_SRC="$ROOT/cmd/pchat-gui/build/bin/pchat-gui"
if [[ -f "$GUI_SRC" ]]; then
  cp -f "$GUI_SRC" "$BIN/pchat-gui"
  chmod +x "$BIN/pchat-gui"
  echo "[package-linux] ok: pchat-gui"
else
  echo "[package-linux] SKIP: pchat-gui not found (run 'task build:gui:linux' first)"
fi

# --- pchat-server (Go HTTP backend) ---
SERVER_SRC="$ROOT/build/bin/pchat-server"
if [[ -f "$SERVER_SRC" ]]; then
  cp -f "$SERVER_SRC" "$BIN/pchat-server"
  chmod +x "$BIN/pchat-server"
  echo "[package-linux] ok: pchat-server"
else
  echo "[package-linux] SKIP: pchat-server not found (run 'task build' first)"
fi

# --- install/uninstall scripts ---
cp -f "$ROOT/scripts/install-linux.sh"  "$BIN/install.sh"
cp -f "$ROOT/scripts/uninstall-linux.sh" "$BIN/uninstall.sh"
chmod +x "$BIN/install.sh" "$BIN/uninstall.sh"
echo "[package-linux] ok: install/uninstall scripts"

# --- SPA frontend ---
if [[ -d "$ROOT/web" ]]; then
  mkdir -p "$BIN/web"
  cp -rf "$ROOT/web/"* "$BIN/web/"
  echo "[package-linux] ok: web/ SPA"
else
  echo "[package-linux] SKIP: web/ not found (run 'npm run build' first)"
fi

echo ""
echo "[package-linux] bundle assembled:"
ls -lh "$BIN/pchat-gui" "$BIN/pchat-server" 2>/dev/null || true
echo ""
echo "  To install:  cd $BIN && ./install.sh"
echo "  Portable:    cd $BIN && ./install.sh --portable"
echo "  System-wide: cd $BIN && sudo ./install.sh --prefix /usr/local"
