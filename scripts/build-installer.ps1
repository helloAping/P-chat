<#
.SYNOPSIS
  Assembles a native setup program for one platform.

.DESCRIPTION
  Windows produces bin/pchat-setup-v{version}.exe.
  Linux produces the ELF bin/pchat-linux-setup-v{version}.
  macOS produces bin/pchat-mac-setup-v{version}.dmg, a disk image whose
  volume contains pchat-setup.app.

  The setup program embeds that platform's GUI, server, CLI, updater, and
  install script, then runs the install script when opened.
#>

param(
    [ValidateSet('win', 'linux', 'mac')]
    [string]$Platform = 'win',

    [string]$Arch = 'amd64'
)

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"
$root = Split-Path -Parent $PSScriptRoot

$versionFile = Join-Path $root "VERSION"
$version = "0.1.0"
if (Test-Path -LiteralPath $versionFile) {
    $v = (Get-Content -LiteralPath $versionFile -Raw).Trim()
    if ($v) { $version = $v }
}

$bin = Join-Path $root "bin"
$assets = Join-Path $root "cmd\pchat-installer\assets"
$webDir = Join-Path $root "web"

function Reset-Assets {
    if (Test-Path -LiteralPath $assets) {
        Remove-Item -LiteralPath $assets -Recurse -Force
    }
    New-Item -ItemType Directory -Path $assets -Force | Out-Null
}

function Copy-WebAssets {
    if (-not (Test-Path -LiteralPath $webDir)) {
        return
    }
    Write-Host "[build-installer] Copy web/ -> $assets\web"
    Copy-Item -LiteralPath $webDir -Destination (Join-Path $assets "web") -Recurse -Force
}

function Require-File {
    param([Parameter(Mandatory = $true)][string]$Path, [Parameter(Mandatory = $true)][string]$Hint)
    if (-not (Test-Path -LiteralPath $Path)) {
        Write-Error "Missing required file: $Path -- $Hint"
        exit 1
    }
}

function Build-SetupBinary {
    param(
        [Parameter(Mandatory = $true)][string]$Output,
        [Parameter(Mandatory = $true)][string]$GOOS,
        [Parameter(Mandatory = $true)][string]$GOARCH
    )
    Write-Host "[build-installer] go build -> $Output"
    $oldGOOS = $env:GOOS
    $oldGOARCH = $env:GOARCH
    $oldCGO = $env:CGO_ENABLED
    $env:GOOS = $GOOS
    $env:GOARCH = $GOARCH
    $env:CGO_ENABLED = "0"
    try {
        $goArgs = @("build", "-o", $Output)
        if ($GOOS -eq "windows") {
            $goArgs += @("-ldflags", "-H=windowsgui")
        }
        $goArgs += (Join-Path $root "cmd\pchat-installer")
        & go @goArgs
        if ($LASTEXITCODE -ne 0) {
            exit $LASTEXITCODE
        }
    } finally {
        if ($null -eq $oldGOOS) { Remove-Item Env:GOOS -ErrorAction SilentlyContinue } else { $env:GOOS = $oldGOOS }
        if ($null -eq $oldGOARCH) { Remove-Item Env:GOARCH -ErrorAction SilentlyContinue } else { $env:GOARCH = $oldGOARCH }
        if ($null -eq $oldCGO) { Remove-Item Env:CGO_ENABLED -ErrorAction SilentlyContinue } else { $env:CGO_ENABLED = $oldCGO }
    }
}

Reset-Assets

switch ($Platform) {
    'win' {
        $guiExe = Join-Path $bin "pchat-gui.exe"
        $serverExe = Join-Path $bin "pchat-server.exe"
        $cliExe = Join-Path $bin "pchat.exe"
        $updaterExe = Join-Path $bin "pchat-updater.exe"
        foreach ($pair in @(
                @($guiExe, "run 'task build:gui'"),
                @($serverExe, "run 'task build'"),
                @($cliExe, "run 'task build'"),
                @($updaterExe, "run 'task build'")
            )) {
            Require-File -Path $pair[0] -Hint $pair[1]
        }
        Write-Host "[build-installer] Copy binaries -> $assets"
        Copy-Item -LiteralPath $guiExe -Destination (Join-Path $assets "pchat-gui.exe") -Force
        Copy-Item -LiteralPath $serverExe -Destination (Join-Path $assets "pchat-server.exe") -Force
        Copy-Item -LiteralPath $cliExe -Destination (Join-Path $assets "pchat.exe") -Force
        Copy-Item -LiteralPath $updaterExe -Destination (Join-Path $assets "pchat-updater.exe") -Force
        Copy-WebAssets
        Write-Host "[build-installer] Copy install scripts"
        Copy-Item -LiteralPath (Join-Path $root "cmd\pchat-gui\install.ps1") -Destination (Join-Path $assets "install.ps1") -Force
        Copy-Item -LiteralPath (Join-Path $root "cmd\pchat-gui\uninstall.ps1") -Destination (Join-Path $assets "uninstall.ps1") -Force
        $outPath = Join-Path $bin "pchat-setup-v$version.exe"
        Build-SetupBinary -Output $outPath -GOOS windows -GOARCH amd64
        Write-Host "[build-installer] Done: $outPath"
    }
    'linux' {
        $gui = Join-Path $root "cmd\pchat-gui\build\bin\pchat-gui"
        Require-File -Path $gui -Hint "run 'task build:gui:linux:binary'"
        Require-File -Path (Join-Path $bin "pchat-server-linux") -Hint "run 'task build:server:linux'"
        Require-File -Path (Join-Path $bin "pchat-linux") -Hint "run 'task build:cli:linux'"
        Require-File -Path (Join-Path $bin "pchat-updater-linux") -Hint "run 'task build:updater:linux'"
        Write-Host "[build-installer] Copy Linux payload -> $assets"
        Copy-Item -LiteralPath $gui -Destination (Join-Path $assets "pchat-gui") -Force
        Copy-Item -LiteralPath (Join-Path $bin "pchat-server-linux") -Destination (Join-Path $assets "pchat-server") -Force
        Copy-Item -LiteralPath (Join-Path $bin "pchat-linux") -Destination (Join-Path $assets "pchat") -Force
        Copy-Item -LiteralPath (Join-Path $bin "pchat-updater-linux") -Destination (Join-Path $assets "pchat-updater") -Force
        Copy-WebAssets
        Copy-Item -LiteralPath (Join-Path $root "scripts\install-linux.sh") -Destination (Join-Path $assets "install.sh") -Force
        Copy-Item -LiteralPath (Join-Path $root "scripts\uninstall-linux.sh") -Destination (Join-Path $assets "uninstall.sh") -Force
        $outPath = Join-Path $bin "pchat-linux-setup-v$version"
        Build-SetupBinary -Output $outPath -GOOS linux -GOARCH amd64
        Write-Host "[build-installer] Done: $outPath"
    }
    'mac' {
        if ($Arch -ne 'arm64' -and $Arch -ne 'amd64') {
            throw "macOS setup arch must be arm64 or amd64, got $Arch"
        }
        $app = Join-Path $root "cmd\pchat-gui\build\bin\pchat-gui.app"
        Require-File -Path (Join-Path $app "Contents\MacOS\pchat-gui") -Hint "run 'task build:gui:macos:binary'"
        Require-File -Path (Join-Path $bin "pchat-server-darwin-$Arch") -Hint "run 'task build:server:macos ARCH=$Arch'"
        Require-File -Path (Join-Path $bin "pchat-darwin-$Arch") -Hint "run 'task build:cli:macos ARCH=$Arch'"
        Require-File -Path (Join-Path $bin "pchat-updater-darwin-$Arch") -Hint "run 'task build:updater:macos ARCH=$Arch'"
        Write-Host "[build-installer] Copy macOS payload -> $assets"
        $stagedApp = Join-Path $assets "pchat-gui.app"
        Copy-Item -LiteralPath $app -Destination $stagedApp -Recurse -Force
        $resources = Join-Path $stagedApp "Contents\Resources"
        New-Item -ItemType Directory -Path $resources -Force | Out-Null
        Copy-Item -LiteralPath (Join-Path $bin "pchat-server-darwin-$Arch") -Destination (Join-Path $resources "pchat-server") -Force
        Copy-Item -LiteralPath (Join-Path $bin "pchat-updater-darwin-$Arch") -Destination (Join-Path $resources "pchat-updater") -Force
        Copy-Item -LiteralPath (Join-Path $bin "pchat-darwin-$Arch") -Destination (Join-Path $assets "pchat") -Force
        Copy-WebAssets
        Copy-Item -LiteralPath (Join-Path $root "scripts\install-macos.sh") -Destination (Join-Path $assets "install.sh") -Force
        Copy-Item -LiteralPath (Join-Path $root "scripts\uninstall-macos.sh") -Destination (Join-Path $assets "uninstall.sh") -Force
        $binary = Join-Path $bin "pchat-mac-setup-v$version"
        Build-SetupBinary -Output $binary -GOOS darwin -GOARCH $Arch

        $diskRoot = Join-Path $bin "pchat-mac-setup-dmg"
        if (Test-Path -LiteralPath $diskRoot) { Remove-Item -LiteralPath $diskRoot -Recurse -Force }
        $setupApp = Join-Path $diskRoot "pchat-setup.app"
        $macOSDir = Join-Path $setupApp "Contents\MacOS"
        New-Item -ItemType Directory -Path $macOSDir -Force | Out-Null
        Copy-Item -LiteralPath $binary -Destination (Join-Path $macOSDir "pchat-setup") -Force
        $plist = @"
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleExecutable</key><string>pchat-setup</string>
  <key>CFBundleIdentifier</key><string>cn.08ms.pchat.setup</string>
  <key>CFBundleName</key><string>P-Chat Setup</string>
  <key>CFBundleDisplayName</key><string>P-Chat Setup</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>$version</string>
  <key>CFBundleVersion</key><string>$version</string>
  <key>LSMinimumSystemVersion</key><string>11.0</string>
  <key>NSHighResolutionCapable</key><true/>
</dict>
</plist>
"@
        [System.IO.File]::WriteAllText((Join-Path $setupApp "Contents\Info.plist"), $plist, [System.Text.UTF8Encoding]::new($false))
        $dmg = Join-Path $bin "pchat-mac-setup-v$version.dmg"
        if (Test-Path -LiteralPath $dmg) { Remove-Item -LiteralPath $dmg -Force }
        Write-Host "[build-installer] hdiutil -> $dmg"
        & hdiutil create -volname "P-Chat $version" -srcfolder $diskRoot -ov -format UDZO $dmg
        if ($LASTEXITCODE -ne 0) {
            throw "hdiutil failed: $LASTEXITCODE"
        }
        Remove-Item -LiteralPath $diskRoot -Recurse -Force
        Write-Host "[build-installer] Done: $dmg"
    }
}
