# Offline program-only snapshots. Hashes detect incomplete or changed local
# copies; they are not publisher signatures or authenticity proof.
Set-StrictMode -Version Latest

function Resolve-NEVRInstallRoots([string]$LocalAppData) {
    if ([string]::IsNullOrWhiteSpace($LocalAppData) -or -not [IO.Path]::IsPathRooted($LocalAppData)) {
        throw 'LOCALAPPDATA must be an absolute user-data directory.'
    }
    $localRoot = [IO.Path]::GetFullPath($LocalAppData).TrimEnd('\', '/')
    if ($localRoot -eq [IO.Path]::GetPathRoot($LocalAppData).TrimEnd('\', '/')) {
        throw 'Refusing a drive root as LOCALAPPDATA.'
    }
    $null = Assert-NEVRRegularPath $localRoot
    $null = Assert-NEVRRegularPath (Join-Path $localRoot 'Programs')
    $installRoot = Assert-NEVRRegularPath (Join-Path $localRoot 'Programs\NEVR-Anticheat')
    $dataRoot = Assert-NEVRRegularPath (Join-Path $localRoot 'NEVR-Anticheat')
    return [pscustomobject]@{ InstallRoot = $installRoot; DataRoot = $dataRoot }
}

function Test-NEVRProgramFileName([string]$Name) {
    return $Name -cmatch '^(nevr-(desktop|ac|server|bridge|compat)\.exe|README\.md|README-WINDOWS\.txt|nevr\.ico|configs/[A-Za-z0-9_-]+\.toml)$'
}

function Assert-NEVRRegularPath([string]$Root, [string]$Relative = '') {
    $current = [IO.Path]::GetFullPath($Root)
    $parts = @('')
    if ($Relative) { $parts += $Relative.Split('/') }
    foreach ($part in $parts) {
        if ($part) { $current = Join-Path $current $part }
        if (Test-Path -LiteralPath $current) {
            $item = Get-Item -LiteralPath $current -Force
            if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
                throw "Refusing linked program/snapshot path: $current"
            }
        }
    }
    return $current
}

function Read-NEVRProgramSnapshot([string]$Snapshot) {
    $manifest = Assert-NEVRRegularPath $Snapshot 'SHA256SUMS.txt'
    if (-not (Test-Path -LiteralPath $manifest -PathType Leaf)) {
        throw 'Snapshot has no integrity manifest. Legacy copies remain available for manual recovery; no files were replaced.'
    }
    if ((Get-Item -LiteralPath $manifest).Length -gt 65536) { throw 'Snapshot manifest is too large.' }
    $lines = @(Get-Content -LiteralPath $manifest)
    if ($lines.Count -lt 2 -or $lines[0] -cne 'nevr-program-snapshot/v1') { throw 'Invalid snapshot manifest header.' }
    $seen = @{}
    $entries = @()
    foreach ($line in $lines | Select-Object -Skip 1) {
        if ($line -notmatch '^([0-9a-fA-F]{64}) \*(.+)$') { throw 'Invalid snapshot checksum entry.' }
        $hash, $name = $Matches[1].ToLowerInvariant(), $Matches[2]
        if (-not (Test-NEVRProgramFileName $name) -or $seen.ContainsKey($name)) { throw "Unsafe or duplicate snapshot entry: $name" }
        $seen[$name] = $true
        $path = Assert-NEVRRegularPath $Snapshot $name
        if (-not (Test-Path -LiteralPath $path -PathType Leaf) -or
            (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant() -cne $hash) {
            throw "Snapshot checksum failed: $name"
        }
        $entries += [pscustomobject]@{ Name = $name; Path = $path; SHA256 = $hash }
    }
    foreach ($name in @('nevr-desktop.exe', 'nevr-ac.exe', 'nevr-server.exe', 'nevr-bridge.exe', 'nevr-compat.exe')) {
        if (-not $seen.ContainsKey($name)) { throw "Snapshot is incomplete: missing $name. Retained for manual recovery only." }
    }
    return $entries
}

function New-NEVRProgramSnapshot([string]$InstallRoot, [string]$RollbackRoot) {
    $null = Assert-NEVRRegularPath $InstallRoot
    $null = Assert-NEVRRegularPath $RollbackRoot
    [IO.Directory]::CreateDirectory($RollbackRoot) | Out-Null
    $snapshot = Join-Path $RollbackRoot ([DateTime]::UtcNow.ToString('yyyyMMdd-HHmmss-fffffff') + '-' + [Guid]::NewGuid().ToString('N'))
    [IO.Directory]::CreateDirectory($snapshot) | Out-Null
    $lines = [Collections.Generic.List[string]]::new()
    $lines.Add('nevr-program-snapshot/v1')
    $files = @(Get-ChildItem -LiteralPath $InstallRoot -File -Force)
    $configDir = Assert-NEVRRegularPath $InstallRoot 'configs'
    if (Test-Path -LiteralPath $configDir -PathType Container) { $files += @(Get-ChildItem -LiteralPath $configDir -File -Force) }
    foreach ($file in $files) {
        $name = $file.FullName.Substring([IO.Path]::GetFullPath($InstallRoot).TrimEnd('\', '/').Length + 1).Replace('\', '/')
        if (-not (Test-NEVRProgramFileName $name)) { continue }
        $source = Assert-NEVRRegularPath $InstallRoot $name
        $destination = Join-Path $snapshot $name
        [IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($destination)) | Out-Null
        $hash = (Get-FileHash -LiteralPath $source -Algorithm SHA256).Hash.ToLowerInvariant()
        Copy-Item -LiteralPath $source -Destination $destination -ErrorAction Stop
        if ((Get-FileHash -LiteralPath $destination -Algorithm SHA256).Hash.ToLowerInvariant() -cne $hash) {
            throw "Snapshot copy verification failed; incomplete copy retained at $snapshot"
        }
        $lines.Add("$hash *$name")
    }
    [IO.File]::WriteAllLines((Join-Path $snapshot 'SHA256SUMS.txt'), $lines, [Text.UTF8Encoding]::new($false))
    $null = Read-NEVRProgramSnapshot $snapshot
    return $snapshot
}

function Assert-NEVRProgramsClosed([string]$InstallRoot) {
    $rootPrefix = [IO.Path]::GetFullPath($InstallRoot).TrimEnd('\', '/') + [IO.Path]::DirectorySeparatorChar
    foreach ($process in @(Get-Process -Name 'nevr-desktop', 'nevr-ac', 'nevr-server', 'nevr-bridge', 'nevr-compat' -ErrorAction SilentlyContinue)) {
        try { $path = $process.Path } catch { throw 'Cannot verify whether a NEVR process is using this installation. Close NEVR applications and retry.' }
        if (-not $path) { throw 'Cannot verify a running NEVR process. Close NEVR applications and retry.' }
        if ([IO.Path]::GetFullPath($path).StartsWith($rootPrefix, [StringComparison]::OrdinalIgnoreCase)) {
            throw "Close NEVR using its Quit button before changing program files (process $($process.Id)). No process was terminated."
        }
    }
}

function Restore-NEVRProgramSnapshot(
    [string]$Snapshot, [string]$InstallRoot, [string]$RollbackRoot,
    [scriptblock]$ReplaceFile = { param($Source, $Target) [IO.File]::Copy($Source, $Target, $true) }
) {
    $entries = @(Read-NEVRProgramSnapshot $Snapshot)
    $null = Assert-NEVRRegularPath $InstallRoot
    Assert-NEVRProgramsClosed $InstallRoot
    foreach ($entry in $entries) { $null = Assert-NEVRRegularPath $InstallRoot $entry.Name }
    $recovery = New-NEVRProgramSnapshot $InstallRoot $RollbackRoot
    $originals = @{}
    foreach ($entry in @(Read-NEVRProgramSnapshot $recovery)) { $originals[$entry.Name] = $entry }
    $changed = [Collections.Generic.List[string]]::new()
    try {
        foreach ($entry in $entries) {
            if ((Get-FileHash -LiteralPath $entry.Path -Algorithm SHA256).Hash.ToLowerInvariant() -cne $entry.SHA256) {
                throw "Snapshot changed during rollback: $($entry.Name)"
            }
            $destination = Assert-NEVRRegularPath $InstallRoot $entry.Name
            [IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($destination)) | Out-Null
            $changed.Add($entry.Name) # Include a partially failed replacement.
            & $ReplaceFile $entry.Path $destination
            if ((Get-FileHash -LiteralPath $destination -Algorithm SHA256).Hash.ToLowerInvariant() -cne $entry.SHA256) {
                throw "Restored file failed verification: $($entry.Name)"
            }
        }
    } catch {
        $failure = $_.Exception.Message
        $rollbackErrors = @()
        foreach ($name in $changed) {
            try {
                $destination = Assert-NEVRRegularPath $InstallRoot $name
                if ($originals.ContainsKey($name)) {
                    [IO.File]::Copy($originals[$name].Path, $destination, $true)
                } elseif (Test-Path -LiteralPath $destination -PathType Leaf) {
                    Remove-Item -LiteralPath $destination -ErrorAction Stop
                }
            } catch { $rollbackErrors += $_.Exception.Message }
        }
        throw "Rollback failed: $failure. Original program recovery: $recovery. Recovery errors: $($rollbackErrors -join '; ')"
    }
    return $recovery
}
