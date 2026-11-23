[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'

# 从 GUI 模块读取唯一允许的 Wails CLI 版本。 / Read the only supported Wails CLI version from the GUI module.
$projectRoot = Split-Path -Parent $PSScriptRoot
$goModPath = Join-Path $projectRoot 'cmd\pchat-gui\go.mod'
$goMod = Get-Content -Raw -LiteralPath $goModPath
$requiredMatch = [regex]::Match(
    $goMod,
    '(?m)^\s*github\.com/wailsapp/wails/v2\s+v(?<version>\d+\.\d+\.\d+)\s*$'
)
if (-not $requiredMatch.Success) {
    Write-Error "Unable to determine the required Wails version from $goModPath"
    exit 1
}
$requiredVersion = $requiredMatch.Groups['version'].Value

# CLI 与库版本不一致会改写生成绑定，因此在任何 build/dev 前失败。 / Fail before build/dev because a mismatched CLI rewrites generated bindings.
$versionLines = @(& wails version 2>&1 | ForEach-Object { "$_" })
if ($LASTEXITCODE -ne 0) {
    Write-Error "Unable to query the Wails CLI version."
    exit 1
}
$actualMatch = [regex]::Match(($versionLines -join "`n"), '(?m)^v?(?<version>\d+\.\d+\.\d+)\s*$')
if (-not $actualMatch.Success) {
    Write-Error "Unable to parse the Wails CLI version."
    exit 1
}
$actualVersion = $actualMatch.Groups['version'].Value

if ($actualVersion -ne $requiredVersion) {
    Write-Error "Wails CLI version mismatch: required v$requiredVersion, found v$actualVersion. Install the pinned CLI with: go install github.com/wailsapp/wails/v2/cmd/wails@v$requiredVersion"
    exit 1
}

exit 0
