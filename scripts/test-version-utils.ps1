$ErrorActionPreference = 'Stop'

. "$PSScriptRoot\version-utils.ps1"

$validVersions = @(
    @{ Version = '1.0.13'; Channel = 'stable' },
    @{ Version = '1.0.13.beta'; Channel = 'beta' },
    @{ Version = '1.0.13-beta'; Channel = 'beta' },
    @{ Version = '1.0.13.beta.2'; Channel = 'beta' },
    @{ Version = '1.0.13-beta.2'; Channel = 'beta' },
    @{ Version = '1.0.13.alpha'; Channel = 'alpha' },
    @{ Version = '1.0.13.rc.1'; Channel = 'rc' }
)

foreach ($case in $validVersions) {
    $info = Get-PChatVersionInfo -Version $case.Version
    if ($info.Version -ne $case.Version) {
        throw "Expected version '$($case.Version)', got '$($info.Version)'."
    }
    if ($info.Channel -ne $case.Channel) {
        throw "Expected channel '$($case.Channel)' for '$($case.Version)', got '$($info.Channel)'."
    }
}

$invalidVersions = '1.0', '1.0.13.preview', '1.0.13.beta.x', '1.0.13-beta-2', '1.0.13.'
foreach ($version in $invalidVersions) {
    try {
        $null = Get-PChatVersionInfo -Version $version
        throw "Expected '$version' to be rejected."
    } catch {
        if ($_.Exception.Message -like "Expected '$version' to be rejected.*") {
            throw
        }
    }
}

$versionOrder = @(
    @{ Left = '1.0.13'; Right = '1.0.12'; Want = 1 },
    @{ Left = 'v1.0.12'; Right = '1.0.12-release'; Want = 0 },
    @{ Left = '1.2.0'; Right = '1.10.0'; Want = -1 },
    @{ Left = '1.0.13.beta'; Right = '1.0.12'; Want = 1 },
    @{ Left = '1.0.13'; Right = '1.0.13.beta'; Want = 1 },
    @{ Left = '1.0.13.beta'; Right = '1.0.13'; Want = -1 },
    @{ Left = '1.0.13.beta.2'; Right = '1.0.13.beta.1'; Want = 1 },
    @{ Left = '1.0.13-beta.2'; Right = '1.0.13.beta.2'; Want = 0 },
    @{ Left = '1.0.13.rc.1'; Right = '1.0.13.beta.9'; Want = 1 },
    @{ Left = '1.0.13.beta'; Right = '1.0.13.alpha'; Want = 1 },
    @{ Left = 'dev'; Right = '1.0.0'; Want = -1 }
)
foreach ($case in $versionOrder) {
    $got = Compare-PChatVersion -Left $case.Left -Right $case.Right
    if ($got -ne $case.Want) {
        throw "Compare-PChatVersion $($case.Left) vs $($case.Right) = $got, want $($case.Want)"
    }
}

Write-Host '[test-version-utils] version formats and release channels passed' -ForegroundColor Green
