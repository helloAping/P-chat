#requires -Version 5.1
<#
.SYNOPSIS
    安装 agent 工具目录符号链接，将 .opencode/.codex/.claude 指向 .agents/。

.DESCRIPTION
    创建以下目录级符号链接（Junction），使得各 agent 工具读取 .agents/ 下的完整内容：

        .opencode  ->  .agents/     (opencode 读取 .opencode/AGENTS.md)
        .codex     ->  .agents/     (codex 读取 .codex/AGENTS.md)
        .claude    ->  .agents/     (claude 读取 .claude/CLAUDE.md)

    目录级的好处：.agents/ 下的所有子目录（docs/、scripts/）都自动通过符号链接可见，
    无需为每个文件单独建链接。

    对于 .p-chat（已有本地配置内容），仅同步 AGENTS.md 文件级副本。

    Windows 优先使用 Junction（mklink /J，无需管理员），失败时回退到副本。

.PARAMETER Force
    替换已存在的目标路径。

.PARAMETER DryRun
    仅打印操作，不做任何更改。

.EXAMPLE
    powershell -NoProfile -ExecutionPolicy Bypass -File .agents\scripts\install.ps1

.EXAMPLE
    powershell -NoProfile -ExecutionPolicy Bypass -File .agents\scripts\install.ps1 -Force
#>

[CmdletBinding()]
param(
    [switch]$Force,
    [switch]$DryRun
)

$ErrorActionPreference = 'Stop'
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$RepoRoot  = Resolve-Path (Join-Path $ScriptDir '..\..')
$AgentsDir = Join-Path $RepoRoot '.agents'
$Canonical = Join-Path $AgentsDir 'AGENTS.md'

# ---- 验证 canonical 文件存在 ----
if (-not (Test-Path -LiteralPath $Canonical)) {
    Write-Error "Canonical file not found: $Canonical"
    exit 1
}

# ---- 确保 .agents/CLAUDE.md 存在（claude 需要的文件名） ----
$ClaudeFile = Join-Path $AgentsDir 'CLAUDE.md'
if (-not (Test-Path -LiteralPath $ClaudeFile)) {
    Write-Host "  Creating $ClaudeFile (copy of AGENTS.md for claude)"
    if (-not $DryRun) {
        Copy-Item -LiteralPath $Canonical -Destination $ClaudeFile -Force
    }
}

# ---- 目录级符号链接列表 ----
# 每个工具目录都指向 .agents/，之后 .opencode/AGENTS.md = .agents/AGENTS.md，依此类推。
$DirLinks = @(
    @{ Path = '.opencode' }   # opencode reads .opencode/AGENTS.md
    @{ Path = '.codex'    }   # codex reads .codex/AGENTS.md
    @{ Path = '.claude'   }   # claude reads .claude/CLAUDE.md
)

# ---- .p-chat AGENTS.md 同步（文件级，因其已有本地配置内容） ----
$PchatAgents = Join-Path $RepoRoot '.p-chat\AGENTS.md'

function New-DirectoryLink {
    param(
        [string]$LinkPath,
        [string]$Target
    )

    $absLinkPath = Join-Path $RepoRoot $LinkPath
    $absTarget   = $AgentsDir

    if ($DryRun) {
        Write-Host "  [dry-run] $LinkPath  ->  .agents/"
        return
    }

    # 如果已存在且是正确的 Junction，跳过。
    if (Test-Path -LiteralPath $absLinkPath) {
        $item = Get-Item -LiteralPath $absLinkPath -Force
        if ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) {
            # 已是符号链接/Junction。检查目标。
            $existingTarget = $item.Target
            $normalizedExisting = (Resolve-Path $existingTarget -ErrorAction SilentlyContinue).Path
            $normalizedTarget = (Resolve-Path $absTarget).Path
            if ($normalizedExisting -eq $normalizedTarget) {
                Write-Host "  [ok]      $LinkPath (already points to .agents/)"
                return
            }
            if ($Force) {
                Remove-Item -LiteralPath $absLinkPath -Recurse -Force
            } else {
                Write-Host "  [skip]    $LinkPath already exists (target=$existingTarget). Use -Force to replace."
                return
            }
        } else {
            # 普通目录。仅 -Force 时移除。
            if ($Force) {
                Remove-Item -LiteralPath $absLinkPath -Recurse -Force
            } else {
                Write-Host "  [skip]    $LinkPath is a real directory. Use -Force to replace."
                return
            }
        }
    }

    # 优先用 Junction（无需管理员权限，目录级，但绝对路径）。
    # 失败时回退到目录符号链接（需要管理员/开发者模式）。
    # 再失败则回退到副本模式。
    try {
        # mklink /J 创建 Junction。目标必须是绝对路径。
        $output = & cmd /c "mklink /J `"$LinkPath`" `"$absTarget`"" 2>&1
        if ($LASTEXITCODE -eq 0) {
            Write-Host "  [junction] $LinkPath  ->  .agents/"
            return
        }
        throw "mklink /J exit $LASTEXITCODE : $output"
    } catch {
        Write-Warning "Junction failed for $LinkPath — trying mklink /D."
        try {
            $output = & cmd /c "mklink /D `"$LinkPath`" `.agents`"" 2>&1
            if ($LASTEXITCODE -eq 0) {
                Write-Host "  [symlink]  $LinkPath  ->  .agents/"
                return
            }
            throw "mklink /D exit $LASTEXITCODE : $output"
        } catch {
            Write-Warning "Directory symlink also failed for $LinkPath — falling back to file copy."
            # 创建目录并复制所有文件。
            try {
                New-Item -ItemType Directory -Path $absLinkPath -Force | Out-Null
                Copy-Item -Recurse -LiteralPath $absTarget\* -Destination $absLinkPath -Force
                Write-Host "  [copied]   $LinkPath  <-  .agents/  (no symlink available)"
            } catch {
                Write-Error "Failed to create $LinkPath : $_"
            }
        }
    }
}

# ---- 执行安装 ----
Write-Host ""
Write-Host "Installing agent tool directory links -> .agents/"
Write-Host "Repo root: $RepoRoot"
Write-Host ""

foreach ($l in $DirLinks) {
    New-DirectoryLink -LinkPath $l.Path -Target $l.Target
}

# ---- .p-chat AGENTS.md 同步（文件级副本，不覆盖目录） ----
$pchatDir = Join-Path $RepoRoot '.p-chat'
if (Test-Path -LiteralPath $pchatDir) {
    if ($DryRun) {
        Write-Host "  [dry-run] .p-chat/AGENTS.md  <-  .agents/AGENTS.md"
    } else {
        Copy-Item -LiteralPath $Canonical -Destination $PchatAgents -Force
        Write-Host "  [copied]   .p-chat/AGENTS.md  <-  .agents/AGENTS.md  (file level, .p-chat has local config)"
    }
} else {
    Write-Host "  [skip]     .p-chat/ not found (nothing to sync)"
}

# ---- 根 AGENTS.md stub 替换 ----
$rootAgents = Join-Path $RepoRoot 'AGENTS.md'
if (Test-Path -LiteralPath $rootAgents) {
    $rootItem = Get-Item -LiteralPath $rootAgents -Force
    if (-not ($rootItem.Attributes -band [IO.FileAttributes]::ReparsePoint)) {
        $stub = $false
        try {
            $contents = Get-Content -LiteralPath $rootAgents -Raw -ErrorAction SilentlyContinue
            if ($contents -match '在此描述你的项目') { $stub = $true }
        } catch {}
        if ($stub) {
            if ($DryRun) {
                Write-Host "  [dry-run] AGENTS.md (root) would be replaced by symlink"
            } else {
                Remove-Item -LiteralPath $rootAgents -Force
                try {
                    & cmd /c "mklink `"AGENTS.md`" `.agents\AGENTS.md`" 2>&1 | Out-Null
                    if ($LASTEXITCODE -eq 0) {
                        Write-Host "  [linked]   AGENTS.md (root)  ->  .agents/AGENTS.md  (was a stub)"
                    } else {
                        throw "mklink exit $LASTEXITCODE"
                    }
                } catch {
                    Copy-Item -LiteralPath $Canonical -Destination $rootAgents -Force
                    Write-Host "  [copied]   AGENTS.md (root)  <-  .agents/AGENTS.md  (no symlink)"
                }
            }
        } else {
            Write-Host "  [skip]     AGENTS.md (root) is non-trivial content; not touching it."
        }
    } else {
        Write-Host "  [ok]       AGENTS.md (root) is already a symlink"
    }
}

Write-Host ""
Write-Host "Done. Agent tools will now read .agents/ content via:"
Write-Host "  .opencode/AGENTS.md  -> .agents/AGENTS.md"
Write-Host "  .codex/AGENTS.md     -> .agents/AGENTS.md"
Write-Host "  .claude/CLAUDE.md    -> .agents/CLAUDE.md"
Write-Host "  .opencode/docs/      -> .agents/docs/"
