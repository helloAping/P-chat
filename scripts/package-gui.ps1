# Helper script for task package:gui. Copies pchat-server.exe and the
# install/uninstall scripts next to pchat-gui.exe so the install bundle
# is self-contained. Run from the repo root.

$ErrorActionPreference = "Stop"

$root    = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot "..")).Path
$srcGui  = Join-Path $root "cmd\pchat-gui\build\bin"
$binDir  = Join-Path $root "bin"

if (-not (Test-Path -LiteralPath $srcGui)) { throw "pchat-gui build dir not found: $srcGui" }
if (-not (Test-Path -LiteralPath $binDir)) { throw "bin dir not found: $binDir" }

Copy-Item -LiteralPath (Join-Path $binDir "pchat-server.exe") -Destination (Join-Path $srcGui "pchat-server.exe") -Force
Copy-Item -LiteralPath (Join-Path $root "cmd\pchat-gui\install.ps1")   -Destination (Join-Path $srcGui "install.ps1")   -Force
Copy-Item -LiteralPath (Join-Path $root "cmd\pchat-gui\uninstall.ps1") -Destination (Join-Path $srcGui "uninstall.ps1") -Force
Copy-Item -LiteralPath (Join-Path $srcGui "pchat-gui.exe") -Destination (Join-Path $binDir "pchat-gui.exe") -Force

Write-Host "[package-gui] bundle ready at $srcGui"
Write-Host "[package-gui] pchat-gui.exe also copied to $binDir"
