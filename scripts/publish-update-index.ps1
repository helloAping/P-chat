<#
.SYNOPSIS
  Merge per-release latest.json files into the stable static index names.

.DESCRIPTION
  Stable releases update {platform}-{arch}.json. They also replace
  {platform}-{arch}.beta.json when the stable version is newer than the
  current beta pointer, so Beta users still move to a newer stable build.
  A beta release only moves the beta pointer forward. Older tags never
  replace a newer pointer. alpha and rc manifests are left out of the index.
#>

[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$ReleaseDir,
    [string]$ExistingDir = '',
    [Parameter(Mandatory = $true)][string]$OutDir
)

$ErrorActionPreference = 'Stop'
. "$PSScriptRoot\version-utils.ps1"

$indexName = '^(windows|linux|macos)-(amd64|arm64|386)(\.beta)?\.json$'

function Test-ShouldReplace {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$NewVersion
    )
    if (-not (Test-Path -LiteralPath $Path)) {
        return $true
    }
    $existing = [string]((Get-Content -LiteralPath $Path -Raw | ConvertFrom-Json).version)
    if ([string]::IsNullOrWhiteSpace($existing)) {
        throw "Existing index has no version: $Path"
    }
    return (Compare-PChatVersion -Left $NewVersion -Right $existing) -gt 0
}

New-Item -ItemType Directory -Path $OutDir -Force | Out-Null
Get-ChildItem -LiteralPath $OutDir -Filter '*.json' -File -ErrorAction SilentlyContinue |
    Remove-Item -Force

if ($ExistingDir -and (Test-Path -LiteralPath $ExistingDir)) {
    Get-ChildItem -LiteralPath $ExistingDir -Filter '*.json' -File |
        Where-Object { $_.Name -match $indexName } |
        ForEach-Object { Copy-Item -LiteralPath $_.FullName -Destination (Join-Path $OutDir $_.Name) -Force }
}

$releases = @(Get-ChildItem -LiteralPath $ReleaseDir -Filter 'pchat-latest-*.json' -File)
foreach ($file in $releases) {
    $manifest = Get-Content -LiteralPath $file.FullName -Raw | ConvertFrom-Json
    $version = [string]$manifest.version
    $platform = [string]$manifest.platform
    $arch = [string]$manifest.arch
    $channel = [string]$manifest.channel
    if (-not $version -or -not $platform -or -not $arch) {
        throw "$($file.Name) is missing version, platform, or arch"
    }
    $info = Get-PChatVersionInfo -Version $version
    if ($info.Channel -ne $channel) {
        throw "$($file.Name) channel '$channel' does not match version '$version' ($($info.Channel))"
    }
    if ($channel -notin @('stable', 'beta')) {
        Write-Host "[publish-update-index] skip $channel $version ($($file.Name))" -ForegroundColor Yellow
        continue
    }

    $stableName = "$platform-$arch.json"
    $betaName = "$platform-$arch.beta.json"
    if ($channel -eq 'stable') {
        $stablePath = Join-Path $OutDir $stableName
        if (Test-ShouldReplace -Path $stablePath -NewVersion $version) {
            Copy-Item -LiteralPath $file.FullName -Destination $stablePath -Force
            Write-Host "[publish-update-index] $stableName <= $version" -ForegroundColor Green
        }
        $betaPath = Join-Path $OutDir $betaName
        if (Test-ShouldReplace -Path $betaPath -NewVersion $version) {
            Copy-Item -LiteralPath $file.FullName -Destination $betaPath -Force
            Write-Host "[publish-update-index] $betaName <= $version" -ForegroundColor Green
        }
        continue
    }

    $betaPath = Join-Path $OutDir $betaName
    if (Test-ShouldReplace -Path $betaPath -NewVersion $version) {
        Copy-Item -LiteralPath $file.FullName -Destination $betaPath -Force
        Write-Host "[publish-update-index] $betaName <= $version" -ForegroundColor Green
    } else {
        Write-Host "[publish-update-index] keep $betaName; $version is not newer" -ForegroundColor Yellow
    }
}
