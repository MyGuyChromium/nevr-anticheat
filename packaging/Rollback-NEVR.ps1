[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'Program-Snapshot.ps1')
if ([string]::IsNullOrWhiteSpace($env:LOCALAPPDATA)) { throw 'LOCALAPPDATA is unavailable.' }
$installRoot = Join-Path $env:LOCALAPPDATA 'Programs\NEVR-Anticheat'
$dataRoot = Join-Path $env:LOCALAPPDATA 'NEVR-Anticheat'
$rollbackRoot = Join-Path $dataRoot 'program-rollbacks'

$null = Assert-NEVRRegularPath $rollbackRoot
if (-not (Test-Path -LiteralPath $rollbackRoot -PathType Container)) {
    throw "No previous NEVR-Anticheat program snapshot is available."
}
$snapshot = Get-ChildItem -LiteralPath $rollbackRoot -Directory |
    Sort-Object Name -Descending | Select-Object -First 1
if ($null -eq $snapshot) {
    throw "No valid previous NEVR-Anticheat program snapshot is available."
}

$recovery = Restore-NEVRProgramSnapshot $snapshot.FullName $installRoot $rollbackRoot
Write-Host "Restored verified program files from $($snapshot.Name). Current program recovery: $recovery"
Write-Host 'Local evidence, installed.toml, settings, and original replays were not changed.'
$config = Join-Path $installRoot 'installed.toml'
Start-Process -FilePath (Join-Path $installRoot 'nevr-desktop.exe') -ArgumentList @('--config', ('"' + $config + '"')) -WorkingDirectory $installRoot -WindowStyle Hidden
