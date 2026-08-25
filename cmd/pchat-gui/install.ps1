# P-Chat Windows installer (PowerShell).
#
# Usage:
#   .\install.ps1                       # 显示可视化安装器 / show the visual installer
#   .\install.ps1 -InstallDir C:\P-Chat # override target explicitly
#   .\install.ps1 -NoStartMenu          # skip Start Menu shortcut
#   .\install.ps1 -DesktopShortcut      # 创建桌面快捷方式 / create a desktop shortcut
#   .\install.ps1 -Portable             # copy beside the script, do not touch %LOCALAPPDATA%
#   .\install.ps1 -AddToPath            # 配置用户 PATH / set PCHAT_HOME and inject %PCHAT_HOME% into user PATH
#   .\install.ps1 -RemoveFromPath       # 从用户 PATH 移除 / remove P-Chat from user PATH
#   .\install.ps1 -Gui                  # 显示可视化安装器 / show the visual installer
#   .\install.ps1 -Launch               # 安装后启动 / launch P-Chat after installation
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
# IMPORTANT: PCHAT_HOME is the INSTALL root (used only for
# PATH resolution). It is NOT the data directory (memory /
# config / skills / …). The data directory is resolved by the
# binary itself: PCHAT_DATA_HOME env var > sibling of binary
# (bin/dev-bin) > $HOME/.p-chat. install.ps1 never touches
# PCHAT_DATA_HOME — let the binary decide. See
# internal/paths/devhome.go for the full resolution chain.
#
# Old confusion (fixed in V4): PCHAT_HOME used to be
# conflated as both install root AND data-dir override. This
# meant that any install with -AddToPath wrote memory + config
# under the install directory (e.g. D:\develop\pchat\memory\)
# instead of under the user's $HOME/.p-chat. The V3→V4 upgrade
# step (stepV3toV4 in internal/upgrade/steps.go) rescues
# existing data stranded at <PCHAT_HOME>/memory/.
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
    [switch] $DesktopShortcut,
    [switch] $Portable,
    [switch] $AddToPath,
    [switch] $RemoveFromPath,
    [switch] $Gui,
    [switch] $Launch,
    [switch] $Force,
    [string] $GuiResultPath = ""
)

$ErrorActionPreference = "Stop"
$pathChoiceFromGui = $false
if ($PSBoundParameters.Count -eq 0) {
    $Gui = $true
}

# --- paths ----------------------------------------------------------------
$scriptDir = $PSCommandPath | Split-Path -Parent
$here      = (Resolve-Path -LiteralPath $scriptDir).Path
$srcGui    = Join-Path $here "pchat-gui.exe"
$srcServer = Join-Path $here "pchat-server.exe"
$srcCli    = Join-Path $here "pchat.exe"

if (-not (Test-Path -LiteralPath $srcGui))    { throw "pchat-gui.exe not found next to install.ps1 ($here)" }
if (-not (Test-Path -LiteralPath $srcServer)) { throw "pchat-server.exe not found next to install.ps1 ($here)" }
if (-not (Test-Path -LiteralPath $srcCli))    { throw "pchat.exe not found next to install.ps1 ($here)" }

function New-PChatShortcut {
    param(
        [Parameter(Mandatory = $true)][object] $Shell,
        [Parameter(Mandatory = $true)][string] $LinkPath,
        [Parameter(Mandatory = $true)][string] $TargetPath,
        [string] $Arguments = "",
        [string] $WorkingDirectory = "",
        [string] $IconLocation = "",
        [string] $Description = ""
    )

    $shortcut = $Shell.CreateShortcut($LinkPath)
    $shortcut.TargetPath = $TargetPath
    if ($Arguments) { $shortcut.Arguments = $Arguments }
    if ($WorkingDirectory) { $shortcut.WorkingDirectory = $WorkingDirectory }
    if ($IconLocation) { $shortcut.IconLocation = $IconLocation }
    if ($Description) { $shortcut.Description = $Description }
    $shortcut.Save()
}

function Test-SamePathText {
    param(
        [Parameter(Mandatory = $true)][string] $Left,
        [Parameter(Mandatory = $true)][string] $Right
    )

    return $Left.TrimEnd('\').Equals($Right.TrimEnd('\'), [System.StringComparison]::OrdinalIgnoreCase)
}

function Set-PChatUserPath {
    param(
        [Parameter(Mandatory = $true)][string] $InstallRoot,
        [Parameter(Mandatory = $true)][bool] $Enabled
    )

    $targetRoot = $InstallRoot.TrimEnd('\')
    $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
    $segments = @()
    if (-not [string]::IsNullOrWhiteSpace($userPath)) {
        $segments = $userPath -split ';'
    }

    $kept = @()
    foreach ($segment in $segments) {
        $s = $segment.Trim()
        if (-not $s) { continue }
        if ($s.Equals("%PCHAT_HOME%", [System.StringComparison]::OrdinalIgnoreCase)) { continue }
        if ($s.Equals("%PCHAT_HOME%\bin", [System.StringComparison]::OrdinalIgnoreCase)) { continue }

        $expanded = [Environment]::ExpandEnvironmentVariables($s)
        if ($expanded -and (Test-SamePathText -Left $expanded -Right $targetRoot)) { continue }
        $kept += $s
    }

    if ($Enabled) {
        [Environment]::SetEnvironmentVariable("PCHAT_HOME", $InstallRoot, "User")
        $kept += "%PCHAT_HOME%"
        Write-Host "[install] set PCHAT_HOME=$InstallRoot"
        Write-Host "[install] configured user PATH: %PCHAT_HOME%"
    } else {
        $pchatHome = [Environment]::GetEnvironmentVariable("PCHAT_HOME", "User")
        if ($pchatHome -and (Test-SamePathText -Left $pchatHome -Right $targetRoot)) {
            [Environment]::SetEnvironmentVariable("PCHAT_HOME", $null, "User")
            Write-Host "[install] cleared PCHAT_HOME"
        }
        Write-Host "[install] removed P-Chat from user PATH"
    }

    [Environment]::SetEnvironmentVariable("Path", ($kept -join ';'), "User")
}

function ConvertTo-PChatProcessArgument {
    param(
        [AllowEmptyString()][string] $Value
    )

    if ($null -eq $Value) { return "''" }
    if ($Value -match '^[A-Za-z0-9_./:\\-]+$') { return $Value }
    return "'" + ($Value -replace "'", "''") + "'"
}

function Join-PChatProcessArguments {
    param(
        [Parameter(Mandatory = $true)][string[]] $Arguments
    )

    return (($Arguments | ForEach-Object { ConvertTo-PChatProcessArgument $_ }) -join ' ')
}

function New-PChatInstallArguments {
    param(
        [Parameter(Mandatory = $true)][object] $Choice,
        [Parameter(Mandatory = $true)][bool] $PortableMode,
        [Parameter(Mandatory = $true)][bool] $ForceInstall,
        [Parameter(Mandatory = $true)][string] $ResultPath
    )

    $args = @("-STA", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", $PSCommandPath)
    if ($PortableMode) {
        $args += "-Portable"
    } else {
        $args += @("-InstallDir", $Choice.InstallDir)
    }
    if (-not $Choice.StartMenu) { $args += "-NoStartMenu" }
    if ($Choice.DesktopShortcut) { $args += "-DesktopShortcut" }
    if ($Choice.AddToPath) {
        $args += "-AddToPath"
    } else {
        $args += "-RemoveFromPath"
    }
    if ($Choice.Launch) { $args += "-Launch" }
    if ($ForceInstall) { $args += "-Force" }
    $args += @("-GuiResultPath", $ResultPath)
    return $args
}

function Start-PChatGuiInstall {
    param(
        [Parameter(Mandatory = $true)][object] $Choice,
        [Parameter(Mandatory = $true)][bool] $PortableMode,
        [Parameter(Mandatory = $true)][bool] $ForceInstall,
        [Parameter(Mandatory = $true)][string] $ResultPath,
        [Parameter(Mandatory = $true)][string] $OutputPath,
        [Parameter(Mandatory = $true)][string] $ErrorPath
    )

    foreach ($logPath in @($OutputPath, $ErrorPath, $ResultPath)) {
        if (Test-Path -LiteralPath $logPath) {
            Remove-Item -LiteralPath $logPath -Force -ErrorAction SilentlyContinue
        }
    }

    $childArgs = New-PChatInstallArguments -Choice $Choice -PortableMode $PortableMode -ForceInstall $ForceInstall -ResultPath $ResultPath
    Start-Process `
        -FilePath "powershell.exe" `
        -ArgumentList (Join-PChatProcessArguments $childArgs) `
        -WorkingDirectory $here `
        -WindowStyle Hidden `
        -RedirectStandardOutput $OutputPath `
        -RedirectStandardError $ErrorPath `
        -PassThru
}

function Read-PChatInstallLog {
    param(
        [Parameter(Mandatory = $true)][string] $Path
    )

    if (-not (Test-Path -LiteralPath $Path)) { return "" }
    return (Get-Content -LiteralPath $Path -Raw -ErrorAction SilentlyContinue)
}

function Test-PChatInstallSucceeded {
    param(
        [Parameter(Mandatory = $true)][int] $ExitCode,
        [Parameter(Mandatory = $true)][string] $ResultPath
    )

    if ($ExitCode -eq 0) { return $true }
    if (-not (Test-Path -LiteralPath $ResultPath)) { return $false }
    $result = (Get-Content -LiteralPath $ResultPath -Raw -ErrorAction SilentlyContinue).Trim()
    return $result.Equals("ok", [System.StringComparison]::OrdinalIgnoreCase)
}

function Show-PChatInstallDialog {
    param(
        [Parameter(Mandatory = $true)][string] $InitialDir,
        [Parameter(Mandatory = $true)][bool] $PortableMode,
        [Parameter(Mandatory = $true)][bool] $ForceInstall
    )

    Add-Type -AssemblyName System.Windows.Forms
    Add-Type -AssemblyName System.Drawing
    [System.Windows.Forms.Application]::EnableVisualStyles()

    $form = New-Object System.Windows.Forms.Form
    $form.Text = "P-Chat 安装程序"
    $form.StartPosition = "CenterScreen"
    $form.FormBorderStyle = "FixedDialog"
    $form.MaximizeBox = $false
    $form.MinimizeBox = $false
    $form.ClientSize = New-Object System.Drawing.Size(620, 430)
    $form.Font = New-Object System.Drawing.Font("Segoe UI", 10)

    $title = New-Object System.Windows.Forms.Label
    $title.Text = "安装 P-Chat"
    $title.Font = New-Object System.Drawing.Font("Segoe UI", 18, [System.Drawing.FontStyle]::Bold)
    $title.AutoSize = $true
    $title.Location = New-Object System.Drawing.Point(24, 22)
    $form.Controls.Add($title)

    $subtitle = New-Object System.Windows.Forms.Label
    $subtitle.Text = "选择安装位置和系统集成选项。"
    $subtitle.AutoSize = $true
    $subtitle.ForeColor = [System.Drawing.Color]::FromArgb(88, 88, 88)
    $subtitle.Location = New-Object System.Drawing.Point(27, 62)
    $form.Controls.Add($subtitle)

    $pathLabel = New-Object System.Windows.Forms.Label
    $pathLabel.Text = "安装目录"
    $pathLabel.AutoSize = $true
    $pathLabel.Location = New-Object System.Drawing.Point(28, 108)
    $form.Controls.Add($pathLabel)

    $pathText = New-Object System.Windows.Forms.TextBox
    $pathText.Text = $InitialDir
    $pathText.Location = New-Object System.Drawing.Point(31, 134)
    $pathText.Size = New-Object System.Drawing.Size(460, 28)
    $form.Controls.Add($pathText)

    $browse = New-Object System.Windows.Forms.Button
    $browse.Text = "浏览..."
    $browse.Location = New-Object System.Drawing.Point(505, 132)
    $browse.Size = New-Object System.Drawing.Size(85, 32)
    $browse.Add_Click({
        $dialog = New-Object System.Windows.Forms.FolderBrowserDialog
        $dialog.Description = "选择 P-Chat 安装目录"
        $dialog.ShowNewFolderButton = $true
        if ($pathText.Text -and (Test-Path -LiteralPath $pathText.Text)) {
            $dialog.SelectedPath = $pathText.Text
        }
        if ($dialog.ShowDialog($form) -eq [System.Windows.Forms.DialogResult]::OK) {
            $pathText.Text = $dialog.SelectedPath
        }
    })
    $form.Controls.Add($browse)

    $optionsLabel = New-Object System.Windows.Forms.Label
    $optionsLabel.Text = "安装选项"
    $optionsLabel.AutoSize = $true
    $optionsLabel.Location = New-Object System.Drawing.Point(28, 190)
    $form.Controls.Add($optionsLabel)

    $startMenuOption = New-Object System.Windows.Forms.CheckBox
    $startMenuOption.Text = "创建开始菜单快捷方式"
    $startMenuOption.Checked = $true
    $startMenuOption.AutoSize = $true
    $startMenuOption.Location = New-Object System.Drawing.Point(32, 220)
    $form.Controls.Add($startMenuOption)

    $desktopOption = New-Object System.Windows.Forms.CheckBox
    $desktopOption.Text = "创建桌面快捷方式"
    $desktopOption.Checked = $true
    $desktopOption.AutoSize = $true
    $desktopOption.Location = New-Object System.Drawing.Point(32, 250)
    $form.Controls.Add($desktopOption)

    $pathOption = New-Object System.Windows.Forms.CheckBox
    $pathOption.Text = "添加 pchat 命令到当前用户 PATH"
    $pathOption.Checked = $true
    $pathOption.AutoSize = $true
    $pathOption.Location = New-Object System.Drawing.Point(32, 280)
    $form.Controls.Add($pathOption)

    $launchOption = New-Object System.Windows.Forms.CheckBox
    $launchOption.Text = "安装完成后启动 P-Chat"
    $launchOption.Checked = $true
    $launchOption.AutoSize = $true
    $launchOption.Location = New-Object System.Drawing.Point(320, 220)
    $form.Controls.Add($launchOption)

    $statusLabel = New-Object System.Windows.Forms.Label
    $statusLabel.Text = "正在安装，请稍候。安装过程中请不要关闭窗口。"
    $statusLabel.AutoSize = $true
    $statusLabel.ForeColor = [System.Drawing.Color]::FromArgb(88, 88, 88)
    $statusLabel.Location = New-Object System.Drawing.Point(31, 320)
    $statusLabel.Visible = $false
    $form.Controls.Add($statusLabel)

    $progress = New-Object System.Windows.Forms.ProgressBar
    $progress.Location = New-Object System.Drawing.Point(31, 348)
    $progress.Size = New-Object System.Drawing.Size(360, 12)
    $progress.Style = [System.Windows.Forms.ProgressBarStyle]::Marquee
    $progress.MarqueeAnimationSpeed = 30
    $progress.Visible = $false
    $form.Controls.Add($progress)

    $install = New-Object System.Windows.Forms.Button
    $install.Text = "安装"
    $install.Location = New-Object System.Drawing.Point(405, 374)
    $install.Size = New-Object System.Drawing.Size(90, 34)
    $state = @{
        Installing = $false
        Process    = $null
        OutputPath = Join-Path ([System.IO.Path]::GetTempPath()) ("pchat-install-{0}.out.log" -f ([System.Guid]::NewGuid().ToString("N")))
        ErrorPath  = Join-Path ([System.IO.Path]::GetTempPath()) ("pchat-install-{0}.err.log" -f ([System.Guid]::NewGuid().ToString("N")))
        ResultPath = Join-Path ([System.IO.Path]::GetTempPath()) ("pchat-install-{0}.result" -f ([System.Guid]::NewGuid().ToString("N")))
    }
    $setBusy = {
        param([bool] $Busy)

        $state.Installing = $Busy
        foreach ($control in @($pathText, $browse, $startMenuOption, $desktopOption, $pathOption, $launchOption)) {
            $control.Enabled = -not $Busy
        }
        $install.Enabled = -not $Busy
        $install.Text = $(if ($Busy) { "安装中..." } else { "安装" })
        $cancel.Enabled = -not $Busy
        $statusLabel.Visible = $Busy
        $progress.Visible = $Busy
        $form.ControlBox = -not $Busy
        $form.UseWaitCursor = $Busy
        [System.Windows.Forms.Application]::DoEvents()
    }

    $timer = New-Object System.Windows.Forms.Timer
    $timer.Interval = 250
    $timer.Add_Tick({
        if (-not $state.Process) { return }
        if (-not $state.Process.HasExited) { return }

        $timer.Stop()
        $state.Process.WaitForExit()
        $exitCode = $state.Process.ExitCode
        $succeeded = Test-PChatInstallSucceeded -ExitCode $exitCode -ResultPath $state.ResultPath
        $output = (Read-PChatInstallLog -Path $state.OutputPath) + (Read-PChatInstallLog -Path $state.ErrorPath)
        foreach ($logPath in @($state.OutputPath, $state.ErrorPath, $state.ResultPath)) {
            if (Test-Path -LiteralPath $logPath) {
                Remove-Item -LiteralPath $logPath -Force -ErrorAction SilentlyContinue
            }
        }

        & $setBusy $false
        if ($succeeded) {
            [System.Windows.Forms.MessageBox]::Show(
                $form,
                "P-Chat 已安装到:`n$($form.Tag.InstallDir)",
                "P-Chat 安装完成",
                [System.Windows.Forms.MessageBoxButtons]::OK,
                [System.Windows.Forms.MessageBoxIcon]::Information
            ) | Out-Null
            $form.DialogResult = [System.Windows.Forms.DialogResult]::OK
            $form.Close()
            return
        }

        $message = "安装失败，请检查安装目录后重试。"
        if (-not [string]::IsNullOrWhiteSpace($output)) {
            $message += "`n`n$output"
        }
        [System.Windows.Forms.MessageBox]::Show(
            $form,
            $message,
            "P-Chat 安装程序",
            [System.Windows.Forms.MessageBoxButtons]::OK,
            [System.Windows.Forms.MessageBoxIcon]::Error
        ) | Out-Null
    })

    $install.Add_Click({
        $selected = $pathText.Text.Trim()
        if (-not $selected) {
            [System.Windows.Forms.MessageBox]::Show($form, "请选择安装目录。", "P-Chat 安装程序", "OK", "Warning") | Out-Null
            return
        }
        $form.Tag = [pscustomobject]@{
            Cancelled       = $false
            InstallDir      = $selected
            StartMenu       = $startMenuOption.Checked
            DesktopShortcut = $desktopOption.Checked
            AddToPath       = $pathOption.Checked
            Launch          = $launchOption.Checked
        }
        & $setBusy $true
        try {
            $state.Process = Start-PChatGuiInstall `
                -Choice $form.Tag `
                -PortableMode $PortableMode `
                -ForceInstall $ForceInstall `
                -ResultPath $state.ResultPath `
                -OutputPath $state.OutputPath `
                -ErrorPath $state.ErrorPath
            $timer.Start()
        } catch {
            & $setBusy $false
            [System.Windows.Forms.MessageBox]::Show($form, "启动安装失败。`n`n$($_.Exception.Message)", "P-Chat 安装程序", "OK", "Error") | Out-Null
        }
    })
    $form.Controls.Add($install)

    $cancel = New-Object System.Windows.Forms.Button
    $cancel.Text = "取消"
    $cancel.Location = New-Object System.Drawing.Point(505, 374)
    $cancel.Size = New-Object System.Drawing.Size(85, 34)
    $cancel.Add_Click({
        if ($state.Installing) { return }
        $form.Tag = [pscustomobject]@{ Cancelled = $true }
        $form.DialogResult = [System.Windows.Forms.DialogResult]::Cancel
        $form.Close()
    })
    $form.Controls.Add($cancel)

    $form.Add_FormClosing({
        if ($state.Installing) {
            $_.Cancel = $true
        }
    })

    $form.AcceptButton = $install
    $form.CancelButton = $cancel

    $result = $form.ShowDialog()
    if ($result -eq [System.Windows.Forms.DialogResult]::OK -and $form.Tag) {
        return $form.Tag
    }
    return [pscustomobject]@{ Cancelled = $true }
}

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

if ($Gui) {
    $initialDir = $InstallDir
    if (-not $initialDir) {
        if ($prevDir) {
            $initialDir = $prevDir
        } else {
            $initialDir = $defaultDir
        }
    }

    $choice = Show-PChatInstallDialog -InitialDir $initialDir -PortableMode ([bool]$Portable) -ForceInstall ([bool]$Force)
    if ($choice.Cancelled) {
        Write-Host "[install] cancelled by user"
        exit 0
    }
    exit 0
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

if ($AddToPath -and $RemoveFromPath) {
    throw "Use either -AddToPath or -RemoveFromPath, not both."
}

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
    New-PChatShortcut `
        -Shell $shell `
        -LinkPath $linkPath `
        -TargetPath (Join-Path $target "pchat-gui.exe") `
        -WorkingDirectory $target `
        -IconLocation (Join-Path $target "pchat-gui.exe,0") `
        -Description "P-Chat Desktop"
    Write-Host "[install] Start Menu shortcut: $linkPath"

    # 卸载快捷方式 / Uninstall shortcut
    $uninstPath = Join-Path $startMenu "P-Chat Uninstall.lnk"
    New-PChatShortcut `
        -Shell $shell `
        -LinkPath $uninstPath `
        -TargetPath "powershell.exe" `
        -Arguments "-NoProfile -ExecutionPolicy Bypass -File `"$target\uninstall.ps1`"" `
        -WorkingDirectory $target `
        -Description "Uninstall P-Chat"
    Write-Host "[install] uninstall shortcut:  $uninstPath"
}

# --- 桌面快捷方式 / Desktop shortcut --------------------------------------
if ($DesktopShortcut -and -not $Portable) {
    $shell = New-Object -ComObject WScript.Shell
    $desktop = [Environment]::GetFolderPath("Desktop")
    $desktopLink = Join-Path $desktop "P-Chat.lnk"
    New-PChatShortcut `
        -Shell $shell `
        -LinkPath $desktopLink `
        -TargetPath (Join-Path $target "pchat-gui.exe") `
        -WorkingDirectory $target `
        -IconLocation (Join-Path $target "pchat-gui.exe,0") `
        -Description "P-Chat Desktop"
    Write-Host "[install] Desktop shortcut: $desktopLink"
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

# --- PATH 配置 / PATH ------------------------------------------------------
if (-not $Portable) {
    if ($AddToPath) {
        Set-PChatUserPath -InstallRoot $target -Enabled $true
    } elseif ($RemoveFromPath -or $pathChoiceFromGui) {
        Set-PChatUserPath -InstallRoot $target -Enabled $false
    }
}

if ($Launch -and -not $Portable) {
    Start-Process -FilePath (Join-Path $target "pchat-gui.exe") -WorkingDirectory $target
    Write-Host "[install] launched P-Chat"
}

if ($Gui) {
    Add-Type -AssemblyName System.Windows.Forms
    [System.Windows.Forms.MessageBox]::Show(
        "P-Chat 已安装到:`n$target",
        "P-Chat 安装完成",
        [System.Windows.Forms.MessageBoxButtons]::OK,
        [System.Windows.Forms.MessageBoxIcon]::Information
    ) | Out-Null
}

Write-Host "[install] done.  Launch: $target\pchat-gui.exe"
if ($GuiResultPath) {
    Set-Content -LiteralPath $GuiResultPath -Value "ok" -Encoding UTF8
}
exit 0
