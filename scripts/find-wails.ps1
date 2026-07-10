# Find Wails CLI: the user installed it once via
# `go install github.com/wailsapp/wails/v2/cmd/wails@latest`,
# which places the binary under $(go env GOPATH)/bin. That
# dir is not always in $PATH on Windows (PowerShell sessions
# and Task's shell don't inherit a go-injected PATH update).
# Search the standard locations and print the dir to prepend.
#
# Usage: powershell -File find-wails.ps1
# Stdout: the directory containing wails.exe, with one
# trailing newline. The caller captures it into a variable
# (e.g. `$dir = & find-wails.ps1`) and either prepends it
# to $PATH or fails fast with a hint pointing at the install
# command.
#
# Why we use `Write-Output` (not `[Console]::Out.Write`):
# `[Console]::Out` writes to the host directly, bypassing
# PowerShell's output stream. A caller that does
# `$dir = & script.ps1` would see `$dir = $null` because
# nothing was written to the stream — the bytes just went
# to the terminal. `Write-Output` (or a bare expression)
# is what enables the standard PowerShell "script returns
# a value to the caller" pattern. The trailing newline is
# harmless: `Join-Path` and `Test-Path` don't care about
# whitespace, and the caller's `if (-not $wailsDir)` check
# works either way (a single-line string is truthy).
#
# Why a script and not a Taskfile inline lookup: Task's
# `sh:` resolver doesn't expose `go env GOPATH` cleanly
# across Windows / WSL / CI runners, and the user has
# historically moved GOPATH between machines. A dedicated
# script gives one place to teach the dev how to install
# wails and a single test surface.

$ErrorActionPreference = 'Stop'

# 1. Try `Get-Command wails` first — if the user already
#    added wails to $PATH, this is the most reliable
#    detection. The output is the full path; we hand back
#    the directory portion.
try {
    $wailsPath = (Get-Command wails -ErrorAction SilentlyContinue).Source
    if ($wailsPath -and (Test-Path $wailsPath)) {
        $dir = [System.IO.Path]::GetDirectoryName($wailsPath)
        Write-Output $dir
        exit 0
    }
} catch {}

# 2. Candidate directories, in priority order. Each entry
#    is a fully-qualified path to a directory that should
#    contain wails.exe.
$candidates = @(
    # `go env GOPATH` may not be available (e.g. when the
    # task runner uses a different Go install). Wrap in
    # try/catch so the script still tries the other
    # candidates.
    (try { (go env GOPATH 2>$null) + '\bin' } catch { $null })
    # 3. %GOBIN% if explicitly set
    $env:GOBIN
    # 4. %USERPROFILE%\go\bin (default GOPATH for the
    #    go-windows MSI installer)
    (Join-Path $env:USERPROFILE 'go\bin')
    # 5. C:\go\bin (the default the go-windows installer
    #    uses when the user doesn't override GOPATH)
    'C:\go\bin'
    # 6. Common Go installation directories. Some users
    #    install go to non-standard locations; we try a
    #    few popular ones. Each is a guess, not a
    #    guarantee, but they cover the dev-box patterns
    #    we've seen.
    'D:\go\bin'
    'D:\develop\Go\bin'
    'D:\develop\golang\bin'
) | Where-Object { $_ -and (Test-Path $_) } | Select-Object -Unique

foreach ($dir in $candidates) {
    $exe = Join-Path $dir 'wails.exe'
    if (Test-Path $exe) {
        Write-Output $dir
        exit 0
    }
}

# Not found. Stdout empty (we wrote nothing). Caller
# should print a hint.
exit 1
