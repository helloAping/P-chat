param(
  [Parameter(Mandatory = $true)][string]$Path
)

$content = [IO.File]::ReadAllBytes($Path)
if ($content.Length -ge 3 -and $content[0] -eq 0xEF -and $content[1] -eq 0xBB -and $content[2] -eq 0xBF) {
  $newContent = $content[3..($content.Length - 1)]
  [IO.File]::WriteAllBytes($Path, $newContent)
  Write-Host ("[strip-bom] stripped 3-byte BOM from {0} ({1} -> {2} bytes)" -f $Path, $content.Length, $newContent.Length)
} else {
  Write-Host "[strip-bom] no BOM present in $Path (first 4 bytes: $($content[0..3] | ForEach-Object { '0x{0:X2}' -f $_ } -join ' '))"
}
