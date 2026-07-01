<#
.SYNOPSIS
  升级风格文件结构：v1 (identity/ + soul/) → v2 (style/)

.DESCRIPTION
  P-Chat v2 将风格定义从分离的 identity/{id}.md + soul/{id}.md 合并
  为单一 style/{id}.md 文件。本脚本自动检测并迁移现有文件。
  
  - 项目级 prompts/ 和 ~/.p-chat/prompts/ 都会处理
  - 已存在的 style/ 文件不会被覆盖
  - 原始 identity/ + soul/ 目录保留不删（手动确认后删除）

.PARAMETER RemoveOld
  迁移后删除原始 identity/ 和 soul/ 目录

.EXAMPLE
  .\scripts\upgrade-style-v2.ps1
  .\scripts\upgrade-style-v2.ps1 -RemoveOld
#>
param(
    [switch]$RemoveOld
)

$ErrorActionPreference = "Stop"
$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$projectRoot = Resolve-Path "$scriptDir\.."

Write-Host "=== P-Chat 风格文件升级 v1 → v2 ===" -ForegroundColor Cyan
Write-Host ""

function Upgrade-Dir {
    param([string]$PromptsDir)

    Write-Host "检查: $PromptsDir" -ForegroundColor Yellow

    $identityDir = Join-Path $PromptsDir "identity"
    $soulDir = Join-Path $PromptsDir "soul"
    $styleDir = Join-Path $PromptsDir "style"

    if (-not (Test-Path $identityDir) -and -not (Test-Path $soulDir)) {
        Write-Host "  无 identity/ 或 soul/ 目录，跳过。" -ForegroundColor Gray
        return
    }

    # 收集所有 style id（从 identity 和 soul 文件名去重）
    $ids = @{}
    foreach ($dir in @($identityDir, $soulDir)) {
        if (Test-Path $dir) {
            Get-ChildItem -Path $dir -Filter "*.md" -File | ForEach-Object {
                $ids[$_.BaseName] = $true
            }
        }
    }

    if ($ids.Count -eq 0) {
        Write-Host "  未找到 .md 文件，跳过。" -ForegroundColor Gray
        return
    }

    # 确保 style/ 目录存在
    if (-not (Test-Path $styleDir)) {
        New-Item -ItemType Directory -Path $styleDir -Force | Out-Null
    }

    $migrated = 0
    $skipped = 0

    foreach ($id in $ids.Keys) {
        $targetFile = Join-Path $styleDir "$id.md"
        if (Test-Path $targetFile) {
            Write-Host "  跳过 $id — style/$id.md 已存在" -ForegroundColor Gray
            $skipped++
            continue
        }

        $content = ""
        $identityFile = Join-Path $identityDir "$id.md"
        $soulFile = Join-Path $soulDir "$id.md"

        if (Test-Path $identityFile) {
            $idContent = Get-Content -Path $identityFile -Raw -Encoding UTF8
            if ($idContent.Trim()) { $content += $idContent.Trim() }
        }
        if (Test-Path $soulFile) {
            $soContent = Get-Content -Path $soulFile -Raw -Encoding UTF8
            if ($soContent.Trim()) {
                if ($content) { $content += "`n`n---`n`n" }
                $content += $soContent.Trim()
            }
        }

        if ($content) {
            Set-Content -Path $targetFile -Value $content -Encoding UTF8 -NoNewline
            Write-Host "  ✓ $id → style/$id.md" -ForegroundColor Green
            $migrated++
        } else {
            Write-Host "  ✗ $id — 内容为空，跳过" -ForegroundColor Red
        }
    }

    Write-Host "  结果: $migrated 迁移, $skipped 跳过" -ForegroundColor White
    return @{ Migrated = $migrated; Skipped = $skipped }
}

# 1. 项目级 prompts/
$projectPrompts = Join-Path $projectRoot "prompts"
if (Test-Path $projectPrompts) {
    $result = Upgrade-Dir -PromptsDir $projectPrompts
    Write-Host ""
}

# 2. 全局 ~/.p-chat/prompts/
$globalPrompts = Join-Path $env:USERPROFILE ".p-chat\prompts"
if (Test-Path $globalPrompts) {
    $result = Upgrade-Dir -PromptsDir $globalPrompts
    Write-Host ""
}

# 3. 可选：删除旧目录
if ($RemoveOld) {
    Write-Host "删除旧目录..." -ForegroundColor Magenta
    foreach ($base in @($projectPrompts, $globalPrompts)) {
        foreach ($sub in @("identity", "soul")) {
            $d = Join-Path $base $sub
            if (Test-Path $d) {
                Remove-Item -Path $d -Recurse -Force
                Write-Host "  已删除: $d" -ForegroundColor DarkGray
            }
        }
    }
} else {
    Write-Host "提示: 确认迁移无误后，可运行:" -ForegroundColor Cyan
    Write-Host "  .\scripts\upgrade-style-v2.ps1 -RemoveOld" -ForegroundColor White
}

Write-Host ""
Write-Host "=== 升级完成 ===" -ForegroundColor Cyan
