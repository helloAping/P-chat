<#
.SYNOPSIS
  Remove the project-local git pre-commit hook.

.DESCRIPTION
  Inverse of install.ps1. Safely removes .git/hooks/pre-commit.
  Idempotent — running on a clean tree is a no-op.

.EXAMPLE
  .\scripts\git-hooks\uninstall.ps1
#>

param()

$ErrorActionPreference = 'Stop'

$repoRoot = (& git rev-parse --show-toplevel 2>$null)
if (-not $repoRoot) {
  Write-Error '[uninstall] not a git repository.'
  exit 2
}
Set-Location -LiteralPath $repoRoot

$gitDir   = (& git rev-parse --git-dir)
if (-not [IO.Path]::IsPathRooted($gitDir)) { $gitDir = Join-Path $repoRoot $gitDir }
$hookDst  = Join-Path $gitDir 'hooks/pre-commit'

if (-not (Test-Path -LiteralPath $hookDst)) {
  Write-Host '[uninstall] no hook installed; nothing to do.'
  exit 0
}

Remove-Item -LiteralPath $hookDst -Force
Write-Host "[uninstall] removed $hookDst"
