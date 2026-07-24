<#
.SYNOPSIS
  Install the project-local git pre-commit hook.

.DESCRIPTION
  Wires .git/hooks/pre-commit to scripts/git-hooks/pre-commit. On
  Windows the symlink path requires either Developer Mode OR admin
  rights; this script falls back to copying the file when symlink
  creation fails (matching the .agents/install.ps1 fallback pattern).

  The hook is intentionally NOT executable bit-driven on Windows
  (Windows doesn't honour the +x bit), but `git` will still invoke
  the file via bash from the shebang, so it works either way.

.PARAMETER Force
  Overwrite an existing pre-commit hook without prompting.

.EXAMPLE
  .\scripts\git-hooks\install.ps1
  .\scripts\git-hooks\install.ps1 -Force
#>

param(
  [switch]$Force
)

$ErrorActionPreference = 'Stop'

$repoRoot = (& git rev-parse --show-toplevel 2>$null)
if (-not $repoRoot) {
  Write-Error '[install] not a git repository. Run from inside the P-Chat checkout.'
  exit 2
}
Set-Location -LiteralPath $repoRoot

$hookSrc   = Join-Path $repoRoot 'scripts/git-hooks/pre-commit'
$gitDir    = (& git rev-parse --git-dir)
if (-not [IO.Path]::IsPathRooted($gitDir)) { $gitDir = Join-Path $repoRoot $gitDir }
$hooksDir  = Join-Path $gitDir  'hooks'
$hookDst   = Join-Path $hooksDir 'pre-commit'

if (-not (Test-Path -LiteralPath $hookSrc)) {
  Write-Error "[install] source hook not found: $hookSrc"
  exit 2
}

if (-not (Test-Path -LiteralPath $hooksDir)) {
  New-Item -ItemType Directory -Force -Path $hooksDir | Out-Null
}

if ((Test-Path -LiteralPath $hookDst) -and -not $Force) {
  Write-Host "[install] hook already exists at $hookDst"
  $answer = Read-Host "[install] overwrite? [y/N]"
  if ($answer -ne 'y' -and $answer -ne 'Y') {
    Write-Host '[install] aborted; existing hook left in place.'
    exit 0
  }
}

# Try symlink first (preserves the "single source of truth" in
# scripts/git-hooks/pre-commit); fall back to copy on permission error.
$symlinked = $false
try {
  if (Test-Path -LiteralPath $hookDst) { Remove-Item -LiteralPath $hookDst -Force }
  # New-Item -ItemType SymbolicLink requires Developer Mode or admin on
  # Windows; if it fails, the catch falls through to the copy path.
  New-Item -ItemType SymbolicLink -Path $hookDst -Target $hookSrc -Force | Out-Null
  $symlinked = $true
} catch {
  # Fall through to copy path.
  $symlinked = $false
}

if (-not $symlinked) {
  Copy-Item -LiteralPath $hookSrc -Destination $hookDst -Force
  Write-Host "[install] NOTE: could not create symlink; copied instead."
  Write-Host "[install]       edits to scripts/git-hooks/pre-commit won't auto-propagate;"
  Write-Host "[install]       re-run this script (or copy manually) to pick up changes."
}

# Make the bash hook executable on Unix. On Windows this is a no-op
# (the bit isn't honoured) but we run it anyway so the file is
# correctly permissioned on macOS / Linux dev boxes.
if ($IsLinux -or $IsMacOS) {
  & chmod +x $hookDst
  & chmod +x (Join-Path (Split-Path $hookSrc) '../check-bom.sh')
}

Write-Host "[install] pre-commit hook installed at $hookDst"
Write-Host "[install] bypass any time with:  git commit --no-verify"
