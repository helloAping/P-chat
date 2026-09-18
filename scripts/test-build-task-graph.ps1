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
    'build:gui:win'                = 1
    'build:gui:mac'                = 1
    'build:win'                    = 1
    'build:linux'                  = 1
    'build:mac'                    = 1
    'build:setup'                  = 1
    'package:gui'                  = 1
    'package:gui:win'              = 1
    'package:gui:mac'              = 1
    'package:gui:linux'            = 1
    'package:gui:linux:prepared'   = 0
    'package:gui:macos'            = 1
    'package:gui:macos:prepared'   = 0
    'package:update:win'           = 1
    'package:update:linux'         = 1
    'package:update:macos'         = 1
    'package:update:mac'           = 1
    'package:update:all'           = 1
}

foreach ($entry in $expectedCounts.GetEnumerator()) {
    $actual = Get-FrontendBuildCount -TaskName $entry.Key
    if ($actual -ne $entry.Value) {
        throw "Expected '$($entry.Key)' to run the frontend build $($entry.Value) time(s), got $actual."
    }

    Write-Host "[test-build-task-graph] $($entry.Key): $actual frontend build(s)" -ForegroundColor Green
}

$buildAllOutput = @(Invoke-TaskDryRun -TaskName 'build:all') -join [Environment]::NewLine
$preparedPlatformTasks = 'build:gui:linux:binary', 'build:gui:macos:binary'
foreach ($taskName in $preparedPlatformTasks) {
    if (-not $buildAllOutput.Contains("-Task `"$taskName`"")) {
        throw "Expected 'build:all' to invoke prepared platform task '$taskName'."
    }

    Write-Host "[test-build-task-graph] build:all invokes $taskName" -ForegroundColor Green
}

foreach ($platformName in 'linux', 'mac') {
    if (-not $buildAllOutput.Contains("-Platform `"$platformName`"")) {
        throw "Expected 'build:all' to pass platform name '$platformName'."
    }

    Write-Host "[test-build-task-graph] build:all passes platform name $platformName" -ForegroundColor Green
}

foreach ($platformName in 'linux', 'mac') {
    if (-not $buildAllOutput.Contains("-File `"scripts/package-platform-archive.ps1`" -Platform `"$platformName`"")) {
        throw "Expected 'build:all' to archive platform '$platformName'."
    }

    Write-Host "[test-build-task-graph] build:all archives platform $platformName" -ForegroundColor Green
}

if (-not $buildAllOutput.Contains("-File scripts/package-update-zip.ps1 -RequireSetup")) {
    throw "Expected 'build:all' to create the Windows update zip after the setup installer."
}
Write-Host "[test-build-task-graph] build:all creates Windows update zip" -ForegroundColor Green

foreach ($platformName in 'linux', 'mac') {
    if (-not $buildAllOutput.Contains("-File scripts/package-update-zip.ps1 -Platform `"$platformName`" -RequireSetup -AllowMissingGui")) {
        throw "Expected 'build:all' to create update zip for platform '$platformName'."
    }

    Write-Host "[test-build-task-graph] build:all creates update zip for $platformName" -ForegroundColor Green
}

$buildDevOutput = @(Invoke-TaskDryRun -TaskName 'build:dev') -join [Environment]::NewLine
$repoRoot = (Split-Path -Parent $PSScriptRoot) -replace '\\', '/'
$expectedStopScript = "-File `"$repoRoot/scripts/stop-dev-processes.ps1`""
if (-not $buildDevOutput.Contains($expectedStopScript)) {
    throw "Expected 'build:dev' to use the root-anchored stop script '$expectedStopScript'."
}
Write-Host '[test-build-task-graph] build:dev anchors the stop script to ROOT_DIR' -ForegroundColor Green
