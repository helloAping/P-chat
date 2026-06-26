# Wipe the previous Vite output before the next build. Vite emits
# content-hashed filenames (index-XXX.js, vendor-YYY.js), so a
# rebuild leaves the old assets sitting next to the new ones —
# bad when the binary embeds `web/` because the bundle size grows
# unboundedly. This script is a no-op on a clean tree.

$ErrorActionPreference = "Stop"
$root = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot "..")).Path

$webIndex = Join-Path $root "web\index.html"
$webAssets = Join-Path $root "web\assets"

if (Test-Path -LiteralPath $webIndex) {
    Remove-Item -LiteralPath $webIndex -Force
}
if (Test-Path -LiteralPath $webAssets) {
    Remove-Item -LiteralPath $webAssets -Recurse -Force
}
Write-Host "[clean-frontend-output] wiped web/index.html + web/assets"
