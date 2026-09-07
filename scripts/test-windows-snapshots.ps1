[CmdletBinding()]
param([string]$OutputDirectory = '')

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot '..\packaging\Program-Snapshot.ps1')
$testRoot = Join-Path ([IO.Path]::GetTempPath()) ('nevr-snapshot-tests-' + [Guid]::NewGuid().ToString('N'))
$testRoot = [IO.Path]::GetFullPath($testRoot)
[IO.Directory]::CreateDirectory($testRoot) | Out-Null
$results = [Collections.Generic.List[object]]::new()
$programNames = @('nevr-desktop.exe', 'nevr-ac.exe', 'nevr-server.exe', 'nevr-bridge.exe', 'nevr-compat.exe')

function Assert-Test([bool]$Condition, [string]$Message) { if (-not $Condition) { throw $Message } }
function Assert-Throws([scriptblock]$Action, [string]$Expected) {
    $message = ''
    try { & $Action | Out-Null } catch { $message = $_.Exception.Message }
    Assert-Test ($message -like "*$Expected*") "Expected refusal containing '$Expected', got '$message'"
}
function New-TestInstall([string]$Name, [string]$Content) {
    $directory = Join-Path $testRoot $Name
    [IO.Directory]::CreateDirectory((Join-Path $directory 'configs')) | Out-Null
    foreach ($file in $programNames) { [IO.File]::WriteAllText((Join-Path $directory $file), "$Content-$file") }
    [IO.File]::WriteAllText((Join-Path $directory 'configs\default.toml'), "$Content-config")
    [IO.File]::WriteAllText((Join-Path $directory 'installed.toml'), 'personal DB configuration')
    [IO.File]::WriteAllText((Join-Path $directory 'nevr-anticheat.db'), 'evidence must survive')
    return $directory
}
function Invoke-Test([string]$Name, [scriptblock]$Action) {
    try {
        & $Action | Out-Null
        $results.Add([pscustomobject]@{ name = $Name; status = 'pass' })
        Write-Host "PASS $Name"
    } catch {
        $results.Add([pscustomobject]@{ name = $Name; status = 'fail'; detail = $_.Exception.Message })
        Write-Host "FAIL $Name`: $($_.Exception.Message)"
    }
}

try {
    $source = New-TestInstall 'old-programs' 'old'
    $snapshots = Join-Path $testRoot 'snapshots'
    $snapshot = New-NEVRProgramSnapshot $source $snapshots
    Invoke-Test 'program_only_snapshot_has_verified_payload' {
        $entries = @(Read-NEVRProgramSnapshot $snapshot)
        Assert-Test ($entries.Count -eq 6) 'Snapshot payload differs from five EXEs and one config'
        Assert-Test (-not (Test-Path (Join-Path $snapshot 'installed.toml'))) 'Copied personal installed configuration'
        Assert-Test (-not (Test-Path (Join-Path $snapshot 'nevr-anticheat.db'))) 'Copied evidence database'
    }
    Invoke-Test 'repeated_snapshots_do_not_overwrite' {
        $next = New-NEVRProgramSnapshot $source $snapshots
        Assert-Test ($next -ne $snapshot) 'Snapshot paths collided'
        $null = Read-NEVRProgramSnapshot $snapshot
    }
    Invoke-Test 'tampered_snapshot_refused_before_replacement' {
        $bad = New-NEVRProgramSnapshot $source $snapshots
        [IO.File]::WriteAllText((Join-Path $bad 'nevr-desktop.exe'), 'changed')
        $target = New-TestInstall 'tamper-target' 'current'
        Assert-Throws { Restore-NEVRProgramSnapshot $bad $target $snapshots } 'checksum failed'
        Assert-Test ([IO.File]::ReadAllText((Join-Path $target 'nevr-desktop.exe')) -eq 'current-nevr-desktop.exe') 'Tampered input changed installation'
    }
    Invoke-Test 'traversal_manifest_refused' {
        $bad = New-NEVRProgramSnapshot $source $snapshots
        [IO.File]::AppendAllText((Join-Path $bad 'SHA256SUMS.txt'), (('a' * 64) + ' *../outside.exe' + [Environment]::NewLine))
        Assert-Throws { Read-NEVRProgramSnapshot $bad } 'Unsafe or duplicate'
    }
    Invoke-Test 'duplicate_manifest_refused' {
        $bad = New-NEVRProgramSnapshot $source $snapshots
        $entry = @(Get-Content (Join-Path $bad 'SHA256SUMS.txt'))[1]
        [IO.File]::AppendAllText((Join-Path $bad 'SHA256SUMS.txt'), ($entry + [Environment]::NewLine))
        Assert-Throws { Read-NEVRProgramSnapshot $bad } 'Unsafe or duplicate'
    }
    Invoke-Test 'legacy_snapshot_retained_but_not_trusted' {
        $legacy = New-TestInstall 'legacy' 'old'
        Assert-Throws { Read-NEVRProgramSnapshot $legacy } 'no integrity manifest'
        Assert-Test (Test-Path (Join-Path $legacy 'nevr-desktop.exe')) 'Removed legacy recovery data'
    }
    Invoke-Test 'missing_program_refused' {
        $bad = New-NEVRProgramSnapshot $source $snapshots
        Remove-Item -LiteralPath (Join-Path $bad 'nevr-ac.exe')
        Assert-Throws { Read-NEVRProgramSnapshot $bad } 'checksum failed'
    }
    Invoke-Test 'rollback_preserves_current_programs_database_and_configuration' {
        $target = New-TestInstall 'success-target' 'current'
        $recovery = Restore-NEVRProgramSnapshot $snapshot $target $snapshots
        Assert-Test ([IO.File]::ReadAllText((Join-Path $target 'nevr-desktop.exe')) -eq 'old-nevr-desktop.exe') 'Old desktop not restored'
        Assert-Test ([IO.File]::ReadAllText((Join-Path $recovery 'nevr-desktop.exe')) -eq 'current-nevr-desktop.exe') 'Current desktop not preserved'
        Assert-Test ([IO.File]::ReadAllText((Join-Path $target 'nevr-anticheat.db')) -eq 'evidence must survive') 'Database changed'
        Assert-Test ([IO.File]::ReadAllText((Join-Path $target 'installed.toml')) -eq 'personal DB configuration') 'Personal configuration changed'
    }
    Invoke-Test 'partial_replacement_failure_restores_every_original' {
        $target = New-TestInstall 'failure-target' 'current'
        $counter = @{ value = 0 }
        $replace = {
            param($Source, $Target)
            $counter.value++
            if ($counter.value -eq 2) {
                [IO.File]::WriteAllText($Target, 'partial replacement')
                throw 'injected replacement failure'
            }
            [IO.File]::Copy($Source, $Target, $true)
        }.GetNewClosure()
        Assert-Throws { Restore-NEVRProgramSnapshot $snapshot $target $snapshots $replace } 'injected replacement failure'
        foreach ($name in $programNames) {
            Assert-Test ([IO.File]::ReadAllText((Join-Path $target $name)) -eq "current-$name") "Original $name not restored"
        }
    }
    Invoke-Test 'process_guard_ignores_unrelated_but_refuses_owned_instance' {
        $target = New-TestInstall 'process-target' 'current'
        function Get-Process { @([pscustomobject]@{ Path = (Join-Path $testRoot 'other\nevr-desktop.exe'); Id = 123 }) }
        Assert-NEVRProgramsClosed $target
        function Get-Process { @([pscustomobject]@{ Path = (Join-Path $target 'nevr-desktop.exe'); Id = 123 }) }
        Assert-Throws { Assert-NEVRProgramsClosed $target } 'No process was terminated'
    }
    Invoke-Test 'linked_snapshot_payload_refused' {
        $bad = New-NEVRProgramSnapshot $source $snapshots
        $configDirectory = Join-Path $bad 'configs'
        Remove-Item -LiteralPath (Join-Path $configDirectory 'default.toml')
        Remove-Item -LiteralPath $configDirectory
        New-Item -ItemType Junction -Path $configDirectory -Target (Join-Path $source 'configs') | Out-Null
        try { Assert-Throws { Read-NEVRProgramSnapshot $bad } 'linked program/snapshot path' }
        finally { [IO.Directory]::Delete($configDirectory) }
    }
    Invoke-Test 'uninstall_roots_refuse_empty_relative_and_drive_roots' {
        foreach ($invalid in @('', 'relative-folder', [IO.Path]::GetPathRoot($testRoot))) {
            Assert-Throws { Resolve-NEVRInstallRoots $invalid } 'LOCALAPPDATA'
        }
        $roots = Resolve-NEVRInstallRoots $testRoot
        Assert-Test ($roots.InstallRoot -eq (Join-Path $testRoot 'Programs\NEVR-Anticheat')) 'Unexpected install target'
        Assert-Test ($roots.DataRoot -eq (Join-Path $testRoot 'NEVR-Anticheat')) 'Unexpected evidence target'
    }
    Invoke-Test 'uninstall_preflight_precedes_removal_without_process_termination' {
        # Do not execute uninstall: even test environment overrides would still
        # resolve the user's real Desktop special folder for shortcut removal.
        $sourceText = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot '..\packaging\Uninstall-NEVR.ps1')
        Assert-Test ($sourceText -notmatch 'Stop-Process|taskkill') 'Uninstall still terminates processes by name'
        Assert-Test ($sourceText.IndexOf('Resolve-NEVRInstallRoots') -lt $sourceText.IndexOf('Remove-Item')) 'Uninstall mutates before validating roots'
        Assert-Test ($sourceText.IndexOf('Assert-NEVRProgramsClosed') -lt $sourceText.IndexOf('Remove-Item')) 'Uninstall mutates before process preflight'
    }
    $report = [ordered]@{
        schema_version = 'nevr-windows-snapshot-tests/v1'
        generated_at = [DateTime]::UtcNow.ToString('o')
        scope = 'Synthetic text EXEs only; no installed app, real database, Setup, viewer, or network used.'
        checks = @($results.ToArray())
        passed = @($results | Where-Object status -eq 'pass').Count
        failed = @($results | Where-Object status -eq 'fail').Count
    }
    if ($OutputDirectory) {
        [IO.Directory]::CreateDirectory([IO.Path]::GetFullPath($OutputDirectory)) | Out-Null
        $reportPath = Join-Path $OutputDirectory 'windows-snapshot-tests.json'
        $report | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath $reportPath -Encoding UTF8
        Write-Host "Report: $reportPath"
    }
    if ($report.failed -gt 0) { exit 1 }
} finally {
    $expectedPrefix = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\', '/') + [IO.Path]::DirectorySeparatorChar + 'nevr-snapshot-tests-'
    if (-not $testRoot.StartsWith($expectedPrefix, [StringComparison]::OrdinalIgnoreCase)) { throw 'Unsafe snapshot test cleanup path.' }
    Remove-Item -LiteralPath $testRoot -Recurse -Force
}
