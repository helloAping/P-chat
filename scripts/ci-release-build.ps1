<#
.SYNOPSIS
  Strict release build for one platform. GUI or arch mismatches fail the job.
#>

[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [ValidateSet('win', 'linux', 'mac')]
    [string]$Platform,

    [string]$Arch = 'amd64'
)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
$env:GOTELEMETRY = 'off'
$root = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '..')).Path
Set-Location -LiteralPath $root

if ([string]::IsNullOrWhiteSpace($env:PCHAT_UPDATE_SIGNING_KEY)) {
    throw "PCHAT_UPDATE_SIGNING_KEY is empty. Add the release private key as a GitHub Actions secret before building."
}

$proxy = if ($env:GOPROXY) { $env:GOPROXY } else { 'https://proxy.golang.org,direct' }

function Invoke-CheckedTask {
    param([Parameter(Mandatory = $true)][string[]]$TaskArgs)
    Write-Host "[ci-release-build] task $($TaskArgs -join ' ')" -ForegroundColor Cyan
    & task @TaskArgs
    if ($LASTEXITCODE -ne 0) {
        throw "task failed (${LASTEXITCODE}): $($TaskArgs -join ' ')"
    }
}

function Assert-PEArch {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$Expected
    )
    $bytes = [System.IO.File]::ReadAllBytes($Path)
    if ($bytes.Length -lt 64 -or $bytes[0] -ne 0x4D -or $bytes[1] -ne 0x5A) {
        throw "Not a PE file: $Path"
    }
    $pe = [System.BitConverter]::ToInt32($bytes, 0x3C)
    if ($pe -lt 0 -or ($pe + 6) -gt $bytes.Length) {
        throw "Invalid PE header: $Path"
    }
    $machine = [System.BitConverter]::ToUInt16($bytes, $pe + 4)
    $got = switch ($machine) {
        0x8664 { 'amd64' }
        0xAA64 { 'arm64' }
        0x014C { '386' }
        default { '0x{0:X}' -f $machine }
    }
    if ($got -ne $Expected) {
        throw "PE arch mismatch for ${Path}: expected $Expected, got $got"
    }
}

function Assert-UnixArch {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$Expected
    )
    if (-not (Get-Command file -ErrorAction SilentlyContinue)) {
        throw "file(1) is required to check $Path"
    }
    $desc = (& file -b -- $Path | Out-String).Trim()
    $ok = $false
    if ($Expected -eq 'arm64') {
        $ok = ($desc -match 'arm64') -and ($desc -notmatch 'x86_64')
    } elseif ($Expected -eq 'amd64') {
        $ok = (($desc -match 'x86-64') -or ($desc -match 'x86_64')) -and ($desc -notmatch 'arm64')
    }
    if (-not $ok) {
        throw "arch mismatch for ${Path}: expected $Expected, file says: $desc"
    }
}

switch ($Platform) {
    'win' {
        if ($Arch -ne 'amd64') {
            throw "Windows release builds amd64 only, got $Arch"
        }
        Invoke-CheckedTask -TaskArgs @('build:win', "GOPROXY=$proxy")
        foreach ($relative in @('bin\pchat-gui.exe', 'bin\pchat-server.exe', 'bin\pchat.exe', 'bin\pchat-updater.exe')) {
            Assert-PEArch -Path (Join-Path $root $relative) -Expected 'amd64'
        }
    }
    'linux' {
        if ($Arch -ne 'amd64') {
            throw "Linux release builds amd64 only, got $Arch"
        }
        Invoke-CheckedTask -TaskArgs @('build:frontend', "GOPROXY=$proxy")
        Invoke-CheckedTask -TaskArgs @('build:server:linux', "GOPROXY=$proxy")
        Invoke-CheckedTask -TaskArgs @('build:cli:linux', "GOPROXY=$proxy")
        Invoke-CheckedTask -TaskArgs @('build:updater:linux', "GOPROXY=$proxy")
        Invoke-CheckedTask -TaskArgs @('build:gui:linux:binary', "GOPROXY=$proxy")
        foreach ($relative in @(
                'bin\pchat-server-linux',
                'bin\pchat-linux',
                'bin\pchat-updater-linux',
                'cmd\pchat-gui\build\bin\pchat-gui'
            )) {
            Assert-UnixArch -Path (Join-Path $root $relative) -Expected 'amd64'
        }
        & (Join-Path $PSScriptRoot 'package-platform-archive.ps1') -Platform linux
        & (Join-Path $PSScriptRoot 'package-update-zip.ps1') -Platform linux -Arch amd64 -RequireSetup
    }
    'mac' {
        if ($Arch -ne 'arm64') {
            throw "macOS release builds arm64 only, got $Arch"
        }
        Invoke-CheckedTask -TaskArgs @('build:frontend', "GOPROXY=$proxy")
        Invoke-CheckedTask -TaskArgs @('build:server:macos', "ARCH=$Arch", "GOPROXY=$proxy")
        Invoke-CheckedTask -TaskArgs @('build:cli:macos', "ARCH=$Arch", "GOPROXY=$proxy")
        Invoke-CheckedTask -TaskArgs @('build:updater:macos', "ARCH=$Arch", "GOPROXY=$proxy")
        foreach ($relative in @(
                "bin\pchat-server-darwin-$Arch",
                "bin\pchat-darwin-$Arch",
                "bin\pchat-updater-darwin-$Arch"
            )) {
            Assert-UnixArch -Path (Join-Path $root $relative) -Expected $Arch
        }
        Invoke-CheckedTask -TaskArgs @('build:gui:macos:binary', "WAILS_PLATFORM=darwin/$Arch", "GOPROXY=$proxy")
        $gui = Join-Path $root 'cmd\pchat-gui\build\bin\pchat-gui.app\Contents\MacOS\pchat-gui'
        if (-not (Test-Path -LiteralPath $gui)) {
            throw "macOS GUI binary missing: $gui"
        }
        Assert-UnixArch -Path $gui -Expected $Arch
        & (Join-Path $PSScriptRoot 'package-platform-archive.ps1') -Platform mac -Arch $Arch
        & (Join-Path $PSScriptRoot 'package-update-zip.ps1') -Platform mac -Arch $Arch -RequireSetup
    }
}

Write-Host "[ci-release-build] validating release artifacts" -ForegroundColor Cyan
& (Join-Path $PSScriptRoot 'validate-release-artifacts.ps1') -Directory (Join-Path $root 'bin') -RequireSignature
if ($LASTEXITCODE -ne 0) {
    throw "validate-release-artifacts failed: $LASTEXITCODE"
}
Write-Host "[ci-release-build] $Platform/$Arch packages verified" -ForegroundColor Green
