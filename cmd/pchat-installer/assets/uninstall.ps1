# P-Chat Windows uninstaller (PowerShell).
#
# Usage:
#   .\uninstall.ps1                 # removes the default install
#   .\uninstall.ps1 -InstallDir C:\P-Chat
#   .\uninstall.ps1 -RemoveData     # also delete ~/.p-chat/

[CmdletBinding()]
param(
    [string] $InstallDir = "",
    [switch] $RemoveData
)

$ErrorActionPreference = "SilentlyContinue"

$scriptDir = $PSCommandPath | Split-Path -Parent
$here = (Resolve-Path -LiteralPath $scriptDir).Path

if (-not $InstallDir) {
    $InstallDir = $here
}

Write-Host "[uninstall] target: $InstallDir"

# 1. Kill any running pchat-gui / pchat-server from this install.
Get-Process -Name "pchat-gui","pchat-server" -ErrorAction SilentlyContinue |
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

# 2. Remove Start Menu shortcuts
$startMenu = [Environment]::GetFolderPath("Programs")
Remove-Item -LiteralPath (Join-Path $startMenu "P-Chat.lnk")             -Force -ErrorAction SilentlyContinue
Remove-Item -LiteralPath (Join-Path $startMenu "P-Chat Uninstall.lnk")   -Force -ErrorAction SilentlyContinue
Write-Host "[uninstall] removed Start Menu shortcuts"

# 3. Remove registry uninstall entry
Remove-Item -Path "HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\P-Chat" -Recurse -Force -ErrorAction SilentlyContinue
Write-Host "[uninstall] removed registry entry"

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

# --- Remove from user PATH ---
$currentPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($currentPath -like "*$InstallDir*") {
    $newPath = ($currentPath -split ';' | Where-Object { $_ -ne $InstallDir }) -join ';'
    [Environment]::SetEnvironmentVariable("Path", $newPath, "User")
    Write-Host "[uninstall] removed $InstallDir from user PATH"
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
