param(
    [string]$OutDir = 'bin'
)

# sync-browser-ext.ps1 — package browser-extension/ into a ZIP and copy it to
# cmd/pchat-server/browser-extension.zip so that go:embed can include it in the
# binary. Also copies to the output directory (bin/ or dev-bin/) for manual
# download.

$ErrorActionPreference = "Stop"

$root = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot "..")).Path

# Step 1: Package into cmd/pchat-server/ for go:embed
& "$PSScriptRoot\package-browser-ext.ps1" -OutDir 'cmd\pchat-server'

# Step 2: Copy to output directory for manual download
$src = Join-Path $root 'cmd\pchat-server\browser-extension.zip'
$dst = Join-Path $root "$OutDir\browser-extension.zip"

if (-not (Test-Path -LiteralPath $src)) {
    throw "source zip not found: $src"
}

# Skip copy if source and destination are the same (e.g. OutDir='cmd\pchat-server')
$srcFull = (Resolve-Path -LiteralPath $src).Path
if (Test-Path -LiteralPath (Split-Path $dst -Parent)) {
    $dstDir = (Resolve-Path -LiteralPath (Split-Path $dst -Parent)).Path
    $dstFull = Join-Path $dstDir (Split-Path $dst -Leaf)
    if ($srcFull -eq $dstFull) {
        Write-Host "[sync-browser-ext] browser-extension.zip already in $OutDir/"
        exit 0
    }
}

if (-not (Test-Path -LiteralPath (Split-Path $dst -Parent))) {
    New-Item -ItemType Directory -Path (Split-Path $dst -Parent) -Force | Out-Null
}

Copy-Item -LiteralPath $src -Destination $dst -Force

Write-Host "[sync-browser-ext] browser-extension.zip → $OutDir/"
