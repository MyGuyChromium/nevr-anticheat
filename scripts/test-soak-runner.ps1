#requires -Version 7.2
[CmdletBinding()]
param([string]$OutputDirectory = '')

$ErrorActionPreference = 'Stop'
$testRoot = [IO.Path]::GetFullPath((Join-Path ([IO.Path]::GetTempPath()) ('nevr-soak-tests-' + [Guid]::NewGuid().ToString('N'))))
[IO.Directory]::CreateDirectory($testRoot) | Out-Null
$oldRoot, $oldMode = $env:NEVR_SOAK_TEST_ROOT, $env:NEVR_SOAK_TEST_MODE
$checks = [Collections.Generic.List[object]]::new()
function Assert-SoakTest([bool]$Condition, [string]$Message) { if (-not $Condition) { throw $Message } }
try {
    $env:NEVR_SOAK_TEST_ROOT = $testRoot
    $helper = Join-Path $testRoot 'soak-helper.exe'
    & go build -o $helper (Join-Path $PSScriptRoot 'testdata\soak-helper.go')
    Assert-SoakTest ($LASTEXITCODE -eq 0) 'Synthetic helper failed to build.'
    $replays = Join-Path $testRoot 'synthetic'
    [IO.Directory]::CreateDirectory($replays) | Out-Null
    [IO.File]::WriteAllText((Join-Path $replays 'fixture.echoreplay'), 'synthetic runner input; not an Echo recording')
    $output = Join-Path $testRoot "O'Brien test runs"
    [IO.Directory]::CreateDirectory($output) | Out-Null
    $sentinel = Join-Path $output 'soak.db'
    [IO.File]::WriteAllText($sentinel, 'existing file must not be opened')
    foreach ($mode in @('success', 'success', 'hang', 'fail', 'memory')) {
        $env:NEVR_SOAK_TEST_MODE = if ($mode -eq 'memory') { 'hang' } else { $mode }
        $before = @(Get-ChildItem -LiteralPath $output -Directory).FullName
        $memoryLimit = if ($mode -eq 'memory') { 1 } else { 0 }
        & (Join-Path $PSScriptRoot 'soak-replays.ps1') -ReplayDirectory $replays -Executable $helper -OutputDirectory $output -Iterations 1 -RunTimeoutSeconds 1 -MaxWorkingSetMiB $memoryLimit -HashInputs
        $exitCode = $LASTEXITCODE
        $after = @(Get-ChildItem -LiteralPath $output -Directory | Where-Object { $_.FullName -notin $before })
        Assert-SoakTest ($after.Count -eq 1) 'Runner did not create exactly one fresh output directory.'
        $report = Get-Content -Raw -LiteralPath (Join-Path $after[0].FullName 'soak-report.json') | ConvertFrom-Json
        Assert-SoakTest ($report.process_runs -eq 1 -and $report.executable_unchanged) 'Runner report did not bind the tested executable.'
        if ($mode -eq 'success') {
            Assert-SoakTest ($exitCode -eq 0 -and $report.failed_runs -eq 0) 'Synthetic success failed (including quoted configuration path).'
            Assert-SoakTest ([IO.File]::ReadAllText($report.isolated_database) -eq 'synthetic runner marker, not a database') 'Wrong isolated database path.'
        } else {
            $expected = switch ($mode) { 'hang' { 'timeout' }; 'fail' { 'analysis_failed' }; 'memory' { 'memory_limit' } }
            Assert-SoakTest ($exitCode -eq 1 -and $report.failed_runs -eq 1 -and $report.runs[0].failure -eq $expected) "Runner did not report $expected correctly."
        }
        Assert-SoakTest ([IO.File]::ReadAllText($sentinel) -eq 'existing file must not be opened') 'Runner overwrote existing soak.db.'
        $checks.Add([pscustomobject]@{ mode = $mode; status = 'pass'; child_failure = $report.runs[0].failure })
        Write-Host "PASS soak runner $mode"
    }
    $restrictedRoot = Join-Path $testRoot 'restricted-root'
    [IO.Directory]::CreateDirectory($restrictedRoot) | Out-Null
    $escapedConfig = Join-Path $testRoot 'escaped-config.toml'
    $escapedMarker = Join-Path $restrictedRoot 'unexpected.db'
    [IO.File]::WriteAllText($escapedConfig, 'db_path = ' + (ConvertTo-Json -InputObject $escapedMarker -Compress))
    $env:NEVR_SOAK_TEST_ROOT, $env:NEVR_SOAK_TEST_MODE = $restrictedRoot, 'success'
    foreach ($config in @($escapedConfig, (Join-Path '..' 'escaped-config.toml'))) {
        & $helper --config $config 2>$null | Out-Null
        Assert-SoakTest ($LASTEXITCODE -ne 0) 'Synthetic helper accepted a configuration outside its isolated root.'
        Assert-SoakTest (-not (Test-Path -LiteralPath $escapedMarker)) 'Synthetic helper read an escaped configuration.'
    }
    $checks.Add([pscustomobject]@{ mode = 'escaped_config'; status = 'pass'; child_failure = $null })
    Write-Host 'PASS soak runner escaped_config'
    $result = [ordered]@{ schema_version = 'nevr-soak-runner-tests/v1'; scope = 'Synthetic helper process; no installer, user database, replay, or network used.'; checks = @($checks.ToArray()); passed = $checks.Count }
    if ($OutputDirectory) {
        [IO.Directory]::CreateDirectory([IO.Path]::GetFullPath($OutputDirectory)) | Out-Null
        $result | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath (Join-Path $OutputDirectory 'soak-runner-tests.json') -Encoding UTF8
    }
} finally {
    $env:NEVR_SOAK_TEST_ROOT, $env:NEVR_SOAK_TEST_MODE = $oldRoot, $oldMode
    $expectedPrefix = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\', '/') + [IO.Path]::DirectorySeparatorChar + 'nevr-soak-tests-'
    if (-not $testRoot.StartsWith($expectedPrefix, [StringComparison]::OrdinalIgnoreCase)) { throw 'Unsafe soak test cleanup path.' }
    Remove-Item -LiteralPath $testRoot -Recurse -Force
}
