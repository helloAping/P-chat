# scripts/package-gui-linux.ps1
#
# Assembles the Linux GUI install bundle under build/bin/.
# Run via `task package:gui:linux` after `build:server:linux`,
# `build:cli:linux`, `build:frontend`, and (best-effort)
# `build:gui:linux` have populated the inputs.
#
# The Wails GUI build (build:gui:linux) is best-effort — Wails
# v2 does not support Windows→Linux cross-compile, so when
# running from Windows the .app/.exe for pchat-gui may be
# absent. We still ship the rest of the bundle (server, CLI,
# SPA, install scripts) so a Linux user can drop in a GUI
# binary built on a Linux host later.
#
# Why a dedicated script: the equivalent inline PowerShell
# in Taskfile.yml trips on the `:` in the warning message
# (Task's YAML key-value parser interprets "WARNING:" as a
# mapping key). Putting the logic in a script sidesteps the
# YAML quoting problem entirely.

$ErrorActionPreference = 'Stop'
$root = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '..')).Path
$binOut = Join-Path $root 'build\bin'

# Create the output dir first. Wails normally creates
# build/bin/ as a side effect, but if its build was skipped or
# failed (e.g. Windows→Linux cross-compile unsupported), the
# dir is missing and Copy-Item would "directory not found".
New-Item -ItemType Directory -Path $binOut -Force | Out-Null

function Copy-IfExists {
    param([string]$Src, [string]$Dst, [string]$Label)
    if (Test-Path -LiteralPath $Src) {
        Copy-Item -LiteralPath $Src -Destination $Dst -Force
        Write-Host ("[package-gui-linux] {0} copied" -f $Label) -ForegroundColor Green
    } else {
        Write-Host ("[package-gui-linux] {0} NOT found at {1} (skipped)" -f $Label, $Src) -ForegroundColor Yellow
    }
}

Write-Host '[package-gui-linux] assembling Linux bundle' -ForegroundColor Cyan

# --- Server binary (mandatory) ---
$serverSrc = Join-Path $root 'bin\pchat-server-linux'
if (-not (Test-Path -LiteralPath $serverSrc)) {
    throw "Linux server binary not found at $serverSrc -- run 'task build:server:linux' first"
}
Copy-Item -LiteralPath $serverSrc -Destination (Join-Path $binOut 'pchat-server') -Force
Write-Host '[package-gui-linux] pchat-server copied' -ForegroundColor Green

# --- CLI binary (optional) ---
Copy-IfExists -Src (Join-Path $root 'bin\pchat-linux') -Dst (Join-Path $binOut 'pchat') -Label 'pchat (CLI)'

# --- SPA web/ ---
$webSrc = Join-Path $root 'web'
$webDst = Join-Path $binOut 'web'
if (Test-Path -LiteralPath $webSrc) {
    if (Test-Path -LiteralPath $webDst) {
        Get-ChildItem -LiteralPath $webDst -Force | Remove-Item -Recurse -Force
    } else {
        New-Item -ItemType Directory -Path $webDst -Force | Out-Null
    }
    Copy-Item -Path (Join-Path $webSrc '*') -Destination $webDst -Recurse -Force
    Write-Host '[package-gui-linux] web/ copied' -ForegroundColor Green
} else {
    Write-Host "[package-gui-linux] WARNING - web/ not found at $webSrc (skipped)" -ForegroundColor Yellow
}

# --- Browser extension zip ---
Copy-IfExists -Src (Join-Path $root 'cmd\pchat-server\browser-extension.zip') -Dst (Join-Path $binOut 'browser-extension.zip') -Label 'browser-extension.zip'

# --- Install/uninstall scripts ---
Copy-Item -LiteralPath (Join-Path $root 'scripts\install-linux.sh')   -Destination (Join-Path $binOut 'install.sh')   -Force
Copy-Item -LiteralPath (Join-Path $root 'scripts\uninstall-linux.sh') -Destination (Join-Path $binOut 'uninstall.sh') -Force
Write-Host '[package-gui-linux] install.sh / uninstall.sh copied' -ForegroundColor Green

# --- GUI binary (best-effort) ---
$guiSrc = Join-Path $root 'cmd\pchat-gui\build\bin\pchat-gui'
if (Test-Path -LiteralPath $guiSrc) {
    Copy-Item -LiteralPath $guiSrc -Destination (Join-Path $binOut 'pchat-gui') -Force
    Write-Host '[package-gui-linux] pchat-gui copied' -ForegroundColor Green
} else {
    Write-Host ''
    Write-Host '[package-gui-linux] WARNING - pchat-gui missing (Wails build:gui:linux failed).' -ForegroundColor Yellow
    Write-Host '                    Bundle is server+CLI+SPA+scripts only.' -ForegroundColor Yellow
    Write-Host '                    Build pchat-gui on a Linux host or via a Linux CI runner' -ForegroundColor Yellow
    Write-Host '                    and drop it into build/bin/pchat-gui before installing.' -ForegroundColor Yellow
    Write-Host ''
}

# --- Final summary ---
Write-Host ''
Write-Host '[package-gui-linux] bundle at build\bin\' -ForegroundColor Cyan
Get-ChildItem -LiteralPath $binOut -Force |
    Sort-Object Name |
    ForEach-Object {
        $kind = if ($_.PSIsContainer) { 'dir ' } else { 'file' }
        $size = if ($_.PSIsContainer) { '' } else { ('{0,10:N0} B' -f $_.Length) }
        Write-Host ('  [{0}] {1,-30}  {2}' -f $kind, $_.Name, $size)
    }
