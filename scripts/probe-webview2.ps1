# probe-webview2.ps1 — find which app owns a busy WebView2 GPU/renderer process.
#
# WHY: Task Manager shows many "Microsoft Edge WebView2 (GPU 渲染)" processes.
# This script samples LIVE CPU% per WebView2 process and walks up the process
# tree to the real owning application, so you can tell whether the hot GPU
# belongs to P-Chat, CC Switch, Clash Verge, SearchHost, Widgets, etc.
#
# Usage:
#   powershell -File probe-webview2.ps1               # 5s sample, all WebView2 procs
#   powershell -File probe-webview2.ps1 -Seconds 10   # longer window
#   powershell -File probe-webview2.ps1 -ParentOf 896 # trace one pid's parent chain
param([int]$Seconds = 5, [int]$ParentOf = 0, [string]$Dir = '')
$ErrorActionPreference = 'SilentlyContinue'

function Get-Type([string]$cl) {
  if ($cl -match '--?type=([a-z_]+)') { return $matches[1] }
  return 'browser'
}

$all = Get-CimInstance Win32_Process
# Key every map by STRING pid: Int32 vs UInt32 lookup mismatch silently
# fails in PowerShell hashtables, so normalize keys up front.
$name = @{}; foreach ($p in $all) { $name["$($p.ProcessId)"] = $p.Name }
$parent = @{}; foreach ($p in $all) { $parent["$($p.ProcessId)"] = [int]$p.ParentProcessId }
$cmd = @{}; foreach ($p in $all) { $cmd["$($p.ProcessId)"] = $p.CommandLine }

# Walk up past msedgewebview2.exe parents to find the real owning app.
function Get-Owner([int]$pid0) {
  $cur = "$pid0"
  for ($i = 0; $i -lt 8; $i++) {
    $nm = $name[$cur]
    if (-not $nm) { return '<gone>' }
    if ($nm -ne 'msedgewebview2.exe' -and $nm -ne 'msedge.exe') { return $nm }
    $cur = "$($parent[$cur])"
  }
  return '<unknown>'
}

if ($ParentOf -gt 0) {
  $wsMap = @{}; foreach ($p in $all) { $wsMap["$($p.ProcessId)"] = $p.WorkingSetSize }
  $cur = "$ParentOf"
  for ($i = 0; $i -lt 10; $i++) {
    $nm = $name[$cur]
    if (-not $nm) { Write-Output ("pid={0}: <gone>" -f $cur); break }
    $wsMB = [math]::Round($wsMap[$cur] / 1MB, 1)
    Write-Output ("pid={0,-7} name={1,-22} parent={2,-7} ws={3,7}MB  owner={4}" -f $cur, $nm, $parent[$cur], $wsMB, (Get-Owner ([int]$cur)))
    $cur = "$($parent[$cur])"
    if ($cur -eq '4') { break }
  }
  exit 0
}

$p1 = Get-CimInstance Win32_Process | Where-Object {
  $_.Name -match 'msedgewebview2|msedge|pchat' -and
  ($Dir -eq '' -or "$($_.CommandLine)" -like "*$Dir*")
}
if (-not $p1) { Write-Output "NO msedgewebview2 / pchat process running"; exit 0 }
$t1 = Get-Date
$c1 = @{}; foreach ($p in $p1) { $c1["$($p.ProcessId)"] = $p.KernelModeTime + $p.UserModeTime }
Start-Sleep -Seconds $Seconds
$p2 = Get-CimInstance Win32_Process | Where-Object {
  $_.Name -match 'msedgewebview2|msedge|pchat' -and
  ($Dir -eq '' -or "$($_.CommandLine)" -like "*$Dir*")
}
$t2 = Get-Date
$wall = ($t2 - $t1).TotalSeconds
$c2 = @{}; foreach ($p in $p2) { $c2["$($p.ProcessId)"] = $p.KernelModeTime + $p.UserModeTime }
$ws = @{}; foreach ($p in $p2) { $ws["$($p.ProcessId)"] = $p.WorkingSetSize }

$rows = foreach ($p in $p2) {
  $id = $p.ProcessId
  $key = "$id"
  $delta = ([double]($c2[$key] - $c1[$key])) / 10000000.0
  $cpu = [math]::Round($delta / $wall * 100.0, 1)
  [pscustomobject]@{
    Pid = $id
    Type = Get-Type $cmd[$key]
    Cpu = $cpu
    Ws = [math]::Round($ws[$key] / 1MB, 1)
    Owner = Get-Owner $id
  }
}
Write-Output ("== {0}s sample — sorted by live CPU (0.0 = idle) ==" -f $wall)
$rows | Sort-Object Cpu -Descending | ForEach-Object {
  Write-Output ("  pid={0,-7} {1,-10} live={2,6}%  ws={3,7}MB  ownedBy={4}" -f $_.Pid, $_.Type, $_.Cpu, $_.Ws, $_.Owner)
}
