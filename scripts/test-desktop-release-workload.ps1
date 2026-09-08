#requires -Version 7.2
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$DesktopExecutable,
    [Parameter(Mandatory)][ValidatePattern('^[a-f0-9]{40}$')][string]$ExpectedCommit,
    [string[]]$ReplayFiles = @(),
    [string]$ReplayManifest = '',
    [string]$OutputDirectory = '',
    [ValidateRange(1, 100)][int]$MinRounds = 3,
    [ValidateRange(0, 86400)][int]$MinDurationSeconds = 120,
    [ValidateRange(1, 86400)][int]$MaxDurationSeconds = 1800,
    [ValidateRange(1, 7200)][int]$UploadTimeoutSeconds = 600,
    [ValidateRange(1, 65536)][int]$MaxWorkingSetMiB = 4096
)

# Actual supplied desktop only: no compilation, installer, updater, browser,
# Spark launch, external service, pre-existing database, or automatic recovery.
# ReplayManifest is an explicitly selected JSON array, not directory discovery.
$ErrorActionPreference = 'Stop'
if (-not $OutputDirectory) { $OutputDirectory = Join-Path $PSScriptRoot '..\dist\desktop-workload' }
$outputParent = [IO.Path]::GetFullPath($OutputDirectory)
$runRoot = Join-Path $outputParent ('workload-' + [Guid]::NewGuid().ToString('N'))
if (Test-Path -LiteralPath $runRoot) { throw 'Refusing to reuse workload output.' }
[IO.Directory]::CreateDirectory($runRoot) | Out-Null
$scratch = Join-Path $runRoot 'isolated-state'
[IO.Directory]::CreateDirectory($scratch) | Out-Null
$db = Join-Path $scratch 'evidence\workload.db'
$config = Join-Path $scratch 'workload.toml'
$checks = [Collections.Generic.List[object]]::new()
$inputs = [Collections.Generic.List[object]]::new()
$runs = [Collections.Generic.List[object]]::new()
$samples = [Collections.Generic.List[object]]::new()
$notes = [Collections.Generic.List[string]]::new()
$script:app = $null
$script:appURL = ''
$script:pendingHealth = $null
$script:pendingQueue = $null
$script:activeUpload = $null
$script:clock = [Diagnostics.Stopwatch]::new()
$script:lastProbeMS = -500.0
$script:peakWorkingSet = 0L
$script:finalWorkingSet = 0L
$script:cpuSeconds = 0.0
$script:healthFailures = 0
$script:queueFailures = 0
$script:healthTimeouts = 0
$script:maxProbeLagMS = 0.0
$script:activeHealthSamples = 0
$script:checkpoint = 'initialization'
$script:exitCode = 1
$rounds = 0
$completedRounds = 0
$frames = 0L
$storedMatches = 0
$databaseBytes = 0L
$provenance = $null
$binaryHash = ''
$binary = ''
$started = [DateTime]::UtcNow
$handler = [Net.Http.HttpClientHandler]::new()
$handler.AllowAutoRedirect = $false
$handler.UseProxy = $false
$handler.UseCookies = $false
$client = [Net.Http.HttpClient]::new($handler)
$client.Timeout = [Threading.Timeout]::InfiniteTimeSpan
# Capture completion inside the .NET task, not when PowerShell next observes
# it. Otherwise the 50 ms pump or note checks inflate reported HTTP latency.
if (-not ('NEVRWorkloadHttpTiming' -as [type])) {
    Add-Type -TypeDefinition @'
using System.Diagnostics;
using System.Net.Http;
using System.Threading;
using System.Threading.Tasks;
public sealed class NEVRWorkloadHttpResult {
    public HttpResponseMessage Response;
    public double ElapsedMilliseconds;
}
public static class NEVRWorkloadHttpTiming {
    public static async Task<NEVRWorkloadHttpResult> SendAsync(HttpClient client, HttpRequestMessage request, CancellationToken token) {
        var clock = Stopwatch.StartNew();
        var response = await client.SendAsync(request, token).ConfigureAwait(false);
        clock.Stop();
        return new NEVRWorkloadHttpResult { Response = response, ElapsedMilliseconds = clock.Elapsed.TotalMilliseconds };
    }
}
'@
}

function Assert-Workload([bool]$Condition, [string]$Message) {
    if (-not $Condition) { $script:checkpoint = $Message; throw 'Controlled workload assertion failed.' }
}
function Add-WorkloadCheck([string]$ID, [string]$Status, [string]$Detail) {
    $checks.Add([pscustomobject]@{ id = $ID; status = $Status; detail = $Detail })
    Write-Host "$Status $ID - $Detail"
}
function Start-WorkloadRequest([string]$Method, [string]$Path, [object]$Body = $null, [string]$Upload = '', [string]$Origin = '', [int]$TimeoutSeconds = 5) {
    $request = [Net.Http.HttpRequestMessage]::new([Net.Http.HttpMethod]::new($Method), $script:appURL + $Path)
    $cts = [Threading.CancellationTokenSource]::new([TimeSpan]::FromSeconds($TimeoutSeconds))
    try {
        if ($Origin) { $request.Headers.Add('Origin', $Origin) }
        if ($Upload) {
            $request.Content = [Net.Http.MultipartFormDataContent]::new()
            # Send an ordinal-neutral filename; original names/paths do not
            # enter the disposable app's history or the exported report.
            $part = [Net.Http.StreamContent]::new([IO.File]::OpenRead($Upload))
            $request.Content.Add($part, 'files', 'workload-input.echoreplay')
        } elseif ($null -ne $Body) {
            $request.Content = [Net.Http.StringContent]::new(($Body | ConvertTo-Json -Depth 8 -Compress), [Text.Encoding]::UTF8, 'application/json')
        }
        return [pscustomobject]@{ request = $request; cts = $cts; watch = [Diagnostics.Stopwatch]::StartNew(); task = [NEVRWorkloadHttpTiming]::SendAsync($client, $request, $cts.Token) }
    } catch { $request.Dispose(); $cts.Dispose(); throw }
}
function Finish-WorkloadRequest($Operation) {
    $response = $null
    try {
        $completed = $Operation.task.GetAwaiter().GetResult()
        $response = $completed.Response
        $raw = $response.Content.ReadAsStringAsync().GetAwaiter().GetResult()
        $body = if ($raw.TrimStart().StartsWith('{')) { $raw | ConvertFrom-Json } else { $null }
        return [pscustomobject]@{ status = [int]$response.StatusCode; body = $body; elapsed_ms = $completed.ElapsedMilliseconds }
    } finally {
        if ($null -ne $response) { $response.Dispose() }
        $Operation.request.Dispose()
        $Operation.cts.Dispose()
    }
}
function Send-WorkloadRequest([string]$Method, [string]$Path, [object]$Body = $null, [string]$Origin = '') {
    return Finish-WorkloadRequest (Start-WorkloadRequest $Method $Path $Body '' $Origin)
}
function Start-WorkloadDesktop {
    $script:checkpoint = 'isolated desktop startup'
    $info = [Diagnostics.ProcessStartInfo]::new()
    $info.FileName = $binary
    $info.WorkingDirectory = $scratch
    $info.UseShellExecute = $false
    $info.CreateNoWindow = $true
    $info.RedirectStandardOutput = $true
    $info.RedirectStandardError = $true
    foreach ($argument in @('--config', $config, '--no-browser', '--port', '0', '--log-level', 'error')) { $info.ArgumentList.Add($argument) }
    foreach ($name in @($info.Environment.Keys)) {
        if ($name -match '(?i)(TOKEN|SECRET|PASSWORD|PASSWD|CREDENTIAL|AUTH|API[_-]?KEY|PRIVATE[_-]?KEY|NEVR_|GITHUB|AZURE|AWS_|GOOGLE_|OPENAI|ANTHROPIC)') { $info.Environment.Remove($name) | Out-Null }
    }
    $info.Environment['APPDATA'] = Join-Path $scratch 'appdata'
    $info.Environment['LOCALAPPDATA'] = Join-Path $scratch 'localappdata'
    $info.Environment['NEVR_REPLAY_VIEWER'] = Join-Path $scratch 'unavailable-viewer.exe'
    $script:app = [Diagnostics.Process]::Start($info)
    $script:stderr = $script:app.StandardError.ReadToEndAsync()
    $lineTask = $script:app.StandardOutput.ReadLineAsync()
    Assert-Workload ($lineTask.Wait(15000)) 'desktop startup URL timeout'
    $match = [regex]::Match([string]$lineTask.GetAwaiter().GetResult(), 'http://127\.0\.0\.1:\d+/[a-f0-9]{32}/')
    Assert-Workload $match.Success 'desktop did not return a scoped loopback URL'
    $script:appURL = $match.Value
    $script:stdout = $script:app.StandardOutput.ReadToEndAsync()
}
function Stop-WorkloadDesktop {
    if ($null -eq $script:app) { return }
    if (-not $script:app.HasExited) {
        $quit = Send-WorkloadRequest 'GET' 'quit'
        Assert-Workload ($quit.status -eq 200) 'desktop did not accept graceful shutdown'
        Assert-Workload ($script:app.WaitForExit(10000)) 'desktop graceful shutdown timeout'
    }
    Assert-Workload ($script:app.ExitCode -eq 0) 'desktop exited unsuccessfully'
    $script:app.Dispose()
    $script:app = $null
    $script:appURL = ''
}
function Receive-WorkloadProbes {
    if ($null -ne $script:pendingHealth -and $script:pendingHealth.task.IsCompleted) {
        $operation = $script:pendingHealth
        $script:pendingHealth = $null
        try {
            $result = Finish-WorkloadRequest $operation
            if ($result.status -ne 200) { $script:healthFailures++ }
            if ($result.body.analysis_active) { $script:activeHealthSamples++ }
            $samples.Add([pscustomobject]@{ kind = 'health'; at_ms = [math]::Round($script:clock.Elapsed.TotalMilliseconds, 1); status = $result.status; elapsed_ms = [math]::Round($result.elapsed_ms, 2) })
        } catch {
            $script:healthFailures++; $script:healthTimeouts++
            $samples.Add([pscustomobject]@{ kind = 'health'; at_ms = [math]::Round($script:clock.Elapsed.TotalMilliseconds, 1); status = 0; elapsed_ms = [math]::Round($operation.watch.Elapsed.TotalMilliseconds, 2); failure = 'timeout_or_transport_failure' })
        }
    }
    if ($null -ne $script:pendingQueue -and $script:pendingQueue.task.IsCompleted) {
        $operation = $script:pendingQueue
        $script:pendingQueue = $null
        try {
            $result = Finish-WorkloadRequest $operation
            if ($result.status -ne 200 -or $result.body.failed -gt 0) { $script:queueFailures++ }
            $samples.Add([pscustomobject]@{ kind = 'queue'; at_ms = [math]::Round($script:clock.Elapsed.TotalMilliseconds, 1); status = $result.status; elapsed_ms = [math]::Round($result.elapsed_ms, 2); running = $result.body.running; failed = $result.body.failed })
        } catch { $script:queueFailures++ }
    }
}
function Pump-Workload {
    Assert-Workload (-not $script:app.HasExited) 'desktop exited during active workload'
    Assert-Workload ($script:clock.Elapsed.TotalSeconds -lt $MaxDurationSeconds) 'total workload duration cutoff reached'
    $script:app.Refresh()
    $script:finalWorkingSet = $script:app.WorkingSet64
    $script:peakWorkingSet = [math]::Max($script:peakWorkingSet, $script:finalWorkingSet)
    $script:cpuSeconds = $script:app.TotalProcessorTime.TotalSeconds
    Assert-Workload ($script:finalWorkingSet -le ([long]$MaxWorkingSetMiB * 1MB)) 'owned desktop memory cutoff reached'
    Receive-WorkloadProbes
    $nowMS = $script:clock.Elapsed.TotalMilliseconds
    if ($nowMS - $script:lastProbeMS -ge 500) {
        $script:maxProbeLagMS = [math]::Max($script:maxProbeLagMS, [math]::Max(0, $nowMS - $script:lastProbeMS - 500))
        $script:lastProbeMS = $nowMS
        if ($null -eq $script:pendingHealth) { $script:pendingHealth = Start-WorkloadRequest 'GET' 'api/health' -TimeoutSeconds 2 }
        if ($null -eq $script:pendingQueue) { $script:pendingQueue = Start-WorkloadRequest 'GET' 'api/queue' -TimeoutSeconds 2 }
    }
}
function Invoke-WorkloadUpload([string]$Path, [int]$Ordinal, [int]$Round) {
    $script:checkpoint = "upload round $Round input $Ordinal"
    $script:activeUpload = Start-WorkloadRequest 'POST' 'api/analyze' -Upload $Path -TimeoutSeconds $UploadTimeoutSeconds
    while (-not $script:activeUpload.task.IsCompleted) { Pump-Workload; Start-Sleep -Milliseconds 50 }
    Pump-Workload
    $operation = $script:activeUpload
    $script:activeUpload = $null
    $result = Finish-WorkloadRequest $operation
    Assert-Workload ($result.status -eq 200 -and @($result.body.results).Count -eq 1) 'upload response was not a successful one-file analysis'
    $entry = $result.body.results[0]
    Assert-Workload ($entry.ok -and -not $entry.error -and -not $entry.already_stored) 'upload returned an error or skipped a previously stored replay'
    $count = 0L
    foreach ($item in @($entry.matches)) {
        Assert-Workload ($item.ok -and -not $item.error -and -not $item.already_stored) 'an uploaded session failed or was skipped'
        $count += [long]$item.match.frames
    }
    Assert-Workload ($count -gt 0) 'successful upload reported no processed frames'
    $runs.Add([pscustomobject]@{ round = $Round; input = $Ordinal; frames = $count; elapsed_ms = [math]::Round($result.elapsed_ms, 2); frames_per_second = [math]::Round($count / ($result.elapsed_ms / 1000), 2) })
    return $count
}
function Test-WorkloadNotes {
    $script:checkpoint = 'synthetic note and HTTP security checks'
    $matchPath = 'api/match/SYN-FIXTURE-001'
    $case = Send-WorkloadRequest 'GET' ($matchPath + '/investigation')
    if ($case.status -ne 200) {
        Add-WorkloadCheck 'synthetic_notes_and_security' 'NOT TESTED' 'Supplied inputs did not expose SYN-FIXTURE-001; note/security checks require that synthetic fixture.'
        return
    }
    $wrong = Send-WorkloadRequest 'GET' ('../00000000000000000000000000000000/' + $matchPath + '/investigation')
    Assert-Workload ($wrong.status -eq 404) 'incorrect random URL token did not return 404'
    $id = [Guid]::NewGuid().ToString()
    $body = @{ note_id = $id; kind = 'note'; body = 'Synthetic operational test note'; frame_index = -1 }
    $denied = Send-WorkloadRequest 'POST' ($matchPath + '/notes') $body 'https://untrusted.invalid'
    Assert-Workload ($denied.status -eq 403) 'cross-origin note mutation was not denied'
    $initial = Send-WorkloadRequest 'GET' ($matchPath + '/notes')
    Assert-Workload (@($initial.body.notes).Count -eq 0) 'denied mutation created a note'
    foreach ($attempt in 1..2) {
        $saved = Send-WorkloadRequest 'POST' ($matchPath + '/notes') $body
        Assert-Workload ($saved.status -eq 200 -and $saved.body.note_id -eq $id) 'note save/retry lacked matching backend confirmation'
    }
    $notes.Add($id)
    $pending = @()
    foreach ($ordinal in 1..2) {
        $id = [Guid]::NewGuid().ToString(); $notes.Add($id)
        $pending += Start-WorkloadRequest 'POST' ($matchPath + '/notes') @{ note_id = $id; kind = 'note'; body = "Independent synthetic note $ordinal"; frame_index = -1 }
    }
    foreach ($operation in $pending) {
        $saved = Finish-WorkloadRequest $operation
        Assert-Workload ($saved.status -eq 200 -and $saved.body.note_id -in $notes) 'concurrent note save was not confirmed'
    }
    $invalid = Send-WorkloadRequest 'POST' ($matchPath + '/notes') @{ kind = 'note'; body = ''; frame_index = -1 }
    Assert-Workload ($invalid.status -eq 400 -and $invalid.body.error) 'invalid note was reported as successful'
    $listed = Send-WorkloadRequest 'GET' ($matchPath + '/notes')
    Assert-Workload ($listed.status -eq 200 -and @($listed.body.notes).Count -eq 3) 'duplicate retry or concurrent note persistence lost its expected row count'
    foreach ($id in $notes) { Assert-Workload (@($listed.body.notes | Where-Object { $_.note_id -eq $id }).Count -eq 1) 'note identity was missing or duplicated' }
    Add-WorkloadCheck 'synthetic_notes_and_security' 'PASS' 'Valid token case route loaded; wrong token 404, cross-origin mutation 403, same-ID retry one row, two concurrent IDs both persisted, invalid note 400.'
}

try {
    Assert-Workload $IsWindows 'actual Windows candidate requires Windows'
    Assert-Workload ($MaxDurationSeconds -gt $MinDurationSeconds) 'maximum duration must exceed minimum duration'
    Assert-Workload (-not ($ReplayManifest -and $ReplayFiles.Count)) 'supply ordered ReplayFiles or ReplayManifest, not both'
    if ($ReplayManifest) {
        $manifestText = [IO.File]::ReadAllText((Resolve-Path -LiteralPath $ReplayManifest).Path)
        Assert-Workload ($manifestText.TrimStart().StartsWith('[')) 'replay manifest must be a JSON array of explicit paths'
        $ReplayFiles = @($manifestText | ConvertFrom-Json)
    }
    Assert-Workload ($ReplayFiles.Count -gt 0) 'explicit replay inputs are required'
    $binary = (Resolve-Path -LiteralPath $DesktopExecutable).Path
    $binaryHash = (Get-FileHash -LiteralPath $binary -Algorithm SHA256).Hash.ToLowerInvariant()
    foreach ($path in $ReplayFiles) {
        $item = Get-Item -LiteralPath $path
        Assert-Workload (-not $item.PSIsContainer -and $item.Extension -ieq '.echoreplay') 'each input must be an explicitly selected echoreplay file'
        $inputs.Add([pscustomobject]@{ ordinal = $inputs.Count + 1; private_path = $item.FullName; bytes = $item.Length; sha256_before = (Get-FileHash -LiteralPath $item.FullName -Algorithm SHA256).Hash.ToLowerInvariant(); sha256_after = ''; unchanged = $false })
    }
    [IO.File]::WriteAllText($config, "[general]`ndb_path = " + ($db | ConvertTo-Json -Compress) + "`nlog_level = 'error'`n")
    [IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($db)) | Out-Null
    [IO.File]::WriteAllText((Join-Path ([IO.Path]::GetDirectoryName($db)) 'nevr-desktop-settings.json'), '{"watch_enabled":false,"automatic_update_checks":false,"seen_files":{}}')
    Start-WorkloadDesktop
    $health = Send-WorkloadRequest 'GET' 'api/health'
    Assert-Workload ($health.status -eq 200 -and $health.body.stored_matches -eq 0 -and [IO.Path]::GetFullPath($health.body.database_path) -eq $db) 'desktop did not open a fresh isolated database'
    $provenance = $health.body.provenance
    Assert-Workload ($provenance.build_commit -ceq $ExpectedCommit -and $provenance.source_revision -ceq $ExpectedCommit -and $provenance.source_modified -eq $false -and $provenance.build_identity -eq 'verified_clean_revision' -and $provenance.executable_sha256 -ceq $binaryHash -and $provenance.review_only -eq $true -and $provenance.enforcement_policy -eq 'review-only-v1') 'candidate identity or immutable review-only policy did not match'
    Add-WorkloadCheck 'isolated_candidate_identity' 'PASS' 'Exact supplied executable hash, clean expected revision and compiled review-only policy verified; fresh database and isolated app-data.'
    $script:clock.Start()
    $expectedMatchCount = $null
    do {
        $rounds++
        foreach ($replayInput in $inputs) { $frames += Invoke-WorkloadUpload $replayInput.private_path $replayInput.ordinal $rounds }
        if ($rounds -eq 1) { Test-WorkloadNotes }
        $roundHealth = Send-WorkloadRequest 'GET' 'api/health'
        Assert-Workload ($roundHealth.status -eq 200) 'round-end health failed'
        if ($null -eq $expectedMatchCount) { $expectedMatchCount = $roundHealth.body.stored_matches }
        Assert-Workload ($roundHealth.body.stored_matches -eq $expectedMatchCount) 'repeated imports changed the stored match count'
        $completedRounds = $rounds
    } while ($rounds -lt $MinRounds -or $script:clock.Elapsed.TotalSeconds -lt $MinDurationSeconds)
    # Complete already-started probes without starting another sampling cycle.
    $drainUntil = [DateTime]::UtcNow.AddSeconds(3)
    while (($null -ne $script:pendingHealth -or $null -ne $script:pendingQueue) -and [DateTime]::UtcNow -lt $drainUntil) { Receive-WorkloadProbes; Start-Sleep -Milliseconds 20 }
    $script:clock.Stop()
    Assert-Workload ($script:healthFailures -eq 0 -and $script:queueFailures -eq 0) 'health or queue probe failed during workload'
    Assert-Workload (@($samples | Where-Object { $_.kind -eq 'health' -and $_.status -eq 200 }).Count -gt 0 -and $script:activeHealthSamples -gt 0) 'no successful health samples proved responsiveness during active analysis'
    Add-WorkloadCheck 'long_lived_desktop_workload' 'PASS' "$rounds complete rounds and $($runs.Count) actual uploads/reanalyses completed in one long-lived process; responsive HTTP probes and sampled resource limits passed."
    $before = Send-WorkloadRequest 'GET' 'api/health'
    $storedMatches = [int]$before.body.stored_matches; $databaseBytes = [long]$before.body.database_bytes
    Stop-WorkloadDesktop
    Start-WorkloadDesktop
    $after = Send-WorkloadRequest 'GET' 'api/health'
    Assert-Workload ($after.status -eq 200 -and $after.body.stored_matches -eq $storedMatches -and $after.body.provenance.executable_sha256 -ceq $binaryHash) 'graceful restart did not preserve matches or executable identity'
    if ($notes.Count -gt 0) {
        $listed = Send-WorkloadRequest 'GET' 'api/match/SYN-FIXTURE-001/notes'
        Assert-Workload ($listed.status -eq 200 -and @($listed.body.notes).Count -eq 3) 'restart changed saved note count'
        foreach ($id in $notes) { Assert-Workload (@($listed.body.notes | Where-Object { $_.note_id -eq $id }).Count -eq 1) 'restart lost a saved note identity' }
        Add-WorkloadCheck 'graceful_restart_persistence' 'PASS' 'Stored match count and all three synthetic notes survived graceful shutdown/reopen of the same isolated database.'
    } else { Add-WorkloadCheck 'graceful_restart_persistence' 'NOT TESTED' 'Match count survived restart, but required synthetic note fixture was absent.' }
    Stop-WorkloadDesktop
    $script:exitCode = if ($notes.Count -eq 3) { 0 } else { 2 }
} catch {
    # Never echo arbitrary HTTP/parse/process errors: they may contain a random
    # URL token, replay body, identity, filename or private filesystem path.
    Add-WorkloadCheck 'workload_failure' 'FAIL' ($script:checkpoint + ' (' + $_.Exception.GetType().Name + ')')
    $script:exitCode = 1
} finally {
    $script:clock.Stop()
    $ownedChildExited = $true
    foreach ($operation in @($script:activeUpload, $script:pendingHealth, $script:pendingQueue)) {
        if ($null -ne $operation) {
            try { $operation.cts.Cancel(); $null = Finish-WorkloadRequest $operation } catch { }
        }
    }
    if ($null -ne $script:app) {
        try {
            if (-not $script:app.HasExited) {
                # Only the Process object started by this invocation is owned.
                # Cutoff cleanup is not a claimed crash/recovery acceptance test.
                $script:app.Kill(); $ownedChildExited = $script:app.WaitForExit(10000)
            }
        } catch { $ownedChildExited = $false; $script:exitCode = 1 }
        finally { $script:app.Dispose(); $script:app = $null }
    }
    foreach ($replayInput in $inputs) {
        try {
            $replayInput.sha256_after = (Get-FileHash -LiteralPath $replayInput.private_path -Algorithm SHA256).Hash.ToLowerInvariant()
            $replayInput.unchanged = $replayInput.sha256_before -ceq $replayInput.sha256_after
            if (-not $replayInput.unchanged) { $script:exitCode = 1 }
        } catch { $script:exitCode = 1 }
    }
    $binaryUnchanged = $false
    if ($binary) { try { $binaryUnchanged = (Get-FileHash -LiteralPath $binary -Algorithm SHA256).Hash.ToLowerInvariant() -ceq $binaryHash } catch { } }
    if (-not $binaryUnchanged) { $script:exitCode = 1 }
    Add-WorkloadCheck 'inputs_and_binary_unchanged' $(if ($binaryUnchanged -and @($inputs | Where-Object { -not $_.unchanged }).Count -eq 0) { 'PASS' } else { 'FAIL' }) 'Source and executable SHA-256 before/after recorded; inputs were opened read-only.'
    Add-WorkloadCheck 'abrupt_kill_recovery' 'NOT TESTED' 'No deliberate active-analysis kill or crash recovery was exercised. Graceful restart is not crash recovery.'
    $resolvedScratch = [IO.Path]::GetFullPath($scratch)
    $requiredScratch = [IO.Path]::GetFullPath((Join-Path $runRoot 'isolated-state'))
    try {
        Assert-Workload ($ownedChildExited) 'owned child did not exit; preserving isolated state'
        Assert-Workload ($resolvedScratch -ceq $requiredScratch -and [IO.Path]::GetFileName($runRoot) -match '^workload-[a-f0-9]{32}$') 'unsafe workload state cleanup path'
        Remove-Item -LiteralPath $resolvedScratch -Recurse -Force
        Add-WorkloadCheck 'isolated_state_cleanup' 'PASS' 'Only the validated newly created state directory was removed after the owned child exited; reports remain.'
    } catch {
        $script:exitCode = 1
        Add-WorkloadCheck 'isolated_state_cleanup' 'FAIL' 'Isolated state cleanup was refused or failed; output remains private. No pre-existing directory was targeted.'
    }
    $latencies = @($samples | Where-Object { $_.kind -eq 'health' -and $_.status -eq 200 } | ForEach-Object { [double]$_.elapsed_ms } | Sort-Object)
    $elapsed = $script:clock.Elapsed.TotalSeconds
    $metrics = [ordered]@{
        complete_rounds = $completedRounds; uploads = $runs.Count; workload_elapsed_seconds = [math]::Round($elapsed, 3); processed_frames = $frames
        frames_per_elapsed_second = if ($elapsed -gt 0) { [math]::Round($frames / $elapsed, 2) } else { $null }
        peak_sampled_working_set_bytes = $script:peakWorkingSet; final_sampled_working_set_bytes = $script:finalWorkingSet; cpu_seconds = [math]::Round($script:cpuSeconds, 3)
        health_samples = $latencies.Count; health_failures = $script:healthFailures; health_timeouts_or_transport_failures = $script:healthTimeouts; active_analysis_health_samples = $script:activeHealthSamples; queue_failures = $script:queueFailures
        health_median_ms = if ($latencies.Count) { $latencies[[math]::Floor(($latencies.Count - 1) / 2)] } else { $null }
        health_p95_ms = if ($latencies.Count) { $latencies[[math]::Ceiling($latencies.Count * 0.95) - 1] } else { $null }
        health_max_ms = if ($latencies.Count) { $latencies[-1] } else { $null }
        probe_scheduling_lag_max_ms = [math]::Round($script:maxProbeLagMS, 2); stored_matches = $storedMatches; final_database_bytes = $databaseBytes
    }
    $report = [ordered]@{
        schema_version = 'nevr-desktop-workload/v1'; started_at = $started.ToString('o'); finished_at = [DateTime]::UtcNow.ToString('o')
        automated_status = if ($script:exitCode -eq 0) { 'PASS' } elseif ($script:exitCode -eq 2) { 'BLOCKED' } else { 'FAIL' }
        expected_build_commit = $ExpectedCommit; executable_sha256 = $binaryHash; executable_unchanged = $binaryUnchanged; provenance = $provenance
        limits = @{ minimum_rounds = $MinRounds; minimum_seconds = $MinDurationSeconds; maximum_seconds = $MaxDurationSeconds; upload_timeout_seconds = $UploadTimeoutSeconds; maximum_working_set_mib = $MaxWorkingSetMiB; probe_interval_ms = 500; probe_timeout_seconds = 2 }
        metrics = $metrics; checks = @($checks.ToArray()); uploads = @($runs.ToArray()); probes = @($samples.ToArray())
        inputs = @($inputs | Select-Object ordinal, bytes, sha256_before, sha256_after, unchanged)
        interpretation = @('Operational repeated-upload measurements, not detector accuracy, real-time latency or a production capacity guarantee.', 'Working-set peaks are sampled; CPU seconds are cumulative CPU time of the long-lived workload process. Health latency measures HttpClient send through full response-body completion inside the async task, excluding PowerShell completion polling; it includes local HTTP and backend work. Scheduling lag belongs to this runner.', 'Only explicit replay paths were read. Reports omit paths, filenames, raw telemetry, player identities and per-run URL tokens; output remains private.', 'No browser, updater, Spark, installer or external integration was invoked. Abrupt crash recovery and arbitrary third-party enforcement integrations remain untested.')
    }
    $report | ConvertTo-Json -Depth 12 | Set-Content -LiteralPath (Join-Path $runRoot 'desktop-workload-report.json') -Encoding utf8
    @($checks | ForEach-Object { "$($_.status) $($_.id) - $($_.detail)" }) | Set-Content -LiteralPath (Join-Path $runRoot 'workload.log') -Encoding utf8
    $client.Dispose(); $handler.Dispose()
    Write-Host 'Desktop workload completed; private report written inside the newly created workload output directory.'
}
exit $script:exitCode
