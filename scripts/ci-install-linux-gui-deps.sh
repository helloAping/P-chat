#!/usr/bin/env bash
# GTK and WebKit libraries required by a native Wails v2 Linux build.
set -euo pipefail

sudo apt-get update
sudo apt-get install -y build-essential pkg-config libgtk-3-dev
if apt-cache show libwebkit2gtk-4.1-dev >/dev/null 2>&1; then
  sudo apt-get install -y libwebkit2gtk-4.1-dev
else
  sudo apt-get install -y libwebkit2gtk-4.0-dev
fi
if apt-cache show libayatana-appindicator3-dev >/dev/null 2>&1; then
  sudo apt-get install -y libayatana-appindicator3-dev
fi
