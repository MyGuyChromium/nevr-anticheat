#requires -Version 7.2
[CmdletBinding()]
param()

# Test the workload harness without starting an app, reading a user replay,
# creating a database or making any network connection. Multipart tests use
# disposable synthetic bytes. Actual packaged-app acceptance
# remains a separate explicitly selected workload run.
$ErrorActionPreference = 'Stop'
$sourcePath = Join-Path $PSScriptRoot 'test-desktop-release-workload.ps1'
$source = [IO.File]::ReadAllText($sourcePath)
$tokens = $null; $parseErrors = $null
$ast = [Management.Automation.Language.Parser]::ParseFile($sourcePath, [ref]$tokens, [ref]$parseErrors)
$passed = 0
function Assert-Runner([bool]$Condition, [string]$Message) { if (-not $Condition) { throw $Message } }
function Pass-Runner([string]$Name) { $script:passed++; Write-Host "PASS $Name" }
Assert-Runner ($parseErrors.Count -eq 0) 'Runner must parse before any candidate can be tested.'
Pass-Runner 'PowerShell syntax'

$timing = @($ast.FindAll({ param($node) $node -is [Management.Automation.Language.CommandAst] -and $node.GetCommandName() -eq 'Add-Type' }, $true))
Assert-Runner ($timing.Count -eq 1) 'Expected one small HTTP completion-timing helper.'
if (-not ('NEVRWorkloadHttpTiming' -as [type])) { Invoke-Expression $timing[0].Extent.Text }
if (-not ('NEVRWorkloadTestHandler' -as [type])) {
    Add-Type -TypeDefinition @'
using System.Net;
using System.Net.Http;
using System.Threading;
using System.Threading.Tasks;
public sealed class NEVRWorkloadTestHandler : HttpMessageHandler {
    public string LastUri, LastMethod, LastBody, LastOrigin;
    public int Status = 200;
    public string Body = "{\"ok\":true}";
    public bool Cancel;
    public bool ThrowTransport;
    protected override async Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, CancellationToken token) {
        LastUri = request.RequestUri.ToString();
        LastMethod = request.Method.Method;
        LastBody = request.Content == null ? "" : await request.Content.ReadAsStringAsync();
        LastOrigin = request.Headers.Contains("Origin") ? string.Join(",", request.Headers.GetValues("Origin")) : "";
        if (ThrowTransport) throw new HttpRequestException("private-url-and-body-must-not-be-reported");
        if (Cancel) await Task.Delay(10000, token);
        return new HttpResponseMessage((HttpStatusCode)Status) { Content = new StringContent(Body) };
    }
}
'@
}
foreach ($name in @('Assert-Workload', 'Get-WorkloadFailureKind', 'Get-WorkloadReplayExtension', 'Start-WorkloadRequest', 'Finish-WorkloadRequest', 'Read-FinalWorkloadSnapshot', 'Assert-ConnectionStatusResponse', 'Pump-Workload')) {
    $functionAst = $ast.Find({ param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq $name }, $true)
    Assert-Runner ($null -ne $functionAst) "Required actual runner helper missing: $name"
    Invoke-Expression $functionAst.Extent.Text
}
$handler = [NEVRWorkloadTestHandler]::new()
$client = [Net.Http.HttpClient]::new($handler)
$script:appURL = 'http://127.0.0.1:9999/11111111111111111111111111111111/'
try {
    # Warm up JIT independently; delay observing an already-completed response.
    $warmup = Finish-WorkloadRequest (Start-WorkloadRequest 'GET' 'api/health')
    Assert-Runner ($warmup.status -eq 200) 'Fake handler was not called.'
    $operation = Start-WorkloadRequest 'GET' 'api/health'
    $completed = $operation.task.GetAwaiter().GetResult()
    $observed = [Diagnostics.Stopwatch]::StartNew()
    Start-Sleep -Milliseconds 250
    $response = Finish-WorkloadRequest $operation
    Assert-Runner ($observed.Elapsed.TotalMilliseconds -ge 200 -and $response.elapsed_ms -eq $completed.ElapsedMilliseconds) 'Reported HTTP duration must use task completion, not delayed observation.'
    Pass-Runner 'HTTP completion timing excludes runner polling delay'

    $request = Start-WorkloadRequest 'POST' 'api/match/SYN-FIXTURE-001/notes' @{ body = 'Synthetic "literal" <text>'; kind = 'note' } '' 'https://untrusted.invalid'
    $response = Finish-WorkloadRequest $request
    Assert-Runner ($handler.LastMethod -eq 'POST' -and $handler.LastOrigin -eq 'https://untrusted.invalid' -and ($handler.LastBody | ConvertFrom-Json).body -ceq 'Synthetic "literal" <text>') 'Request helper changed note body or omitted Origin.'
    Pass-Runner 'Actual JSON/origin request helper'

    $multipartRoot = Join-Path ([IO.Path]::GetTempPath()) ('nevr-workload-multipart-' + [Guid]::NewGuid().ToString('N'))
    [IO.Directory]::CreateDirectory($multipartRoot) | Out-Null
    try {
        foreach ($extension in @('.echoreplay', '.tape', '.TAPE')) {
            $path = Join-Path $multipartRoot ("private O'Brien name" + $extension)
            $content = 'synthetic multipart bytes; not a gameplay recording'
            [IO.File]::WriteAllText($path, $content)
            $response = Finish-WorkloadRequest (Start-WorkloadRequest 'POST' 'api/analyze' -Upload $path)
            $expectedName = 'workload-input' + $extension.ToLowerInvariant()
            Assert-Runner ($response.status -eq 200 -and $handler.LastBody.Contains($expectedName) -and $handler.LastBody.Contains($content)) 'Native/legacy multipart filename extension or fixture payload was lost.'
            Assert-Runner (-not $handler.LastBody.Contains("private O'Brien") -and -not $handler.LastBody.Contains($multipartRoot)) 'Multipart exposed an original private filename or directory.'
            Assert-Runner ([IO.File]::ReadAllText($path) -ceq $content) 'Upload changed the selected source.'
        }
        foreach ($unsupported in @('fixture.json', 'fixture.nevrcap', 'fixture.tape.exe', 'fixture')) {
            $failed = $false
            try { $null = Get-WorkloadReplayExtension $unsupported } catch { $failed = $true }
            Assert-Runner $failed 'Unsupported workload input extension was accepted.'
        }
        Assert-Runner ($source.Contains('$null = Get-WorkloadReplayExtension $item.FullName')) 'Initial explicit-input validation does not use the tested format allowlist.'
        Pass-Runner 'Native and legacy multipart preserve selected format without private filenames'
    } finally {
        foreach ($item in @(Get-ChildItem -LiteralPath $multipartRoot -File)) { Remove-Item -LiteralPath $item.FullName }
        Remove-Item -LiteralPath $multipartRoot
    }

    $handler.Status = 404
    $response = Finish-WorkloadRequest (Start-WorkloadRequest 'GET' '../00000000000000000000000000000000/api/match/SYN-FIXTURE-001/investigation')
    Assert-Runner ($response.status -eq 404 -and ([Uri]$handler.LastUri).AbsolutePath -eq '/00000000000000000000000000000000/api/match/SYN-FIXTURE-001/investigation') 'Wrong-token probe did not actually replace the token path.'
    Pass-Runner 'Wrong-token probe and non-success status preservation'

    $handler.Status = 400; $handler.Body = '{"error":"synthetic invalid note"}'
    $response = Finish-WorkloadRequest (Start-WorkloadRequest 'POST' 'api/match/SYN-FIXTURE-001/notes' @{ body = '' })
    Assert-Runner ($response.status -eq 400 -and $response.body.error) 'Backend failure became success.'
    Pass-Runner 'Invalid note response remains failure'

    $handler.Cancel = $true
    $cancelled = $false
    $operation = Start-WorkloadRequest 'GET' 'api/status' -TimeoutSeconds 1
    try { $null = Finish-WorkloadRequest $operation } catch { $cancelled = $true }
    Assert-Runner ($cancelled -and $operation.failure_kind -ceq 'deadline_cancelled') 'Request timeout failed to cancel the task or was misclassified.'
    Pass-Runner 'Bounded request cancellation'
    $handler.Cancel = $false; $handler.ThrowTransport = $true
    $operation = Start-WorkloadRequest 'GET' 'api/status'
    $failed = $false
    try { $null = Finish-WorkloadRequest $operation } catch { $failed = $true }
    Assert-Runner ($failed -and $operation.failure_kind -ceq 'transport_failure') 'Transport exception was not safely distinguished from deadline cancellation.'
    Pass-Runner 'Transport failure classification does not include private exception text'
    $handler.ThrowTransport = $false; $handler.Status = 200; $handler.Body = '{"broken":'
    $operation = Start-WorkloadRequest 'GET' 'api/status'
    $failed = $false
    try { $null = Finish-WorkloadRequest $operation } catch { $failed = $true }
    Assert-Runner ($failed -and $operation.failure_kind -ceq 'invalid_response') 'Invalid JSON response was falsely counted as a transport timeout.'
    Pass-Runner 'Invalid response classification preserves the failure without mislabeling timeout'
} finally { $client.Dispose(); $handler.Dispose() }

& {
    $storedMatches = $null; $databaseBytes = $null
    foreach ($snapshot in @([pscustomobject]@{status=500;body=[pscustomobject]@{stored_matches=9;database_bytes=123}}, [pscustomobject]@{status=200;body=[pscustomobject]@{stored_matches=9}})) {
        $failed = $false
        try {
            $final = Read-FinalWorkloadSnapshot $snapshot
            $storedMatches = $final.stored_matches; $databaseBytes = $final.database_bytes
        } catch { $failed = $true }
        Assert-Runner ($failed -and $null -eq $storedMatches -and $null -eq $databaseBytes) 'Unmeasured final data was fabricated as zero or accepted from a failed snapshot.'
    }
    $final = Read-FinalWorkloadSnapshot ([pscustomobject]@{status=200;body=[pscustomobject]@{stored_matches=0;database_bytes=456}})
    Assert-Runner ($final.stored_matches -eq 0 -and $final.database_bytes -eq 456) 'A successfully observed zero was confused with unavailable data.'
    Assert-Runner ($source.Contains('$storedMatches = $null') -and $source.Contains('$databaseBytes = $null')) 'Actual runner defaults still invent unmeasured final values.'
    Pass-Runner 'Unmeasured final snapshots stay null; observed zeros remain valid'
}

& {
    $commit = 'a' * 40; $hash = 'c' * 64
    $good = [pscustomobject]@{ schema_version='nevr-desktop-status/v1'; version='0.14.0'; analysis_active=$true; provenance=[pscustomobject]@{version='nevr-runtime-provenance/v1';app_version='0.14.0';build_commit=$commit;source_revision=$commit;source_modified=$false;build_identity='verified_clean_revision';executable_sha256=$hash;review_only=$true;enforcement_policy='review-only-v1'} }
    Assert-ConnectionStatusResponse $good $commit $hash
    foreach ($mutation in @('schema', 'activity', 'commit', 'hash', 'policy', 'dirty')) {
        $bad = $good | ConvertTo-Json -Depth 6 | ConvertFrom-Json
        switch ($mutation) {
            'schema' { $bad.schema_version = 'unknown' }
            'activity' { $bad.analysis_active = 'true' }
            'commit' { $bad.provenance.build_commit = 'b' * 40 }
            'hash' { $bad.provenance.executable_sha256 = 'd' * 64 }
            'policy' { $bad.provenance.review_only = $false }
            'dirty' { $bad.provenance.source_modified = $true }
        }
        $failed = $false
        try { Assert-ConnectionStatusResponse $bad $commit $hash } catch { $failed = $true }
        Assert-Runner $failed 'An HTTP 200 with invalid status schema/activity/provenance was accepted.'
    }
    Pass-Runner 'Connection status requires schema boolean activity and unchanged review-only candidate'
}

# Exercise the actual upload response consumer without file/network I/O. This
# shape follows analyzeEntry -> matches[] -> matchView.FramesProcessed, not the
# unrelated playerView.frames field. Keep the source contract checked as well.
$serverSource = [IO.File]::ReadAllText((Join-Path $PSScriptRoot '..\cmd\desktop\server.go'))
Assert-Runner ($serverSource -match 'FramesProcessed\s+int\s+`json:"frames_processed"`') 'Server matchView frame-count contract changed; update the workload integration test.'
& {
    $uploadFunction = $ast.Find({ param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq 'Invoke-WorkloadUpload' }, $true)
    Assert-Runner ($null -ne $uploadFunction) 'Actual upload consumer is missing.'
    Invoke-Expression $uploadFunction.Extent.Text
    function Start-WorkloadRequest { return [pscustomobject]@{ task = [Threading.Tasks.Task]::CompletedTask } }
    function Finish-WorkloadRequest { return $responseFixture }
    function Pump-Workload { }
    $UploadTimeoutSeconds = 5
    $runs = [Collections.Generic.List[object]]::new()
    $responseFixture = [pscustomobject]@{
        status = 200; elapsed_ms = 1000
        body = ('{"results":[{"file":"workload-input.echoreplay","ok":true,"match_id":"SYN-FIXTURE-001","match":{"match_id":"SYN-FIXTURE-001","frames_processed":12},"matches":[{"ok":true,"match_id":"SYN-FIXTURE-001","match":{"match_id":"SYN-FIXTURE-001","frames_processed":12}},{"ok":true,"match_id":"SYN-FIXTURE-002","match":{"match_id":"SYN-FIXTURE-002","frames_processed":8}}]}]}' | ConvertFrom-Json)
    }
    $frames = Invoke-WorkloadUpload 'synthetic-not-opened.echoreplay' 2 3
    Assert-Runner ($frames -eq 20 -and $runs.Count -eq 1 -and $runs[0].frames -eq 20 -and $runs[0].frames_per_second -eq 20 -and $runs[0].round -eq 3 -and $runs[0].input -eq 2) 'Actual multi-session frames_processed payload did not aggregate once into upload metrics.'
    Pass-Runner 'Actual upload consumer aggregates server frames_processed shape'
    foreach ($invalidMatch in @('{"frames_processed":0}', '{"frames_processed":-1}', '{}', '{"frames":99}')) {
        # Keep the first session valid: a positive total must not hide missing
        # or zero processed-frame evidence for the second uploaded session.
        $responseFixture.body.results[0].matches[1].match = $invalidMatch | ConvertFrom-Json
        $rejected = $false
        try { $null = Invoke-WorkloadUpload 'synthetic-not-opened.echoreplay' 2 3 } catch { $rejected = $true }
        Assert-Runner ($rejected -and $runs.Count -eq 1) 'Missing/zero/wrong-key session frames were accepted or appended as a successful run.'
    }
    Pass-Runner 'Upload consumer rejects missing zero negative and wrong-key frame counts'
}

# ListInvestigationNotes starts with a nil Go slice: its real empty response
# is {"notes":null}. Drive the actual full note/security helper from that
# response, preserving IDs and returning the same route/status contracts.
& {
    $noteFunction = $ast.Find({ param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq 'Test-WorkloadNotes' }, $true)
    Assert-Runner ($null -ne $noteFunction) 'Actual note workload consumer is missing.'
    Invoke-Expression $noteFunction.Extent.Text
    $savedRows = [ordered]@{}
    $notes = [Collections.Generic.List[string]]::new()
    $recordedChecks = [Collections.Generic.List[object]]::new()
    function Add-WorkloadCheck($ID, $Status, $Detail) { $recordedChecks.Add([pscustomobject]@{ id = $ID; status = $Status }) }
    function Send-WorkloadRequest($Method, $Path, $Body = $null, $Origin = '') {
        if ($Path.EndsWith('/investigation')) {
            return [pscustomobject]@{ status = $(if ($Path.StartsWith('../')) { 404 } else { 200 }); body = $null }
        }
        Assert-Runner ($Path -eq 'api/match/SYN-FIXTURE-001/notes') 'Unexpected mock route; do not hide missing endpoint coverage.'
        if ($Method -eq 'GET') {
            return [pscustomobject]@{ status = 200; body = [pscustomobject]@{ notes = $(if ($savedRows.Count) { @($savedRows.Values) } else { $null }) } }
        }
        if ($Origin) { return [pscustomobject]@{ status = 403; body = [pscustomobject]@{ error = 'cross-origin request denied' } } }
        if (-not $Body.body) { return [pscustomobject]@{ status = 400; body = [pscustomobject]@{ error = 'note body is required' } } }
        $savedRows[$Body.note_id] = [pscustomobject]$Body
        return [pscustomobject]@{ status = 200; body = $savedRows[$Body.note_id] }
    }
    function Start-WorkloadRequest($Method, $Path, $Body) { return [pscustomobject]@{ result = (Send-WorkloadRequest $Method $Path $Body) } }
    function Finish-WorkloadRequest($Operation) { return $Operation.result }
    Test-WorkloadNotes
    Assert-Runner ($notes.Count -eq 3 -and $savedRows.Count -eq 3 -and $recordedChecks.Count -eq 1 -and $recordedChecks[0].status -eq 'PASS') 'Real empty-note response or same-ID/concurrent note projection failed.'
    Pass-Runner 'Actual note helper accepts Go nil slice and preserves three unique IDs'
}

# Exercise the actual early cutoff code with a non-process fake object. It
# must throw before probes or any process action can occur.
$script:app = [pscustomobject]@{ HasExited = $false; WorkingSet64 = 2MB; TotalProcessorTime = [TimeSpan]::FromSeconds(1) }
$script:app | Add-Member -MemberType ScriptMethod -Name Refresh -Value { }
$script:clock = [Diagnostics.Stopwatch]::StartNew()
$MaxDurationSeconds = 30; $MaxWorkingSetMiB = 1
$script:peakWorkingSet = 0L
$cutoff = $false
try { Pump-Workload } catch { $cutoff = $script:checkpoint -eq 'owned desktop memory cutoff reached' }
Assert-Runner $cutoff 'Memory cutoff was not applied before further workload requests.'
Pass-Runner 'Actual memory cutoff guard'
$MaxDurationSeconds = 0
$cutoff = $false
try { Pump-Workload } catch { $cutoff = $script:checkpoint -eq 'total workload duration cutoff reached' }
Assert-Runner $cutoff 'Duration cutoff was not applied before further workload requests.'
Pass-Runner 'Actual total duration cutoff guard'
$script:clock.Stop()

Assert-Runner ($source.Contains('$info.UseShellExecute = $false') -and $source.Contains('$info.CreateNoWindow = $true') -and $source.Contains("'APPDATA'") -and $source.Contains("'LOCALAPPDATA'") -and $source.Contains('$info.Environment.Remove($name)') -and $source.Contains('$handler.AllowAutoRedirect = $false') -and $source.Contains('$handler.UseProxy = $false')) 'Missing process or HTTP isolation control.'
Pass-Runner 'Hidden owned process, app-data isolation and credential/proxy/redirect boundary'
Assert-Runner (-not ($source -match '(?im)^\s*(?:&\s*)?(?:go\s+build|Start-Process|Stop-Process|taskkill|Get-Process)\b') -and -not ($source -match "api/(?:update|replay-viewer|watch/scan)")) 'Harness must not compile replacement binaries, select unrelated processes or invoke external-action endpoints.'
Pass-Runner 'No replacement build, broad process operation or external-action endpoint'
Assert-Runner ($source.Contains('$script:app.Kill()') -and $source.Contains('$ownedChildExited = $script:app.WaitForExit(10000)') -and $source.Contains('Assert-Workload ($ownedChildExited)') -and $source.Contains('$resolvedScratch -ceq $requiredScratch')) 'Cleanup must own the child, verify exit, and resolve the exact newly-created state path.'
Pass-Runner 'Cleanup validates owned process exit and exact state target'
Assert-Runner ($source.Contains('[IO.File]::OpenRead($Upload)') -and $source.Contains('inputs = @($inputs | Select-Object ordinal, bytes, sha256_before, sha256_after, unchanged)') -and -not ($source -match '\$_\.Exception\.Message')) 'Read-only inputs or identity-free report projection missing.'
Pass-Runner 'Read-only replay inputs and path-free input report'
Assert-Runner ($source.Contains('$script:activeHealthSamples -gt 0') -and $source.Contains('while ($rounds -lt $MinRounds -or $script:clock.Elapsed.TotalSeconds -lt $MinDurationSeconds)')) 'Sustained workload must satisfy both floors and prove probes during active analysis.'
Pass-Runner 'Sustained-workload and active-analysis evidence floors'
Assert-Runner ($source.Contains("Start-WorkloadRequest 'GET' 'api/status' -TimeoutSeconds 2") -and -not $source.Contains("Start-WorkloadRequest 'GET' 'api/health' -TimeoutSeconds 2") -and [regex]::Matches($source, "Send-WorkloadRequest 'GET' 'api/health'").Count -ge 4 -and $source.Contains("connection_status_endpoint = 'api/status'")) 'Timed status polling or explicit detailed-health checks no longer match the actual UI split.'
Pass-Runner 'Current UI status polling keeps 2-second deadline and detailed health checks separate'
Write-Host "$passed workload runner regressions passed; no actual candidate or replay was exercised."
