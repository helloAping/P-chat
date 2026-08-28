<#
.SYNOPSIS
  Assemble a platform setup archive from already-built artifacts.

.DESCRIPTION
  This script is the final archive step used by task build:linux and
  task build:mac. It intentionally runs on Windows too: Go server/CLI
  binaries can be cross-compiled there, while the Wails GUI binary is
  included when a native build artifact is available.
#>

[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [Alias('PlatformName', 'Name')]
    [string]$Platform,

    [switch]$AllowMissingGui
)

$ErrorActionPreference = 'Stop'

$root = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '..')).Path
$platformLabel = & "$PSScriptRoot\resolve-platform.ps1" -Name $Platform -Format label
if (-not $platformLabel) {
    throw "Unknown platform: $Platform"
}

$versionFile = Join-Path $root 'VERSION'
$version = '0.0.0'
if (Test-Path -LiteralPath $versionFile) {
    $v = (Get-Content -LiteralPath $versionFile -Raw).Trim()
    if ($v) { $version = $v }
}

$stageRoot = Join-Path $root 'build\package'
$stage = Join-Path $stageRoot ("pchat-{0}-setup" -f $platformLabel)
$outDir = Join-Path $root 'bin'
New-Item -ItemType Directory -Path $stageRoot -Force | Out-Null
New-Item -ItemType Directory -Path $outDir -Force | Out-Null
if (Test-Path -LiteralPath $stage) {
    Remove-Item -LiteralPath $stage -Recurse -Force
}
New-Item -ItemType Directory -Path $stage -Force | Out-Null

function Require-File {
    param([Parameter(Mandatory = $true)][string]$Path, [Parameter(Mandatory = $true)][string]$Hint)
    if (-not (Test-Path -LiteralPath $Path)) {
        throw "Missing required file: $Path -- $Hint"
    }
}

function Copy-RequiredFile {
    param(
        [Parameter(Mandatory = $true)][string]$Source,
        [Parameter(Mandatory = $true)][string]$Destination,
        [Parameter(Mandatory = $true)][string]$Hint
    )
    Require-File -Path $Source -Hint $Hint
    Copy-Item -LiteralPath $Source -Destination $Destination -Force
}

function Copy-OptionalFile {
    param([string]$Source, [string]$Destination)
    if ($Source -and (Test-Path -LiteralPath $Source)) {
        Copy-Item -LiteralPath $Source -Destination $Destination -Force
        return $true
    }
    return $false
}

function Copy-WebAssets {
    $webSrc = Join-Path $root 'web'
    $webDst = Join-Path $stage 'web'
    if (Test-Path -LiteralPath $webSrc) {
        Copy-Item -LiteralPath $webSrc -Destination $webDst -Recurse -Force
    }
}

function Find-FirstExisting {
    param([string[]]$Candidates)
    foreach ($candidate in $Candidates) {
        if (Test-Path -LiteralPath $candidate) {
            return (Resolve-Path -LiteralPath $candidate).Path
        }
    }
    return ''
}

$guiIncluded = $false
$notes = New-Object System.Collections.Generic.List[string]

switch ($platformLabel) {
    'linux' {
        Copy-RequiredFile -Source (Join-Path $root 'bin\pchat-server-linux') -Destination (Join-Path $stage 'pchat-server') -Hint "run 'task build:server:linux'"
        Copy-RequiredFile -Source (Join-Path $root 'bin\pchat-linux') -Destination (Join-Path $stage 'pchat') -Hint "run 'task build:cli:linux'"
        Copy-RequiredFile -Source (Join-Path $root 'bin\pchat-updater-linux') -Destination (Join-Path $stage 'pchat-updater') -Hint "run 'task build:updater:linux'"
        Copy-RequiredFile -Source (Join-Path $root 'scripts\install-linux.sh') -Destination (Join-Path $stage 'install.sh') -Hint 'missing install script'
        Copy-RequiredFile -Source (Join-Path $root 'scripts\uninstall-linux.sh') -Destination (Join-Path $stage 'uninstall.sh') -Hint 'missing uninstall script'
        Copy-WebAssets
        Copy-OptionalFile -Source (Join-Path $root 'cmd\pchat-server\browser-extension.zip') -Destination (Join-Path $stage 'browser-extension.zip') | Out-Null

        $gui = Find-FirstExisting @(
            (Join-Path $root 'cmd\pchat-gui\build\bin\pchat-gui'),
            (Join-Path $root 'build\artifacts\linux\pchat-gui'),
            (Join-Path $root 'artifacts\linux\pchat-gui')
        )
        if ($gui) {
            Copy-Item -LiteralPath $gui -Destination (Join-Path $stage 'pchat-gui') -Force
            $guiIncluded = $true
        } else {
            $notes.Add('Linux Wails GUI binary is missing. Build on Linux or provide artifacts/linux/pchat-gui before packaging.')
        }
        break
    }
    'mac' {
        Copy-RequiredFile -Source (Join-Path $root 'bin\pchat-server-darwin-amd64') -Destination (Join-Path $stage 'pchat-server') -Hint "run 'task build:server:macos'"
        Copy-RequiredFile -Source (Join-Path $root 'bin\pchat-darwin-amd64') -Destination (Join-Path $stage 'pchat') -Hint "run 'task build:cli:macos'"
        Copy-RequiredFile -Source (Join-Path $root 'bin\pchat-updater-darwin-amd64') -Destination (Join-Path $stage 'pchat-updater') -Hint "run 'task build:updater:macos'"
        Copy-RequiredFile -Source (Join-Path $root 'scripts\install-macos.sh') -Destination (Join-Path $stage 'install.sh') -Hint 'missing install script'
        Copy-RequiredFile -Source (Join-Path $root 'scripts\uninstall-macos.sh') -Destination (Join-Path $stage 'uninstall.sh') -Hint 'missing uninstall script'
        Copy-WebAssets
        Copy-OptionalFile -Source (Join-Path $root 'cmd\pchat-server\browser-extension.zip') -Destination (Join-Path $stage 'browser-extension.zip') | Out-Null

        $app = Find-FirstExisting @(
            (Join-Path $root 'cmd\pchat-gui\build\bin\pchat-gui.app'),
            (Join-Path $root 'build\artifacts\mac\pchat-gui.app'),
            (Join-Path $root 'artifacts\mac\pchat-gui.app')
        )
        if ($app) {
            $dstApp = Join-Path $stage 'pchat-gui.app'
            Copy-Item -LiteralPath $app -Destination $dstApp -Recurse -Force
            $resources = Join-Path $dstApp 'Contents\Resources'
            New-Item -ItemType Directory -Path $resources -Force | Out-Null
            Copy-Item -LiteralPath (Join-Path $stage 'pchat-server') -Destination (Join-Path $resources 'pchat-server') -Force
            Copy-Item -LiteralPath (Join-Path $stage 'pchat-updater') -Destination (Join-Path $resources 'pchat-updater') -Force
            Copy-OptionalFile -Source (Join-Path $stage 'pchat') -Destination (Join-Path $resources 'pchat') | Out-Null
            Copy-OptionalFile -Source (Join-Path $stage 'browser-extension.zip') -Destination (Join-Path $resources 'browser-extension.zip') | Out-Null
            $guiIncluded = $true
        } else {
            $notes.Add('macOS Wails .app is missing. Build on macOS or provide artifacts/mac/pchat-gui.app before packaging.')
        }
        break
    }
    default {
        throw "package-platform-archive only supports linux and mac. Use task build:win for Windows setup."
    }
}

if (-not $guiIncluded -and -not $AllowMissingGui) {
    throw "GUI artifact missing for $platformLabel. Re-run with -AllowMissingGui to create a partial archive."
}

$manifest = @(
    "P-Chat setup archive",
    "version=$version",
    "platform=$platformLabel",
    "gui_included=$guiIncluded",
    "created_at_utc=$((Get-Date).ToUniversalTime().ToString('s'))Z",
    "",
    "Install:",
    "  ./install.sh",
    "",
    "Notes:"
)
if ($notes.Count -eq 0) {
    $manifest += "  none"
} else {
    foreach ($note in $notes) {
        $manifest += "  - $note"
    }
}
[System.IO.File]::WriteAllText((Join-Path $stage 'MANIFEST.txt'), ($manifest -join [Environment]::NewLine), [System.Text.UTF8Encoding]::new($false))

if (-not $guiIncluded) {
    [System.IO.File]::WriteAllText((Join-Path $stage 'MISSING_GUI.txt'), ($notes -join [Environment]::NewLine), [System.Text.UTF8Encoding]::new($false))
}

$archiveBase = Join-Path $outDir ("pchat-{0}-setup-v{1}" -f $platformLabel, $version)
$tarPath = "$archiveBase.tar.gz"
$zipPath = "$archiveBase.zip"
Remove-Item -LiteralPath $tarPath, $zipPath -Force -ErrorAction SilentlyContinue

$tarCmd = Get-Command tar -ErrorAction SilentlyContinue
if ($tarCmd) {
    Push-Location -LiteralPath $stageRoot
    try {
        & tar -czf $tarPath (Split-Path -Leaf $stage)
        if ($LASTEXITCODE -ne 0) {
            throw "tar exited with code $LASTEXITCODE"
        }
        Write-Host "[package-platform-archive] ready: $tarPath" -ForegroundColor Green
    } finally {
        Pop-Location
    }
} else {
    Compress-Archive -LiteralPath $stage -DestinationPath $zipPath -Force
    Write-Host "[package-platform-archive] ready: $zipPath" -ForegroundColor Green
}

if (-not $guiIncluded) {
    Write-Host "[package-platform-archive] WARNING: GUI artifact missing; archive is partial." -ForegroundColor Yellow
}
