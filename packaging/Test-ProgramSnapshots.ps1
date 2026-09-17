# Regression tests for snapshot ordering, rollback selection, snapshot pruning
# (PowerShell and Setup's Pascal code) and the ZIP-install uninstaller.
#
#   powershell -NoProfile -ExecutionPolicy Bypass -File packaging\Test-ProgramSnapshots.ps1
#
# Everything runs inside one scratch folder under %TEMP% with LOCALAPPDATA and
# APPDATA pointed into it. Synthetic text "executables" only: no installed app,
# no real database, no real Desktop or Start menu, no network. The Setup checks
# need the Inno Setup compiler and are reported as skipped without it unless
# -RequireSetupCompiler is given (CI passes it).
[CmdletBinding()]
param([switch]$RequireSetupCompiler)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. (Join-Path $PSScriptRoot 'Program-Snapshot.ps1')
$testRoot = [IO.Path]::GetFullPath((Join-Path ([IO.Path]::GetTempPath()) ('nevr-release-safety-tests-' + [Guid]::NewGuid().ToString('N'))))
[IO.Directory]::CreateDirectory($testRoot) | Out-Null
$results = [Collections.Generic.List[object]]::new()
$programNames = @('nevr-desktop.exe', 'nevr-ac.exe', 'nevr-server.exe', 'nevr-bridge.exe', 'nevr-compat.exe')

function Assert-Test([bool]$Condition, [string]$Message) { if (-not $Condition) { throw $Message } }
function Invoke-Test([string]$Name, [scriptblock]$Action) {
    try {
        $outcome = & $Action
        if ($outcome -eq 'skipped') { $results.Add([pscustomobject]@{ name = $Name; status = 'skip' }); Write-Host "SKIP $Name"; return }
        $results.Add([pscustomobject]@{ name = $Name; status = 'pass' }); Write-Host "PASS $Name"
    } catch {
        $results.Add([pscustomobject]@{ name = $Name; status = 'fail' }); Write-Host "FAIL $Name`: $($_.Exception.Message)"
    }
}
function New-TestInstall([string]$Directory, [string]$Content) {
    [IO.Directory]::CreateDirectory((Join-Path $Directory 'configs')) | Out-Null
    foreach ($file in $programNames) { [IO.File]::WriteAllText((Join-Path $Directory $file), "$Content-$file") }
    [IO.File]::WriteAllText((Join-Path $Directory 'configs\default.toml'), "$Content-config")
    return $Directory
}
# A verified snapshot with a chosen folder name and a chosen creation instant.
function New-TestSnapshot([string]$RollbackRoot, [string]$Name, [string]$Content, [DateTime]$CreatedUtc) {
    $directory = New-TestInstall (Join-Path $RollbackRoot $Name) $Content
    $lines = @('nevr-program-snapshot/v1')
    foreach ($file in $programNames + 'configs/default.toml') {
        $lines += (Get-FileHash -LiteralPath (Join-Path $directory $file) -Algorithm SHA256).Hash.ToLowerInvariant() + " *$file"
    }
    [IO.File]::WriteAllLines((Join-Path $directory 'SHA256SUMS.txt'), $lines, [Text.UTF8Encoding]::new($false))
    [IO.Directory]::SetCreationTimeUtc($directory, $CreatedUtc)
    [IO.Directory]::SetLastWriteTimeUtc($directory, $CreatedUtc)
    return $directory
}
function Get-SnapshotNames([string]$RollbackRoot) { return @(Get-ChildItem -LiteralPath $RollbackRoot -Directory | Sort-Object Name | ForEach-Object Name) }

try {
    # --- ordering -----------------------------------------------------------
    Invoke-Test 'rollback_selects_by_instant_not_by_folder_name_across_both_clocks' {
        # The reported failure, in a UTC-5 zone. 14:00Z: a rollback writes a
        # recovery snapshot of a bad build, named in UTC (...-140000-...).
        # 15:00Z = 10:00 local: Setup snapshots the working build, legacy name in
        # LOCAL time (...-100000-0). By name the older recovery copy "wins".
        $root = Join-Path $testRoot 'order'
        $recovery = New-TestSnapshot $root ('20260310-140000-0000000-' + ('a' * 32)) 'bad-build' ([DateTime]::new(2026, 3, 10, 14, 0, 0, [DateTimeKind]::Utc))
        $setup = New-TestSnapshot $root '20260310-100000-0' 'working-build' ([DateTime]::new(2026, 3, 10, 15, 0, 0, [DateTimeKind]::Utc))
        $byName = @(Get-ChildItem -LiteralPath $root -Directory | Sort-Object Name -Descending)[0].FullName
        Assert-Test ($byName -eq $recovery) 'Fixture does not reproduce the by-name inversion'
        $selection = Select-NEVRRollbackSnapshot $root
        Assert-Test ($selection.Snapshot.FullName -eq $setup) "Selected $($selection.Snapshot.Name); the Setup snapshot taken an hour later is newer"
    }
    Invoke-Test 'utc_named_snapshots_are_ordered_by_name_even_if_the_folder_was_copied' {
        $root = Join-Path $testRoot 'order-utc'
        # Creation times deliberately contradict the names, as after a profile copy.
        $older = New-TestSnapshot $root '20260310-090000Z-0' 'older' ([DateTime]::new(2026, 5, 1, 0, 0, 0, [DateTimeKind]::Utc))
        $newer = New-TestSnapshot $root ('20260310-093000-0000000-' + ('b' * 32)) 'newer' ([DateTime]::new(2026, 4, 1, 0, 0, 0, [DateTimeKind]::Utc))
        $ordered = @(Get-NEVRProgramSnapshots $root)
        Assert-Test ($ordered[0].FullName -eq $newer -and $ordered[1].FullName -eq $older) 'UTC names were not authoritative'
        Assert-Test ($ordered[1].CreatedUtc -eq [DateTime]::new(2026, 3, 10, 9, 0, 0, [DateTimeKind]::Utc)) "Setup UTC name parsed as $($ordered[1].CreatedUtc.ToString('o'))"
    }
    Invoke-Test 'rollback_skips_unusable_snapshots_instead_of_failing_on_the_newest' {
        $root = Join-Path $testRoot 'skip'
        $valid = New-TestSnapshot $root '20260301-120000Z-0' 'good' ([DateTime]::new(2026, 3, 1, 12, 0, 0, [DateTimeKind]::Utc))
        # Setup interrupted mid-snapshot: a newer folder without a manifest.
        $null = New-TestInstall (Join-Path $root '20260302-120000Z-0') 'interrupted'
        $tampered = New-TestSnapshot $root '20260303-120000Z-0' 'tampered' ([DateTime]::new(2026, 3, 3, 12, 0, 0, [DateTimeKind]::Utc))
        [IO.File]::WriteAllText((Join-Path $tampered 'nevr-ac.exe'), 'changed after the snapshot')
        $selection = Select-NEVRRollbackSnapshot $root
        Assert-Test ($selection.Snapshot.FullName -eq $valid) "Selected $($selection.Snapshot.Name)"
        Assert-Test ($selection.Skipped.Count -eq 2) "Skipped: $($selection.Skipped -join ' | ')"
        Assert-Test (Test-Path -LiteralPath (Join-Path $tampered 'nevr-desktop.exe')) 'An unusable snapshot was deleted'
        $empty = Join-Path $testRoot 'skip-none'
        $null = New-TestInstall (Join-Path $empty '20260302-120000Z-0') 'interrupted'
        $message = ''
        try { $null = Select-NEVRRollbackSnapshot $empty } catch { $message = $_.Exception.Message }
        Assert-Test ($message -like '*No valid previous*20260302-120000Z-0*') "Refusal was: $message"
    }

    # --- pruning (PowerShell) -------------------------------------------------
    Invoke-Test 'pruning_keeps_the_newest_verified_snapshots_and_everything_unverifiable' {
        $root = Join-Path $testRoot 'prune'
        $day = { param($d) [DateTime]::new(2026, 3, $d, 12, 0, 0, [DateTimeKind]::Utc) }
        foreach ($d in 1..5) { $null = New-TestSnapshot $root ('202603{0:d2}-120000Z-0' -f $d) "v$d" (& $day $d) }
        $null = New-TestInstall (Join-Path $root '20260201-120000Z-0') 'legacy-without-manifest'
        # An interrupted Setup NEWER than a snapshot worth keeping: it must not
        # use up one of the three places, or a usable snapshot is lost for it.
        $null = New-TestInstall (Join-Path $root '20260304-180000Z-0') 'interrupted-without-manifest'
        $foreign = New-TestSnapshot $root '20260202-120000Z-0' 'foreign' ([DateTime]::new(2026, 2, 2, 12, 0, 0, [DateTimeKind]::Utc))
        [IO.File]::WriteAllText((Join-Path $foreign 'notes-from-the-user.txt'), 'not ours')
        $outcome = Remove-NEVRStaleProgramSnapshots $root 3
        Assert-Test ((@($outcome.Removed | Sort-Object) -join ',') -eq '20260301-120000Z-0,20260302-120000Z-0') "Removed: $($outcome.Removed -join ',')"
        Assert-Test ((Get-SnapshotNames $root) -join ',' -eq '20260201-120000Z-0,20260202-120000Z-0,20260303-120000Z-0,20260304-120000Z-0,20260304-180000Z-0,20260305-120000Z-0') "Left: $((Get-SnapshotNames $root) -join ',')"
        Assert-Test ((@(Get-ChildItem -LiteralPath $foreign -Recurse -File).Count) -eq 8) 'A snapshot holding foreign content was partially deleted'
        foreach ($name in '20260303-120000Z-0', '20260304-120000Z-0', '20260305-120000Z-0') { $null = Read-NEVRProgramSnapshot (Join-Path $root $name) }
    }
    Invoke-Test 'pruning_never_follows_a_junction_out_of_the_snapshot' {
        $root = Join-Path $testRoot 'prune-link'
        $outside = New-TestInstall (Join-Path $testRoot 'outside-target') 'outside'
        foreach ($d in 2..5) { $null = New-TestSnapshot $root ('202604{0:d2}-120000Z-0' -f $d) "v$d" ([DateTime]::new(2026, 4, $d, 12, 0, 0, [DateTimeKind]::Utc)) }
        $linked = New-TestSnapshot $root '20260401-120000Z-0' 'linked' ([DateTime]::new(2026, 4, 1, 12, 0, 0, [DateTimeKind]::Utc))
        New-Item -ItemType Junction -Path (Join-Path $linked 'extra') -Target $outside | Out-Null
        try {
            $null = Remove-NEVRStaleProgramSnapshots $root 3
            Assert-Test (Test-Path -LiteralPath (Join-Path $outside 'nevr-desktop.exe')) 'Pruning deleted files THROUGH a junction'
            Assert-Test (Test-Path -LiteralPath (Join-Path $linked 'nevr-desktop.exe')) 'A snapshot holding a junction was partially deleted'
            Assert-Test (-not (Test-Path -LiteralPath (Join-Path $root '20260402-120000Z-0'))) 'The plain stale snapshot was not pruned'
        } finally { [IO.Directory]::Delete((Join-Path $linked 'extra')) }
    }
    Invoke-Test 'rollback_prunes_but_never_the_snapshot_it_restores_from' {
        $root = Join-Path $testRoot 'restore-protect'
        $target = New-TestInstall (Join-Path $testRoot 'restore-target') 'current'
        $oldest = New-TestSnapshot $root '20260101-120000Z-0' 'oldest' ([DateTime]::new(2026, 1, 1, 12, 0, 0, [DateTimeKind]::Utc))
        foreach ($d in 2..4) { $null = New-TestSnapshot $root ('202601{0:d2}-120000Z-0' -f $d) "v$d" ([DateTime]::new(2026, 1, $d, 12, 0, 0, [DateTimeKind]::Utc)) }
        $recovery = Restore-NEVRProgramSnapshot $oldest $target $root
        Assert-Test ([IO.File]::ReadAllText((Join-Path $target 'nevr-desktop.exe')) -eq 'oldest-nevr-desktop.exe') 'Oldest snapshot was not restored'
        $null = Read-NEVRProgramSnapshot $oldest
        $left = Get-SnapshotNames $root
        Assert-Test ($left.Count -eq 4 -and $left -contains '20260101-120000Z-0' -and $left -contains (Split-Path -Leaf $recovery) -and
            $left -notcontains '20260102-120000Z-0') "Left: $($left -join ',')"
    }
    Invoke-Test 'every_new_snapshot_prunes_so_installs_cannot_accumulate' {
        $root = Join-Path $testRoot 'accumulate'
        $source = New-TestInstall (Join-Path $testRoot 'accumulate-source') 'program'
        foreach ($i in 1..6) { $null = New-NEVRProgramSnapshot $source $root }
        Assert-Test ((Get-SnapshotNames $root).Count -eq 3) "Six installs left $((Get-SnapshotNames $root).Count) snapshots"
    }

    # --- uninstaller ------------------------------------------------------------
    Invoke-Test 'uninstaller_started_from_its_installed_folder_removes_the_program_then_the_shortcuts' {
        $profileRoot = Join-Path $testRoot 'uninstall'
        $local = Join-Path $profileRoot 'Local'
        $install = New-TestInstall (Join-Path $local 'Programs\NEVR-Anticheat') 'installed'
        $data = Join-Path $local 'NEVR-Anticheat'
        $desktop = Join-Path $profileRoot 'Desktop'
        $startMenu = Join-Path $profileRoot 'Roaming\Microsoft\Windows\Start Menu\Programs'
        foreach ($directory in $data, $desktop, $startMenu) { [IO.Directory]::CreateDirectory($directory) | Out-Null }
        [IO.File]::WriteAllText((Join-Path $data 'nevr-anticheat.db'), 'evidence must survive')
        foreach ($shortcut in (Join-Path $desktop 'NEVR-Anticheat.lnk'), (Join-Path $startMenu 'NEVR-Anticheat.lnk')) { [IO.File]::WriteAllText($shortcut, 'lnk') }
        foreach ($name in 'Uninstall-NEVR.ps1', 'Uninstall-NEVR.cmd', 'Program-Snapshot.ps1') { Copy-Item -LiteralPath (Join-Path $PSScriptRoot $name) -Destination $install }
        $saved = $env:LOCALAPPDATA, $env:APPDATA
        try {
            $env:LOCALAPPDATA = $local; $env:APPDATA = Join-Path $profileRoot 'Roaming'
            # Exactly the situation of a double-clicked Uninstall-NEVR.cmd: a
            # cmd.exe AND its PowerShell child both sitting in the program folder.
            # The script is called directly only so the scratch Desktop can be named.
            $command = 'powershell.exe -NoProfile -ExecutionPolicy Bypass -File Uninstall-NEVR.ps1 -NoPause -DesktopDirectory "' + $desktop + '"'
            $wrapper = Start-Process -FilePath 'cmd.exe' -ArgumentList '/d', '/c', $command -WorkingDirectory $install -WindowStyle Hidden -Wait -PassThru
            Assert-Test ($wrapper.ExitCode -eq 0) "Uninstaller exited with $($wrapper.ExitCode)"
        } finally { $env:LOCALAPPDATA, $env:APPDATA = $saved }
        $deadline = [DateTime]::UtcNow.AddSeconds(45)
        while ((Test-Path -LiteralPath $install) -and [DateTime]::UtcNow -lt $deadline) { Start-Sleep -Milliseconds 300 }
        if (Test-Path -LiteralPath $install) { throw "Program folder still exists with: $((Get-ChildItem -LiteralPath $install -Name) -join ', ')" }
        $deadline = [DateTime]::UtcNow.AddSeconds(10)
        while ((Test-Path -LiteralPath (Join-Path $desktop 'NEVR-Anticheat.lnk')) -and [DateTime]::UtcNow -lt $deadline) { Start-Sleep -Milliseconds 300 }
        Assert-Test (-not (Test-Path -LiteralPath (Join-Path $desktop 'NEVR-Anticheat.lnk'))) 'Desktop shortcut was left behind'
        Assert-Test (-not (Test-Path -LiteralPath (Join-Path $startMenu 'NEVR-Anticheat.lnk'))) 'Start-menu shortcut was left behind'
        Assert-Test ([IO.File]::ReadAllText((Join-Path $data 'nevr-anticheat.db')) -eq 'evidence must survive') 'Evidence was removed without -RemoveEvidence'
    }
    Invoke-Test 'uninstaller_removes_shortcuts_only_after_the_program_folder_is_gone' {
        $text = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'Uninstall-NEVR.ps1')
        $program = $text.IndexOf('Remove-Item -LiteralPath $installRoot')
        $shortcuts = $text.IndexOf('Remove-Item -LiteralPath $startShortcut')
        Assert-Test ($program -ge 0 -and $shortcuts -gt $program) 'Shortcuts are removed before the program folder'
        Assert-Test ($text -notmatch 'Stop-Process|taskkill') 'Uninstall terminates processes'
    }

    # --- Setup's Pascal code ----------------------------------------------------
    Invoke-Test 'setup_names_snapshots_in_utc_and_prunes_like_powershell' {
        $iscc = @(@(
            (Get-Command ISCC.exe -ErrorAction SilentlyContinue | Select-Object -ExpandProperty Source -ErrorAction SilentlyContinue),
            (Join-Path $env:LOCALAPPDATA 'Programs\Inno Setup 6\ISCC.exe'),
            (Join-Path ${env:ProgramFiles(x86)} 'Inno Setup 7\ISCC.exe'),
            (Join-Path ${env:ProgramFiles(x86)} 'Inno Setup 6\ISCC.exe')
        ) | Where-Object { $_ -and (Test-Path -LiteralPath $_) })
        if ($iscc.Count -eq 0) {
            if ($RequireSetupCompiler) { throw 'Inno Setup compiler not found.' }
            return 'skipped'
        }
        # Compile the REAL [Code] section of the installer into a harness whose
        # only action is to call the snapshot routines on a scratch folder. It
        # installs nothing: InitializeSetup returns False.
        $script = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'NEVR-Anticheat.iss')
        $code = $script.Substring($script.IndexOf('[Code]') + '[Code]'.Length)
        $work = Join-Path $testRoot 'setup-code'
        [IO.Directory]::CreateDirectory($work) | Out-Null
        $harness = @"
[Setup]
AppName=NEVR snapshot code harness
AppVersion=0.0.0
CreateAppDir=no
Uninstallable=no
PrivilegesRequired=lowest
OutputDir=$work
OutputBaseFilename=nevr-snapshot-harness
[Code]
$code
function InitializeSetup(): Boolean;
var
  Root: String;
begin
  Root := ExpandConstant('{param:TestRoot}');
  SaveStringToFile(AddBackslash(Root) + 'stamp.txt', UtcSnapshotStamp, False);
  PruneProgramSnapshots(AddBackslash(Root) + 'program-rollbacks', AddBackslash(Root) + 'program-rollbacks\' + ExpandConstant('{param:Current}'), SnapshotsToKeep - 1);
  SaveStringToFile(AddBackslash(Root) + 'done.txt', 'done', False);
  Result := False;
end;
"@
        $harnessPath = Join-Path $work 'harness.iss'
        [IO.File]::WriteAllText($harnessPath, $harness)
        & $iscc[0] '/Qp' $harnessPath | Out-Null
        if ($LASTEXITCODE -ne 0) { throw "Harness compilation failed with exit code $LASTEXITCODE" }

        $root = Join-Path $work 'program-rollbacks'
        # Names contradict ages on purpose: legacy LOCAL names sort above UTC ones.
        $null = New-TestSnapshot $root '20260509-230000-0' 'oldest-but-greatest-name' ([DateTime]::new(2026, 5, 1, 12, 0, 0, [DateTimeKind]::Utc))
        $null = New-TestSnapshot $root '20260502-120000Z-0' 'v2' ([DateTime]::new(2026, 5, 2, 12, 0, 0, [DateTimeKind]::Utc))
        $null = New-TestSnapshot $root ('20260503-120000-0000000-' + ('c' * 32)) 'v3' ([DateTime]::new(2026, 5, 3, 12, 0, 0, [DateTimeKind]::Utc))
        $null = New-TestSnapshot $root '20260504-120000Z-0' 'v4' ([DateTime]::new(2026, 5, 4, 12, 0, 0, [DateTimeKind]::Utc))
        # "Current" is the snapshot Setup just wrote; it is kept even if its folder time is odd.
        $null = New-TestSnapshot $root '20260505-120000Z-0' 'current' ([DateTime]::new(2026, 4, 1, 12, 0, 0, [DateTimeKind]::Utc))
        $null = New-TestInstall (Join-Path $root '20260101-120000-0') 'legacy-without-manifest'
        $null = New-TestInstall (Join-Path $root '20260504-180000Z-0') 'interrupted-without-manifest'
        $foreign = New-TestSnapshot $root '20260102-120000Z-0' 'foreign' ([DateTime]::new(2026, 1, 2, 12, 0, 0, [DateTimeKind]::Utc))
        [IO.File]::WriteAllText((Join-Path $foreign 'notes-from-the-user.txt'), 'not ours')
        [IO.Directory]::SetLastWriteTimeUtc($foreign, [DateTime]::new(2026, 1, 2, 12, 0, 0, [DateTimeKind]::Utc))

        $run = Start-Process -FilePath (Join-Path $work 'nevr-snapshot-harness.exe') -Wait -PassThru -WindowStyle Hidden `
            -ArgumentList '/VERYSILENT', '/SUPPRESSMSGBOXES', '/NORESTART', ('/TestRoot="' + $work + '"'), '/Current=20260505-120000Z-0'
        Assert-Test (Test-Path -LiteralPath (Join-Path $work 'done.txt')) "Harness did not finish (exit $($run.ExitCode))"

        $stamp = [IO.File]::ReadAllText((Join-Path $work 'stamp.txt'))
        Assert-Test ($stamp -cmatch '^\d{8}-\d{6}Z$') "Setup stamp is '$stamp'"
        $stampUtc = [DateTime]::ParseExact($stamp.TrimEnd('Z'), 'yyyyMMdd-HHmmss', [Globalization.CultureInfo]::InvariantCulture, [Globalization.DateTimeStyles]::AssumeUniversal -bor [Globalization.DateTimeStyles]::AdjustToUniversal)
        Assert-Test ([Math]::Abs(([DateTime]::UtcNow - $stampUtc).TotalMinutes) -lt 5) "Setup stamp $stamp is not the current UTC time"
        $parsed = Get-NEVRSnapshotCreatedUtc ([IO.DirectoryInfo]::new((Join-Path $root "$stamp-0")))
        Assert-Test ([Math]::Abs(($parsed - $stampUtc).TotalSeconds) -lt 1) 'PowerShell does not read Setup folder names as UTC'

        $left = (Get-SnapshotNames $root) -join ','
        $expected = @('20260101-120000-0', '20260102-120000Z-0', ('20260503-120000-0000000-' + ('c' * 32)), '20260504-120000Z-0', '20260504-180000Z-0', '20260505-120000Z-0') -join ','
        Assert-Test ($left -eq $expected) "Setup left: $left"
        Assert-Test ((@(Get-ChildItem -LiteralPath $foreign -Recurse -File).Count) -eq 8) 'Setup partially deleted a snapshot holding foreign content'
    }

    $failed = @($results | Where-Object status -eq 'fail').Count
    Write-Host "$(@($results | Where-Object status -eq 'pass').Count) passed, $failed failed, $(@($results | Where-Object status -eq 'skip').Count) skipped"
    if ($failed -gt 0) { exit 1 }
} finally {
    $expectedPrefix = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\', '/') + [IO.Path]::DirectorySeparatorChar + 'nevr-release-safety-tests-'
    if (-not $testRoot.StartsWith($expectedPrefix, [StringComparison]::OrdinalIgnoreCase)) { throw 'Unsafe test cleanup path.' }
    Get-ChildItem -LiteralPath $testRoot -Recurse -Force -ErrorAction SilentlyContinue | Where-Object { $_.Attributes -band [IO.FileAttributes]::ReparsePoint } | ForEach-Object { [IO.Directory]::Delete($_.FullName) }
    Remove-Item -LiteralPath $testRoot -Recurse -Force -ErrorAction SilentlyContinue
}
