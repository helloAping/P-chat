[CmdletBinding(SupportsShouldProcess = $true)]
param()

# 只停止当前仓库 dev-bin 下的进程；绝不按进程名全局结束正式版。
# Stop only processes whose executable is inside this repository's dev-bin;
# never terminate installed production processes merely because names match.
$projectRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot ".."))
$devBin = [IO.Path]::GetFullPath((Join-Path $projectRoot "dev-bin"))
$devPrefix = $devBin.TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
$names = @("pchat-gui", "pchat-server", "pchat", "pchat-updater")

$stopped = 0
foreach ($process in Get-Process -Name $names -ErrorAction SilentlyContinue) {
    try {
        $executable = [IO.Path]::GetFullPath($process.Path)
    }
    catch {
        Write-Warning "[stop-dev-processes] skip PID=$($process.Id): executable path unavailable"
        continue
    }

    if (-not $executable.StartsWith($devPrefix, [StringComparison]::OrdinalIgnoreCase)) {
        Write-Host "[stop-dev-processes] keep PID=$($process.Id) outside dev-bin: $executable"
        continue
    }

    if ($PSCmdlet.ShouldProcess("PID=$($process.Id) $executable", "Stop development process")) {
        try {
            Stop-Process -Id $process.Id -Force -ErrorAction Stop
            $stopped++
            Write-Host "[stop-dev-processes] stopped PID=$($process.Id): $executable"
        }
        catch {
            # GUI 退出时可能已经结束其子 server；上方已校验可执行路径，这是无害竞态。
            # GUI shutdown may already have stopped its child server.
            # The executable path was verified above, so this is a benign race.
            Write-Warning "[stop-dev-processes] PID=$($process.Id) already exited or could not be stopped: $($_.Exception.Message)"
        }
    }
}

Write-Host "[stop-dev-processes] completed; stopped=$stopped dev_bin=$devBin"
