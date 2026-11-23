<#
.SYNOPSIS
  Build platform update zips and upload manifests for 08ms releases.

.DESCRIPTION
  The update zip is a latest install-root payload marked as a patch artifact.
  The client can download it, verify SHA-256, restart into pchat-updater, and
  overwrite the existing install files without running the setup installer.
#>

[CmdletBinding()]
param(
    [string]$Platform = "windows",
    [string]$Arch = "amd64",
    [switch]$RequireSetup,
    [switch]$AllowMissingGui
)

$ErrorActionPreference = "Stop"

. (Join-Path $PSScriptRoot 'version-utils.ps1')

$root = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot "..")).Path
$platformOS = (& (Join-Path $PSScriptRoot 'resolve-platform.ps1') -Name $Platform -Format os).Trim()
$packagePlatform = if ($platformOS -eq "darwin") { "macos" } else { $platformOS }
$version = (Get-Content -LiteralPath (Join-Path $root "VERSION") -Raw).Trim()
$versionInfo = Get-PChatVersionInfo -Version $version
$releaseChannel = $versionInfo.Channel

$bin = Join-Path $root "bin"
$stageRoot = Join-Path $root "build\package"
$stage = Join-Path $stageRoot ("pchat-update-{0}-{1}-v{2}" -f $packagePlatform, $Arch, $version)
$zipPath = Join-Path $bin ("pchat-update-{0}-{1}-v{2}.zip" -f $packagePlatform, $Arch, $version)
$releaseManifestPath = Join-Path $bin ("pchat-release-manifest-{0}-{1}-v{2}.json" -f $packagePlatform, $Arch, $version)
$latestJsonPath = Join-Path $bin ("pchat-latest-{0}-{1}-v{2}.json" -f $packagePlatform, $Arch, $version)

function Assert-File {
    param([Parameter(Mandatory = $true)][string]$Path)
    if (-not (Test-Path -LiteralPath $Path)) {
        throw "Missing required file: $Path"
    }
}

function Assert-OptionalFile {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$Label
    )
    if (Test-Path -LiteralPath $Path) {
        return $true
    }
    if ($AllowMissingGui) {
        Write-Host "[package-update-zip] WARNING: $Label missing at $Path (skipped)" -ForegroundColor Yellow
        return $false
    }
    throw "Missing required file: $Path"
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

function Get-HostOS {
    if ([System.Runtime.InteropServices.RuntimeInformation]::IsOSPlatform([System.Runtime.InteropServices.OSPlatform]::Windows)) {
        return "windows"
    }
    if ([System.Runtime.InteropServices.RuntimeInformation]::IsOSPlatform([System.Runtime.InteropServices.OSPlatform]::Linux)) {
        return "linux"
    }
    if ([System.Runtime.InteropServices.RuntimeInformation]::IsOSPlatform([System.Runtime.InteropServices.OSPlatform]::OSX)) {
        return "darwin"
    }
    return "unknown"
}

function Assert-BinaryVersion {
    param(
        [Parameter(Mandatory = $true)][string]$Label,
        [Parameter(Mandatory = $true)][string]$Path
    )
    Assert-File $Path
    if ((Get-HostOS) -ne $platformOS) {
        Write-Host "[package-update-zip] skip version check for $Label (target=$platformOS host=$(Get-HostOS))" -ForegroundColor Yellow
        return
    }
    $text = (& $Path version 2>&1 | Out-String).Trim()
    Assert-VersionText -Label $Label -Text $text -Expected $version
}

function Get-RelativePath {
    param(
        [Parameter(Mandatory = $true)][string]$Base,
        [Parameter(Mandatory = $true)][string]$Path
    )
    $baseFull = [System.IO.Path]::GetFullPath($Base)
    $pathFull = [System.IO.Path]::GetFullPath($Path)
    # Path.GetRelativePath exists on PowerShell 7. Windows PowerShell 5.1 keeps the URI fallback.
    # A trailing backslash makes Unix paths look like relative URIs to MakeRelativeUri.
    $getRelative = [System.IO.Path].GetMethod('GetRelativePath', [type[]]@([string], [string]))
    if ($null -ne $getRelative) {
        $relative = [System.IO.Path]::GetRelativePath($baseFull, $pathFull)
    } else {
        $separator = [System.IO.Path]::DirectorySeparatorChar
        $baseUri = [Uri]($baseFull.TrimEnd('\', '/') + $separator)
        $pathUri = [Uri]$pathFull
        $relative = [Uri]::UnescapeDataString($baseUri.MakeRelativeUri($pathUri).ToString())
    }
    return ($relative -replace '/', '\')
}

function Write-JsonNoBom {
    param(
        [Parameter(Mandatory = $true)]$Value,
        [Parameter(Mandatory = $true)][string]$Path
    )
    $json = $Value | ConvertTo-Json -Depth 8
    # Windows PowerShell 5.1 escapes < and > as \u003c and \u003e. Keep the upload placeholder literal.
    $json = $json.Replace('\u003c', '<').Replace('\u003C', '<').Replace('\u003e', '>').Replace('\u003E', '>')
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

function Get-UpdateSignature {
    param(
        [Parameter(Mandatory = $true)][string]$Sha256,
        [Parameter(Mandatory = $true)][string]$Kind
    )
    $key = [string]$env:PCHAT_UPDATE_SIGNING_KEY
    if ([string]::IsNullOrWhiteSpace($key)) {
        return ""
    }
    $sigFile = Join-Path ([System.IO.Path]::GetTempPath()) ("pchat-sig-" + [guid]::NewGuid().ToString("n") + ".txt")
    $env:PCHAT_SIGN_SHA256 = $Sha256
    $env:PCHAT_SIGN_VERSION = $version
    $env:PCHAT_SIGN_KIND = $Kind
    $env:PCHAT_SIGN_OUTPUT = $sigFile
    $signTool = Join-Path $root "bin\pchat-updatesign.exe"
    Push-Location $root
    try {
        if (Test-Path -LiteralPath $signTool) {
            & $signTool
        } else {
            & go run ./cmd/pchat-updatesign
        }
        if ($LASTEXITCODE -ne 0) {
            throw "Failed to sign update artifact (kind=$Kind, exit $LASTEXITCODE). The private key must match the embedded public key."
        }
    } finally {
        Pop-Location
        Remove-Item Env:PCHAT_SIGN_SHA256, Env:PCHAT_SIGN_VERSION, Env:PCHAT_SIGN_KIND, Env:PCHAT_SIGN_OUTPUT -ErrorAction SilentlyContinue
    }
    if (-not (Test-Path -LiteralPath $sigFile)) {
        throw "Failed to sign update artifact (kind=$Kind). Signer did not write a signature file."
    }
    try {
        $sig = [System.IO.File]::ReadAllText($sigFile).Trim()
    } finally {
        Remove-Item -LiteralPath $sigFile -ErrorAction SilentlyContinue
    }
    if ($sig -notmatch '^[A-Za-z0-9+/]+=*$') {
        throw "Failed to sign update artifact (kind=$Kind). Signer output was not a base64 signature."
    }
    return $sig
}

function New-ArtifactEntry {
    param(
        [Parameter(Mandatory = $true)][string]$Kind,
        [Parameter(Mandatory = $true)][string]$Path
    )
    $file = Get-Item -LiteralPath $Path
    $sha = Get-SHA256Hex -Path $file.FullName
    $entry = [ordered]@{
        kind     = $Kind
        platform = $packagePlatform
        arch     = $Arch
        version  = $version
        file     = $file.Name
        size     = $file.Length
        sha256   = $sha
    }
    $sig = Get-UpdateSignature -Sha256 $sha -Kind $Kind
    if ($sig) {
        $entry["signature"] = $sig
    }
    return $entry
}

function Find-FirstExisting {
    param([Parameter(Mandatory = $true)][string[]]$Candidates)
    foreach ($candidate in $Candidates) {
        if (Test-Path -LiteralPath $candidate) {
            return (Resolve-Path -LiteralPath $candidate).Path
        }
    }
    return ""
}

function Find-SetupPackage {
    switch ($platformOS) {
        "windows" {
            return Find-FirstExisting @((Join-Path $bin ("pchat-setup-v{0}.exe" -f $version)))
        }
        "linux" {
            return Find-FirstExisting @(
                (Join-Path $bin ("pchat-linux-setup-v{0}.tar.gz" -f $version)),
                (Join-Path $bin ("pchat-linux-setup-v{0}.zip" -f $version))
            )
        }
        "darwin" {
            return Find-FirstExisting @(
                (Join-Path $bin ("pchat-mac-setup-v{0}.tar.gz" -f $version)),
                (Join-Path $bin ("pchat-mac-setup-v{0}.zip" -f $version))
            )
        }
    }
    return ""
}

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

switch ($platformOS) {
    "windows" {
        $wailsJson = Get-Content -LiteralPath (Join-Path $root "cmd\pchat-gui\wails.json") -Raw | ConvertFrom-Json
        if ($wailsJson.info.productVersion -ne $version) {
            throw "wails.json productVersion mismatch: expected '$version', got '$($wailsJson.info.productVersion)'"
        }

        $installPs1 = Get-Content -LiteralPath (Join-Path $root "cmd\pchat-gui\install.ps1") -Raw
        $displayVersionPattern = 'DisplayVersion"\s+-Value\s+"' + [regex]::Escape($version) + '"'
        if ($installPs1 -notmatch $displayVersionPattern) {
            throw "install.ps1 DisplayVersion mismatch: expected '$version'"
        }

        Assert-BinaryVersion -Label "pchat.exe" -Path (Join-Path $bin "pchat.exe")
        Assert-BinaryVersion -Label "pchat-server.exe" -Path (Join-Path $bin "pchat-server.exe")
        Assert-BinaryVersion -Label "pchat-updater.exe" -Path (Join-Path $bin "pchat-updater.exe")
        Assert-File (Join-Path $bin "pchat-gui.exe")

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
    }
    "linux" {
        $guiPath = Find-FirstExisting @(
            (Join-Path $root "cmd\pchat-gui\build\bin\pchat-gui"),
            (Join-Path $root "build\artifacts\linux\pchat-gui"),
            (Join-Path $root "artifacts\linux\pchat-gui")
        )
        if ($guiPath -or (Assert-OptionalFile -Path (Join-Path $root "cmd\pchat-gui\build\bin\pchat-gui") -Label "Linux pchat-gui")) {
            if (-not $guiPath) {
                $guiPath = Join-Path $root "cmd\pchat-gui\build\bin\pchat-gui"
            }
            Copy-Item -LiteralPath $guiPath -Destination (Join-Path $stage "pchat-gui") -Force
        }
        Assert-BinaryVersion -Label "pchat-server-linux" -Path (Join-Path $bin "pchat-server-linux")
        Assert-BinaryVersion -Label "pchat-linux" -Path (Join-Path $bin "pchat-linux")
        Assert-BinaryVersion -Label "pchat-updater-linux" -Path (Join-Path $bin "pchat-updater-linux")

        Copy-Item -LiteralPath (Join-Path $bin "pchat-server-linux") -Destination (Join-Path $stage "pchat-server") -Force
        Copy-Item -LiteralPath (Join-Path $bin "pchat-linux") -Destination (Join-Path $stage "pchat") -Force
        Copy-Item -LiteralPath (Join-Path $bin "pchat-updater-linux") -Destination (Join-Path $stage "pchat-updater") -Force
        Copy-Item -LiteralPath (Join-Path $root "scripts\uninstall-linux.sh") -Destination (Join-Path $stage "uninstall.sh") -Force
    }
    "darwin" {
        $appPath = Find-FirstExisting @(
            (Join-Path $root "cmd\pchat-gui\build\bin\pchat-gui.app"),
            (Join-Path $root "build\artifacts\mac\pchat-gui.app"),
            (Join-Path $root "artifacts\mac\pchat-gui.app")
        )
        $resources = Join-Path $stage "Contents\Resources"
        New-Item -ItemType Directory -Path $resources -Force | Out-Null
        if ($appPath) {
            Get-ChildItem -LiteralPath $appPath -Force | ForEach-Object {
                Copy-Item -LiteralPath $_.FullName -Destination $stage -Recurse -Force
            }
        } elseif (-not $AllowMissingGui) {
            throw "Missing required macOS app bundle: cmd\pchat-gui\build\bin\pchat-gui.app"
        } else {
            Write-Host "[package-update-zip] WARNING: macOS pchat-gui.app missing (patch zip will not contain GUI)" -ForegroundColor Yellow
        }

        Assert-BinaryVersion -Label "pchat-server-darwin-$Arch" -Path (Join-Path $bin "pchat-server-darwin-$Arch")
        Assert-BinaryVersion -Label "pchat-darwin-$Arch" -Path (Join-Path $bin "pchat-darwin-$Arch")
        Assert-BinaryVersion -Label "pchat-updater-darwin-$Arch" -Path (Join-Path $bin "pchat-updater-darwin-$Arch")

        Copy-Item -LiteralPath (Join-Path $bin "pchat-server-darwin-$Arch") -Destination (Join-Path $resources "pchat-server") -Force
        Copy-Item -LiteralPath (Join-Path $bin "pchat-darwin-$Arch") -Destination (Join-Path $resources "pchat") -Force
        Copy-Item -LiteralPath (Join-Path $bin "pchat-updater-darwin-$Arch") -Destination (Join-Path $resources "pchat-updater") -Force

        $browserExt = Join-Path $root "cmd\pchat-server\browser-extension.zip"
        if (Test-Path -LiteralPath $browserExt) {
            Copy-Item -LiteralPath $browserExt -Destination (Join-Path $resources "browser-extension.zip") -Force
        }
    }
    default {
        throw "Unsupported platform: $Platform"
    }
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
    kind           = "patch"
    version        = $version
    platform       = $packagePlatform
    arch           = $Arch
    created_at_utc = (Get-Date).ToUniversalTime().ToString("s") + "Z"
    files          = $files
}
Write-JsonNoBom -Value $updateManifest -Path (Join-Path $stage "pchat-update.json")

if (Test-Path -LiteralPath $zipPath) {
    Remove-Item -LiteralPath $zipPath -Force
}
Compress-Archive -Path (Join-Path $stage "*") -DestinationPath $zipPath -Force
$updateArtifact = New-ArtifactEntry -Kind "patch" -Path $zipPath

$artifacts = @()
$setupArtifact = $null
$setupPath = Find-SetupPackage
if ($setupPath) {
    $setupArtifact = New-ArtifactEntry -Kind "full" -Path $setupPath
    $artifacts += $setupArtifact
} elseif ($RequireSetup) {
    throw "Setup package is required but missing for platform=$packagePlatform version=$version"
}
$artifacts += $updateArtifact

$createdAtUTC = (Get-Date).ToUniversalTime().ToString("s") + "Z"
$updateURL = "<upload-url>/" + $updateArtifact.file
$primaryArtifact = if ($setupArtifact) { $setupArtifact } else { $updateArtifact }
$primaryURL = "<upload-url>/" + $primaryArtifact.file
$patchEntry = [ordered]@{
    kind     = "patch"
    platform = $packagePlatform
    arch     = $Arch
    size     = $updateArtifact.size
    sha256   = $updateArtifact.sha256
    url      = $updateURL
}
if ($updateArtifact.signature) {
    $patchEntry["signature"] = $updateArtifact.signature
}
$latestJson = [ordered]@{
    name          = "P-Chat"
    slug          = "p-chat"
    version       = $version
    channel       = $releaseChannel
    release_notes = ""
    published_at  = $createdAtUTC
    platform      = $packagePlatform
    arch          = $Arch
    size          = $primaryArtifact.size
    sha256        = $primaryArtifact.sha256
    url           = $primaryURL
    patch         = $patchEntry
}
if ($primaryArtifact.signature) {
    $latestJson["signature"] = $primaryArtifact.signature
}
if ($setupArtifact) {
    $fullEntry = [ordered]@{
        kind     = "full"
        platform = $packagePlatform
        arch     = $Arch
        size     = $setupArtifact.size
        sha256   = $setupArtifact.sha256
        url      = "<upload-url>/" + $setupArtifact.file
    }
    if ($setupArtifact.signature) {
        $fullEntry["signature"] = $setupArtifact.signature
    }
    $latestJson["full"] = $fullEntry
}
if (-not $env:PCHAT_UPDATE_SIGNING_KEY) {
    Write-Host "[package-update-zip] WARNING: PCHAT_UPDATE_SIGNING_KEY unset; latest.json has no signatures and clients will reject downloads" -ForegroundColor Yellow
}
Write-JsonNoBom -Value $latestJson -Path $latestJsonPath

$releaseManifest = [ordered]@{
    name             = "P-Chat"
    slug             = "p-chat"
    version          = $version
    channel          = $releaseChannel
    platform         = $packagePlatform
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
