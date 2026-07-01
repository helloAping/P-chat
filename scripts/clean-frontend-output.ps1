# Wipe the previous Vite output before the next build. Vite emits
# content-hashed filenames (index-XXX.js, vendor-YYY.js), so a
# rebuild leaves the old assets sitting next to the new ones —
# bad when the binary embeds `web/` because the bundle size grows
# unboundedly. This script is a no-op on a clean tree.

# NOTE: do NOT set $ErrorActionPreference = "Stop". The Test-Path
# guards below can race with a parallel writer (e.g. vite still
# emitting from a previous task run) and return a stale "exists"
# result, after which Remove-Item then sees "not found". We want
# both cases — stale test, missing path — to be silent no-ops.

$root = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot "..")).Path

$webIndex = Join-Path $root "web\index.html"
$webAssets = Join-Path $root "web\assets"

if (Test-Path -LiteralPath $webIndex) {
    Remove-Item -LiteralPath $webIndex -Force -ErrorAction SilentlyContinue
}
if (Test-Path -LiteralPath $webAssets) {
    Remove-Item -LiteralPath $webAssets -Recurse -Force -ErrorAction SilentlyContinue
}
Write-Host "[clean-frontend-output] wiped web/index.html + web/assets"
