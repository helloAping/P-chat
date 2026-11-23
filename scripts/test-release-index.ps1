$ErrorActionPreference = 'Stop'

. "$PSScriptRoot\version-utils.ps1"

$root = Join-Path ([System.IO.Path]::GetTempPath()) ("pchat-release-index-" + [guid]::NewGuid().ToString('n'))
New-Item -ItemType Directory -Path $root | Out-Null
try {
    $release = Join-Path $root 'release'
    $existing = Join-Path $root 'existing'
    $out = Join-Path $root 'out'
    New-Item -ItemType Directory -Path $release, $existing | Out-Null

    $stableBody = @'
{"version":"1.0.15","channel":"stable","platform":"windows","arch":"amd64","url":"<upload-url>/pchat-setup-v1.0.15.exe"}
'@
    $stablePath = Join-Path $release 'pchat-latest-windows-amd64-v1.0.15.json'
    [System.IO.File]::WriteAllText($stablePath, $stableBody.Trim(), [System.Text.UTF8Encoding]::new($false))
    & "$PSScriptRoot\rewrite-update-urls.ps1" -Directory $release -UploadBaseUrl 'https://github.com/helloAping/P-chat/releases/download/v1.0.15'
    $rewritten = [System.IO.File]::ReadAllText($stablePath)
    if ($rewritten -notlike '*https://github.com/helloAping/P-chat/releases/download/v1.0.15/pchat-setup-v1.0.15.exe*') {
        throw "rewrite did not insert the release URL: $rewritten"
    }
    if ($rewritten -like '*<upload-url>*') {
        throw "rewrite left a placeholder: $rewritten"
    }

    $betaOld = '{"version":"1.0.15.beta","channel":"beta","platform":"windows","arch":"amd64"}'
    [System.IO.File]::WriteAllText((Join-Path $existing 'windows-amd64.beta.json'), $betaOld, [System.Text.UTF8Encoding]::new($false))
    $linuxKeep = '{"version":"1.0.14","channel":"stable","platform":"linux","arch":"amd64"}'
    [System.IO.File]::WriteAllText((Join-Path $existing 'linux-amd64.json'), $linuxKeep, [System.Text.UTF8Encoding]::new($false))

    & "$PSScriptRoot\publish-update-index.ps1" -ReleaseDir $release -ExistingDir $existing -OutDir $out
    $stableIndex = Get-Content -LiteralPath (Join-Path $out 'windows-amd64.json') -Raw | ConvertFrom-Json
    $betaIndex = Get-Content -LiteralPath (Join-Path $out 'windows-amd64.beta.json') -Raw | ConvertFrom-Json
    $linuxIndex = Get-Content -LiteralPath (Join-Path $out 'linux-amd64.json') -Raw | ConvertFrom-Json
    if ($stableIndex.version -ne '1.0.15' -or $betaIndex.version -ne '1.0.15') {
        throw "stable release did not advance both windows pointers: stable=$($stableIndex.version) beta=$($betaIndex.version)"
    }
    if ($linuxIndex.version -ne '1.0.14') {
        throw "linux index was overwritten: $($linuxIndex.version)"
    }

    $olderBeta = Join-Path $root 'older-beta'
    New-Item -ItemType Directory -Path $olderBeta | Out-Null
    $olderBody = '{"version":"1.0.15.beta","channel":"beta","platform":"windows","arch":"amd64"}'
    [System.IO.File]::WriteAllText((Join-Path $olderBeta 'pchat-latest-windows-amd64-v1.0.15.beta.json'), $olderBody, [System.Text.UTF8Encoding]::new($false))
    $roundOlder = Join-Path $root 'round-older'
    & "$PSScriptRoot\publish-update-index.ps1" -ReleaseDir $olderBeta -ExistingDir $out -OutDir $roundOlder
    $kept = Get-Content -LiteralPath (Join-Path $roundOlder 'windows-amd64.beta.json') -Raw | ConvertFrom-Json
    if ($kept.version -ne '1.0.15') {
        throw "older beta replaced a newer stable pointer: $($kept.version)"
    }

    $newerBetaDir = Join-Path $root 'newer-beta'
    New-Item -ItemType Directory -Path $newerBetaDir | Out-Null
    $newerBody = '{"version":"1.0.16.beta","channel":"beta","platform":"windows","arch":"amd64"}'
    [System.IO.File]::WriteAllText((Join-Path $newerBetaDir 'pchat-latest-windows-amd64-v1.0.16.beta.json'), $newerBody, [System.Text.UTF8Encoding]::new($false))
    $roundNewer = Join-Path $root 'round-newer'
    & "$PSScriptRoot\publish-update-index.ps1" -ReleaseDir $newerBetaDir -ExistingDir $roundOlder -OutDir $roundNewer
    $advanced = Get-Content -LiteralPath (Join-Path $roundNewer 'windows-amd64.beta.json') -Raw | ConvertFrom-Json
    $stableKept = Get-Content -LiteralPath (Join-Path $roundNewer 'windows-amd64.json') -Raw | ConvertFrom-Json
    if ($advanced.version -ne '1.0.16.beta') {
        throw "newer beta did not advance the beta pointer: $($advanced.version)"
    }
    if ($stableKept.version -ne '1.0.15') {
        throw "beta release changed the stable pointer: $($stableKept.version)"
    }

    Add-Type -AssemblyName System.IO.Compression
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $pkgDir = Join-Path $root 'pkg'
    New-Item -ItemType Directory -Path $pkgDir | Out-Null
    $zipPath = Join-Path $pkgDir 'pchat-update-windows-amd64-v1.0.15.zip'
    $setupPath = Join-Path $pkgDir 'pchat-setup-v1.0.15.exe'
    [System.IO.File]::WriteAllBytes($setupPath, [byte[]](1, 2, 3, 4))
    $zip = [System.IO.Compression.ZipFile]::Open($zipPath, 'Create')
    try {
        foreach ($name in @('pchat-gui.exe', 'pchat-server.exe')) {
            $entry = $zip.CreateEntry($name)
            $writer = New-Object System.IO.StreamWriter($entry.Open())
            try { $writer.Write('x') } finally { $writer.Dispose() }
        }
    } finally {
        $zip.Dispose()
    }

    function Get-SHA256Hex([string]$Path) {
        $stream = [System.IO.File]::OpenRead($Path)
        try {
            $sha = [System.Security.Cryptography.SHA256]::Create()
            try { return ([System.BitConverter]::ToString($sha.ComputeHash($stream))).Replace('-', '').ToLowerInvariant() }
            finally { $sha.Dispose() }
        } finally { $stream.Dispose() }
    }
    $zipSum = Get-SHA256Hex $zipPath
    $setupSum = Get-SHA256Hex $setupPath
    $manifest = @"
{
  "version": "1.0.15",
  "channel": "stable",
  "platform": "windows",
  "arch": "amd64",
  "full": {"kind":"full","platform":"windows","arch":"amd64","url":"https://github.com/helloAping/P-chat/releases/download/v1.0.15/pchat-setup-v1.0.15.exe","sha256":"$setupSum","signature":"dGVzdA=="},
  "patch": {"kind":"patch","platform":"windows","arch":"amd64","url":"https://github.com/helloAping/P-chat/releases/download/v1.0.15/pchat-update-windows-amd64-v1.0.15.zip","sha256":"$zipSum","signature":"dGVzdA=="}
}
"@
    [System.IO.File]::WriteAllText((Join-Path $pkgDir 'pchat-latest-windows-amd64-v1.0.15.json'), $manifest.Trim(), [System.Text.UTF8Encoding]::new($false))
    & "$PSScriptRoot\validate-release-artifacts.ps1" -Directory $pkgDir -RequireSignature -RejectPlaceholder -SkipCryptoVerify

    $badDir = Join-Path $root 'bad'
    New-Item -ItemType Directory -Path $badDir | Out-Null
    Copy-Item -Path (Join-Path $pkgDir '*') -Destination $badDir
    $badJson = Join-Path $badDir 'pchat-latest-windows-amd64-v1.0.15.json'
    $badText = [System.IO.File]::ReadAllText($badJson).Replace('https://github.com', '<upload-url>')
    [System.IO.File]::WriteAllText($badJson, $badText, [System.Text.UTF8Encoding]::new($false))
    $rejected = $false
    try {
        & "$PSScriptRoot\validate-release-artifacts.ps1" -Directory $badDir -RequireSignature -RejectPlaceholder -SkipCryptoVerify
    } catch {
        $rejected = $true
    }
    if (-not $rejected) {
        throw 'validate-release-artifacts accepted a placeholder URL'
    }
} finally {
    Remove-Item -LiteralPath $root -Recurse -Force -ErrorAction SilentlyContinue
}

Write-Host '[test-release-index] rewrite, index merge, and artifact validation passed' -ForegroundColor Green
