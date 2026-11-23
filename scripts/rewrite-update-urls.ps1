<#
.SYNOPSIS
  Replace <upload-url>/ placeholders in pchat-latest-*.json with a GitHub Release base URL.
#>

[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$Directory,
    [Parameter(Mandatory = $true)][string]$UploadBaseUrl
)

$ErrorActionPreference = 'Stop'

$base = $UploadBaseUrl.Trim().TrimEnd('/')
if ($base -notmatch '^https://') {
    throw "Upload base URL must use https: $UploadBaseUrl"
}

$files = @(Get-ChildItem -LiteralPath $Directory -Filter 'pchat-latest-*.json' -File)
if ($files.Count -eq 0) {
    throw "No pchat-latest-*.json files in $Directory"
}

foreach ($file in $files) {
    $text = [System.IO.File]::ReadAllText($file.FullName)
    # PowerShell 5.1 writes the placeholder as \u003cupload-url\u003e/.
    $patterns = @(
        '<upload-url>/',
        '\u003cupload-url\u003e/',
        '\u003Cupload-url\u003E/'
    )
    $found = $false
    foreach ($pattern in $patterns) {
        if ($text.Contains($pattern)) {
            $found = $true
            $text = $text.Replace($pattern, "$base/")
        }
    }
    if (-not $found) {
        throw "$($file.Name) has no <upload-url>/ placeholder"
    }
    $updated = $text
    if ($updated -like '*<upload-url>*' -or $updated -like '*upload-url\u003e*') {
        throw "$($file.Name) still contains <upload-url>"
    }
    [System.IO.File]::WriteAllText($file.FullName, $updated, [System.Text.UTF8Encoding]::new($false))
    Write-Host "[rewrite-update-urls] $($file.FullName)" -ForegroundColor Green
}
