# Sync VERSION file into wails.json and install.ps1.
# Reads the VERSION file from the repo root and patches
# `productVersion` in wails.json and `DisplayVersion` in install.ps1.

param(
    [string]$Root = ""
)

if (-not $Root) {
    $Root = Join-Path $PSScriptRoot ".."
}
$Root = (Resolve-Path $Root).Path

$versionFile = Join-Path $Root "VERSION"
if (-not (Test-Path $versionFile)) {
    Write-Error "VERSION file not found at $versionFile"
    exit 1
}
$v = (Get-Content $versionFile -Raw).Trim()
if (-not $v) {
    Write-Error "VERSION file is empty"
    exit 1
}

$wailsJson = Join-Path $Root "cmd\pchat-gui\wails.json"
if (Test-Path $wailsJson) {
    $content = Get-Content $wailsJson -Raw
    $pat = '"productVersion"\s*:\s*"[^"]*"'
    $repl = '"productVersion": "' + $v + '"'
    $newContent = $content -replace $pat, $repl
    if ($newContent -ne $content) {
        $utf8NoBom = New-Object System.Text.UTF8Encoding($false)
        [System.IO.File]::WriteAllText($wailsJson, $newContent, $utf8NoBom)
        Write-Host "[sync:version] wails.json -> $v"
    } else {
        Write-Host "[sync:version] wails.json already $v (no change)"
    }
}

$installPs1 = Join-Path $Root "cmd\pchat-gui\install.ps1"
if (Test-Path $installPs1) {
    $content = Get-Content $installPs1 -Raw
    $pat = 'DisplayVersion\s+-Value\s+"[^"]*"'
    $repl = 'DisplayVersion  -Value "' + $v + '"'
    $newContent = $content -replace $pat, $repl
    if ($newContent -ne $content) {
        Set-Content $installPs1 -Value $newContent -Encoding UTF8
        Write-Host "[sync:version] install.ps1 -> $v"
    } else {
        Write-Host "[sync:version] install.ps1 already $v (no change)"
    }
}
