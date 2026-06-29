# P-Chat Windows installer (PowerShell).
#
# Usage:
#   .\install.ps1                       # install to %LOCALAPPDATA%\Programs\P-Chat\
#   .\install.ps1 -InstallDir C:\P-Chat
#   .\install.ps1 -NoStartMenu          # skip Start Menu shortcut
#   .\install.ps1 -Portable             # copy beside the script, do not touch %LOCALAPPDATA%
#   .\install.ps1 -AddToPath            # add install dir to user PATH (for pchat CLI)
#
# Uninstall: run uninstall.ps1 next to pchat-gui.exe.
#
# The script does NOT require admin. It writes per-user.

[CmdletBinding()]
param(
    [string] $InstallDir = "",
    [switch] $NoStartMenu,
    [switch] $Portable,
    [switch] $AddToPath
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
    $srcCli = Join-Path $scriptDir "pchat.exe"
    if (Test-Path -LiteralPath $srcCli) {
        Copy-Item -LiteralPath $srcCli -Destination (Join-Path $target "pchat.exe") -Force
        Write-Host "[install] included pchat CLI"
    }
    Write-Host "[install] copied binaries to $target"
} else {
    Write-Host "[install] portable mode, target == source; skipping copy"
}

# --- Start Menu shortcut --------------------------------------------------
if (-not $NoStartMenu -and -not $Portable) {
    $startMenu = [Environment]::GetFolderPath("Programs")
    New-Item -ItemType Directory -Path $startMenu -Force | Out-Null

    try {
        $shell = New-Object -ComObject WScript.Shell
        $guiExe = Join-Path $target "pchat-gui.exe"

        # Create the main shortcut
        $linkPath = Join-Path $startMenu "P-Chat.lnk"
        $shortcut = $shell.CreateShortcut($linkPath)
        $shortcut.TargetPath       = $guiExe
        $shortcut.WorkingDirectory = $target
        $shortcut.IconLocation     = "$guiExe,0"
        $shortcut.Description      = "P-Chat AI Desktop"
        $shortcut.Save()
        Write-Host "[install] start menu: $linkPath -> $guiExe"

        # Uninstall shortcut
        $uninstPath = Join-Path $startMenu "P-Chat Uninstall.lnk"
        $u = $shell.CreateShortcut($uninstPath)
        $u.TargetPath       = "powershell.exe"
        $u.Arguments        = "-NoProfile -ExecutionPolicy Bypass -File `"$target\uninstall.ps1`""
        $u.WorkingDirectory = $target
        $u.Description      = "Uninstall P-Chat"
        $u.Save()
        Write-Host "[install] start menu: $uninstPath"
    } catch {
        Write-Warning "[install] 快捷方式创建失败: $_"
        Write-Warning "[install] 你可以手动启动: $guiExe"
    }
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

# --- User PATH (for pchat CLI) ---
if ($AddToPath -and -not $Portable) {
    $currentPath = [Environment]::GetEnvironmentVariable("Path", "User")
    if ($currentPath -notlike "*$target*") {
        [Environment]::SetEnvironmentVariable("Path", "$currentPath;$target", "User")
        Write-Host "[install] added to user PATH: $target"
        Write-Host "[install] NOTE: restart terminal for 'pchat' command to be available."
    } else {
        Write-Host "[install] $target is already in user PATH"
    }
}

Write-Host "[install] done.  Launch: $target\pchat-gui.exe"
