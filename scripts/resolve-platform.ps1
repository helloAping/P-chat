[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$Name,
    [ValidateSet('os', 'wails', 'label')][string]$Format = 'wails'
)

$n = $Name.Trim().ToLowerInvariant()

if ($n -eq '') {
    Write-Error 'Platform name is empty. Use win, linux, or mac.'
    exit 1
}

if ($n -match '[/\\]') {
    switch -Regex ($n) {
        '^windows/' { $os = 'windows'; break }
        '^linux/'   { $os = 'linux'; break }
        '^darwin/'  { $os = 'darwin'; break }
        default {
            Write-Error "Unknown platform '$Name'. Use win, linux, mac, or a Wails platform like linux/amd64."
            exit 1
        }
    }
    if ($Format -eq 'wails') {
        Write-Output $n
        exit 0
    }
} else {
    switch ($n) {
        { $_ -in @('win', 'windows') } {
            $os = 'windows'
            $wails = 'windows/amd64'
            break
        }
        { $_ -in @('linux', 'lin') } {
            $os = 'linux'
            $wails = 'linux/amd64'
            break
        }
        { $_ -in @('mac', 'macos', 'darwin') } {
            $os = 'darwin'
            $wails = 'darwin/universal'
            break
        }
        default {
            Write-Error "Unknown platform '$Name'. Use win, linux, or mac."
            exit 1
        }
    }
}

if (-not $wails) {
    switch ($os) {
        'windows' { $wails = 'windows/amd64' }
        'linux'   { $wails = 'linux/amd64' }
        'darwin'  { $wails = 'darwin/universal' }
    }
}

switch ($Format) {
    'os'    { Write-Output $os }
    'wails' { Write-Output $wails }
    'label' {
        switch ($os) {
            'windows' { Write-Output 'win' }
            'linux'   { Write-Output 'linux' }
            'darwin'  { Write-Output 'mac' }
        }
    }
}

exit 0
