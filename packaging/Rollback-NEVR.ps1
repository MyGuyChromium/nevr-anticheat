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
# Newest by the instant the snapshot was taken, not by folder name: Setup used
# to name folders in local time and PowerShell in UTC. Unusable folders are
# skipped, not fatal, so an interrupted Setup cannot block every older snapshot.
$selection = Select-NEVRRollbackSnapshot $rollbackRoot
$snapshot = $selection.Snapshot
foreach ($skipped in $selection.Skipped) { Write-Warning "Skipped unusable snapshot $skipped" }

$recovery = Restore-NEVRProgramSnapshot $snapshot.FullName $installRoot $rollbackRoot
Write-Host "Restored verified program files from $($snapshot.Name) (taken $($snapshot.CreatedUtc.ToString('u'))). Current program recovery: $recovery"
Write-Host 'Local evidence, installed.toml, settings, and original replays were not changed.'
$config = Join-Path $installRoot 'installed.toml'
Start-Process -FilePath (Join-Path $installRoot 'nevr-desktop.exe') -ArgumentList @('--config', ('"' + $config + '"')) -WorkingDirectory $installRoot -WindowStyle Hidden
