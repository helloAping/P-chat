# scripts/package-browser-ext.ps1
#
# Packages the browser-extension/ directory into a ZIP file for user
# side-loading in Chrome.
#
# Implementation note — we deliberately use
# [System.IO.Compression.ZipFile]::CreateFromDirectory() instead of
# `Compress-Archive`. The cmdlet hits intermittent "file is being used
# by another process" failures on Windows when:
#   - a previous build left the destination zip open (PowerShell's
#     FileStream handle lingers a few ms after `Compress-Archive`
#     returns),
#   - AV/file-indexer opens the freshly written file mid-build,
#   - explorer.exe keeps a thumbnail lock on the dir.
# The .NET API is atomic (open → write → close in one syscall) and
# far more reliable for build-time invocation. We've kept the
# Compress-Archive call behind a try/catch + retry as a fallback
# for the rare case the .NET path also trips a transient lock.
#
# Usage:
#   task package:browser-ext                       # default → bin/
#   powershell ... -OutDir dev-bin                  # custom output directory
#   powershell ... -OutDir cmd\pchat-server         # for go:embed into binary

param(
    [string]$OutDir = 'bin'
)

$ErrorActionPreference = 'Stop'
[System.Reflection.Assembly]::LoadWithPartialName('System.IO.Compression.FileSystem') | Out-Null
Add-Type -AssemblyName System.IO.Compression.FileSystem

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
    # Best-effort delete. If a previous process still holds the
    # handle, fall through to the retry loop below.
    try { Remove-Item -LiteralPath $dst -Force -ErrorAction Stop }
    catch { Start-Sleep -Milliseconds 250 }
}

# Primary path: .NET ZipFile (atomic, no PowerShell file-stream
# handle leaking).
$created = $false
$maxAttempts = 6
for ($i = 1; $i -le $maxAttempts; $i++) {
    try {
        [System.IO.Compression.ZipFile]::CreateFromDirectory(
            $src, $dst,
            [System.IO.Compression.CompressionLevel]::Optimal,
            $false
        )
        $created = $true
        break
    } catch {
        if ($i -eq $maxAttempts) {
            Write-Warning "[package-browser-ext] .NET ZipFile failed $maxAttempts times, falling back to Compress-Archive"
            break
        }
        Start-Sleep -Milliseconds (200 * $i)
    }
}

# Fallback: Compress-Archive (rarely needed, kept for symmetry).
if (-not $created) {
    for ($i = 1; $i -le 3; $i++) {
        try {
            if (Test-Path $dst) { Remove-Item -LiteralPath $dst -Force -ErrorAction Stop }
            Compress-Archive -Path (Join-Path $src '*') -DestinationPath $dst -Force -ErrorAction Stop
            $created = $true
            break
        } catch {
            if ($i -eq 3) { throw }
            Start-Sleep -Milliseconds (300 * $i)
        }
    }
}

$size = (Get-Item $dst).Length
$sizeKB = [math]::Round($size / 1024, 1)
Write-Host "Browser extension packed: $dst ($sizeKB KB)"
