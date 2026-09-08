#requires -Version 7.2
[CmdletBinding()]
param(
    [string]$PackageZip = '',
    [string]$Installer = '',
    [string]$ExpectedCommit = '',
    [string]$ReplayDirectory = '',
    [string[]]$ReplayFiles = @(),
    [string]$OutputDirectory = '',
    [ValidateRange(1, 100)][int]$Iterations = 3,
    [ValidateRange(1, 86400)][int]$RunTimeoutSeconds = 600,
    [ValidateRange(1, 1048576)][int]$MaxWorkingSetMiB = 2048,
    [ValidateRange(1, 86400)][int]$DesktopMinDurationSeconds = 120,
    [switch]$SkipSourceTests
)

# This maintainer entrypoint never builds/replaces the supplied artifacts,
# executes Setup, grants a safety bypass, publishes, or opens installed data.
# Dot-sourcing defines only the pure input-validation helpers for unit tests.
function Assert-TestRelease([bool]$Condition, [string]$Message) {
    if (-not $Condition) { throw $Message }
}

function Get-TestReleaseArtifact([string]$Path) {
    $resolved = (Resolve-Path -LiteralPath $Path -ErrorAction Stop).Path
    Assert-TestRelease (Test-Path -LiteralPath $resolved -PathType Leaf) 'Candidate must be a file.'
    $sidecar = $resolved + '.sha256'
    Assert-TestRelease (Test-Path -LiteralPath $sidecar -PathType Leaf) 'Candidate checksum sidecar is required.'
    $line = (Get-Content -Raw -LiteralPath $sidecar).Trim()
    $match = [regex]::Match($line, '^([a-fA-F0-9]{64})\s+\*?([^\r\n]+)$')
    Assert-TestRelease ($match.Success -and $match.Groups[2].Value -ceq [IO.Path]::GetFileName($resolved)) 'Checksum must identify exactly this candidate filename.'
    $hash = (Get-FileHash -LiteralPath $resolved -Algorithm SHA256).Hash.ToLowerInvariant()
    Assert-TestRelease ($hash -ceq $match.Groups[1].Value.ToLowerInvariant()) 'Candidate checksum mismatch.'
    return [pscustomobject]@{ path = $resolved; name = [IO.Path]::GetFileName($resolved); bytes = (Get-Item -LiteralPath $resolved).Length; sha256 = $hash }
}

function Get-TestReleaseZipEntries([IO.Compression.ZipArchive]$Zip) {
    $names = [Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
    foreach ($entry in $Zip.Entries) {
        $name = $entry.FullName
        Assert-TestRelease ($name -and -not $name.StartsWith('/') -and -not $name.Contains('\') -and -not $name.Contains(':') -and -not ($name.Split('/') -contains '..') -and -not ($name.Split('/') -contains '.')) 'Unsafe ZIP member path.'
        Assert-TestRelease ($names.Add($name)) 'Duplicate or case-ambiguous ZIP member.'
        Assert-TestRelease ((($entry.ExternalAttributes -shr 16) -band 0xF000) -ne 0xA000 -and ($entry.ExternalAttributes -band 1024) -eq 0) 'Linked ZIP members are not accepted.'
    }
    $desktops = @($Zip.Entries | Where-Object { [IO.Path]::GetFileName($_.FullName) -ceq 'nevr-desktop.exe' })
    Assert-TestRelease ($desktops.Count -eq 1) 'Candidate ZIP must contain exactly one desktop executable.'
    $prefix = $desktops[0].FullName.Substring(0, $desktops[0].FullName.Length - 'nevr-desktop.exe'.Length)
    $cli = $Zip.GetEntry($prefix + 'nevr-ac.exe')
    Assert-TestRelease ($null -ne $cli) 'Candidate ZIP must contain its CLI beside the desktop.'
    foreach ($entry in @($desktops[0], $cli)) {
        Assert-TestRelease ($entry.Length -gt 0 -and $entry.Length -le 512MB) 'Candidate executable has an invalid or excessive uncompressed size.'
    }
    return @($desktops[0], $cli)
}

function Get-TestReleaseBuildIdentity([string]$Binary, [string]$Commit) {
    Assert-TestRelease ($Commit -cmatch '^[a-f0-9]{40}$') 'ExpectedCommit must be the explicit 40-character artifact build revision.'
    $info = & go version -m $Binary 2>&1
    Assert-TestRelease ($LASTEXITCODE -eq 0) 'Could not inspect candidate Go build metadata.'
    $text = $info -join "`n"
    $revision = [regex]::Match($text, 'vcs\.revision=([a-f0-9]{40})')
    Assert-TestRelease ($revision.Success -and $revision.Groups[1].Value -ceq $Commit) 'Candidate build revision differs from ExpectedCommit.'
    Assert-TestRelease ($text -match 'vcs\.modified=false(?:\s|$)') 'Candidate must be built from a clean committed source tree.'
    Assert-TestRelease ($text -match 'GOOS=windows(?:\s|$)' -and $text -match 'GOARCH=amd64(?:\s|$)') 'Candidate must be the Windows x64 build.'
    return [pscustomobject]@{ name = [IO.Path]::GetFileName($Binary); revision = $Commit; dirty = $false; sha256 = (Get-FileHash -LiteralPath $Binary -Algorithm SHA256).Hash.ToLowerInvariant() }
}

function Convert-TestReleaseStatus([string]$Status) {
    switch ($Status) { 'pass' { 'PASS' }; 'fail' { 'FAIL' }; 'incomplete' { 'BLOCKED' }; 'not_run' { 'NOT TESTED' }; default { throw 'Unknown child check status.' } }
}

function Assert-TestReleaseReadinessReport([object]$Report, [int]$ExitCode, [string]$Commit, [bool]$SourceSkipped) {
    Assert-TestRelease ($Report.schema_version -ceq 'nevr-private-beta-readiness/v1') 'Unknown readiness report schema.'
    $ids = [Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    foreach ($check in @($Report.checks)) {
        Assert-TestRelease ($check.id -and $ids.Add([string]$check.id)) 'Missing or duplicate readiness check ID.'
        Assert-TestRelease ($check.status -cin @('pass', 'fail', 'not_run') -and $check.kind -cin @('automated', 'manual')) 'Unknown readiness check status/kind.'
    }
    foreach ($required in @('source_regressions', 'program_snapshot_safety', 'desktop_binary', 'binary_build_provenance', 'installer_inventory', 'portable_zip_inventory',
        'isolated_startup', 'runtime_build_identity', 'fixture_import', 'duplicate_import', 'corrupt_upload_isolation', 'database_backup', 'database_restore_roundtrip',
        'restart_preserves_evidence', 'clean_shutdown', 'installer_version_alignment', 'candidate_binary_binding')) {
        $entry = @($Report.checks | Where-Object { $_.id -ceq $required })
        Assert-TestRelease ($entry.Count -eq 1 -and $entry[0].kind -ceq 'automated') "Required readiness check missing: $required."
    }
    $failed = @($Report.checks | Where-Object { $_.status -ceq 'fail' }).Count
    $missing = @($Report.checks | Where-Object { $_.kind -ceq 'automated' -and $_.status -ceq 'not_run' }).Count
    $expectedStatus = if ($failed) { 'fail' } elseif ($missing) { 'incomplete' } else { 'pass' }
    $expectedExit = if ($failed) { 1 } elseif ($missing) { 2 } else { 0 }
    Assert-TestRelease ($Report.automated_status -ceq $expectedStatus -and $ExitCode -eq $expectedExit) 'Readiness status/exit disagrees with its check inventory.'
    $source = @($Report.checks | Where-Object { $_.id -ceq 'source_regressions' })[0]
    if ($SourceSkipped) {
        Assert-TestRelease ($source.status -ceq 'not_run') 'Explicitly skipped source tests cannot be marked passed.'
    } elseif ($source.status -ceq 'pass') {
        Assert-TestRelease ($Report.source_revision -ceq $Commit -and $Report.source_dirty -is [bool] -and -not $Report.source_dirty) 'Source regression evidence must come from the exact clean ExpectedCommit checkout.'
    }
}

function Assert-TestReleaseSoakReport([object]$Report, [int]$ExitCode, [string]$CLIHash, [int]$Rounds) {
    Assert-TestRelease ($Report.schema_version -ceq 'nevr-soak-report/v1') 'Unknown CLI workload report schema.'
    Assert-TestRelease ($Report.iterations -eq $Rounds -and $Report.files -gt 0 -and $Report.process_runs -eq $Report.files * $Rounds -and @($Report.runs).Count -eq $Report.process_runs) 'Incomplete CLI workload run inventory.'
    foreach ($run in @($Report.runs)) { Assert-TestRelease ($run.status -cin @('pass', 'fail')) 'Unknown CLI workload run status.' }
    $failures = @($Report.runs | Where-Object { $_.status -ceq 'fail' }).Count
    Assert-TestRelease ($Report.failed_runs -eq $failures -and $Report.executable_unchanged -is [bool] -and $Report.executable_sha256 -ceq $CLIHash) 'CLI workload result is not bound to the supplied binary/run inventory.'
    $expectedExit = if ($failures -or -not $Report.executable_unchanged) { 1 } else { 0 }
    Assert-TestRelease ($ExitCode -eq $expectedExit) 'CLI workload exit disagrees with its report.'
}

function Assert-TestReleaseDesktopReport([object]$Report, [int]$ExitCode, [string]$Commit, [string]$DesktopHash) {
    Assert-TestRelease ($Report.schema_version -ceq 'nevr-desktop-workload/v1') 'Unknown desktop workload report schema.'
    Assert-TestRelease ($Report.automated_status -cin @('PASS', 'FAIL', 'BLOCKED')) 'Unknown desktop workload summary status.'
    $expectedExit = switch ($Report.automated_status) { 'PASS' { 0 }; 'FAIL' { 1 }; 'BLOCKED' { 2 } }
    Assert-TestRelease ($ExitCode -eq $expectedExit) 'Desktop workload exit disagrees with its report.'
    Assert-TestRelease ($Report.expected_build_commit -ceq $Commit -and $Report.executable_sha256 -ceq $DesktopHash) 'Desktop workload report is bound to a different candidate.'
    $ids = [Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    foreach ($check in @($Report.checks)) {
        Assert-TestRelease ($check.id -and $ids.Add([string]$check.id)) 'Missing or duplicate desktop workload check ID.'
        Assert-TestRelease ($check.status -cin @('PASS', 'FAIL', 'NOT TESTED')) 'Unknown desktop workload check status.'
    }
    if ($Report.automated_status -cne 'FAIL') {
        foreach ($required in @('isolated_candidate_identity', 'synthetic_notes_and_security', 'long_lived_desktop_workload', 'graceful_restart_persistence', 'inputs_and_binary_unchanged', 'abrupt_kill_recovery')) {
            Assert-TestRelease ($ids.Contains($required)) "Required desktop workload check missing: $required."
        }
        Assert-TestRelease (@($Report.checks | Where-Object { $_.status -ceq 'FAIL' }).Count -eq 0) 'Desktop workload summary conceals a failed check.'
        if ($Report.automated_status -ceq 'PASS') {
            Assert-TestRelease (@($Report.checks | Where-Object { $_.id -cne 'abrupt_kill_recovery' -and $_.status -cne 'PASS' }).Count -eq 0) 'Desktop workload summary conceals an untested required check.'
        }
    }
}

if ($MyInvocation.InvocationName -eq '.') { return }
$ErrorActionPreference = 'Stop'
$repoRoot = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '..')).Path
if (-not $OutputDirectory) { $OutputDirectory = Join-Path $repoRoot 'dist\test-release-verification' }
$runRoot = Join-Path ([IO.Path]::GetFullPath($OutputDirectory)) ([DateTime]::UtcNow.ToString('yyyyMMddTHHmmssZ') + '-' + [Guid]::NewGuid().ToString('N').Substring(0, 8))
[IO.Directory]::CreateDirectory($runRoot) | Out-Null
$scratch = Join-Path ([IO.Path]::GetTempPath()) ('nevr-test-release-' + [Guid]::NewGuid().ToString('N'))
[IO.Directory]::CreateDirectory($scratch) | Out-Null
$checks = [Collections.Generic.List[object]]::new()
$artifacts = [Collections.Generic.List[object]]::new()
$started = [DateTime]::UtcNow
$readinessReport = $null
$soakReport = $null
$runnerExit = 1

function Add-ReleaseCheck([string]$ID, [string]$Status, [string]$Detail) {
    $checks.Add([pscustomobject]@{ id = $ID; status = $Status; detail = $Detail })
    Write-Host "$Status $ID - $Detail"
}

function Invoke-ReleaseScript([string]$Name, [string[]]$Arguments, [string]$LogName) {
    # Each existing script owns its exit code; run it in a child PowerShell so
    # its explicit exit cannot skip this wrapper's final report and cleanup.
    $info = [Diagnostics.ProcessStartInfo]::new()
    $info.FileName = $script:releasePowerShell
    $info.UseShellExecute = $false
    $info.CreateNoWindow = $true
    $info.RedirectStandardOutput = $true
    $info.RedirectStandardError = $true
    foreach ($argument in @('-NoProfile', '-File', (Join-Path $PSScriptRoot $Name)) + $Arguments) { $info.ArgumentList.Add($argument) }
    foreach ($key in @($info.Environment.Keys)) {
        if ($key -match '^(GH_|GITHUB_|NEVR_|NAKAMA_|AZURE_|AWS_|GOOGLE_|CLOUDSDK_|ANTICHEAT_|SPARK_)|TOKEN|SECRET|PASSWORD|API_KEY|PRIVATE_KEY|SESSION') { $null = $info.Environment.Remove($key) }
    }
    $process = [Diagnostics.Process]::Start($info)
    try {
        $stdout = $process.StandardOutput.ReadToEndAsync()
        $stderr = $process.StandardError.ReadToEndAsync()
        $process.WaitForExit()
        [IO.File]::WriteAllText((Join-Path $runRoot $LogName), $stdout.GetAwaiter().GetResult() + "`n" + $stderr.GetAwaiter().GetResult())
        return $process.ExitCode
    } finally { $process.Dispose() }
}

try {
    Assert-TestRelease ($IsWindows) 'Supplied Windows artifacts must be tested on Windows.'
    Assert-TestRelease ($ExpectedCommit -cmatch '^[a-f0-9]{40}$') 'ExpectedCommit is mandatory and must identify the actual artifact build revision.'
    Assert-TestRelease ($PackageZip -and $Installer -and $ReplayDirectory) 'Supply PackageZip, Installer and an explicitly selected ReplayDirectory.'
    $script:releasePowerShell = (Get-Command pwsh -ErrorAction Stop).Source
    $null = Get-Command go -ErrorAction Stop
    $null = Resolve-Path -LiteralPath $ReplayDirectory -ErrorAction Stop
    $zipArtifact = Get-TestReleaseArtifact $PackageZip
    $setupArtifact = Get-TestReleaseArtifact $Installer
    $artifacts.Add($zipArtifact)
    $artifacts.Add($setupArtifact)
    Add-ReleaseCheck 'artifact_checksums' 'PASS' 'Both supplied artifacts match filename-bound SHA256 sidecars. This is integrity, not publisher trust.'

    $zip = [IO.Compression.ZipFile]::OpenRead($zipArtifact.path)
    try {
        $selected = @(Get-TestReleaseZipEntries $zip)
        foreach ($entry in $selected) {
            $destination = Join-Path $scratch ([IO.Path]::GetFileName($entry.FullName))
            $source = $entry.Open()
            try {
                $target = [IO.File]::Open($destination, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
                try { $source.CopyTo($target) } finally { $target.Dispose() }
            } finally { $source.Dispose() }
        }
    } finally { $zip.Dispose() }
    $desktop = Join-Path $scratch 'nevr-desktop.exe'
    $cli = Join-Path $scratch 'nevr-ac.exe'
    foreach ($binary in @($desktop, $cli)) { $artifacts.Add((Get-TestReleaseBuildIdentity $binary $ExpectedCommit)) }
    Add-ReleaseCheck 'exact_candidate_build_identity' 'PASS' 'Only the two supplied ZIP payloads were extracted; both are clean Windows x64 builds at ExpectedCommit. No replacement binary was compiled.'

    $readinessRoot = Join-Path $runRoot 'readiness'
    $arguments = @('-DesktopExecutable', $desktop, '-Installer', $setupArtifact.path, '-PackageZip', $zipArtifact.path, '-OutputDirectory', $readinessRoot)
    if ($SkipSourceTests) { $arguments += '-SkipSourceTests' }
    $code = Invoke-ReleaseScript 'private-beta-readiness.ps1' $arguments 'readiness.log'
    $reports = @(Get-ChildItem -LiteralPath $readinessRoot -Filter 'readiness-report.json' -File -Recurse)
    Assert-TestRelease ($reports.Count -eq 1) 'Readiness did not produce exactly one report.'
    $readinessReport = Get-Content -Raw -LiteralPath $reports[0].FullName | ConvertFrom-Json
    Assert-TestReleaseReadinessReport $readinessReport $code $ExpectedCommit ([bool]$SkipSourceTests)
    foreach ($check in $readinessReport.checks) { Add-ReleaseCheck ('readiness.' + $check.id) (Convert-TestReleaseStatus $check.status) $check.detail }
    if ($code -ne 0 -and $code -ne 2) { Add-ReleaseCheck 'readiness_process' 'FAIL' "Readiness exited $code; inspect readiness.log." }
    $identity = @($readinessReport.artifacts | Where-Object { $_.role -eq 'tested_desktop' })
    Assert-TestRelease ($identity.Count -eq 1 -and $identity[0].embedded_build_commit -ceq $ExpectedCommit) 'Running desktop build identity differs from ExpectedCommit.'
    $restore = @($readinessReport.checks | Where-Object { $_.id -eq 'database_restore_roundtrip' -and $_.status -eq 'pass' })
    Assert-TestRelease ($restore.Count -eq 1) 'Actual candidate backup/restore round trip did not pass.'

    $testCode = Invoke-ReleaseScript 'test-soak-runner.ps1' @('-OutputDirectory', $runRoot) 'soak-runner-tests.log'
    Assert-TestRelease ($testCode -eq 0) 'Existing soak-runner isolation/safety regressions failed.'
    Add-ReleaseCheck 'soak_runner_safety' 'PASS' 'Existing isolated success, duplicate-run, timeout, failure, memory-limit and escaped-config checks passed.'
    $soakRoot = Join-Path $runRoot 'soak'
    $soakCode = Invoke-ReleaseScript 'soak-replays.ps1' @('-ReplayDirectory', $ReplayDirectory, '-Executable', $cli, '-OutputDirectory', $soakRoot,
        '-Iterations', [string]$Iterations, '-RunTimeoutSeconds', [string]$RunTimeoutSeconds, '-MaxWorkingSetMiB', [string]$MaxWorkingSetMiB, '-HashInputs') 'soak.log'
    $soakFiles = @(Get-ChildItem -LiteralPath $soakRoot -Filter 'soak-report.json' -File -Recurse)
    Assert-TestRelease ($soakFiles.Count -eq 1) 'Soak runner did not produce exactly one report.'
    $soakReport = Get-Content -Raw -LiteralPath $soakFiles[0].FullName | ConvertFrom-Json
    $cliIdentity = @($artifacts | Where-Object { $_.name -eq 'nevr-ac.exe' })[0]
    Assert-TestReleaseSoakReport $soakReport $soakCode $cliIdentity.sha256 $Iterations
    Assert-TestRelease ($soakCode -eq 0 -and $soakReport.failed_runs -eq 0 -and $soakReport.executable_unchanged) 'Candidate workload failed or its binary changed; inspect soak report.'
    Assert-TestRelease ($soakReport.executable_sha256 -ceq $cliIdentity.sha256) 'Soak report is not bound to the extracted candidate CLI.'
    Add-ReleaseCheck 'candidate_cli_workload' 'PASS' "$($soakReport.process_runs) actual candidate process runs completed over $($soakReport.total_elapsed_seconds) seconds; peak working set $($soakReport.peak_working_set_bytes) bytes. This is a repeated CLI workload, not a long-lived desktop soak or accuracy measurement."
    if ($ReplayFiles.Count -gt 0) {
        $manifest = Join-Path $runRoot 'desktop-replay-manifest.json'
        ConvertTo-Json -InputObject @($ReplayFiles) | Set-Content -LiteralPath $manifest -Encoding utf8
        $desktopRoot = Join-Path $runRoot 'desktop-workload'
        $desktopCode = Invoke-ReleaseScript 'test-desktop-release-workload.ps1' @('-DesktopExecutable', $desktop, '-ExpectedCommit', $ExpectedCommit,
            '-ReplayManifest', $manifest, '-OutputDirectory', $desktopRoot, '-MinDurationSeconds', [string]$DesktopMinDurationSeconds) 'desktop-workload.log'
        $desktopReports = @(Get-ChildItem -LiteralPath $desktopRoot -Filter 'desktop-workload-report.json' -File -Recurse)
        Assert-TestRelease ($desktopReports.Count -eq 1) 'Desktop workload did not produce exactly one report.'
        $desktopReport = Get-Content -Raw -LiteralPath $desktopReports[0].FullName | ConvertFrom-Json
        $desktopIdentity = @($artifacts | Where-Object { $_.name -eq 'nevr-desktop.exe' })[0]
        Assert-TestReleaseDesktopReport $desktopReport $desktopCode $ExpectedCommit $desktopIdentity.sha256
        foreach ($check in $desktopReport.checks) { Add-ReleaseCheck ('desktop.' + $check.id) $check.status $check.detail }
        Add-ReleaseCheck 'long_lived_desktop_workload' $desktopReport.automated_status 'Supplied desktop workload result; see desktop-workload report for measured responsiveness, memory and database behavior.'
        if ($desktopCode -eq 2) { $code = 2 }
        elseif ($desktopCode -ne 0) { Add-ReleaseCheck 'desktop_workload_process' 'FAIL' "Desktop workload exited $desktopCode; inspect desktop-workload.log." }
    }
    foreach ($artifact in @($zipArtifact, $setupArtifact)) {
        Assert-TestRelease ((Get-FileHash -LiteralPath $artifact.path -Algorithm SHA256).Hash.ToLowerInvariant() -ceq $artifact.sha256) 'A supplied candidate changed during verification.'
    }
    Add-ReleaseCheck 'supplied_artifacts_unchanged' 'PASS' 'Setup and ZIP hashes are unchanged after verification.'
    $runnerExit = if (@($checks | Where-Object { $_.status -eq 'FAIL' }).Count) { 1 } elseif ($SkipSourceTests -or $code -eq 2) { 2 } else { 0 }
} catch {
    $detail = $_.Exception.Message.Replace($scratch, '<owned-temp>')
    Add-ReleaseCheck 'verification_entrypoint' 'FAIL' $detail
} finally {
    Add-ReleaseCheck 'installer_payload_binding' 'NOT TESTED' 'Local verification never runs Setup. Independently record disposable CI installed-payload hashes matching the ZIP; matching installer/desktop version is insufficient.'
    if (@($checks | Where-Object { $_.id -eq 'long_lived_desktop_workload' }).Count -eq 0) {
        Add-ReleaseCheck 'long_lived_desktop_workload' 'NOT TESTED' 'CLI iterations restart a process. Supply explicit ReplayFiles for the timed desktop workload; do not claim it from CLI results.'
    }
    $report = [ordered]@{
        schema_version = 'nevr-test-release-verification/v1'; started_at = $started.ToString('o'); finished_at = [DateTime]::UtcNow.ToString('o')
        expected_build_commit = $ExpectedCommit; artifact_source = 'caller_supplied'; distribution = 'private_controlled_review_only'
        automated_status = if ($runnerExit -eq 0) { 'PASS' } elseif ($runnerExit -eq 2) { 'BLOCKED' } else { 'FAIL' }
        release_status = 'BLOCKED'; reason = 'Manual/disposable-system acceptance evidence remains required. No release was published.'
        artifacts = @($artifacts); checks = @($checks)
        workload = if ($null -eq $soakReport) { $null } else { [ordered]@{ process_runs = $soakReport.process_runs; elapsed_seconds = $soakReport.total_elapsed_seconds; peak_working_set_bytes = $soakReport.peak_working_set_bytes; final_database_bytes = $soakReport.final_database_bytes } }
        limits = @('No production database, real installer, updater, Spark process, signing account or external publication was used.', 'Private reports may contain original replay paths; keep the entire output directory ignored and do not commit it.', 'Synthetic/repeated replay passes are not independent detector accuracy or final threshold calibration.')
    }
    $report | ConvertTo-Json -Depth 10 | Set-Content -LiteralPath (Join-Path $runRoot 'test-release-report.json') -Encoding utf8
    $scratchFull = [IO.Path]::GetFullPath($scratch)
    $allowedPrefix = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\', '/') + [IO.Path]::DirectorySeparatorChar + 'nevr-test-release-'
    if (-not $scratchFull.StartsWith($allowedPrefix, [StringComparison]::OrdinalIgnoreCase)) { throw 'Unsafe verification scratch cleanup path.' }
    Remove-Item -LiteralPath $scratchFull -Recurse -Force
    Write-Host "Test release report: $(Join-Path $runRoot 'test-release-report.json')"
}
exit $runnerExit
