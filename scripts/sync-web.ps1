# sync-web.ps1 — copy repo-root web/ into cmd/pchat-server/web/ so that
# `//go:embed all:web` in cmd/pchat-server/main.go can find a real
# directory. Go's embed package refuses to follow reparse points
# (junctions / symlinks), so we have to do a physical copy. Run this
# before `go build` / `wails build` whenever web/index.html changes.

$ErrorActionPreference = "Stop"

$root = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot "..")).Path
$src  = Join-Path $root "web"
$dst  = Join-Path $root "cmd\pchat-server\web"

if (-not (Test-Path -LiteralPath $src)) { throw "source not found: $src" }

# Wipe and re-create the destination so removed files don't linger.
if (Test-Path -LiteralPath $dst) {
    Get-ChildItem -LiteralPath $dst -Recurse -Force | Remove-Item -Recurse -Force
} else {
    New-Item -ItemType Directory -Path $dst -Force | Out-Null
}

# robocopy returns non-zero exit codes even on success (1=files copied,
# 2=extras, 3=both). We only treat >=8 as a real error.
$robocopyLog = Join-Path $env:TEMP "pchat-robocopy-$(Get-Random).log"
cmd.exe /c "robocopy `"$src`" `"$dst`" /MIR /NJH /NJS /NDL /NFL /NP > `"$robocopyLog`" 2>&1"
$rc = $LASTEXITCODE
if ($rc -ge 8) {
    Get-Content -LiteralPath $robocopyLog -ErrorAction SilentlyContinue
    throw "robocopy failed (exit $rc)"
}
Remove-Item -LiteralPath $robocopyLog -ErrorAction SilentlyContinue

Write-Host "[sync-web] $src -> $dst"
