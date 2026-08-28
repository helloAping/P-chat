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
$utf8NoBom = New-Object System.Text.UTF8Encoding($false)

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
    $content = [System.IO.File]::ReadAllText($wailsJson, [System.Text.Encoding]::UTF8)
    $pat = '"productVersion"\s*:\s*"[^"]*"'
    $repl = '"productVersion": "' + $v + '"'
    $newContent = $content -replace $pat, $repl
    if ($newContent -ne $content) {
        [System.IO.File]::WriteAllText($wailsJson, $newContent, $utf8NoBom)
        Write-Host "[sync:version] wails.json -> $v"
    } else {
        Write-Host "[sync:version] wails.json already $v (no change)"
    }
}

$installPs1 = Join-Path $Root "cmd\pchat-gui\install.ps1"
if (Test-Path $installPs1) {
    $content = [System.IO.File]::ReadAllText($installPs1, [System.Text.Encoding]::UTF8)
    $pat = '(-Name\s+"DisplayVersion"\s+-Value\s+)"[^"]*"'
    $repl = '${1}"' + $v + '"'
    $newContent = [regex]::Replace($content, $pat, $repl)
    if ($newContent -ne $content) {
        [System.IO.File]::WriteAllText($installPs1, $newContent, $utf8NoBom)
        Write-Host "[sync:version] install.ps1 -> $v"
    } else {
        Write-Host "[sync:version] install.ps1 already $v (no change)"
    }
}
