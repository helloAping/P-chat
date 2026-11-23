#!/usr/bin/env bash
# Install PowerShell 7 and expose it as both pwsh and powershell.
set -euo pipefail

link_pwsh() {
  local bin
  bin="$(command -v pwsh || true)"
  if [[ -z "$bin" ]]; then
    echo "pwsh not found after install" >&2
    exit 1
  fi
  sudo mkdir -p /usr/local/bin
  sudo ln -sfn "$bin" /usr/local/bin/pwsh
  sudo ln -sfn "$bin" /usr/local/bin/powershell
}

if command -v powershell >/dev/null 2>&1 && command -v pwsh >/dev/null 2>&1; then
  exit 0
fi
if command -v pwsh >/dev/null 2>&1; then
  link_pwsh
  exit 0
fi

version="7.4.6"
os="$(uname -s)"
arch="$(uname -m)"
case "$os" in
  Linux)
    case "$arch" in
      x86_64) asset="powershell-${version}-linux-x64.tar.gz" ;;
      aarch64|arm64) asset="powershell-${version}-linux-arm64.tar.gz" ;;
      *) echo "unsupported linux arch: $arch" >&2; exit 1 ;;
    esac
    ;;
  Darwin)
    case "$arch" in
      arm64) asset="powershell-${version}-osx-arm64.tar.gz" ;;
      x86_64) asset="powershell-${version}-osx-x64.tar.gz" ;;
      *) echo "unsupported macOS arch: $arch" >&2; exit 1 ;;
    esac
    ;;
  *)
    echo "unsupported OS: $os" >&2
    exit 1
    ;;
esac

tmp="$(mktemp -d)"
url="https://github.com/PowerShell/PowerShell/releases/download/v${version}/${asset}"
curl -fsSL -o "$tmp/pwsh.tar.gz" "$url"
sudo mkdir -p /opt/microsoft/powershell/7
sudo tar -xzf "$tmp/pwsh.tar.gz" -C /opt/microsoft/powershell/7
sudo chmod +x /opt/microsoft/powershell/7/pwsh
link_pwsh
powershell -NoProfile -Command 'Write-Output $PSVersionTable.PSVersion.ToString()'
