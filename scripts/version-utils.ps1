# Shared release-version validation for build and release scripts.
# 构建与发布脚本共用的版本号校验。

function Get-PChatVersionInfo {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true)]
        [string]$Version
    )

    $normalized = $Version.Trim()
    $pattern = '^\d+\.\d+\.\d+(?:(?:-|\.)(?<channel>alpha|beta|rc)(?:\.(?<sequence>\d+))?)?$'
    $match = [regex]::Match($normalized, $pattern, [System.Text.RegularExpressions.RegexOptions]::IgnoreCase)
    if (-not $match.Success) {
        throw "VERSION must be MAJOR.MINOR.PATCH with an optional alpha/beta/rc suffix (for example 1.0.13.beta or 1.0.13-beta.1); got '$normalized'"
    }

    $channel = if ($match.Groups['channel'].Success) {
        $match.Groups['channel'].Value.ToLowerInvariant()
    } else {
        'stable'
    }

    return [pscustomobject]@{
        Version = $normalized
        Channel = $channel
    }
}

function Get-PChatVersionParts {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Version
    )

    $v = $Version.Trim()
    if ($v.StartsWith('v') -or $v.StartsWith('V')) {
        $v = $v.Substring(1)
    }
    $pieces = @($v.Split('.'))
    $nums = @(0, 0, 0)
    $rank = 4
    $seq = 0

    for ($i = 0; $i -lt [Math]::Min(3, $pieces.Count); $i++) {
        $piece = [string]$pieces[$i]
        $end = 0
        while ($end -lt $piece.Length -and [char]::IsDigit($piece[$end])) {
            $end++
        }
        if ($end -gt 0) {
            $nums[$i] = [int]$piece.Substring(0, $end)
        }
    }

    $snapshot = {
        [pscustomobject]@{
            Parts = $nums
            Rank  = $rank
            Seq   = $seq
        }
    }
    if ($pieces.Count -lt 3) {
        return (& $snapshot)
    }

    $patch = [string]$pieces[2]
    $patchDigits = 0
    while ($patchDigits -lt $patch.Length -and [char]::IsDigit($patch[$patchDigits])) {
        $patchDigits++
    }
    $suffix = New-Object System.Collections.Generic.List[string]
    if ($patchDigits -lt $patch.Length) {
        $rest = $patch.Substring($patchDigits).TrimStart('-')
        if ($rest) {
            $suffix.Add($rest)
        }
    }
    for ($i = 3; $i -lt $pieces.Count; $i++) {
        $suffix.Add([string]$pieces[$i])
    }
    if ($suffix.Count -eq 0) {
        return (& $snapshot)
    }

    switch ($suffix[0].Trim().ToLowerInvariant()) {
        'alpha' { $rank = 1 }
        'beta' { $rank = 2 }
        'rc' { $rank = 3 }
        default { return (& $snapshot) }
    }
    if ($suffix.Count -gt 1) {
        $parsed = 0
        if ([int]::TryParse($suffix[1].Trim(), [ref]$parsed)) {
            $seq = $parsed
        }
    }
    return (& $snapshot)
}

function Compare-PChatVersion {
    param(
        [Parameter(Mandatory = $true)][string]$Left,
        [Parameter(Mandatory = $true)][string]$Right
    )

    $a = Get-PChatVersionParts -Version $Left
    $b = Get-PChatVersionParts -Version $Right
    for ($i = 0; $i -lt 3; $i++) {
        if ($a.Parts[$i] -gt $b.Parts[$i]) { return 1 }
        if ($a.Parts[$i] -lt $b.Parts[$i]) { return -1 }
    }
    if ($a.Rank -gt $b.Rank) { return 1 }
    if ($a.Rank -lt $b.Rank) { return -1 }
    if ($a.Seq -gt $b.Seq) { return 1 }
    if ($a.Seq -lt $b.Seq) { return -1 }
    return 0
}
