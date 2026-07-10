<#
.SYNOPSIS
  Local release helper — tag, push, create Draft Release via CI.

.DESCRIPTION
  Reads VERSION, optionally runs sync:version, commits + tags + pushes.
  GitHub Actions picks up the tag, builds all binaries, and creates
  a Draft Release. After CI finishes, review the Draft on GitHub
  and click Publish.

  If CI fails: delete the Draft Release + delete the tag (git tag -d
  && git push --delete), fix the issue, re-run this script.

  If already Published and buggy: bump VERSION to a higher number.

.PARAMETER DryRun
  Show what would happen without making any changes.

.PARAMETER SkipSync
  Skip `task sync:version` (use when already synced).

.PARAMETER Message
  Custom commit message (default: "release v{version}").

.EXAMPLE
  .\scripts\release.ps1
  .\scripts\release.ps1 -DryRun
  .\scripts\release.ps1 -Message "release v1.0.4 — fix sandbox race"
#>

param(
  [switch]$DryRun,
  [switch]$SkipSync,
  [string]$Message
)

$ErrorActionPreference = "Stop"
Set-Location -LiteralPath (Split-Path $PSScriptRoot -Parent)

# ── 1. Read VERSION ──────────────────────────────────────────
if (-not (Test-Path VERSION)) { throw "VERSION file not found in current directory" }
$version = (Get-Content VERSION -Raw).Trim()
if (-not $version) { throw "VERSION file is empty" }
$tag = "v$version"

# ── 2. Pre-flight checks ────────────────────────────────────
$branch = (git rev-parse --abbrev-ref HEAD).Trim()
$changed = (git status --porcelain).Trim()

Write-Host "┌─ Release: $tag" -ForegroundColor Cyan
Write-Host "├─ Branch: $branch" -ForegroundColor Gray
Write-Host "├─ VERSION: $version" -ForegroundColor Gray
Write-Host "├─ Changed files:" -ForegroundColor Gray
if ($changed) {
  Write-Host "│  $changed" -ForegroundColor Yellow
  if ($SkipSync) {
    Write-Host "│  WARNING: -SkipSync with dirty tree — uncommitted files will be included in the release commit." -ForegroundColor Yellow
  }
}
else           { Write-Host "│  (clean)" -ForegroundColor Green }

$remoteTag = git ls-remote --tags origin "$tag" 2>$null
if ($remoteTag) { throw "Tag '$tag' already exists on remote. Delete it first: git push --delete origin $tag" }

# ── 3. Confirmation ─────────────────────────────────────────
Write-Host "└─ Steps: sync:version → commit → tag → push" -ForegroundColor Gray

if ($DryRun) {
  Write-Host "`n[DRY-RUN] Would tag and push '$tag'. No changes made." -ForegroundColor Magenta
  exit 0
}

$confirm = Read-Host "`nProceed? [y/N]"
if ($confirm -notmatch '^y') {
  Write-Host "Aborted."
  exit 0
}

# ── 4. Sync version ─────────────────────────────────────────
if (-not $SkipSync) {
  Write-Host "`n→ Running task sync:version..." -ForegroundColor Cyan
  task sync:version
  if ($LASTEXITCODE -ne 0) { throw "sync:version failed" }
}

# ── 5. Commit + tag + push ──────────────────────────────────
if (-not $Message) { $Message = "release $tag" }

Write-Host "`n→ git add -A && git commit -m '$Message'" -ForegroundColor Cyan
git add -A
git commit -m "$Message"
if ($LASTEXITCODE -ne 0 -and $LASTEXITCODE -ne 1) { throw "commit failed" }

Write-Host "`n→ git tag $tag" -ForegroundColor Cyan
git tag -a "$tag" -m "$Message"
if ($LASTEXITCODE -ne 0) { throw "tag failed" }

Write-Host "`n→ git push origin HEAD && git push origin $tag" -ForegroundColor Cyan
git push origin HEAD
if ($LASTEXITCODE -ne 0) { throw "push branch failed" }
git push origin $tag
if ($LASTEXITCODE -ne 0) { throw "push tag failed" }

Write-Host @"

Done! Tag '$tag' pushed.

Next:
  1. Watch GitHub Actions: https://github.com/helloAping/P-chat/actions
  2. CI builds → Draft Release created
  3. Review Draft → click Publish
  4. If CI fails: delete Draft + delete tag, fix, re-run this script

Rollback (if needed):
  git tag -d $tag
  git push --delete origin $tag
  # Also delete the Draft Release on GitHub Releases page

"@ -ForegroundColor Green
