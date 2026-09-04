[CmdletBinding()]
param([switch]$RemoveEvidence)

$ErrorActionPreference = 'Stop'
$installRoot = Join-Path $env:LOCALAPPDATA 'Programs\NEVR-Anticheat'
$dataRoot = Join-Path $env:LOCALAPPDATA 'NEVR-Anticheat'
$startShortcut = Join-Path $env:APPDATA 'Microsoft\Windows\Start Menu\Programs\NEVR-Anticheat.lnk'
$desktopShortcut = Join-Path ([Environment]::GetFolderPath('Desktop')) 'NEVR-Anticheat.lnk'

Get-Process -Name 'nevr-desktop' -ErrorAction SilentlyContinue | Stop-Process -Force
Remove-Item -LiteralPath $startShortcut, $desktopShortcut -Force -ErrorAction SilentlyContinue
if (Test-Path -LiteralPath $installRoot) { Remove-Item -LiteralPath $installRoot -Recurse -Force }
if ($RemoveEvidence -and (Test-Path -LiteralPath $dataRoot)) {
    Remove-Item -LiteralPath $dataRoot -Recurse -Force
    Write-Host 'NEVR-Anticheat and its local evidence were removed.'
} else {
    Write-Host "NEVR-Anticheat was removed. Evidence was preserved at $dataRoot"
}
