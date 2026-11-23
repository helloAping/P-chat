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
    if ($text -notlike '*<upload-url>/*') {
        throw "$($file.Name) has no <upload-url>/ placeholder"
    }
    $updated = $text.Replace('<upload-url>/', "$base/")
    if ($updated -like '*<upload-url>*') {
        throw "$($file.Name) still contains <upload-url>"
    }
    [System.IO.File]::WriteAllText($file.FullName, $updated, [System.Text.UTF8Encoding]::new($false))
    Write-Host "[rewrite-update-urls] $($file.FullName)" -ForegroundColor Green
}
