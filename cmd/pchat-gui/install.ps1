# P-Chat Windows installer (PowerShell).
#
# Usage:
#   .\install.ps1                       # install to %LOCALAPPDATA%\Programs\P-Chat\
#   .\install.ps1 -InstallDir C:\P-Chat
#   .\install.ps1 -NoStartMenu          # skip Start Menu shortcut
#   .\install.ps1 -Portable             # copy beside the script, do not touch %LOCALAPPDATA%
#
# Uninstall: run uninstall.ps1 next to pchat-gui.exe.
#
# The script does NOT require admin. It writes per-user.

[CmdletBinding()]
param(
    [string] $InstallDir = "",
    [switch] $NoStartMenu,
    [switch] $Portable
)

$ErrorActionPreference = "Stop"

# --- paths ----------------------------------------------------------------
$scriptDir = $PSCommandPath | Split-Path -Parent
$here      = (Resolve-Path -LiteralPath $scriptDir).Path
$srcGui    = Join-Path $here "pchat-gui.exe"
$srcServer = Join-Path $here "pchat-server.exe"

if (-not (Test-Path -LiteralPath $srcGui))    { throw "pchat-gui.exe not found next to install.ps1 ($here)" }
if (-not (Test-Path -LiteralPath $srcServer)) { throw "pchat-server.exe not found next to install.ps1 ($here)" }

if ($Portable) {
    $target = $here
} else {
    if (-not $InstallDir) {
        $InstallDir = Join-Path $env:LOCALAPPDATA "Programs\P-Chat"
    }
    $target = $InstallDir
}

Write-Host "[install] target: $target"

# --- copy binaries --------------------------------------------------------
New-Item -ItemType Directory -Path $target -Force | Out-Null
if ((Resolve-Path -LiteralPath $target).Path -ne $here) {
    Copy-Item -LiteralPath $srcGui    -Destination (Join-Path $target "pchat-gui.exe")    -Force
    Copy-Item -LiteralPath $srcServer -Destination (Join-Path $target "pchat-server.exe") -Force
    Copy-Item -LiteralPath (Join-Path $scriptDir "uninstall.ps1") -Destination (Join-Path $target "uninstall.ps1") -Force
    Write-Host "[install] copied binaries to $target"
} else {
    Write-Host "[install] portable mode, target == source; skipping copy"
}

# --- Start Menu shortcut --------------------------------------------------
if (-not $NoStartMenu -and -not $Portable) {
    $shell = New-Object -ComObject WScript.Shell
    $startMenu = [Environment]::GetFolderPath("Programs")
    $linkPath = Join-Path $startMenu "P-Chat.lnk"
    $shortcut = $shell.CreateShortcut($linkPath)
    $shortcut.TargetPath       = Join-Path $target "pchat-gui.exe"
    $shortcut.WorkingDirectory = $target
    $shortcut.IconLocation     = Join-Path $target "pchat-gui.exe,0"
    $shortcut.Description      = "P-Chat Desktop"
    $shortcut.Save()
    Write-Host "[install] Start Menu shortcut: $linkPath"

    # Uninstall shortcut
    $uninstPath = Join-Path $startMenu "P-Chat Uninstall.lnk"
    $u = $shell.CreateShortcut($uninstPath)
    $u.TargetPath       = "powershell.exe"
    $u.Arguments        = "-NoProfile -ExecutionPolicy Bypass -File `"$target\uninstall.ps1`""
    $u.WorkingDirectory = $target
    $u.Description      = "Uninstall P-Chat"
    $u.Save()
    Write-Host "[install] uninstall shortcut:  $uninstPath"
}

# --- registry: uninstall entry -------------------------------------------
if (-not $Portable) {
    $regPath = "HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\P-Chat"
    New-Item -Path $regPath -Force | Out-Null
    Set-ItemProperty -LiteralPath $regPath -Name "DisplayName"     -Value "P-Chat"
    Set-ItemProperty -LiteralPath $regPath -Name "DisplayVersion"  -Value "0.1.0"
    Set-ItemProperty -LiteralPath $regPath -Name "Publisher"       -Value "P-Chat"
    Set-ItemProperty -LiteralPath $regPath -Name "InstallLocation" -Value $target
    Set-ItemProperty -LiteralPath $regPath -Name "UninstallString" -Value "powershell.exe -NoProfile -ExecutionPolicy Bypass -File `"$target\uninstall.ps1`""
    Set-ItemProperty -LiteralPath $regPath -Name "DisplayIcon"     -Value "$target\pchat-gui.exe,0"
    Set-ItemProperty -LiteralPath $regPath -Name "NoModify"        -Value 1
    Set-ItemProperty -LiteralPath $regPath -Name "NoRepair"        -Value 1
    Write-Host "[install] registered uninstall entry: $regPath"
}

Write-Host "[install] done.  Launch: $target\pchat-gui.exe"
