<#
.SYNOPSIS
  Build the Windows update zip and upload manifest for 08ms releases.

.DESCRIPTION
  The update zip is a full latest install-root payload. The client can
  download it, verify SHA-256, restart into pchat-updater.exe, and overwrite
  the existing install directory without running the setup installer.
#>

[CmdletBinding()]
param(
    [string]$Platform = "windows",
    [string]$Arch = "amd64",
    [switch]$RequireSetup
)

$ErrorActionPreference = "Stop"

$root = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot "..")).Path
$version = (Get-Content -LiteralPath (Join-Path $root "VERSION") -Raw).Trim()
if (-not ($version -match '^\d+\.\d+\.\d+$')) {
    throw "VERSION must be MAJOR.MINOR.PATCH for update detection; got '$version'"
}

$bin = Join-Path $root "bin"
$stageRoot = Join-Path $root "build\package"
$stage = Join-Path $stageRoot ("pchat-update-{0}-{1}-v{2}" -f $Platform, $Arch, $version)
$zipPath = Join-Path $bin ("pchat-update-{0}-{1}-v{2}.zip" -f $Platform, $Arch, $version)
$releaseManifestPath = Join-Path $bin ("pchat-release-manifest-v{0}.json" -f $version)
$latestJsonPath = Join-Path $bin ("pchat-latest-{0}-{1}-v{2}.json" -f $Platform, $Arch, $version)

function Assert-File {
    param([Parameter(Mandatory = $true)][string]$Path)
    if (-not (Test-Path -LiteralPath $Path)) {
        throw "Missing required file: $Path"
    }
}

function Assert-VersionText {
    param(
        [Parameter(Mandatory = $true)][string]$Label,
        [Parameter(Mandatory = $true)][string]$Text,
        [Parameter(Mandatory = $true)][string]$Expected
    )
    if ($Text -notmatch [regex]::Escape($Expected)) {
        throw "$Label version mismatch: expected '$Expected', got '$Text'"
    }
}

function Get-RelativePath {
    param(
        [Parameter(Mandatory = $true)][string]$Base,
        [Parameter(Mandatory = $true)][string]$Path
    )
    $baseUri = [Uri]($Base.TrimEnd('\') + '\')
    $pathUri = [Uri]$Path
    return [Uri]::UnescapeDataString($baseUri.MakeRelativeUri($pathUri).ToString()).Replace('/', '\')
}

function Write-JsonNoBom {
    param(
        [Parameter(Mandatory = $true)]$Value,
        [Parameter(Mandatory = $true)][string]$Path
    )
    $json = $Value | ConvertTo-Json -Depth 8
    [System.IO.File]::WriteAllText($Path, $json + [Environment]::NewLine, [System.Text.UTF8Encoding]::new($false))
}

function Get-SHA256Hex {
    param([Parameter(Mandatory = $true)][string]$Path)
    $stream = [System.IO.File]::OpenRead($Path)
    try {
        $sha = [System.Security.Cryptography.SHA256]::Create()
        try {
            $hash = $sha.ComputeHash($stream)
            return ([System.BitConverter]::ToString($hash)).Replace("-", "").ToLowerInvariant()
        } finally {
            $sha.Dispose()
        }
    } finally {
        $stream.Dispose()
    }
}

function New-ArtifactEntry {
    param(
        [Parameter(Mandatory = $true)][string]$Kind,
        [Parameter(Mandatory = $true)][string]$Path
    )
    $file = Get-Item -LiteralPath $Path
    return [ordered]@{
        kind     = $Kind
        platform = $Platform
        arch     = $Arch
        version  = $version
        file     = $file.Name
        size     = $file.Length
        sha256   = Get-SHA256Hex -Path $file.FullName
    }
}

Assert-File (Join-Path $bin "pchat.exe")
Assert-File (Join-Path $bin "pchat-server.exe")
Assert-File (Join-Path $bin "pchat-gui.exe")
Assert-File (Join-Path $bin "pchat-updater.exe")

$wailsJson = Get-Content -LiteralPath (Join-Path $root "cmd\pchat-gui\wails.json") -Raw | ConvertFrom-Json
if ($wailsJson.info.productVersion -ne $version) {
    throw "wails.json productVersion mismatch: expected '$version', got '$($wailsJson.info.productVersion)'"
}

$installPs1 = Get-Content -LiteralPath (Join-Path $root "cmd\pchat-gui\install.ps1") -Raw
$displayVersionPattern = 'DisplayVersion"\s+-Value\s+"' + [regex]::Escape($version) + '"'
if ($installPs1 -notmatch $displayVersionPattern) {
    throw "install.ps1 DisplayVersion mismatch: expected '$version'"
}

$cliVersion = (& (Join-Path $bin "pchat.exe") version 2>&1 | Out-String).Trim()
Assert-VersionText -Label "pchat.exe" -Text $cliVersion -Expected $version

$serverVersion = (& (Join-Path $bin "pchat-server.exe") version 2>&1 | Out-String).Trim()
Assert-VersionText -Label "pchat-server.exe" -Text $serverVersion -Expected $version

$updaterVersion = (& (Join-Path $bin "pchat-updater.exe") version 2>&1 | Out-String).Trim()
Assert-VersionText -Label "pchat-updater.exe" -Text $updaterVersion -Expected $version

New-Item -ItemType Directory -Path $stageRoot -Force | Out-Null
if (Test-Path -LiteralPath $stage) {
    $resolvedStage = (Resolve-Path -LiteralPath $stage).Path
    $resolvedStageRoot = (Resolve-Path -LiteralPath $stageRoot).Path
    if (-not $resolvedStage.StartsWith($resolvedStageRoot, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw "Refusing to remove stage outside build package root: $resolvedStage"
    }
    Remove-Item -LiteralPath $stage -Recurse -Force
}
New-Item -ItemType Directory -Path $stage -Force | Out-Null

Copy-Item -LiteralPath (Join-Path $bin "pchat-gui.exe") -Destination (Join-Path $stage "pchat-gui.exe") -Force
Copy-Item -LiteralPath (Join-Path $bin "pchat-server.exe") -Destination (Join-Path $stage "pchat-server.exe") -Force
Copy-Item -LiteralPath (Join-Path $bin "pchat.exe") -Destination (Join-Path $stage "pchat.exe") -Force
Copy-Item -LiteralPath (Join-Path $bin "pchat-updater.exe") -Destination (Join-Path $stage "pchat-updater.exe") -Force
Copy-Item -LiteralPath (Join-Path $root "cmd\pchat-gui\uninstall.ps1") -Destination (Join-Path $stage "uninstall.ps1") -Force

$browserExt = Join-Path $bin "browser-extension.zip"
if (Test-Path -LiteralPath $browserExt) {
    Copy-Item -LiteralPath $browserExt -Destination (Join-Path $stage "browser-extension.zip") -Force
}

$webDir = Join-Path $root "web"
if (Test-Path -LiteralPath $webDir) {
    Copy-Item -LiteralPath $webDir -Destination (Join-Path $stage "web") -Recurse -Force
}

$files = @()
Get-ChildItem -LiteralPath $stage -File -Recurse |
    Sort-Object FullName |
    ForEach-Object {
        $files += [ordered]@{
            path   = Get-RelativePath -Base $stage -Path $_.FullName
            size   = $_.Length
            sha256 = Get-SHA256Hex -Path $_.FullName
        }
    }

$updateManifest = [ordered]@{
    name           = "P-Chat"
    slug           = "p-chat"
    kind           = "full"
    version        = $version
    platform       = $Platform
    arch           = $Arch
    created_at_utc = (Get-Date).ToUniversalTime().ToString("s") + "Z"
    files          = $files
}
Write-JsonNoBom -Value $updateManifest -Path (Join-Path $stage "pchat-update.json")

if (Test-Path -LiteralPath $zipPath) {
    Remove-Item -LiteralPath $zipPath -Force
}
Compress-Archive -Path (Join-Path $stage "*") -DestinationPath $zipPath -Force
$updateArtifact = New-ArtifactEntry -Kind "full" -Path $zipPath

$artifacts = @()
$setupPath = Join-Path $bin ("pchat-setup-v{0}.exe" -f $version)
if (Test-Path -LiteralPath $setupPath) {
    $artifacts += New-ArtifactEntry -Kind "setup" -Path $setupPath
} elseif ($RequireSetup) {
    throw "Setup installer is required but missing: $setupPath"
}
$artifacts += $updateArtifact

$createdAtUTC = (Get-Date).ToUniversalTime().ToString("s") + "Z"
$updateURL = "<upload-url>/" + $updateArtifact.file
$latestJson = [ordered]@{
    name          = "P-Chat"
    slug          = "p-chat"
    version       = $version
    channel       = "stable"
    release_notes = ""
    published_at  = $createdAtUTC
    platform      = $Platform
    arch          = $Arch
    size          = $updateArtifact.size
    sha256        = $updateArtifact.sha256
    url           = $updateURL
    full          = [ordered]@{
        kind     = "full"
        platform = $Platform
        arch     = $Arch
        size     = $updateArtifact.size
        sha256   = $updateArtifact.sha256
        url      = $updateURL
    }
}
Write-JsonNoBom -Value $latestJson -Path $latestJsonPath

$releaseManifest = [ordered]@{
    name             = "P-Chat"
    slug             = "p-chat"
    version          = $version
    channel          = "stable"
    platform         = $Platform
    arch             = $Arch
    created_at_utc   = $createdAtUTC
    artifacts        = $artifacts
    latest_json_file = (Split-Path -Leaf $latestJsonPath)
    latest_json_hint = $latestJson
}
Write-JsonNoBom -Value $releaseManifest -Path $releaseManifestPath

Write-Host "[package-update-zip] update zip: $zipPath" -ForegroundColor Green
Write-Host "[package-update-zip] manifest:   $releaseManifestPath" -ForegroundColor Green
Write-Host "[package-update-zip] latest json: $latestJsonPath" -ForegroundColor Green
