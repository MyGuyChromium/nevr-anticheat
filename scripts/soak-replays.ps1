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
    [switch]$HashInputs
)

$ErrorActionPreference = "Stop"
$replayRoot = (Resolve-Path -LiteralPath $ReplayDirectory).Path
$binary = (Resolve-Path -LiteralPath $Executable).Path
if (-not $OutputDirectory) {
    $OutputDirectory = Join-Path $PWD ("dist\soak-" + (Get-Date).ToUniversalTime().ToString("yyyyMMddTHHmmssZ"))
}
$outputRoot = [IO.Path]::GetFullPath($OutputDirectory)
New-Item -ItemType Directory -Path $outputRoot -Force | Out-Null

$files = @(Get-ChildItem -LiteralPath $replayRoot -Recurse -File -Filter "*.echoreplay" | Sort-Object FullName)
if ($MaxFiles -gt 0) { $files = @($files | Select-Object -First $MaxFiles) }
if ($files.Count -eq 0) { throw "No .echoreplay files were found under $replayRoot" }

$database = Join-Path $outputRoot "soak.db"
$config = Join-Path $outputRoot "soak.toml"
$escapedDatabase = $database.Replace("'", "''")
@"
[general]
db_path = '$escapedDatabase'
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
        $process = Start-Process -FilePath $binary -ArgumentList @(
            "--config", ('"' + $config + '"'), "analyze", ('"' + $file.FullName + '"'), "--force"
        ) -RedirectStandardOutput $stdoutPath -RedirectStandardError $stderrPath -PassThru -WindowStyle Hidden
        $peakWorkingSet = [int64]0
        do {
            try {
                $process.Refresh()
                $peakWorkingSet = [Math]::Max($peakWorkingSet, [int64]$process.WorkingSet64)
                $peakWorkingSet = [Math]::Max($peakWorkingSet, [int64]$process.PeakWorkingSet64)
            } catch { }
            if (-not $process.HasExited) { Start-Sleep -Milliseconds 100 }
        } while (-not $process.HasExited)
        $process.WaitForExit()
        $timer.Stop()
        $databaseBytes = if (Test-Path -LiteralPath $database) { (Get-Item -LiteralPath $database).Length } else { 0 }
        $runs.Add([pscustomobject][ordered]@{
            iteration = $iteration
            replay = $file.FullName
            input_bytes = $file.Length
            exit_code = $process.ExitCode
            elapsed_seconds = [Math]::Round($timer.Elapsed.TotalSeconds, 6)
            input_mib_per_second = if ($timer.Elapsed.TotalSeconds -gt 0) { [Math]::Round(($file.Length / 1MB) / $timer.Elapsed.TotalSeconds, 3) } else { 0 }
            peak_working_set_bytes = $peakWorkingSet
            database_bytes = [int64]$databaseBytes
            stdout_log = [IO.Path]::GetFileName($stdoutPath)
            stderr_log = [IO.Path]::GetFileName($stderrPath)
        })
    }
}

$failures = @($runs | Where-Object { $_.exit_code -ne 0 }).Count
$totalSeconds = ($runs | Measure-Object -Property elapsed_seconds -Sum).Sum
$report = [ordered]@{
    schema_version = "nevr-soak-report/v1"
    started_at = $startedAt.ToString("o")
    finished_at = (Get-Date).ToUniversalTime().ToString("o")
    executable = $binary
    replay_root = $replayRoot
    isolated_database = $database
    iterations = $Iterations
    files = $files.Count
    input_bytes_per_iteration = [int64](($files | Measure-Object -Property Length -Sum).Sum)
    process_runs = $runs.Count
    failed_runs = $failures
    total_elapsed_seconds = [Math]::Round([double]$totalSeconds, 6)
    peak_working_set_bytes = [int64](($runs | Measure-Object -Property peak_working_set_bytes -Maximum).Maximum)
    final_database_bytes = if (Test-Path -LiteralPath $database) { [int64](Get-Item -LiteralPath $database).Length } else { 0 }
    inventory = @($inventory)
    runs = @($runs)
}
$reportPath = Join-Path $outputRoot "soak-report.json"
$report | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $reportPath -Encoding utf8

Write-Host "Soak report: $reportPath"
Write-Host "Runs: $($runs.Count); failures: $failures; peak working set: $([Math]::Round($report.peak_working_set_bytes / 1MB, 1)) MiB"
if ($failures -gt 0) { exit 1 }
