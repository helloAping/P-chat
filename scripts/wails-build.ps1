# Wrapper for `wails build` that handles the PATH dance
# (find-wails.ps1 locates the wails binary, we prepend it to
# $PATH so the actual wails invocation works regardless of the
# caller's environment).
#
# Why a wrapper script and not inline PowerShell in the
# Taskfile: the inline form was a 200+ char string with many
# embedded `: ` and `"` characters that broke YAML's plain-
# scalar parser ("mapping values are not allowed in this
# context" at the first `: ` inside the string). A dedicated
# script with normal command-line arguments is much easier to
# read and edit, and the Taskfile stays a thin glue layer.
#
# Usage:
#   powershell -File wails-build.ps1 -Platform windows/amd64 -Ldflags "-X ...Version=... -X ...GitCommit=..."
#
# Exit code: propagates the wails build's exit code (0 on
# success, non-zero on failure). If wails is not found, prints
# the install hint and exits 1.

[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$Platform,
    [Parameter(Mandatory = $true)][string]$Ldflags
)

$ErrorActionPreference = 'Stop'

# Resolve wails: search the standard GOPATH/bin locations.
$wailsDir = & "$PSScriptRoot\find-wails.ps1"
if (-not $wailsDir) {
    Write-Error "wails CLI not found. Install with: go install github.com/wailsapp/wails/v2/cmd/wails@latest"
    exit 1
}

# Prepend the discovered dir to $PATH so the bare `wails`
# call below resolves correctly. This mutates the *current
# process*'s PATH, which is what the spawned `wails build`
# inherits on Windows.
$env:PATH = "$wailsDir;$env:PATH"

# Split the ldflags string on `-X ` boundaries so each `-X`
# flag is its own positional argument. The string is built
# up by the Taskfile from `{{.VERSION}}` / `{{.GIT_COMMIT}}`
# substitutions and arrives here as a single token.
# PowerShell's argument parser doesn't split on whitespace,
# so without this, wails would see a single
# "-X ...Version=... -X ...GitCommit=..." argument and
# reject it.
$ldFlagArgs = @()
$pieces = $Ldflags -split ' -X '
for ($i = 0; $i -lt $pieces.Count; $i++) {
    $p = $pieces[$i]
    if ($i -eq 0 -and $p -notmatch '^-X ') {
        # First piece didn't start with -X (e.g. it was an
        # empty split result). Skip.
        continue
    }
    if ($p -match '^-X ') {
        $ldFlagArgs += , $p
    } elseif ($ldFlagArgs.Count -gt 0) {
        # Continuation of the previous -X flag (the value
        # contained a literal "-X " in the middle). Stitch
        # back together.
        $ldFlagArgs[-1] = $ldFlagArgs[-1] + " -X " + $p
    }
}
if ($ldFlagArgs.Count -eq 0) {
    # Fallback: pass the whole string as a single -ldflags
    # value. This is the only way wails will accept it
    # without us splitting correctly.
    $ldFlagArgs = @($Ldflags)
}

& wails build -platform $Platform -ldflags $ldFlagArgs -s
exit $LASTEXITCODE
