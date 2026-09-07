#requires -Version 7.2
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$ReplayDirectory,
    [string]$Executable = ".\nevr-ac.exe",
    [string]$OutputDirectory = "",
    [ValidateRange(1, 100)]
    [int]$Iterations = 2,
    [ValidateRange(0, 1000000)]
    [int]$MaxFiles = 0,
    [ValidateRange(1, 86400)]
    [int]$RunTimeoutSeconds = 600,
    [ValidateRange(0, 1048576)]
    [int]$MaxWorkingSetMiB = 0,
    [switch]$HashInputs
)

$ErrorActionPreference = "Stop"
$replayRoot = (Resolve-Path -LiteralPath $ReplayDirectory).Path
$binary = (Resolve-Path -LiteralPath $Executable).Path
if (-not (Test-Path -LiteralPath $replayRoot -PathType Container)) { throw 'ReplayDirectory must be a directory.' }
if (-not (Test-Path -LiteralPath $binary -PathType Leaf)) { throw 'Executable must be a file.' }
if (-not $OutputDirectory) { $OutputDirectory = Join-Path $PWD 'dist\soak' }
# Every invocation owns a fresh child directory, even if the caller reuses the
# same output parent. Never silently reopen or replace an existing soak.db.
$outputRoot = Join-Path ([IO.Path]::GetFullPath($OutputDirectory)) ([DateTime]::UtcNow.ToString('yyyyMMddTHHmmssZ') + '-' + [Guid]::NewGuid().ToString('N').Substring(0, 8))
New-Item -ItemType Directory -Path $outputRoot -Force | Out-Null
$binaryHash = (Get-FileHash -LiteralPath $binary -Algorithm SHA256).Hash.ToLowerInvariant()

$files = @(Get-ChildItem -LiteralPath $replayRoot -Recurse -File -Filter "*.echoreplay" | Sort-Object FullName)
if ($MaxFiles -gt 0) { $files = @($files | Select-Object -First $MaxFiles) }
if ($files.Count -eq 0) { throw "No .echoreplay files were found under $replayRoot" }

$database = Join-Path $outputRoot "soak.db"
$config = Join-Path $outputRoot "soak.toml"
$escapedDatabase = ConvertTo-Json -InputObject $database -Compress
@"
[general]
db_path = $escapedDatabase
log_level = 'warn'
log_format = 'text'
"@ | Set-Content -LiteralPath $config -Encoding utf8

$inventory = foreach ($file in $files) {
    $item = [ordered]@{ path = $file.FullName; bytes = $file.Length }
    if ($HashInputs) { $item.sha256 = (Get-FileHash -LiteralPath $file.FullName -Algorithm SHA256).Hash.ToLowerInvariant() }
    [pscustomobject]$item
}

$runs = [Collections.Generic.List[object]]::new()
$startedAt = (Get-Date).ToUniversalTime()
for ($iteration = 1; $iteration -le $Iterations; $iteration++) {
    foreach ($file in $files) {
        $slug = "i{0:D2}-{1:D5}" -f $iteration, ($runs.Count + 1)
        $stdoutPath = Join-Path $outputRoot ($slug + ".stdout.log")
        $stderrPath = Join-Path $outputRoot ($slug + ".stderr.log")
        $timer = [Diagnostics.Stopwatch]::StartNew()
        $process = $null
        $peakWorkingSet = [int64]0
        $exitCode = -1
        $failure = ''
        try {
            $process = Start-Process -FilePath $binary -ArgumentList @(
                "--config", ('"' + $config + '"'), "analyze", ('"' + $file.FullName + '"'), "--force"
            ) -RedirectStandardOutput $stdoutPath -RedirectStandardError $stderrPath -PassThru -WindowStyle Hidden
            do {
                try {
                    $process.Refresh()
                    $peakWorkingSet = [Math]::Max($peakWorkingSet, [int64]$process.WorkingSet64)
                    $peakWorkingSet = [Math]::Max($peakWorkingSet, [int64]$process.PeakWorkingSet64)
                } catch { }
                if (-not $process.HasExited) {
                    if ($timer.Elapsed.TotalSeconds -ge $RunTimeoutSeconds) { $failure = 'timeout' }
                    elseif ($MaxWorkingSetMiB -gt 0 -and $peakWorkingSet -gt ([int64]$MaxWorkingSetMiB * 1MB)) { $failure = 'memory_limit' }
                    if ($failure) {
                        $process.Kill($true) # This exact child and descendants belong to this run.
                        if (-not $process.WaitForExit(10000)) { throw 'Owned analysis process did not stop after its safety limit.' }
                        break
                    }
                    Start-Sleep -Milliseconds 100
                }
            } while (-not $process.HasExited)
            $process.WaitForExit()
            $exitCode = $process.ExitCode
            if (-not $failure -and $exitCode -ne 0) { $failure = 'analysis_failed' }
        } catch {
            $failure = 'launch_or_monitor_failed: ' + $_.Exception.Message
        } finally {
            if ($null -ne $process) {
                if (-not $process.HasExited) { $process.Kill($true); $null = $process.WaitForExit(10000) }
                $process.Dispose()
            }
        }
        $timer.Stop()
        $databaseBytes = if (Test-Path -LiteralPath $database) { (Get-Item -LiteralPath $database).Length } else { 0 }
        $runs.Add([pscustomobject][ordered]@{
            iteration = $iteration
            replay = $file.FullName
            input_bytes = $file.Length
            exit_code = $exitCode
            status = if ($failure) { 'fail' } else { 'pass' }
            failure = $failure
            elapsed_seconds = [Math]::Round($timer.Elapsed.TotalSeconds, 6)
            input_mib_per_second = if ($timer.Elapsed.TotalSeconds -gt 0) { [Math]::Round(($file.Length / 1MB) / $timer.Elapsed.TotalSeconds, 3) } else { 0 }
            peak_working_set_bytes = $peakWorkingSet
            database_bytes = [int64]$databaseBytes
            stdout_log = [IO.Path]::GetFileName($stdoutPath)
            stderr_log = [IO.Path]::GetFileName($stderrPath)
        })
    }
}

$failures = @($runs | Where-Object { $_.status -eq 'fail' }).Count
$totalSeconds = ($runs | Measure-Object -Property elapsed_seconds -Sum).Sum
$report = [ordered]@{
    schema_version = "nevr-soak-report/v1"
    started_at = $startedAt.ToString("o")
    finished_at = (Get-Date).ToUniversalTime().ToString("o")
    executable = $binary
    executable_sha256 = $binaryHash
    executable_unchanged = ((Get-FileHash -LiteralPath $binary -Algorithm SHA256).Hash.ToLowerInvariant() -ceq $binaryHash)
    replay_root = $replayRoot
    isolated_database = $database
    iterations = $Iterations
    run_timeout_seconds = $RunTimeoutSeconds
    max_working_set_mib = $MaxWorkingSetMiB
    files = $files.Count
    input_bytes_per_iteration = [int64](($files | Measure-Object -Property Length -Sum).Sum)
    process_runs = $runs.Count
    failed_runs = $failures
    total_elapsed_seconds = [Math]::Round([double]$totalSeconds, 6)
    peak_working_set_bytes = [int64](($runs | Measure-Object -Property peak_working_set_bytes -Maximum).Maximum)
    final_database_bytes = if (Test-Path -LiteralPath $database) { [int64](Get-Item -LiteralPath $database).Length } else { 0 }
    inventory = @($inventory)
    runs = @($runs.ToArray())
}
$reportPath = Join-Path $outputRoot "soak-report.json"
$report | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $reportPath -Encoding utf8

Write-Host "Soak report: $reportPath"
Write-Host "Runs: $($runs.Count); failures: $failures; peak working set: $([Math]::Round($report.peak_working_set_bytes / 1MB, 1)) MiB"
if ($failures -gt 0 -or -not $report.executable_unchanged) { exit 1 }
