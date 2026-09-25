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
