[CmdletBinding()]
param([switch]$RemoveEvidence)

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'Program-Snapshot.ps1')
$roots = Resolve-NEVRInstallRoots $env:LOCALAPPDATA
$installRoot, $dataRoot = $roots.InstallRoot, $roots.DataRoot
$startShortcut = Join-Path $env:APPDATA 'Microsoft\Windows\Start Menu\Programs\NEVR-Anticheat.lnk'
$desktopShortcut = Join-Path ([Environment]::GetFolderPath('Desktop')) 'NEVR-Anticheat.lnk'

Assert-NEVRProgramsClosed $installRoot
Remove-Item -LiteralPath $startShortcut, $desktopShortcut -Force -ErrorAction SilentlyContinue
if (Test-Path -LiteralPath $installRoot) { Remove-Item -LiteralPath $installRoot -Recurse -Force }
if ($RemoveEvidence -and (Test-Path -LiteralPath $dataRoot)) {
    Remove-Item -LiteralPath $dataRoot -Recurse -Force
    Write-Host 'NEVR-Anticheat and its local evidence were removed.'
} else {
    Write-Host "NEVR-Anticheat was removed. Evidence was preserved at $dataRoot"
}
