#!/usr/bin/env bash
# scripts/package-gui-macos.sh
#
# Assembles the macOS GUI install bundle under
# `build/bin/pchat-gui-macos/`. Run via `task package:gui:macos` —
# the task itself is gated to macOS hosts (Windows/Linux skip with
# a clear message).
#
# Inputs (must already exist):
#   - cmd/pchat-gui/build/bin/pchat-gui.app   (from `task build:gui:macos`)
#   - bin/pchat-server-darwin-amd64           (from `task build:server:macos`)
#   - bin/pchat-darwin-amd64                  (from `task build:cli:macos`, optional)
#   - web/                                    (from `task build:frontend`)
#   - cmd/pchat-server/browser-extension.zip  (from `task build:frontend`)
#   - scripts/install-macos.sh + uninstall-macos.sh
#
# Output:
#   build/bin/pchat-gui-macos/
#     ├── pchat-gui.app/
#     ├── pchat-server
#     ├── pchat                       (optional, if cross-compiled)
#     ├── web/                        (SPA — also embedded in pchat-server)
#     ├── browser-extension.zip
#     ├── install.sh
#     └── uninstall.sh

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SRC_APP="$ROOT/cmd/pchat-gui/build/bin/pchat-gui.app"
BIN_DIR="$ROOT/build/bin/pchat-gui-macos"

if [[ ! -d "$SRC_APP" ]]; then
    echo "ERROR: pchat-gui.app not found at $SRC_APP" >&2
    echo "       run 'task build:gui:macos' first" >&2
    exit 1
fi

if [[ ! -f "$ROOT/bin/pchat-server-darwin-amd64" ]]; then
    echo "ERROR: macOS server binary not found at bin/pchat-server-darwin-amd64" >&2
    echo "       run 'task build:server:macos' first" >&2
    exit 1
fi

# Wipe + recreate the bundle dir so removed/renamed files from a
# prior build don't linger (cp -R doesn't delete stale entries).
if [[ -d "$BIN_DIR" ]]; then
    rm -rf "$BIN_DIR"
fi
mkdir -p "$BIN_DIR"

# --- .app bundle ---
cp -R "$SRC_APP" "$BIN_DIR/"
echo "[package-gui-macos] copied pchat-gui.app"

# --- server binary ---
cp "$ROOT/bin/pchat-server-darwin-amd64" "$BIN_DIR/pchat-server"
chmod +x "$BIN_DIR/pchat-server"
echo "[package-gui-macos] copied pchat-server"

# --- CLI (optional) ---
if [[ -f "$ROOT/bin/pchat-darwin-amd64" ]]; then
    cp "$ROOT/bin/pchat-darwin-amd64" "$BIN_DIR/pchat"
    chmod +x "$BIN_DIR/pchat"
    echo "[package-gui-macos] copied pchat (CLI)"
fi

# --- web/ (SPA — already embedded in pchat-server, kept here for
#     transparency so users can inspect the served assets) ---
if [[ -d "$ROOT/web" ]]; then
    mkdir -p "$BIN_DIR/web"
    cp -R "$ROOT/web/" "$BIN_DIR/web/"
    echo "[package-gui-macos] copied web/"
fi

# --- browser extension ---
if [[ -f "$ROOT/cmd/pchat-server/browser-extension.zip" ]]; then
    cp "$ROOT/cmd/pchat-server/browser-extension.zip" "$BIN_DIR/browser-extension.zip"
    echo "[package-gui-macos] copied browser-extension.zip"
fi

# --- install/uninstall scripts ---
cp "$ROOT/scripts/install-macos.sh"   "$BIN_DIR/install.sh"
cp "$ROOT/scripts/uninstall-macos.sh" "$BIN_DIR/uninstall.sh"
chmod +x "$BIN_DIR/install.sh" "$BIN_DIR/uninstall.sh"
echo "[package-gui-macos] copied install.sh / uninstall.sh"

echo ""
echo "[package-gui-macos] bundle ready at $BIN_DIR"
echo "[package-gui-macos] contents:"
ls -la "$BIN_DIR"
