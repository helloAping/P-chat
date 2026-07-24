<#
.SYNOPSIS
  Lint: fail if any source file starts with a UTF-8 BOM (UTF-8 BOM = EF BB BF).

.DESCRIPTION
  `go test -cover` (and a few other Go-toolchain paths) reject files that
  start with a UTF-8 BOM, while `go build` tolerates them. That asymmetry
  means a BOM can sneak in via a Windows editor and only surface in
  coverage / CI runs. This script catches it at lint time on Windows,
  where developers usually don't have a working bash.

  Companion to `check-bom.sh`. Both call the same underlying check;
  this one is pure PowerShell so it works in cmd / PowerShell / VS Code
  terminals without WSL.

.PARAMETER Staged
  Only scan files staged for commit (default: all tracked + untracked).

.EXAMPLE
  .\scripts\check-bom.ps1
  .\scripts\check-bom.ps1 -Staged
#>

param(
  [switch]$Staged
)

$ErrorActionPreference = 'Stop'

# Extensions we lint. `.md` is included because some Windows editors add
# BOMs to Markdown too, which breaks piping into `grep` / `awk` and (less
# importantly) inflates file size.
$extensions = @(
  '*.go','*.ts','*.tsx','*.js','*.jsx','*.vue',
  '*.css','*.scss','*.html','*.json','*.yml','*.yaml','*.toml',
  '*.sh','*.ps1','*.bat','*.cmd','*.md'
)

# Locate the repo root once. We need it for `git ls-files` so the script
# behaves correctly when run from a subdirectory.
$repoRoot = (& git rev-parse --show-toplevel 2>$null)
if (-not $repoRoot) {
  Write-Error '[check-bom] FAIL: not a git repository (no `git rev-parse`).'
  exit 2
}
Set-Location -LiteralPath $repoRoot

# Build the file list. For `--Staged` we use `git diff --cached
# --name-only -z` because `git ls-files --cached` always returns the
# full tracked set, not the diff against HEAD. `--diff-filter=ACMR`
# skips deleted entries. NUL-delimited so paths with spaces don't get
# torn apart by `git`'s own column-aligned output.
if ($Staged) {
  $fileList = (& git diff --cached --name-only --diff-filter=ACMR -z) -split "`0"
} else {
  $fileList = (& git ls-files -z) -split "`0"
}

$untracked = @()
if (-not $Staged) {
  $untracked = (& git ls-files --others --exclude-standard -z) -split "`0"
}

$candidates = @($fileList + $untracked) |
  Where-Object { $_ -ne '' } |
  Where-Object {
    foreach ($ext in $extensions) {
      if ($_ -like $ext) { return $true }
    }
    return $false
  }

$fail = 0
$count = 0
foreach ($f in $candidates) {
  # Read first 3 bytes; if they match EF BB BF, flag the file.
  $path = Join-Path $repoRoot $f
  if (-not (Test-Path -LiteralPath $path)) { continue }
  $bytes = [IO.File]::ReadAllBytes($path)
  if ($bytes.Length -ge 3 -and $bytes[0] -eq 0xEF -and $bytes[1] -eq 0xBB -and $bytes[2] -eq 0xBF) {
    Write-Host "  BOM: $f"
    $fail = 1
    $count++
  }
}

if ($fail -ne 0) {
  Write-Host ""
  Write-Host "[check-bom] FAIL: $count file(s) start with a UTF-8 BOM." -ForegroundColor Red
  Write-Host "[check-bom] Strip with:  .\scripts\strip-bom.ps1 -Path <file>"
  exit 1
}

Write-Host "[check-bom] OK: no BOMs found." -ForegroundColor Green
exit 0
