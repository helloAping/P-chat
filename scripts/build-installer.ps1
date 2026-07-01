<#
.SYNOPSIS
  Assembles the pchat-setup.exe installer.

.DESCRIPTION
  1. Copies fresh pchat.exe / pchat-server.exe / pchat-gui.exe into
     cmd/pchat-installer/assets/ (binaries).
  2. Copies web/ + install.ps1 + uninstall.ps1 into assets/ (data).
  3. Runs `go build -o bin/pchat-setup.exe ./cmd/pchat-installer`.

  Prerequisites: `task build` and `task build:gui` must have run
  first so bin/pchat.exe / bin/pchat-server.exe / bin/pchat-gui.exe
  exist. The script verifies all three before proceeding.
#>

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot

# Read version from VERSION file (single source of truth).
$versionFile = Join-Path $root "VERSION"
$version  = "0.1.0"
if (Test-Path -LiteralPath $versionFile) {
    $v = (Get-Content -LiteralPath $versionFile -Raw).Trim()
    if ($v) { $version = $v }
}

$exeName = "pchat-setup-v$version.exe"

$bin       = Join-Path $root "bin"
$assets    = Join-Path $root "cmd\pchat-installer\assets"

$guiExe    = Join-Path $bin "pchat-gui.exe"
$serverExe = Join-Path $bin "pchat-server.exe"
$cliExe    = Join-Path $bin "pchat.exe"

$webDir    = Join-Path $root "web"
$installPs = Join-Path $root "cmd\pchat-gui\install.ps1"
$uninstPs  = Join-Path $root "cmd\pchat-gui\uninstall.ps1"

# --- validation ---
foreach ($f in @($guiExe, $serverExe, $cliExe)) {
    if (-not (Test-Path -LiteralPath $f)) {
        Write-Error "Missing binary: $f -- run 'task build && task build:gui' first"
        exit 1
    }
}

# --- copy assets ---
Write-Host "[build-installer] Copy binaries -> $assets"
Copy-Item -LiteralPath $guiExe    -Destination "$assets\pchat-gui.exe"    -Force
Copy-Item -LiteralPath $serverExe -Destination "$assets\pchat-server.exe" -Force
Copy-Item -LiteralPath $cliExe    -Destination "$assets\pchat.exe"        -Force

Write-Host "[build-installer] Copy web/ -> $assets\web"
if (Test-Path -LiteralPath "$assets\web") { Remove-Item -Recurse -Force "$assets\web" }
Copy-Item -Path "$webDir" -Destination "$assets\web" -Recurse -Force

Write-Host "[build-installer] Copy install scripts"
Copy-Item -LiteralPath $installPs -Destination "$assets\install.ps1" -Force
Copy-Item -LiteralPath $uninstPs  -Destination "$assets\uninstall.ps1" -Force

# --- build ---
Write-Host "[build-installer] go build -> $exeName"
$outPath = Join-Path $bin $exeName
go build -o $outPath "$root\cmd\pchat-installer"

Write-Host "[build-installer] Done: $outPath"
