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

# Snapshot age. Two writers share program-rollbacks and historically used two
# clocks: Setup.exe named folders in LOCAL time (yyyymmdd-hhnnss-N) while this
# file names them in UTC, so "newest by name" could pick the wrong one by hours.
# Both writers now use UTC (Setup: yyyymmdd-hhnnssZ-N). Names are trusted only
# when their format says which clock wrote them.
function Get-NEVRSnapshotCreatedUtc([IO.DirectoryInfo]$Directory) {
    $invariant = [Globalization.CultureInfo]::InvariantCulture
    $assumeUtc = [Globalization.DateTimeStyles]::AssumeUniversal -bor [Globalization.DateTimeStyles]::AdjustToUniversal
    $parsed = [DateTime]::MinValue
    if ($Directory.Name -cmatch '^(\d{8}-\d{6}-\d{7})-[0-9a-f]{32}$' -and
        [DateTime]::TryParseExact($Matches[1], 'yyyyMMdd-HHmmss-fffffff', $invariant, $assumeUtc, [ref]$parsed)) { return $parsed }
    if ($Directory.Name -cmatch '^(\d{8}-\d{6})Z-\d+$' -and
        [DateTime]::TryParseExact($Matches[1], 'yyyyMMdd-HHmmss', $invariant, $assumeUtc, [ref]$parsed)) { return $parsed }
    $created = $Directory.CreationTimeUtc
    if ($Directory.Name -cmatch '^(\d{8}-\d{6})-\d+$' -and
        [DateTime]::TryParseExact($Matches[1], 'yyyyMMdd-HHmmss', $invariant, [Globalization.DateTimeStyles]::None, [ref]$parsed)) {
        # Legacy Setup name in the local time of the moment it was written. If
        # the folder's own creation time differs from the name by a plausible
        # zone offset, the folder is the original and its creation time is the
        # exact instant, whatever zone or DST rule applied then.
        $offset = [DateTime]::SpecifyKind($parsed, [DateTimeKind]::Utc) - $created
        $quarterHours = [Math]::Round($offset.TotalMinutes / 15)
        if ([Math]::Abs($offset.TotalHours) -le 14.5 -and [Math]::Abs($offset.TotalSeconds - $quarterHours * 900) -le 5) { return $created }
        # A copied or restored folder: fall back to today's zone rules.
        try { return [TimeZoneInfo]::ConvertTimeToUtc($parsed, [TimeZoneInfo]::Local) } catch { return $created }
    }
    return $created
}

function Get-NEVRProgramSnapshots([string]$RollbackRoot) {
    $null = Assert-NEVRRegularPath $RollbackRoot
    if (-not (Test-Path -LiteralPath $RollbackRoot -PathType Container)) { return @() }
    $snapshots = foreach ($directory in @(Get-ChildItem -LiteralPath $RollbackRoot -Directory -Force)) {
        [pscustomobject]@{ Name = $directory.Name; FullName = $directory.FullName; CreatedUtc = (Get-NEVRSnapshotCreatedUtc $directory) }
    }
    return @($snapshots | Sort-Object -Property @{ Expression = 'CreatedUtc'; Descending = $true }, @{ Expression = 'Name'; Descending = $true })
}

# The newest snapshot that passes verification. One unusable folder (Setup
# interrupted before it wrote SHA256SUMS.txt, a legacy copy, a damaged file)
# must not make every older, valid snapshot unreachable.
function Select-NEVRRollbackSnapshot([string]$RollbackRoot) {
    $skipped = @()
    foreach ($candidate in @(Get-NEVRProgramSnapshots $RollbackRoot)) {
        try {
            $null = Read-NEVRProgramSnapshot $candidate.FullName
            return [pscustomobject]@{ Snapshot = $candidate; Skipped = @($skipped) }
        } catch {
            $skipped += "$($candidate.Name): $($_.Exception.Message)"
        }
    }
    $detail = if ($skipped.Count -gt 0) { ' Unusable snapshots were kept for manual recovery: ' + ($skipped -join ' | ') } else { '' }
    throw "No valid previous NEVR-Anticheat program snapshot is available.$detail"
}

# Deletes one VERIFIED snapshot without any recursive delete: only the files its
# manifest lists, the manifest, and the then-empty folders. A folder holding
# anything else is not ours to remove and is left exactly as it was.
function Remove-NEVRVerifiedSnapshot([string]$Snapshot, [object[]]$Entries) {
    $root = Assert-NEVRRegularPath $Snapshot
    $expected = @{ 'SHA256SUMS.txt' = $true }
    foreach ($entry in $Entries) { $expected[$entry.Name] = $true }
    foreach ($item in @(Get-ChildItem -LiteralPath $root -Force)) {
        if ($item.PSIsContainer) {
            if ($item.Name -cne 'configs' -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw "unexpected folder $($item.Name)" }
            foreach ($child in @(Get-ChildItem -LiteralPath $item.FullName -Force)) {
                if ($child.PSIsContainer -or -not $expected.ContainsKey("configs/$($child.Name)")) { throw "unexpected content configs/$($child.Name)" }
            }
        } elseif (-not $expected.ContainsKey($item.Name)) { throw "unexpected file $($item.Name)" }
    }
    foreach ($name in @($Entries | ForEach-Object Name) + 'SHA256SUMS.txt') {
        $path = Assert-NEVRRegularPath $root $name
        [IO.File]::SetAttributes($path, [IO.FileAttributes]::Normal)
        [IO.File]::Delete($path)
    }
    $configs = Join-Path $root 'configs'
    if (Test-Path -LiteralPath $configs -PathType Container) { [IO.Directory]::Delete($configs, $false) }
    [IO.Directory]::Delete($root, $false)
}

# Every update and every rollback adds a ~40 MB copy of the program, and the
# rolling release republishes on every master push. Keep the newest $Keep
# verified snapshots. Never removed: anything in $Protect (a snapshot being
# restored from), and anything that does NOT verify, because an unverifiable
# folder cannot be shown to be a NEVR snapshot and the existing contract is that
# such copies stay available for manual recovery.
function Remove-NEVRStaleProgramSnapshots([string]$RollbackRoot, [int]$Keep = 3, [string[]]$Protect = @()) {
    if ($Keep -lt 1) { throw 'At least one program snapshot must be kept.' }
    $protected = @{}
    foreach ($path in @($Protect | Where-Object { $_ })) { $protected[[IO.Path]::GetFullPath($path).TrimEnd('\', '/')] = $true }
    $removed = @(); $retained = @(); $valid = 0
    foreach ($candidate in @(Get-NEVRProgramSnapshots $RollbackRoot)) {
        try { $entries = @(Read-NEVRProgramSnapshot $candidate.FullName) } catch { $retained += $candidate.Name; continue }
        $valid++
        if ($valid -le $Keep -or $protected.ContainsKey([IO.Path]::GetFullPath($candidate.FullName).TrimEnd('\', '/'))) { continue }
        try {
            Remove-NEVRVerifiedSnapshot $candidate.FullName $entries
            $removed += $candidate.Name
        } catch { $retained += $candidate.Name }
    }
    return [pscustomobject]@{ Removed = @($removed); Retained = @($retained) }
}

function New-NEVRProgramSnapshot([string]$InstallRoot, [string]$RollbackRoot, [string[]]$Protect = @(), [int]$Keep = 3) {
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
    # Housekeeping only after the new snapshot verified, and never fatal: a
    # pruning problem must not block an install or a rollback.
    try { $null = Remove-NEVRStaleProgramSnapshots $RollbackRoot $Keep (@($Protect) + $snapshot) }
    catch { Write-Warning "Old program snapshots were not pruned: $($_.Exception.Message)" }
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
    # The snapshot being restored from must survive the pruning that follows
    # the recovery snapshot, however old it is.
    $recovery = New-NEVRProgramSnapshot $InstallRoot $RollbackRoot @($Snapshot)
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
