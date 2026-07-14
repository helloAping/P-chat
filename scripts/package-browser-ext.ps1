# scripts/package-browser-ext.ps1
#
# Packages the browser-extension/ directory into a ZIP file for user
# side-loading in Chrome.
#
# Usage:
#   task package:browser-ext                       # default → bin/
#   powershell ... -OutDir dev-bin                  # custom output directory
#   powershell ... -OutDir cmd\pchat-server         # for go:embed into binary

param(
    [string]$OutDir = 'bin'
)

$ErrorActionPreference = 'Stop'

$root = (Resolve-Path "$PSScriptRoot\..").Path
$src  = Join-Path $root 'browser-extension'
$bin  = Join-Path $root $OutDir
$dst  = Join-Path $bin 'browser-extension.zip'

if (!(Test-Path $src)) {
    Write-Error "browser-extension/ directory not found at $src"
    exit 1
}

if (!(Test-Path $bin)) {
    New-Item -ItemType Directory -Path $bin -Force | Out-Null
}

if (Test-Path $dst) {
    Remove-Item -Force $dst
}

Compress-Archive -Path (Join-Path $src '*') -DestinationPath $dst -Force

$size = (Get-Item $dst).Length
$sizeKB = [math]::Round($size / 1024, 1)
Write-Host "Browser extension packed: $dst ($sizeKB KB)"
