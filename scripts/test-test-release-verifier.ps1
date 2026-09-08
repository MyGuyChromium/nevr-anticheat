#requires -Version 7.2
[CmdletBinding()]
param([string]$OutputDirectory = '')
$ErrorActionPreference = 'Stop'
# The imported script has its own parameter defaults; preserve this runner's
# requested report destination across dot-sourcing those validation helpers.
$requestedVerifierReportDirectory = $OutputDirectory
. (Join-Path $PSScriptRoot 'verify-test-release.ps1')
$OutputDirectory = $requestedVerifierReportDirectory
$testRoot = [IO.Path]::GetFullPath((Join-Path ([IO.Path]::GetTempPath()) ('nevr-release-contract-tests-' + [Guid]::NewGuid().ToString('N'))))
[IO.Directory]::CreateDirectory($testRoot) | Out-Null
$checks = [Collections.Generic.List[object]]::new()

function Expect-ReleaseFailure([scriptblock]$Action, [string]$Contains) {
    $message = ''
    try { & $Action | Out-Null } catch { $message = $_.Exception.Message }
    Assert-TestRelease ($message.Contains($Contains)) "Expected refusal '$Contains', got '$message'."
}
function Test-ReleaseContract([string]$Name, [scriptblock]$Action) {
    & $Action | Out-Null
    $checks.Add([pscustomobject]@{ name = $Name; status = 'PASS' })
    Write-Host "PASS $Name"
}
function New-ContractZip([string[]]$Names, [switch]$Link) {
    $path = Join-Path $testRoot ([Guid]::NewGuid().ToString('N') + '.zip')
    $zip = [IO.Compression.ZipFile]::Open($path, [IO.Compression.ZipArchiveMode]::Create)
    try {
        foreach ($name in $Names) {
            $entry = $zip.CreateEntry($name)
            if ($Link -and $name.EndsWith('nevr-desktop.exe')) { $entry.ExternalAttributes = -1610612736 }
            $stream = $entry.Open()
            try { $stream.WriteByte(1) } finally { $stream.Dispose() }
        }
    } finally { $zip.Dispose() }
    return $path
}
function Read-ContractZip([string]$Path) {
    $zip = [IO.Compression.ZipFile]::OpenRead($Path)
    try { return @(Get-TestReleaseZipEntries $zip).Count } finally { $zip.Dispose() }
}
function New-ReadinessContractReport {
    $ids = @('source_regressions', 'program_snapshot_safety', 'desktop_binary', 'binary_build_provenance', 'installer_inventory', 'portable_zip_inventory',
        'isolated_startup', 'runtime_build_identity', 'fixture_import', 'duplicate_import', 'corrupt_upload_isolation', 'database_backup', 'database_restore_roundtrip',
        'restart_preserves_evidence', 'clean_shutdown', 'installer_version_alignment', 'candidate_binary_binding')
    return [pscustomobject]@{ schema_version = 'nevr-private-beta-readiness/v1'; source_revision = ('a' * 40); source_dirty = $false; automated_status = 'pass';
        checks = @($ids | ForEach-Object { [pscustomobject]@{ id = $_; kind = 'automated'; status = 'pass' } }) }
}

try {
    $candidate = Join-Path $testRoot 'synthetic-candidate.exe'
    [IO.File]::WriteAllText($candidate, 'synthetic verifier bytes; not an executable')
    $hash = (Get-FileHash -LiteralPath $candidate -Algorithm SHA256).Hash
    Test-ReleaseContract 'missing_checksum_refused' { Expect-ReleaseFailure { Get-TestReleaseArtifact $candidate } 'sidecar' }
    [IO.File]::WriteAllText(($candidate + '.sha256'), "$hash  synthetic-candidate.exe")
    Test-ReleaseContract 'filename_bound_checksum_accepted' { Assert-TestRelease ((Get-TestReleaseArtifact $candidate).sha256 -ceq $hash.ToLowerInvariant()) 'Hash was not preserved.' }
    [IO.File]::WriteAllText(($candidate + '.sha256'), "$hash  other.exe")
    Test-ReleaseContract 'different_checksum_filename_refused' { Expect-ReleaseFailure { Get-TestReleaseArtifact $candidate } 'filename' }
    [IO.File]::WriteAllText(($candidate + '.sha256'), (('0' * 64) + '  synthetic-candidate.exe'))
    Test-ReleaseContract 'changed_artifact_refused' { Expect-ReleaseFailure { Get-TestReleaseArtifact $candidate } 'mismatch' }
    Test-ReleaseContract 'single_consistent_zip_root_accepted' { Assert-TestRelease ((Read-ContractZip (New-ContractZip @('candidate/nevr-desktop.exe', 'candidate/nevr-ac.exe'))) -eq 2) 'Wrong extracted payload count.' }
    Test-ReleaseContract 'zip_traversal_refused' { Expect-ReleaseFailure { Read-ContractZip (New-ContractZip @('../outside', 'nevr-desktop.exe', 'nevr-ac.exe')) } 'Unsafe' }
    Test-ReleaseContract 'zip_absolute_path_refused' { Expect-ReleaseFailure { Read-ContractZip (New-ContractZip @('/outside', 'nevr-desktop.exe', 'nevr-ac.exe')) } 'Unsafe' }
    Test-ReleaseContract 'zip_case_collision_refused' { Expect-ReleaseFailure { Read-ContractZip (New-ContractZip @('nevr-desktop.exe', 'NEVR-DESKTOP.EXE', 'nevr-ac.exe')) } 'case-ambiguous' }
    Test-ReleaseContract 'zip_link_refused' { Expect-ReleaseFailure { Read-ContractZip (New-ContractZip @('nevr-desktop.exe', 'nevr-ac.exe') -Link) } 'Linked' }
    Test-ReleaseContract 'multiple_desktops_refused' { Expect-ReleaseFailure { Read-ContractZip (New-ContractZip @('one/nevr-desktop.exe', 'two/nevr-desktop.exe', 'one/nevr-ac.exe')) } 'exactly one' }
    Test-ReleaseContract 'mismatched_cli_root_refused' { Expect-ReleaseFailure { Read-ContractZip (New-ContractZip @('one/nevr-desktop.exe', 'two/nevr-ac.exe')) } 'beside' }
    Test-ReleaseContract 'explicit_expected_commit_required' { Expect-ReleaseFailure { Get-TestReleaseBuildIdentity $candidate '' } 'ExpectedCommit' }
    Test-ReleaseContract 'status_mapping_does_not_turn_missing_into_pass' {
        Assert-TestRelease ((Convert-TestReleaseStatus 'pass') -ceq 'PASS') 'Pass mapping failed.'
        Assert-TestRelease ((Convert-TestReleaseStatus 'fail') -ceq 'FAIL') 'Failure mapping failed.'
        Assert-TestRelease ((Convert-TestReleaseStatus 'incomplete') -ceq 'BLOCKED') 'Incomplete mapping failed.'
        Assert-TestRelease ((Convert-TestReleaseStatus 'not_run') -ceq 'NOT TESTED') 'Skipped check was not explicit.'
    }
    Test-ReleaseContract 'unknown_check_status_refused' { Expect-ReleaseFailure { Convert-TestReleaseStatus '' } 'Unknown' }
    Test-ReleaseContract 'matching_clean_readiness_accepted' { Assert-TestReleaseReadinessReport (New-ReadinessContractReport) 0 ('a' * 40) $false }
    Test-ReleaseContract 'unrelated_source_regressions_refused' { Expect-ReleaseFailure { Assert-TestReleaseReadinessReport (New-ReadinessContractReport) 0 ('b' * 40) $false } 'exact clean' }
    Test-ReleaseContract 'dirty_source_regressions_refused' {
        $report = New-ReadinessContractReport; $report.source_dirty = $true
        Expect-ReleaseFailure { Assert-TestReleaseReadinessReport $report 0 ('a' * 40) $false } 'exact clean'
    }
    Test-ReleaseContract 'missing_required_readiness_check_refused' {
        $report = New-ReadinessContractReport; $report.checks = @($report.checks | Where-Object { $_.id -ne 'database_restore_roundtrip' })
        Expect-ReleaseFailure { Assert-TestReleaseReadinessReport $report 0 ('a' * 40) $false } 'Required'
    }
    Test-ReleaseContract 'duplicate_readiness_check_refused' {
        $report = New-ReadinessContractReport; $report.checks += $report.checks[0]
        Expect-ReleaseFailure { Assert-TestReleaseReadinessReport $report 0 ('a' * 40) $false } 'duplicate'
    }
    Test-ReleaseContract 'unknown_readiness_schema_refused' {
        $report = New-ReadinessContractReport; $report.schema_version = 'future'
        Expect-ReleaseFailure { Assert-TestReleaseReadinessReport $report 0 ('a' * 40) $false } 'schema'
    }
    Test-ReleaseContract 'child_exit_disagreement_refused' { Expect-ReleaseFailure { Assert-TestReleaseReadinessReport (New-ReadinessContractReport) 2 ('a' * 40) $false } 'disagrees' }
    Test-ReleaseContract 'explicit_source_skip_stays_incomplete' {
        $report = New-ReadinessContractReport; $report.source_revision = 'other'; $report.source_dirty = $true; $report.checks[0].status = 'not_run'; $report.automated_status = 'incomplete'
        Assert-TestReleaseReadinessReport $report 2 ('a' * 40) $true
    }
    Test-ReleaseContract 'explicit_source_skip_cannot_pass' { Expect-ReleaseFailure { Assert-TestReleaseReadinessReport (New-ReadinessContractReport) 0 ('a' * 40) $true } 'skipped' }
    Test-ReleaseContract 'missing_soak_runs_refused' {
        $report = [pscustomobject]@{schema_version='nevr-soak-report/v1';iterations=2;files=1;process_runs=2;runs=@()}
        Expect-ReleaseFailure { Assert-TestReleaseSoakReport $report 0 ('c' * 64) 2 } 'Incomplete'
    }
    Test-ReleaseContract 'missing_desktop_check_inventory_refused' {
        $report = [pscustomobject]@{schema_version='nevr-desktop-workload/v1';automated_status='PASS';expected_build_commit=('a' * 40);executable_sha256=('c' * 64);checks=@()}
        Expect-ReleaseFailure { Assert-TestReleaseDesktopReport $report 0 ('a' * 40) ('c' * 64) } 'Required'
    }
    Test-ReleaseContract 'missing_desktop_cleanup_check_refused' {
        $ids = @('isolated_candidate_identity', 'synthetic_notes_and_security', 'long_lived_desktop_workload', 'graceful_restart_persistence', 'inputs_and_binary_unchanged', 'abrupt_kill_recovery')
        $report = [pscustomobject]@{schema_version='nevr-desktop-workload/v1';automated_status='PASS';expected_build_commit=('a' * 40);executable_sha256=('c' * 64);
            checks=@($ids | ForEach-Object { [pscustomobject]@{id=$_;status=$(if ($_ -eq 'abrupt_kill_recovery') {'NOT TESTED'} else {'PASS'})} })}
        Expect-ReleaseFailure { Assert-TestReleaseDesktopReport $report 0 ('a' * 40) ('c' * 64) } 'isolated_state_cleanup'
    }
    Test-ReleaseContract 'desktop_child_receives_user_memory_cutoff' {
        $parseErrors = $null; $tokens = $null
        $wrapper = [Management.Automation.Language.Parser]::ParseFile((Join-Path $PSScriptRoot 'verify-test-release.ps1'), [ref]$tokens, [ref]$parseErrors)
        $launches = @($wrapper.FindAll({ param($node) $node -is [Management.Automation.Language.CommandAst] -and $node.GetCommandName() -eq 'Invoke-ReleaseScript' -and $node.CommandElements[1].Value -eq 'test-desktop-release-workload.ps1' }, $true))
        Assert-TestRelease ($parseErrors.Count -eq 0 -and $launches.Count -eq 1) 'Expected exactly one desktop workload launch.'
        & {
            $MaxWorkingSetMiB = 1536
            $desktop = 'not-launched.exe'; $manifest = 'not-read.json'; $desktopRoot = 'not-created'; $ExpectedCommit = 'a' * 40; $DesktopMinDurationSeconds = 120
            function Invoke-ReleaseScript([string]$Name, [string[]]$Arguments, [string]$LogName) {
                $index = [Array]::IndexOf($Arguments, '-MaxWorkingSetMiB')
                Assert-TestRelease ($index -ge 0 -and $Arguments[$index + 1] -ceq '1536') 'Desktop child ignored or changed the user-specified memory cutoff.'
                return 0
            }
            Invoke-Expression $launches[0].Extent.Text | Out-Null
        }
    }
    if ($OutputDirectory) {
        [IO.Directory]::CreateDirectory([IO.Path]::GetFullPath($OutputDirectory)) | Out-Null
        [ordered]@{ schema_version = 'nevr-test-release-contract-tests/v1'; passed = $checks.Count; checks = @($checks); scope = 'Synthetic bytes and ZIPs only; no candidate, installer, database, network or process launched.' } |
            ConvertTo-Json -Depth 6 | Set-Content -LiteralPath (Join-Path $OutputDirectory 'test-release-contract-tests.json') -Encoding utf8
    }
} finally {
    $prefix = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\', '/') + [IO.Path]::DirectorySeparatorChar + 'nevr-release-contract-tests-'
    if (-not $testRoot.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase)) { throw 'Unsafe verifier test cleanup path.' }
    Remove-Item -LiteralPath $testRoot -Recurse -Force
}
