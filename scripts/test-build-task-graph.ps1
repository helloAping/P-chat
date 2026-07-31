$ErrorActionPreference = 'Stop'

function Invoke-TaskDryRun {
    param([Parameter(Mandatory = $true)][string]$TaskName)

    $previousErrorAction = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    $output = & task --dry $TaskName 2>&1
    $exitCode = $LASTEXITCODE
    $ErrorActionPreference = $previousErrorAction

    if ($exitCode -ne 0) {
        throw "task --dry $TaskName failed:`n$($output -join [Environment]::NewLine)"
    }

    return $output
}

function Get-FrontendBuildCount {
    param([Parameter(Mandatory = $true)][string]$TaskName)

    $output = @(Invoke-TaskDryRun -TaskName $TaskName)
    return @($output | Select-String -SimpleMatch 'npm run build').Count
}

$expectedCounts = [ordered]@{
    'build:all'                    = 1
    'build'                        = 1
    'build:gui'                    = 1
    'build:setup'                  = 1
    'package:gui'                  = 1
    'package:gui:linux'            = 1
    'package:gui:linux:prepared'   = 0
    'package:gui:macos'            = 1
    'package:gui:macos:prepared'   = 0
}

foreach ($entry in $expectedCounts.GetEnumerator()) {
    $actual = Get-FrontendBuildCount -TaskName $entry.Key
    if ($actual -ne $entry.Value) {
        throw "Expected '$($entry.Key)' to run the frontend build $($entry.Value) time(s), got $actual."
    }

    Write-Host "[test-build-task-graph] $($entry.Key): $actual frontend build(s)" -ForegroundColor Green
}

$buildAllOutput = @(Invoke-TaskDryRun -TaskName 'build:all') -join [Environment]::NewLine
$preparedPlatformTasks = 'package:gui:linux:prepared', 'package:gui:macos:prepared'
foreach ($taskName in $preparedPlatformTasks) {
    if (-not $buildAllOutput.Contains("-Task `"$taskName`"")) {
        throw "Expected 'build:all' to invoke prepared platform task '$taskName'."
    }

    Write-Host "[test-build-task-graph] build:all invokes $taskName" -ForegroundColor Green
}
