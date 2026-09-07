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

Write-Host '[test-version-utils] version formats and release channels passed' -ForegroundColor Green
