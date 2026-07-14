<#
.SYNOPSIS
  Run a Task task with platform gating and best-effort error handling.

.DESCRIPTION
  This wrapper lets `build:all` (or any aggregate task) try to build
  for platforms that may or may not be reachable from the current
  host, without the aggregate task failing when one platform can't
  be built.

  Behavior:
    - If -RequiredOS is set and the current OS does not match,
      print a yellow "[skip] ..." line and exit 0. The aggregate
      task continues.
    - If the wrapped task exits non-zero, print a yellow
      "best-effort failure" line with the exit code and a hint,
      then exit 0. The aggregate task continues.
    - If the wrapped task succeeds, print a green "[ok] ..." line
      and exit 0.

  Why a wrapper instead of Task's `ignore_error: true`:
    `ignore_error` only suppresses the error code; the aggregate
    still sees the task as failed because the command's
    $LASTEXITCODE is non-zero. We need the wrapper to *itself*
    exit 0 even when the inner task fails, so the Taskfile cmd
    sees a clean exit and the next cmd runs.

.PARAMETER RequiredOS
  One of: darwin, linux, windows. If the host is not this OS,
  the wrapper skips with a warning. Empty = no platform check.

.PARAMETER Task
  The Task task to invoke, e.g. "package:gui:linux".

.PARAMETER Platform
  Human-readable platform label (e.g. "linux/amd64",
  "darwin/universal") for the failure hint. Optional.

.EXAMPLE
  powershell -File run-platform-build.ps1 -Task "package:gui:linux" -Platform "linux/amd64"
  # Tries to package the Linux GUI. Skips on macOS host only if
  # -RequiredOS is also darwin. Best-effort on Windows.

.EXAMPLE
  powershell -File run-platform-build.ps1 -RequiredOS darwin -Task "package:gui:macos" -Platform "darwin/universal"
  # Skips on Windows/Linux hosts; on macOS runs the task.
#>

[CmdletBinding()]
param(
    [string]$RequiredOS = '',
    [Parameter(Mandatory = $true)][string]$Task,
    [string]$Platform = ''
)

$ErrorActionPreference = 'Continue'

# --- platform gate ---
$currentOS = switch ([System.Runtime.InteropServices.RuntimeInformation]::IsOSPlatform([System.Runtime.InteropServices.OSPlatform]::OSX)) {
    $true  { 'darwin' }
    default {
        if ([System.Runtime.InteropServices.RuntimeInformation]::IsOSPlatform([System.Runtime.InteropServices.OSPlatform]::Linux)) {
            'linux'
        } elseif ([System.Runtime.InteropServices.RuntimeInformation]::IsOSPlatform([System.Runtime.InteropServices.OSPlatform]::Windows)) {
            'windows'
        } else {
            'unknown'
        }
    }
}

if ($RequiredOS -and $currentOS -ne $RequiredOS) {
    Write-Host "[run-platform-build] skip: $Task requires $RequiredOS host (current: $currentOS)" -ForegroundColor Yellow
    exit 0
}

# --- invoke the task ---
# Resolve `task` from PATH. We don't pin a specific binary path so
# the wrapper works in dev shells, CI runners, and Task's own
# internal sub-processes alike.
$taskCmd = Get-Command task -ErrorAction SilentlyContinue
if (-not $taskCmd) {
    Write-Host "[run-platform-build] skip: 'task' (go-task) not on PATH; cannot run $Task" -ForegroundColor Yellow
    exit 0
}

Write-Host "[run-platform-build] run: $Task (host=$currentOS)" -ForegroundColor Cyan
& task $Task
$code = $LASTEXITCODE

if ($code -ne 0) {
    Write-Host "[run-platform-build] best-effort failure: $Task exited $code" -ForegroundColor Yellow
    if ($Platform) {
        Write-Host "[run-platform-build] hint: $Platform build typically needs a native host or a working cross-compile toolchain" -ForegroundColor Yellow
    }
    exit 0
}

Write-Host "[run-platform-build] ok: $Task" -ForegroundColor Green
exit 0
