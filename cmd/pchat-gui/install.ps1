# P-Chat Windows installer (PowerShell).
#
# Usage:
#   .\install.ps1                       # install to PCHAT_HOME if set, else
#                                      # %LOCALAPPDATA%\Programs\P-Chat\
#   .\install.ps1 -InstallDir C:\P-Chat # override target explicitly
#   .\install.ps1 -NoStartMenu          # skip Start Menu shortcut
#   .\install.ps1 -Portable             # copy beside the script, do not touch %LOCALAPPDATA%
#   .\install.ps1 -AddToPath            # set PCHAT_HOME and inject %PCHAT_HOME% into user PATH
#   .\install.ps1 -Force                # overwrite even when the running install path differs
#
# Uninstall: run uninstall.ps1 next to pchat-gui.exe.
#
# When -AddToPath is passed, install.ps1 sets a user-level
# PCHAT_HOME environment variable to the install path, then
# appends "%PCHAT_HOME%" to user PATH. Re-installing into a new
# directory only requires updating PCHAT_HOME — the PATH entry
# references the variable and follows it automatically.
#
# Re-install detection: PCHAT_HOME is the source of truth.
# If PCHAT_HOME is set, install.ps1 installs into that
# directory (creating it if needed), overwriting the
# previous binaries in place. The user can always override
# with -InstallDir. Any pchat-gui / pchat-server / pchat
# processes whose executable lives in the target dir are
# stopped first — otherwise the Copy-Item would fail with
# "file in use" on the running binary.
#
# The script does NOT require admin. It writes per-user.

[CmdletBinding()]
param(
    [string] $InstallDir = "",
    [switch] $NoStartMenu,
    [switch] $Portable,
    [switch] $AddToPath,
    [switch] $Force
)

$ErrorActionPreference = "Stop"

# --- paths ----------------------------------------------------------------
$scriptDir = $PSCommandPath | Split-Path -Parent
$here      = (Resolve-Path -LiteralPath $scriptDir).Path
$srcGui    = Join-Path $here "pchat-gui.exe"
$srcServer = Join-Path $here "pchat-server.exe"
$srcCli    = Join-Path $here "pchat.exe"

if (-not (Test-Path -LiteralPath $srcGui))    { throw "pchat-gui.exe not found next to install.ps1 ($here)" }
if (-not (Test-Path -LiteralPath $srcServer)) { throw "pchat-server.exe not found next to install.ps1 ($here)" }
if (-not (Test-Path -LiteralPath $srcCli))    { throw "pchat.exe not found next to install.ps1 ($here)" }

# Default install path: %LOCALAPPDATA%\Programs\P-Chat. We
# may override this with PCHAT_HOME (set by a previous
# install with -AddToPath, or by the user manually).
$defaultDir = Join-Path $env:LOCALAPPDATA "Programs\P-Chat"

# --- detect a previous install -------------------------------------------
# PCHAT_HOME is the source of truth for "where is P-Chat
# installed". It is set by install.ps1 -AddToPath on first
# install, and updated on every re-install. If it points
# anywhere valid we treat that as the install location —
# the user wants to overwrite-in-place, not accumulate
# copies in different directories. (The HKCU\...\Uninstall
# entry is still written for Apps & Features visibility,
# but we no longer read it for detection — it can fall out
# of sync with reality if the user manually moves the dir
# or restores from backup.)
$prevDir = $null
$envHome = [Environment]::GetEnvironmentVariable("PCHAT_HOME", "User")
if ($envHome) {
    $prevDir = (Resolve-Path -LiteralPath $envHome -ErrorAction SilentlyContinue).Path
    if ($prevDir) {
        Write-Host "[install] detected PCHAT_HOME=$prevDir — will overwrite in place"
    } else {
        Write-Host "[install] PCHAT_HOME=$envHome is set but path is invalid; treating as new install"
        $prevDir = $envHome
    }
} else {
    Write-Host "[install] no PCHAT_HOME set, defaulting to: $defaultDir"
}

if ($Portable) {
    $target = $here
} else {
    if (-not $InstallDir) {
        # Explicit -InstallDir wins; otherwise PCHAT_HOME
        # (the previous install location); otherwise the
        # default.
        if ($prevDir) {
            $InstallDir = $prevDir
        } else {
            $InstallDir = $defaultDir
        }
    }
    $target = $InstallDir
}

Write-Host "[install] target: $target"

# --- stop any running pchat-gui / pchat-server / pchat from the target dir -------
# Stopping a running pchat-gui.exe / pchat-server.exe / pchat.exe
# is required before the Copy-Item below, otherwise the
# in-use mapping on Windows would refuse the copy with
# "file in use". We only kill processes whose executable
# lives under $target, so an install to a different path
# doesn't accidentally quit an unrelated running install.
#
# -Force opts in to killing processes from *any* install
# dir, not just $target. Without it we leave a running
# install in another path alone — the user might have
# pinned a particular version there.
$stoppedAny = $false
Get-Process -Name "pchat-gui","pchat-server","pchat" -ErrorAction SilentlyContinue |
    ForEach-Object {
        $p = $null
        try {
            $exeDir = Split-Path -LiteralPath $_.MainModule.FileName -Parent
            $p = (Resolve-Path -LiteralPath $exeDir -ErrorAction Stop).Path
        } catch { return }
        if ($Force -or $p -eq $target) {
            Write-Host "[install] stopping PID=$($_.Id) ($($_.ProcessName)) at $p"
            $_ | Stop-Process -Force -ErrorAction SilentlyContinue
            $script:stoppedAny = $true
        } else {
            Write-Host "[install] PID=$($_.Id) ($($_.ProcessName)) at $p kept (different install; use -Force to override)"
        }
    }
# Give Windows a moment to actually release the file
# handles. Without this the next Copy-Item occasionally
# still races with the just-stopped process.
if ($stoppedAny) {
    Start-Sleep -Milliseconds 500
}

# --- copy binaries --------------------------------------------------------
# pchat-gui.exe and pchat-server.exe are required. pchat.exe
# (the CLI / REPL) is also copied so that -AddToPath can expose
# `pchat` as a global command — the user wants to type `pchat`
# in any terminal and have it land in the CLI REPL.
New-Item -ItemType Directory -Path $target -Force | Out-Null
if ((Resolve-Path -LiteralPath $target).Path -ne $here) {
    Copy-Item -LiteralPath $srcGui    -Destination (Join-Path $target "pchat-gui.exe")    -Force
    Copy-Item -LiteralPath $srcServer -Destination (Join-Path $target "pchat-server.exe") -Force
    Copy-Item -LiteralPath $srcCli    -Destination (Join-Path $target "pchat.exe")        -Force
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
# This entry is for "Apps & Features" visibility (Settings >
# Apps > Installed apps) — it lets the user uninstall P-Chat
# from the standard Windows control panel. We do NOT use
# it for re-install detection; PCHAT_HOME is the source of
# truth for that.
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

# --- PATH ----------------------------------------------------------------
# We expose the install dir to PATH *indirectly* via a PCHAT_HOME
# env var, then reference it from PATH as %PCHAT_HOME%. This way:
#   1. Re-installing into a different directory only requires
#      updating PCHAT_HOME — every consumer (PATH entry, your
#      own scripts, the uninstaller) follows the variable.
#   2. The user can see at a glance what PCHAT_HOME points to,
#      and can override it manually if they want to shadow the
#      install with a different copy.
#   3. The PATH entry itself is portable across reinstalls.
if ($AddToPath -and -not $Portable) {
    # PCHAT_HOME: the install root. Always write — even if the
    # user already had a PCHAT_HOME pointing elsewhere, they
    # explicitly asked us to manage PATH, so they presumably
    # want the variable aligned with the install.
    [Environment]::SetEnvironmentVariable("PCHAT_HOME", $target, "User")
    Write-Host "[install] set PCHAT_HOME=$target"

    # PATH: append the %PCHAT_HOME% reference if it isn't
    # already there. The literal "$target" form is treated as
    # the same entry — older installs may have inlined the
    # path; we collapse them to the variable form to keep
    # PATH tidy.
    $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
    $hasVar = $userPath -like "*%PCHAT_HOME%*"
    $hasLit = $userPath -like "*$target*"
    if (-not $hasVar) {
        if ($hasLit) {
            # Migrate the literal entry to the variable form.
            $userPath = $userPath.Replace($target, "%PCHAT_HOME%")
            [Environment]::SetEnvironmentVariable("Path", $userPath, "User")
            Write-Host "[install] migrated literal PATH entry to %PCHAT_HOME%"
        } else {
            [Environment]::SetEnvironmentVariable("Path", "$userPath;%PCHAT_HOME%", "User")
            Write-Host "[install] added to user PATH: %PCHAT_HOME%"
        }
    } else {
        Write-Host "[install] PATH already contains: %PCHAT_HOME%"
    }
}

Write-Host "[install] done.  Launch: $target\pchat-gui.exe"
