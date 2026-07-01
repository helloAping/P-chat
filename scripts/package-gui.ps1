# Helper script for task package:gui. Bundles the Wails GUI binary
# together with the freshly-built pchat-server.exe (which already
# //go:embeds the Vue SPA from cmd/pchat-server/web/), copies the
# install/uninstall scripts, and ALSO copies the built SPA assets
# (web/index.html + web/assets/*) into the GUI bundle as
# pchat-gui/web/ — so the installer can ship the SPA next to the
# binary even if the user later wants to mount the Wails asset
# server directly. The runtime path still goes through the
# pchat-server child process; the extra copy is for transparency.
# Run from the repo root.

$ErrorActionPreference = "Stop"

$root    = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot "..")).Path
$srcGui  = Join-Path $root "cmd\pchat-gui\build\bin"
$binDir  = Join-Path $root "bin"
$webDir  = Join-Path $root "web"
$guiWeb  = Join-Path $srcGui "web"

if (-not (Test-Path -LiteralPath $srcGui)) { throw "pchat-gui build dir not found: $srcGui" }
if (-not (Test-Path -LiteralPath $binDir)) { throw "bin dir not found: $binDir" }
if (-not (Test-Path -LiteralPath $webDir)) { throw "web/ (built SPA) not found: $webDir — run 'task build:frontend' first" }

# --- Server binary (embeds the SPA) ---
Copy-Item -LiteralPath (Join-Path $binDir "pchat-server.exe") -Destination (Join-Path $srcGui "pchat-server.exe") -Force

# --- CLI binary (REPL) ---
# install.ps1 (now) requires pchat.exe to sit next to it, so
# `pchat` can land in $target during install and become
# available on the user PATH after `-AddToPath`.
Copy-Item -LiteralPath (Join-Path $binDir "pchat.exe") -Destination (Join-Path $srcGui "pchat.exe") -Force

# --- Install scripts ---
Copy-Item -LiteralPath (Join-Path $root "cmd\pchat-gui\install.ps1")   -Destination (Join-Path $srcGui "install.ps1")   -Force
Copy-Item -LiteralPath (Join-Path $root "cmd\pchat-gui\uninstall.ps1") -Destination (Join-Path $srcGui "uninstall.ps1") -Force

# --- Built SPA copied into the GUI bundle (web/index.html + assets) ---
# Wipe any existing contents so removed/renamed files from a prior
# build don't linger (Copy-Item -Recurse doesn't delete stale files
# in the destination).
if (Test-Path -LiteralPath $guiWeb) {
    Get-ChildItem -LiteralPath $guiWeb -Force | Remove-Item -Recurse -Force
} else {
    New-Item -ItemType Directory -Path $guiWeb -Force | Out-Null
}
Copy-Item -LiteralPath (Join-Path $webDir "index.html") -Destination (Join-Path $guiWeb "index.html") -Force
$srcAssets = Join-Path $webDir "assets"
if (Test-Path -LiteralPath $srcAssets) {
    $dstAssets = Join-Path $guiWeb "assets"
    New-Item -ItemType Directory -Path $dstAssets -Force | Out-Null
    # Copy files (not subdirs — Vite output is flat). Wildcard form
    # is more reliable than Get-ChildItem | Copy-Item here, which
    # occasionally yields nothing when invoked through Task.
    Copy-Item -Path (Join-Path $srcAssets "*") -Destination $dstAssets -Force
}

# --- GUI binary into bin/ ---
Copy-Item -LiteralPath (Join-Path $srcGui "pchat-gui.exe") -Destination (Join-Path $binDir "pchat-gui.exe") -Force

Write-Host "[package-gui] bundle ready at $srcGui"
Write-Host "[package-gui] pchat-gui.exe also copied to $binDir"
Write-Host "[package-gui] SPA copied to $guiWeb (index.html + assets/)"
