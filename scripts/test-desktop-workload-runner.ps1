#requires -Version 7.2
[CmdletBinding()]
param()

# Test the workload harness without starting an app, reading a replay, creating
# a database or making any network connection. Actual packaged-app acceptance
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
    protected override async Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, CancellationToken token) {
        LastUri = request.RequestUri.ToString();
        LastMethod = request.Method.Method;
        LastBody = request.Content == null ? "" : await request.Content.ReadAsStringAsync();
        LastOrigin = request.Headers.Contains("Origin") ? string.Join(",", request.Headers.GetValues("Origin")) : "";
        if (Cancel) await Task.Delay(10000, token);
        return new HttpResponseMessage((HttpStatusCode)Status) { Content = new StringContent(Body) };
    }
}
'@
}
foreach ($name in @('Assert-Workload', 'Start-WorkloadRequest', 'Finish-WorkloadRequest', 'Pump-Workload')) {
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
    try { $null = Finish-WorkloadRequest (Start-WorkloadRequest 'GET' 'api/health' -TimeoutSeconds 1) } catch { $cancelled = $true }
    Assert-Runner $cancelled 'Request timeout failed to cancel the task.'
    Pass-Runner 'Bounded request cancellation'
} finally { $client.Dispose(); $handler.Dispose() }

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
Write-Host "$passed workload runner regressions passed; no actual candidate or replay was exercised."
