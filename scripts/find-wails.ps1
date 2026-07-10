# Find Wails CLI: the user installed it once via `go install
# github.com/wailsapp/wails/v2/cmd/wails@latest`, which places the
# binary under $(go env GOPATH)/bin. That dir is not always in
# $PATH on Windows (PowerShell sessions and Task's shell don't
# inherit a go-injected PATH update). Search the standard
# locations and print the dir to prepend.
#
# Usage: powershell -File find-wails.ps1
# Stdout: the directory containing wails.exe (no trailing
# newline), or empty if not found. The caller checks for empty
# stdout and either adds the dir to PATH or fails fast with a
# hint pointing at the install command.
#
# Why a script and not a Taskfile inline lookup: Task's
# `sh:` resolver doesn't expose `go env GOPATH` cleanly across
# Windows / WSL / CI runners, and the user has historically
# moved GOPATH between machines. A dedicated script gives one
# place to teach the dev how to install wails and a single
# test surface.

$ErrorActionPreference = 'Stop'

# Candidate locations, in priority order. Each entry is a
# fully-qualified path to a directory that should contain
# wails.exe. We check them in order; first match wins.
$candidates = @(
    # 1. go env GOPATH/bin (most common)
    (Join-Path (go env GOPATH 2>$null) 'bin')
    # 2. %GOBIN% if explicitly set
    $env:GOBIN
    # 3. %USERPROFILE%\go\bin (default GOPATH for the
    #    go-windows MSI installer)
    (Join-Path $env:USERPROFILE 'go\bin')
    # 4. C:\go\bin (the default the go-windows installer
    #    uses when the user doesn't override GOPATH)
    'C:\go\bin'
) | Where-Object { $_ -and (Test-Path $_) }

foreach ($dir in $candidates) {
    $exe = Join-Path $dir 'wails.exe'
    if (Test-Path $exe) {
        # Stdout: just the dir, no trailing newline. The
        # caller `set`s PATH to `${dir};${env:PATH}`.
        [Console]::Out.Write($dir)
        exit 0
    }
}

# Not found. Stdout empty. Caller should print a hint.
exit 1
