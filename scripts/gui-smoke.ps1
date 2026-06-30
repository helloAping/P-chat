# End-to-end smoke test for the desktop app bundle.
#
#   1. install.ps1 to a temp InstallDir (with -Portable so we don't
#      touch the real Start Menu / registry)
#   2. launch the installed pchat-gui.exe, wait for pchat-server
#      to become healthy
#   3. hit /api/v1/health + /api/v1/providers through the spawned
#      server
#   4. kill pchat-gui
#   5. uninstall.ps1 to clean up
#
# Exits 0 on full pass, non-zero on any failure.

$ErrorActionPreference = "Stop"

$bundle   = "D:\develop\project\P-chat\cmd\pchat-gui\build\bin"
$install  = Join-Path $env:TEMP "pchat-smoke-install"
$log      = Join-Path $env:TEMP "pchat-smoke.log"
$guiLog   = Join-Path $install "pchat-gui.log"
$serverLog= Join-Path $install "pchat-server.log"

function Step($n, $msg) { Write-Host ""; Write-Host "==== [$n] $msg ====" -ForegroundColor Cyan }

# 0. clean previous state
Get-Process -Name "pchat-gui","pchat-server","pchat" -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
Start-Sleep -Milliseconds 500
if (Test-Path -LiteralPath $install) { Remove-Item -LiteralPath $install -Recurse -Force }

# 1. install (no -Portable, so we get a real separate install dir; no
#    Start Menu / registry because the test passes -NoStartMenu)
Step 1 "install.ps1 -NoStartMenu -InstallDir $install"
& powershell -NoProfile -ExecutionPolicy Bypass -File (Join-Path $bundle "install.ps1") -NoStartMenu -InstallDir $install 2>&1 | Tee-Object -FilePath $log | Out-Null
if (-not (Test-Path -LiteralPath (Join-Path $install "pchat-gui.exe"))) {
    Write-Host "FAIL: pchat-gui.exe missing after install" -ForegroundColor Red
    Get-Content -LiteralPath $log -Raw | Out-String
    exit 1
}
if (-not (Test-Path -LiteralPath (Join-Path $install "pchat-server.exe"))) {
    Write-Host "FAIL: pchat-server.exe missing after install" -ForegroundColor Red
    Get-Content -LiteralPath $log -Raw | Out-String
    exit 1
}
if (-not (Test-Path -LiteralPath (Join-Path $install "pchat.exe"))) {
    Write-Host "FAIL: pchat.exe missing after install (CLI not bundled)" -ForegroundColor Red
    Get-Content -LiteralPath $log -Raw | Out-String
    exit 1
}
if (-not (Test-Path -LiteralPath (Join-Path $install "uninstall.ps1"))) {
    Write-Host "FAIL: uninstall.ps1 missing after install" -ForegroundColor Red
    exit 1
}
Write-Host "OK: bundle present in $install" -ForegroundColor Green

# 2. launch the installed pchat-gui
Step 2 "launch pchat-gui.exe from $install"
Remove-Item -LiteralPath $guiLog,$serverLog -ErrorAction SilentlyContinue
$proc = Start-Process -FilePath (Join-Path $install "pchat-gui.exe") -PassThru -WindowStyle Hidden
Start-Sleep -Seconds 2
if ($proc.HasExited) {
    Write-Host "FAIL: pchat-gui exited immediately (code=$($proc.ExitCode))" -ForegroundColor Red
    if (Test-Path -LiteralPath $guiLog) { Get-Content -LiteralPath $guiLog -Raw | Out-String }
    exit 1
}
Write-Host "OK: pchat-gui PID=$($proc.Id) is running" -ForegroundColor Green

# 3. wait for pchat-server to become healthy (pchat-gui spawns it as a child)
Step 3 "wait for pchat-server to become healthy (max 30s)"
$port = $null
$deadline = (Get-Date).AddSeconds(30)
while ((Get-Date) -lt $deadline) {
    if (Test-Path -LiteralPath $guiLog) {
        $m = Select-String -Path $guiLog -Pattern "picked port (\d+)" -ErrorAction SilentlyContinue
        if ($m) { $port = [int]$m.Matches[0].Groups[1].Value; break }
    }
    Start-Sleep -Milliseconds 500
}
if (-not $port) {
    Write-Host "FAIL: pchat-gui never picked a port" -ForegroundColor Red
    if (Test-Path -LiteralPath $guiLog) { Get-Content -LiteralPath $guiLog -Raw | Out-String }
    if (Test-Path -LiteralPath $serverLog) { Get-Content -LiteralPath $serverLog -Raw | Out-String }
    $proc | Stop-Process -Force -ErrorAction SilentlyContinue
    exit 1
}
Write-Host "OK: pchat-gui picked port $port" -ForegroundColor Green

# 4. hit /api/v1/health
Step 4 "GET http://127.0.0.1:$port/api/v1/health"
try {
    $r = Invoke-WebRequest -Uri "http://127.0.0.1:$port/api/v1/health" -UseBasicParsing -TimeoutSec 5
    if ($r.StatusCode -ne 200) { throw "status=$($r.StatusCode)" }
    Write-Host "OK: $($r.Content)" -ForegroundColor Green
} catch {
    Write-Host "FAIL: $($_.Exception.Message)" -ForegroundColor Red
    $proc | Stop-Process -Force -ErrorAction SilentlyContinue
    exit 1
}

# 5. hit /api/v1/providers to confirm web UI flow
Step 5 "GET http://127.0.0.1:$port/api/v1/providers"
try {
    $r = Invoke-WebRequest -Uri "http://127.0.0.1:$port/api/v1/providers" -UseBasicParsing -TimeoutSec 5
    if ($r.StatusCode -ne 200) { throw "status=$($r.StatusCode)" }
    $body = $r.Content
    if ($body -notmatch '"cs"') { throw "providers response missing 'cs': $body" }
    Write-Host "OK: providers list contains user-configured 'cs' provider" -ForegroundColor Green
} catch {
    Write-Host "FAIL: $($_.Exception.Message)" -ForegroundColor Red
    $proc | Stop-Process -Force -ErrorAction SilentlyContinue
    exit 1
}

# 6. fetch the web UI HTML to confirm web/index.html is reachable
Step 6 "GET http://127.0.0.1:$port/app/index.html"
try {
    $r = Invoke-WebRequest -Uri "http://127.0.0.1:$port/app/index.html" -UseBasicParsing -TimeoutSec 5
    if ($r.StatusCode -ne 200) { throw "status=$($r.StatusCode)" }
    if ($r.Content -notmatch '<title>P-Chat</title>') { throw "html title is not P-Chat" }
    Write-Host "OK: web/index.html served, content-length=$($r.Content.Length)" -ForegroundColor Green
} catch {
    Write-Host "FAIL: $($_.Exception.Message)" -ForegroundColor Red
    $proc | Stop-Process -Force -ErrorAction SilentlyContinue
    exit 1
}

# 6a. exercise the upload endpoint: a tiny PNG must be accepted,
#     classified as image, and stored at ~/.p-chat/uploads/.
Step "6a" "POST /api/v1/uploads accepts a PNG and returns metadata"
try {
    # 1x1 transparent PNG (8-byte signature + IHDR + IDAT + IEND)
    $png = [byte[]]@(
        0x89,0x50,0x4e,0x47,0x0d,0x0a,0x1a,0x0a,
        0x00,0x00,0x00,0x0d,0x49,0x48,0x44,0x52,
        0x00,0x00,0x00,0x01,0x00,0x00,0x00,0x01,
        0x08,0x06,0x00,0x00,0x00,0x1f,0x15,0xc4,
        0x89,0x00,0x00,0x00,0x0d,0x49,0x44,0x41,
        0x54,0x78,0x9c,0x62,0x00,0x01,0x00,0x00,
        0x05,0x00,0x01,0x0d,0x0a,0x2d,0xb4,0x00,
        0x00,0x00,0x00,0x49,0x45,0x4e,0x44,0xae,
        0x42,0x60,0x82
    )
    $tmpPng = Join-Path $env:TEMP "pchat-smoke-attachment.png"
    [System.IO.File]::WriteAllBytes($tmpPng, $png)
    try {
        # Use HttpClient + MultipartFormDataContent — this is the
        # canonical way to build a multipart/form-data body in
        # .NET and avoids the brittle boundary-escaping that
        # hand-rolled StreamWriter constructions hit.
        Add-Type -AssemblyName System.Net.Http
        $handler = [System.Net.Http.HttpClientHandler]::new()
        $handler.UseDefaultCredentials = $true
        $http = [System.Net.Http.HttpClient]::new($handler)
        $content = [System.Net.Http.MultipartFormDataContent]::new()
        $fileStream = [System.IO.File]::OpenRead($tmpPng)
        $fileContent = [System.Net.Http.StreamContent]::new($fileStream)
        $fileContent.Headers.ContentType = [System.Net.Http.Headers.MediaTypeHeaderValue]::Parse("image/png")
        $content.Add($fileContent, "file", "dot.png")
        $resp = $http.PostAsync("http://127.0.0.1:$port/api/v1/uploads", $content).GetAwaiter().GetResult()
        $respBody = $resp.Content.ReadAsStringAsync().GetAwaiter().GetResult()
        if ($resp.StatusCode -ne 201) { throw "upload status = $($resp.StatusCode), body = $respBody" }
        $up = $respBody | ConvertFrom-Json
        if ($up.kind -ne "image") { throw "kind = $($up.kind), want image" }
        if ($up.id.Length -ne 16) { throw "id length = $($up.id.Length), want 16" }
        if (-not (Test-Path -LiteralPath (Join-Path $env:USERPROFILE ".p-chat\uploads\$($up.id)-$($up.name)"))) {
            throw "uploaded file not on disk at ~/.p-chat/uploads/$($up.id)-$($up.name)"
        }
        Write-Host "OK: uploaded id=$($up.id) kind=$($up.kind) size=$($up.size)" -ForegroundColor Green
    } finally {
        Remove-Item -LiteralPath $tmpPng -ErrorAction SilentlyContinue
    }
} catch {
    Write-Host "FAIL: $($_.Exception.Message)" -ForegroundColor Red
    $proc | Stop-Process -Force -ErrorAction SilentlyContinue
    exit 1
}

# 6b. exercise the per-session model + PATCH meta endpoint, so a
# regression on the new sessionMeta persistence path is caught
# even when no real LLM is configured.
Step "6b" "per-session model: create + PATCH + list"
try {
    # Pick a real provider/model from the running config so we
    # don't have to hard-code one in the smoke test.
    $provs = Invoke-WebRequest -Uri "http://127.0.0.1:$port/api/v1/providers" -UseBasicParsing -TimeoutSec 5 | ConvertFrom-Json
    $prov = $provs.providers[0]
    $model = $prov.models[0].name
    $provName = $prov.name
    Write-Host "  using provider=$provName model=$model" -ForegroundColor DarkGray

    # Create session A with model X.
    $bodyA = @{ provider = $provName; model = $model } | ConvertTo-Json
    $sessA = Invoke-WebRequest -Uri "http://127.0.0.1:$port/api/v1/sessions" -Method POST -ContentType "application/json" -Body $bodyA -UseBasicParsing -TimeoutSec 5 | ConvertFrom-Json
    if ($sessA.provider -ne $provName -or $sessA.model -ne $model) {
        throw "create: provider=$($sessA.provider) model=$($sessA.model), want $provName/$model"
    }
    Write-Host "OK: created session A id=$($sessA.id) meta=($($sessA.provider)/$($sessA.model))" -ForegroundColor Green

    # PATCH the session to swap the model.
    $patchBody = @{ model = $prov.models[-1].name } | ConvertTo-Json
    $patched = Invoke-WebRequest -Uri "http://127.0.0.1:$port/api/v1/sessions/$($sessA.id)" -Method PATCH -ContentType "application/json" -Body $patchBody -UseBasicParsing -TimeoutSec 5 | ConvertFrom-Json
    if ($patched.model -ne $prov.models[-1].name) {
        throw "patch: model=$($patched.model), want $($prov.models[-1].name)"
    }
    Write-Host "OK: PATCH updated session A model -> $($patched.model)" -ForegroundColor Green

    # GET /sessions/:id round-trip to confirm the meta is
    # actually persisted on the server.
    $re = Invoke-WebRequest -Uri "http://127.0.0.1:$port/api/v1/sessions/$($sessA.id)" -UseBasicParsing -TimeoutSec 5 | ConvertFrom-Json
    if ($re.model -ne $prov.models[-1].name) {
        throw "GET round-trip: model=$($re.model), want $($prov.models[-1].name)"
    }
    Write-Host "OK: GET /sessions/:id returns the updated model" -ForegroundColor Green

    # /sessions list should include the same meta.
    $list = Invoke-WebRequest -Uri "http://127.0.0.1:$port/api/v1/sessions" -UseBasicParsing -TimeoutSec 5 | ConvertFrom-Json
    $found = $list.sessions | Where-Object { $_.id -eq $sessA.id } | Select-Object -First 1
    if (-not $found -or $found.model -ne $prov.models[-1].name) {
        throw "list: meta for session A not present or wrong ($($found.model))"
    }
    Write-Host "OK: GET /sessions includes the per-session meta" -ForegroundColor Green

    # Bad model → 400.
    try {
        $bad = @{ provider = $provName; model = "definitely-not-a-real-model" } | ConvertTo-Json
        $null = Invoke-WebRequest -Uri "http://127.0.0.1:$port/api/v1/sessions/$($sessA.id)" -Method PATCH -ContentType "application/json" -Body $bad -UseBasicParsing -TimeoutSec 5
        throw "PATCH with bad model returned non-error"
    } catch {
        # 4xx is what we want. Accept any non-2xx response.
        if ($_.Exception.Response.StatusCode.value__ -lt 400) { throw }
    }
    Write-Host "OK: PATCH with unknown model → 4xx" -ForegroundColor Green
} catch {
    Write-Host "FAIL: $($_.Exception.Message)" -ForegroundColor Red
    $proc | Stop-Process -Force -ErrorAction SilentlyContinue
    exit 1
}

# 7. kill pchat-gui (which should kill the child pchat-server)
Step 7 "stop pchat-gui (kills child pchat-server)"
# Use taskkill /T /F so the whole process tree dies even if pchat-gui's
# graceful-shutdown handler doesn't get a chance to run. This is the
# same behavior the uninstall script relies on.
& taskkill /T /F /PID $proc.Id 2>&1 | Out-Null
# Give the kernel a moment to reap.
for ($i = 0; $i -lt 20; $i++) {
    $still = Get-Process -Name "pchat-gui","pchat-server","pchat" -ErrorAction SilentlyContinue
    if (-not $still) { break }
    Start-Sleep -Milliseconds 250
}
$stillRunning = Get-Process -Name "pchat-gui","pchat-server","pchat" -ErrorAction SilentlyContinue
if ($stillRunning) {
    Write-Host "WARN: $($stillRunning.Count) leftover process(es) - cleaning up" -ForegroundColor Yellow
    & taskkill /T /F /IM "pchat-server.exe" 2>&1 | Out-Null
    & taskkill /T /F /IM "pchat-gui.exe"    2>&1 | Out-Null
    & taskkill /T /F /IM "pchat.exe"        2>&1 | Out-Null
}
Write-Host "OK: cleanup done" -ForegroundColor Green

# 8. uninstall (full remove)
Step 8 "uninstall.ps1"
& powershell -NoProfile -ExecutionPolicy Bypass -File (Join-Path $install "uninstall.ps1") 2>&1 | Tee-Object -FilePath $log -Append | Out-Null
Start-Sleep -Seconds 1
if (Test-Path -LiteralPath $install) {
    # The uninstall may have scheduled a deferred delete; wait briefly
    Start-Sleep -Seconds 2
    if (Test-Path -LiteralPath $install) {
        Write-Host "WARN: $install still exists after uninstall; manual cleanup" -ForegroundColor Yellow
    } else {
        Write-Host "OK: $install removed" -ForegroundColor Green
    }
} else {
    Write-Host "OK: $install removed" -ForegroundColor Green
}

Write-Host ""
Write-Host "==== SMOKE TEST PASSED ====" -ForegroundColor Green
exit 0
