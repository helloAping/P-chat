# Thin wrapper around `wails dev` and `wails build` that handles
# the PATH resolution (see scripts/find-wails.ps1 for the
# rationale). Used by the Taskfile's `gui` (wails dev) and
# `build:dev:gui` (wails build -debug) tasks.
#
# Args:
#   $args[0] - subcommand, either "dev" or "build-debug"
#
# Exit code: propagates wails's exit code.

$ErrorActionPreference = 'Stop'

if ($args.Count -ne 1) {
    Write-Error "Usage: wails-dev.ps1 {dev|build-debug}"
    exit 1
}

$sub = $args[0]
if ($sub -ne 'dev' -and $sub -ne 'build-debug') {
    Write-Error "Unknown subcommand '$sub'. Use 'dev' or 'build-debug'."
    exit 1
}

$wailsDir = & "$PSScriptRoot\find-wails.ps1"
if (-not $wailsDir) {
    Write-Error "wails CLI not found. Install with: go install github.com/wailsapp/wails/v2/cmd/wails@latest"
    exit 1
}
$env:PATH = "$wailsDir;$env:PATH"

switch ($sub) {
    'dev'          { & wails dev; exit $LASTEXITCODE }
    'build-debug'  { & wails build -debug -s; exit $LASTEXITCODE }
}
