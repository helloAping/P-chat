<#
.SYNOPSIS
  Fail a release build unless each latest.json matches its zip, setup package, and signature.
#>

[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$Directory,
    [switch]$RequireSignature,
    [switch]$RejectPlaceholder,
    [switch]$SkipCryptoVerify
)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
$env:GOTELEMETRY = 'off'

$root = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '..')).Path
Add-Type -AssemblyName System.IO.Compression.FileSystem

function Get-SHA256Hex {
    param([Parameter(Mandatory = $true)][string]$Path)
    $stream = [System.IO.File]::OpenRead($Path)
    try {
        $sha = [System.Security.Cryptography.SHA256]::Create()
        try {
            return ([System.BitConverter]::ToString($sha.ComputeHash($stream))).Replace('-', '').ToLowerInvariant()
        } finally {
            $sha.Dispose()
        }
    } finally {
        $stream.Dispose()
    }
}

function Get-ArtifactFileName {
    param([string]$Url)
    $leaf = (($Url -replace '\\', '/') -split '/')[-1]
    if ([string]::IsNullOrWhiteSpace($leaf)) {
        throw "Artifact URL has no file name: $Url"
    }
    return $leaf
}

function Get-ZipNames {
    param([Parameter(Mandatory = $true)][string]$Path)
    $zip = [System.IO.Compression.ZipFile]::OpenRead($Path)
    try {
        return @($zip.Entries | ForEach-Object { $_.FullName.Replace('\', '/').TrimStart('/') })
    } finally {
        $zip.Dispose()
    }
}

function Assert-ZipContains {
    param(
        [Parameter(Mandatory = $true)][string]$ZipPath,
        [Parameter(Mandatory = $true)][string[]]$Needles
    )
    $names = @(Get-ZipNames -Path $ZipPath)
    foreach ($needle in $Needles) {
        $found = $false
        foreach ($name in $names) {
            if ($name -eq $needle -or $name.EndsWith("/$needle")) {
                $found = $true
                break
            }
        }
        if (-not $found) {
            $preview = ($names | Select-Object -First 30) -join ', '
            throw "Update zip $ZipPath is missing '$needle'. Entries: $preview"
        }
    }
}

function Get-VerifyTool {
    foreach ($name in @('pchat-updatesign.exe', 'pchat-updatesign')) {
        $path = Join-Path $root (Join-Path 'bin' $name)
        if (Test-Path -LiteralPath $path) {
            Write-Host "[validate-release-artifacts] using $path" -ForegroundColor Cyan
            return (Resolve-Path -LiteralPath $path).Path
        }
    }
    throw "pchat-updatesign is missing from bin/. The release build must compile it before validation."
}

function Assert-Artifact {
    param(
        [Parameter(Mandatory = $true)]$Entry,
        [Parameter(Mandatory = $true)][string]$Version,
        [Parameter(Mandatory = $true)][string]$Directory,
        [string]$VerifyTool
    )
    $kind = [string]$Entry.kind
    if ([string]::IsNullOrWhiteSpace($kind)) {
        throw "Artifact is missing kind"
    }
    $fileName = Get-ArtifactFileName -Url ([string]$Entry.url)
    $path = Join-Path $Directory $fileName
    if (-not (Test-Path -LiteralPath $path)) {
        throw "Artifact file missing: $path"
    }
    $got = Get-SHA256Hex -Path $path
    $want = ([string]$Entry.sha256).Trim().ToLowerInvariant()
    if ($got -ne $want) {
        throw "$fileName sha256 mismatch: manifest $want, file $got"
    }
    $signature = [string]$Entry.signature
    if ($RequireSignature -and [string]::IsNullOrWhiteSpace($signature)) {
        throw "$fileName is missing signature"
    }
    if ($VerifyTool -and -not [string]::IsNullOrWhiteSpace($signature)) {
        $env:PCHAT_SIGN_SHA256 = $want
        $env:PCHAT_SIGN_VERSION = $Version
        $env:PCHAT_SIGN_KIND = $kind
        $env:PCHAT_SIGN_SIGNATURE = $signature.Trim()
        try {
            & $VerifyTool verify
            if ($LASTEXITCODE -ne 0) {
                throw "$fileName signature does not match the embedded public key (kind=$kind, sha256=$want)"
            }
        } finally {
            Remove-Item Env:PCHAT_SIGN_SHA256, Env:PCHAT_SIGN_VERSION, Env:PCHAT_SIGN_KIND, Env:PCHAT_SIGN_SIGNATURE -ErrorAction SilentlyContinue
        }
    }
    if ($kind -eq 'patch') {
        $platform = [string]$Entry.platform
        switch ($platform) {
            'windows' { Assert-ZipContains -ZipPath $path -Needles @('pchat-gui.exe', 'pchat-server.exe') }
            'linux' { Assert-ZipContains -ZipPath $path -Needles @('pchat-gui', 'pchat-server') }
            'macos' { Assert-ZipContains -ZipPath $path -Needles @('Contents/MacOS/pchat-gui', 'Contents/Resources/pchat-server') }
            default { throw "Unknown patch platform '$platform'" }
        }
    }
}

$files = @(Get-ChildItem -LiteralPath $Directory -Filter 'pchat-latest-*.json' -File)
if ($files.Count -eq 0) {
    throw "No pchat-latest-*.json files in $Directory"
}

$verifyTool = $null
if (-not $SkipCryptoVerify) {
    $verifyTool = Get-VerifyTool
}

foreach ($file in $files) {
    $text = [System.IO.File]::ReadAllText($file.FullName)
    if ($RejectPlaceholder -and $text -like '*<upload-url>*') {
        throw "$($file.Name) still contains <upload-url>"
    }
    $manifest = $text | ConvertFrom-Json
    $version = [string]$manifest.version
    if ([string]::IsNullOrWhiteSpace($version)) {
        throw "$($file.Name) has no version"
    }
    foreach ($entry in @($manifest.full, $manifest.patch)) {
        if ($null -eq $entry) { continue }
        Assert-Artifact -Entry $entry -Version $version -Directory $Directory -VerifyTool $verifyTool
    }
    Write-Host "[validate-release-artifacts] $($file.Name)" -ForegroundColor Green
}
