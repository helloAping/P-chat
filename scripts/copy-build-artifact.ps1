param(
    [Parameter(Mandatory = $true)]
    [string]$Source,

    [Parameter(Mandatory = $true)]
    [string]$Destination
)

$ErrorActionPreference = "Stop"

try {
    $sourcePath = (Resolve-Path -LiteralPath $Source).Path
} catch {
    Write-Error "[copy-build-artifact] source not found: $Source"
    exit 1
}

$destinationParent = Split-Path -Parent $Destination
if ($destinationParent) {
    New-Item -ItemType Directory -Path $destinationParent -Force | Out-Null
}

try {
    if (Test-Path -LiteralPath $Destination) {
        Set-ItemProperty -LiteralPath $Destination -Name IsReadOnly -Value $false -ErrorAction SilentlyContinue
    }
    Copy-Item -LiteralPath $sourcePath -Destination $Destination -Force
} catch {
    Write-Error @"
[copy-build-artifact] failed to copy artifact.
source: $sourcePath
destination: $Destination
reason: $($_.Exception.Message)

If the destination is an .exe, close any running process using it and run the task again.
"@
    exit 1
}
