#requires -Version 5.1
<#
.SYNOPSIS
    Install agent tool symlinks pointing to .agents/AGENTS.md.

.DESCRIPTION
    Creates the following symlinks so that opencode / codex / claude
    all read the same canonical agent instructions:

        .opencode/AGENTS.md  -> ./.agents/AGENTS.md
        .codex/AGENTS.md     -> ./.agents/AGENTS.md
        .claude/CLAUDE.md    -> ./.agents/AGENTS.md

    On Windows, symlinks require either administrator privileges
    or Developer Mode. If the symlink creation fails, this script
    falls back to copying the file (no longer a symlink — the
    user will need to re-run this script to pick up future edits).

.PARAMETER Force
    Replace existing files / symlinks at the target paths.

.PARAMETER DryRun
    Print what would be done without making any changes.

.EXAMPLE
    powershell -NoProfile -ExecutionPolicy Bypass -File .agents\scripts\install.ps1
    # Idempotent install. Run from the repo root.

.EXAMPLE
    powershell -NoProfile -ExecutionPolicy Bypass -File .agents\scripts\install.ps1 -Force -DryRun
    # Show what install -Force would do, without doing it.
#>

[CmdletBinding()]
param(
    [switch]$Force,
    [switch]$DryRun
)

$ErrorActionPreference = 'Stop'
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$RepoRoot  = Resolve-Path (Join-Path $ScriptDir '..\..')
$Canonical = Join-Path $RepoRoot '.agents\AGENTS.md'

# Validate the canonical file exists.
if (-not (Test-Path -LiteralPath $Canonical)) {
    Write-Error "Canonical file not found: $Canonical"
    exit 1
}

# Each tool's expected location -> relative target inside the repo.
$Links = @(
    @{ Path = '.opencode\AGENTS.md';  Target = '.agents\AGENTS.md' }
    @{ Path = '.codex\AGENTS.md';     Target = '.agents\AGENTS.md' }
    @{ Path = '.claude\CLAUDE.md';    Target = '.agents\AGENTS.md' }
)

function New-OrUpdate-Link {
    param(
        [string]$LinkPath,
        [string]$Target
    )

    $absLinkPath = Join-Path $RepoRoot $LinkPath
    $linkDir     = Split-Path -Parent $absLinkPath
    # All tool symlinks in this project sit at the repo root one
    # level deep (.opencode/, .codex/, .claude/), so the relative
    # path from each link's parent directory back to the canonical
    # is always "../.agents/AGENTS.md". We don't use
    # [System.IO.Path]::GetRelativePath() because it isn't
    # available on PowerShell 5.1 (Desktop Edition) — it's a
    # .NET Core / .NET 5+ API.
    $relTarget = '../.agents/AGENTS.md'

    if ($DryRun) {
        Write-Host "  [dry-run] $LinkPath  ->  $relTarget"
        return
    }

    # Ensure the parent directory exists.
    if (-not (Test-Path -LiteralPath $linkDir)) {
        New-Item -ItemType Directory -Path $linkDir -Force | Out-Null
    }

    # If the link already exists, decide what to do.
    if (Test-Path -LiteralPath $absLinkPath) {
        $item = Get-Item -LiteralPath $absLinkPath -Force
        if ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) {
            # Already a symlink. Check the target.
            $existingTarget = $item.Target
            if ($existingTarget -eq $relTarget) {
                Write-Host "  [ok]      $LinkPath (already points to $relTarget)"
                return
            }
            if ($Force) {
                Remove-Item -LiteralPath $absLinkPath -Force
            } else {
                Write-Host "  [skip]    $LinkPath already exists (target=$existingTarget, want=$relTarget). Use -Force to replace."
                return
            }
        } else {
            # A real file or directory. With -Force, replace it.
            if ($Force) {
                Remove-Item -LiteralPath $absLinkPath -Recurse -Force
            } else {
                Write-Host "  [skip]    $LinkPath is a real file/dir. Use -Force to replace."
                return
            }
        }
    }

    # Try symlink first. On Windows, this requires either admin
    # privileges or Developer Mode to be enabled. If it fails,
    # fall back to a plain copy.
    try {
        $output = & cmd /c "mklink `"$LinkPath`" `"$relTarget`"" 2>&1
        if ($LASTEXITCODE -eq 0) {
            Write-Host "  [linked]  $LinkPath  ->  $relTarget"
            return
        }
        $errCode = $LASTEXITCODE
        throw "mklink exit ${errCode}: $output"
    } catch {
        Write-Warning "Symlink failed for $LinkPath — falling back to copy. (Enable Developer Mode or run as admin for symlinks.)"
        try {
            # mklink with a relative target creates a symlink; if
            # it failed (e.g. permission denied), copy the file.
            # Note: we always copy the file CONTENTS so the target
            # is identical, not a symlink to the canonical.
            $canonicalContents = Get-Content -LiteralPath $Canonical -Raw
            Set-Content -LiteralPath $absLinkPath -Value $canonicalContents -NoNewline
            Write-Host "  [copied]  $LinkPath  <-  $Canonical  (no symlink: $(($_.Exception.Message -split "`n")[0]))"
        } catch {
            Write-Error "Failed to create $LinkPath : $_"
        }
    }
}

Write-Host ""
Write-Host "Installing agent tool symlinks (canonical: $Canonical)"
Write-Host "Repo root: $RepoRoot"
Write-Host ""

foreach ($l in $Links) {
    New-OrUpdate-Link -LinkPath $l.Path -Target $l.Target
}

# If the root AGENTS.md is still the project-init stub
# (the old `176 bytes` template), replace it with a symlink
# so all paths lead to the same canonical file.
$rootAgents = Join-Path $RepoRoot 'AGENTS.md'
if (Test-Path -LiteralPath $rootAgents) {
    $rootItem = Get-Item -LiteralPath $rootAgents -Force
    if (-not ($rootItem.Attributes -band [IO.FileAttributes]::ReparsePoint)) {
        $stub = $false
        try {
            $contents = Get-Content -LiteralPath $rootAgents -Raw -ErrorAction SilentlyContinue
            if ($contents -match '在此描述你的项目') { $stub = $true }
        } catch {}
        if ($stub) {
            $relRoot = '../.agents/AGENTS.md'
            if ($DryRun) {
                Write-Host "  [dry-run] AGENTS.md (root)  ->  $relRoot  (would replace stub)"
            } else {
                try {
                    Remove-Item -LiteralPath $rootAgents -Force
                    & cmd /c "mklink `"AGENTS.md`" `"$relRoot`"" 2>&1 | Out-Null
                    if ($LASTEXITCODE -eq 0) {
                        Write-Host "  [linked]  AGENTS.md (root)  ->  $relRoot  (was a stub)"
                    } else {
                        throw "mklink exit $LASTEXITCODE"
                    }
                } catch {
                    # Symlink failed. Recover by writing the
                    # canonical contents back as a plain file
                    # (better than leaving the root with no
                    # AGENTS.md at all — many tools read the
                    # root path unconditionally).
                    Write-Warning "Root AGENTS.md symlink failed; rewriting it as a copy of the canonical. (Run as admin or enable Developer Mode for symlinks.)"
                    try {
                        $canonicalContents = Get-Content -LiteralPath $Canonical -Raw
                        Set-Content -LiteralPath $rootAgents -Value $canonicalContents -NoNewline
                        Write-Host "  [copied]  AGENTS.md (root)  <-  $Canonical  (no symlink)"
                    } catch {
                        Write-Error "Failed to write $rootAgents : $_"
                    }
                }
            }
        } else {
            Write-Host "  [skip]    AGENTS.md (root) is non-trivial content; not touching it."
        }
    } else {
        Write-Host "  [ok]      AGENTS.md (root) is already a symlink"
    }
}

Write-Host ""
Write-Host "Done. Agent tools will now read .agents/AGENTS.md."
Write-Host "Edit .agents/AGENTS.md to update the canonical spec for all tools."
