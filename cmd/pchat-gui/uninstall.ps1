# P-Chat Windows uninstaller (PowerShell).
#
# Usage:
#   .\uninstall.ps1                 # removes the default install
#   .\uninstall.ps1 -InstallDir C:\P-Chat
#   .\uninstall.ps1 -RemoveData     # also delete ~/.p-chat/
#
# This script only cleans the INSTALL side: binaries, Start Menu
# shortcuts, registry entry, and the PCHAT_HOME env var (when it
# still points at this install). It does NOT touch the data
# directory (memory / config / skills / …) — that lives under
# ~/.p-chat/ by default, or wherever PCHAT_DATA_HOME points, and
# is removed only when -RemoveData is passed. See
# install.ps1 for the PCHAT_HOME (install root) vs
# PCHAT_DATA_HOME (data dir) distinction.

[CmdletBinding()]
param(
    [string] $InstallDir = "",
    [switch] $RemoveData
)

$ErrorActionPreference = "SilentlyContinue"

$scriptDir = $PSCommandPath | Split-Path -Parent
$here = (Resolve-Path -LiteralPath $scriptDir).Path

function Test-SamePathText {
    param(
        [Parameter(Mandatory = $true)][string] $Left,
        [Parameter(Mandatory = $true)][string] $Right
    )

    return $Left.TrimEnd('\').Equals($Right.TrimEnd('\'), [System.StringComparison]::OrdinalIgnoreCase)
}

function Remove-PChatShortcutForInstall {
    param(
        [Parameter(Mandatory = $true)][string] $LinkPath,
        [Parameter(Mandatory = $true)][string] $InstallRoot
    )

    if (-not (Test-Path -LiteralPath $LinkPath)) { return }
    $shell = New-Object -ComObject WScript.Shell
    $shortcut = $shell.CreateShortcut($LinkPath)
    $targetPath = $shortcut.TargetPath
    if ($targetPath -and (Test-SamePathText -Left $targetPath -Right (Join-Path $InstallRoot "pchat-gui.exe"))) {
        Remove-Item -LiteralPath $LinkPath -Force -ErrorAction SilentlyContinue
    }
}

if (-not $InstallDir) {
    $InstallDir = $here
}

Write-Host "[uninstall] target: $InstallDir"

# 1. Kill any running pchat-gui / pchat-server / pchat (CLI REPL)
#    from this install.
Get-Process -Name "pchat-gui","pchat-server","pchat" -ErrorAction SilentlyContinue |
    Where-Object {
        try {
            $p = (Resolve-Path -LiteralPath (Split-Path -LiteralPath $_.MainModule.FileName -Parent) -ErrorAction Stop).Path
            $p -eq $InstallDir
        } catch { $false }
    } |
    ForEach-Object {
        Write-Host "[uninstall] stopping PID=$($_.Id) ($($_.ProcessName))"
        $_ | Stop-Process -Force -ErrorAction SilentlyContinue
    }
Start-Sleep -Milliseconds 500

# 2. 删除开始菜单快捷方式 / Remove Start Menu shortcuts
$startMenu = [Environment]::GetFolderPath("Programs")
Remove-Item -LiteralPath (Join-Path $startMenu "P-Chat.lnk")             -Force -ErrorAction SilentlyContinue
Remove-Item -LiteralPath (Join-Path $startMenu "P-Chat Uninstall.lnk")   -Force -ErrorAction SilentlyContinue
Write-Host "[uninstall] removed Start Menu shortcuts"

# 2a. 删除指向当前安装目录的桌面快捷方式 / Remove Desktop shortcut for this install
$desktop = [Environment]::GetFolderPath("Desktop")
Remove-PChatShortcutForInstall -LinkPath (Join-Path $desktop "P-Chat.lnk") -InstallRoot $InstallDir
Write-Host "[uninstall] removed Desktop shortcut"

# 3. Remove registry uninstall entry
Remove-Item -Path "HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\P-Chat" -Recurse -Force -ErrorAction SilentlyContinue
Write-Host "[uninstall] removed registry entry"

# 3a. 清理 install.ps1 -AddToPath 写入的 PATH/PCHAT_HOME / Clean PATH/PCHAT_HOME set by install.ps1 -AddToPath
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
$pchatHome = [Environment]::GetEnvironmentVariable("PCHAT_HOME", "User")
$installRoot = $InstallDir.TrimEnd('\')

# 按 PATH 段精确移除，避免误删前缀相似目录 / Remove exact PATH segments to avoid touching prefix-matched directories.
$segments = @()
if (-not [string]::IsNullOrWhiteSpace($userPath)) {
    $segments = $userPath -split ';' | Where-Object {
        $s = $_.Trim()
        $keep = $true
        if (-not $s) { $keep = $false }
        if ($s.Equals('%PCHAT_HOME%', [System.StringComparison]::OrdinalIgnoreCase)) { $keep = $false }
        if ($s.Equals('%PCHAT_HOME%\bin', [System.StringComparison]::OrdinalIgnoreCase)) { $keep = $false }
        $expanded = [Environment]::ExpandEnvironmentVariables($s)
        if ($expanded -and (Test-SamePathText -Left $expanded -Right $installRoot)) { $keep = $false }
        $keep
    }
}
$newPath = ($segments -join ';').TrimEnd(';')
if ($newPath -ne $userPath) {
    [Environment]::SetEnvironmentVariable("Path", $newPath, "User")
    Write-Host "[uninstall] removed PCHAT_HOME entry from user PATH"
} else {
    Write-Host "[uninstall] PCHAT_HOME entry not in user PATH"
}

if ($pchatHome -and (Test-SamePathText -Left $pchatHome -Right $installRoot)) {
    [Environment]::SetEnvironmentVariable("PCHAT_HOME", $null, "User")
    Write-Host "[uninstall] cleared PCHAT_HOME (was $pchatHome)"
} else {
    Write-Host "[uninstall] PCHAT_HOME kept (points to: $pchatHome)"
}

# 4. Remove the install directory. If the .ps1 itself lives in the
#    target, defer the delete to a fresh powershell process so we
#    don't try to delete the script that's currently running.
if ((Resolve-Path -LiteralPath $InstallDir -ErrorAction SilentlyContinue).Path -eq $here) {
    $cmd = "Start-Sleep -Milliseconds 500; Remove-Item -LiteralPath `"$InstallDir`" -Recurse -Force -ErrorAction SilentlyContinue"
    $encoded = [Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes($cmd))
    $arg = "-NoProfile -ExecutionPolicy Bypass -EncodedCommand $encoded"
    Start-Process -FilePath "powershell.exe" -ArgumentList $arg -WindowStyle Hidden
    Write-Host "[uninstall] scheduled removal of $InstallDir (script ran from inside it)"
} else {
    Remove-Item -LiteralPath $InstallDir -Recurse -Force -ErrorAction SilentlyContinue
    Write-Host "[uninstall] removed $InstallDir"
}

if ($RemoveData) {
    $dataDir = Join-Path $env:USERPROFILE ".p-chat"
    if (Test-Path -LiteralPath $dataDir) {
        Remove-Item -LiteralPath $dataDir -Recurse -Force -ErrorAction SilentlyContinue
        Write-Host "[uninstall] removed user data dir: $dataDir"
    }
} else {
    Write-Host "[uninstall] keeping user data dir: $((Join-Path $env:USERPROFILE '.p-chat'))  (use -RemoveData to also delete it)"
}

Write-Host "[uninstall] done."
