[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
$installRoot = Join-Path $env:LOCALAPPDATA 'Programs\NEVR-Anticheat'
$dataRoot = Join-Path $env:LOCALAPPDATA 'NEVR-Anticheat'
$rollbackRoot = Join-Path $dataRoot 'program-rollbacks'

if (-not (Test-Path -LiteralPath $rollbackRoot)) {
    throw "No previous NEVR-Anticheat program snapshot is available."
}
$snapshot = Get-ChildItem -LiteralPath $rollbackRoot -Directory |
    Sort-Object Name -Descending | Select-Object -First 1
if ($null -eq $snapshot -or -not (Test-Path -LiteralPath (Join-Path $snapshot.FullName 'nevr-desktop.exe'))) {
    throw "No valid previous NEVR-Anticheat program snapshot is available."
}

Get-Process -Name 'nevr-desktop' -ErrorAction SilentlyContinue | Stop-Process -Force
New-Item -ItemType Directory -Force -Path $installRoot | Out-Null
Get-ChildItem -LiteralPath $snapshot.FullName -File | Copy-Item -Destination $installRoot -Force
if (Test-Path -LiteralPath (Join-Path $snapshot.FullName 'configs')) {
    Copy-Item -LiteralPath (Join-Path $snapshot.FullName 'configs') -Destination $installRoot -Recurse -Force
}
Write-Host "Restored the program files from $($snapshot.Name). Local evidence was not changed."
Start-Process -FilePath (Join-Path $installRoot 'nevr-desktop.exe') -ArgumentList @('--config', (Join-Path $installRoot 'installed.toml'))
