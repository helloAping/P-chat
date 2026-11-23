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
#   powershell -File wails-build.ps1 -Platform win -Ldflags "-X ...Version=... -X ...GitCommit=..."
#
# Exit code: propagates the wails build's exit code (0 on
# success, non-zero on failure). If wails is not found, prints
# the install hint and exits 1.

[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [Alias('PlatformName', 'Name')]
    [string]$Platform,
    [Parameter(Mandatory = $true)][string]$Ldflags
)

$ErrorActionPreference = 'Stop'

# Resolve wails: search the standard GOPATH/bin locations.
$wailsDir = & (Join-Path $PSScriptRoot 'find-wails.ps1')
if (-not $wailsDir) {
    Write-Error "wails CLI not found. Install with: go install github.com/wailsapp/wails/v2/cmd/wails@v2.12.0"
    exit 1
}

# Prepend the discovered dir to $PATH so the bare `wails`
# call below resolves correctly. This mutates the current process PATH.
$env:PATH = "$wailsDir$([System.IO.Path]::PathSeparator)$env:PATH"

& (Join-Path $PSScriptRoot 'assert-wails-version.ps1')
if ($LASTEXITCODE -ne 0) {
    exit $LASTEXITCODE
}

$wailsPlatform = & (Join-Path $PSScriptRoot 'resolve-platform.ps1') -Name $Platform -Format wails
if (-not $wailsPlatform) {
    exit 1
}

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

$wailsArgs = @('build', '-platform', $wailsPlatform, '-ldflags') + @($ldFlagArgs) + @('-s')
if ($wailsPlatform -like 'linux*') {
    # Ubuntu 24.04+ only ships webkit2gtk-4.1. Wails v2 uses 4.0 unless this tag is set.
    $has40 = $false
    $has41 = $false
    if (Get-Command pkg-config -ErrorAction SilentlyContinue) {
        & pkg-config --exists webkit2gtk-4.0
        if ($LASTEXITCODE -eq 0) { $has40 = $true }
        & pkg-config --exists webkit2gtk-4.1
        if ($LASTEXITCODE -eq 0) { $has41 = $true }
    }
    if (-not $has40 -and $has41) {
        $wailsArgs += @('-tags', 'webkit2_41')
    } elseif (-not $has40 -and -not $has41) {
        throw 'webkit2gtk development files not found. Install libwebkit2gtk-4.0-dev or libwebkit2gtk-4.1-dev.'
    }
}
& wails @wailsArgs
exit $LASTEXITCODE
