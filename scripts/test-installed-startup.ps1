#requires -Version 7.2
[CmdletBinding()]
param([Parameter(Mandatory = $true)][string]$InstallRoot)

# This check opens the real installed database. Only the disposable hosted
# release runner may invoke it, immediately after a fresh silent installation.
$ErrorActionPreference = 'Stop'
if ($env:GITHUB_ACTIONS -ne 'true' -or $env:RUNNER_ENVIRONMENT -ne 'github-hosted' -or $env:RUNNER_OS -ne 'Windows') {
    throw 'Installed startup smoke requires a disposable GitHub-hosted Windows runner.'
}
$repoRoot = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '..')).Path
$installPath = (Resolve-Path -LiteralPath $InstallRoot).Path
$defaultInstallPath = [IO.Path]::GetFullPath((Join-Path $env:LOCALAPPDATA 'Programs\NEVR-Anticheat'))
if ($installPath -ne $defaultInstallPath) { throw 'Smoke must use the default installed application directory.' }
$binary = Join-Path $installPath 'nevr-desktop.exe'
$configPath = Join-Path $installPath 'installed.toml'
$dataRoot = Join-Path $env:LOCALAPPDATA 'NEVR-Anticheat'
$dbPath = Join-Path $dataRoot 'nevr-anticheat.db'
foreach ($path in @($binary, $configPath)) {
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw "Installed file is missing: $path" }
}
if (-not (Test-Path -LiteralPath $dataRoot -PathType Container)) {
    throw 'Fresh installer did not create the default evidence directory before first launch.'
}
if (@(Get-ChildItem -LiteralPath $dataRoot -Force).Count -ne 0) {
    throw 'First-launch smoke requires the installer-created evidence directory to be empty.'
}
$configHash = (Get-FileHash -LiteralPath $configPath -Algorithm SHA256).Hash
$expectedVersions = @([regex]::Matches(
    (Get-Content -Raw -LiteralPath (Join-Path $repoRoot 'internal\storage\sqlite\migrations.go')),
    '(?m)^\s*Version:\s*(\d+),'
) | ForEach-Object { [int]$_.Groups[1].Value } | Sort-Object)
if ($expectedVersions.Count -eq 0) { throw 'Could not determine expected schema migrations.' }
$expectedVersion = $expectedVersions[-1]
$app = $null
$stderr = $null
$failure = $null
$handler = [Net.Http.HttpClientHandler]::new()
$handler.UseProxy = $false
$handler.AllowAutoRedirect = $false
$client = [Net.Http.HttpClient]::new($handler)
$client.Timeout = [TimeSpan]::FromSeconds(10)

function Read-InstalledPage([string]$Url) {
    $response = $client.GetAsync($Url).GetAwaiter().GetResult()
    try {
        if ([int]$response.StatusCode -ne 200) { throw "Installed app returned HTTP $([int]$response.StatusCode)." }
        return $response.Content.ReadAsStringAsync().GetAwaiter().GetResult()
    } finally { $response.Dispose() }
}

try {
    foreach ($launch in 1..2) {
        $info = [Diagnostics.ProcessStartInfo]::new()
        $info.FileName = $binary
        $info.WorkingDirectory = $installPath
        $info.UseShellExecute = $false
        $info.CreateNoWindow = $true
        $info.RedirectStandardOutput = $true
        $info.RedirectStandardError = $true
        foreach ($arg in @('--config', $configPath, '--no-browser', '--port', '0')) { $info.ArgumentList.Add($arg) }
        $info.Environment.Remove('NEVR_GITHUB_TOKEN') | Out-Null
        $app = [Diagnostics.Process]::Start($info)
        $stderr = $app.StandardError.ReadToEndAsync()
        $lineTask = $app.StandardOutput.ReadLineAsync()
        if (-not $lineTask.Wait(30000)) { throw 'Installed desktop did not publish its URL within 30 seconds.' }
        $line = [string]$lineTask.GetAwaiter().GetResult()
        # The already-running handoff line is deliberately rejected: the HTTP
        # server and quit request must belong to the process started by this test.
        $urlMatch = [regex]::Match($line, '^NEVR-Anticheat desktop: open (http://127\.0\.0\.1:\d+/[a-f0-9]{32}/) \(press Ctrl\+C to quit\)$')
        if (-not $urlMatch.Success -or $app.HasExited) { throw 'Installed desktop failed before serving its own loopback URL.' }
        $appUrl = $urlMatch.Groups[1].Value
        $stdout = $app.StandardOutput.ReadToEndAsync()
        $listener = @(Get-NetTCPConnection -LocalAddress '127.0.0.1' -LocalPort ([uri]$appUrl).Port -State Listen)
        if ($listener.Count -ne 1 -or $listener[0].OwningProcess -ne $app.Id) {
            throw 'Installed desktop URL does not belong to the test process.'
        }

        $health = Read-InstalledPage ($appUrl + 'api/health') | ConvertFrom-Json
        if ([IO.Path]::GetFullPath([string]$health.database_path) -ne $dbPath) { throw 'Installed desktop opened the wrong evidence database.' }
        if ($health.stored_matches -ne 0 -or $health.schema_version -ne $expectedVersion -or $health.database_bytes -le 0) {
            throw 'Installed desktop did not report an empty, initialized database at the expected schema version.'
        }
        $page = Read-InstalledPage $appUrl
        if ($page -notmatch '(?i)<!doctype html' -or $page -notmatch 'NEVR-Anticheat') {
            throw 'Installed desktop did not serve the application HTML.'
        }
        if (-not (Test-Path -LiteralPath $dbPath -PathType Leaf)) { throw 'Installed database file was not created.' }
        if ((Get-FileHash -LiteralPath $configPath -Algorithm SHA256).Hash -ne $configHash) {
            throw 'First launch changed the installed configuration.'
        }
        if ($app.HasExited) { throw 'Installed desktop exited before the shutdown request.' }
        $null = Read-InstalledPage ($appUrl + 'quit')
        if (-not $app.WaitForExit(10000)) { throw 'Installed desktop did not shut down within 10 seconds.' }
        if ($app.ExitCode -ne 0) { throw "Installed desktop exited with code $($app.ExitCode)." }

        # NewStore completes and verifies every migration before publishing the
        # URL. Reaching health and serving its DB queries checks that startup path.
        Write-Host "PASS installed launch ${launch}: desktop $($health.version), schema $expectedVersion, app HTML, exact default database and clean shutdown."
        $app.Dispose()
        $app = $null
    }
} catch {
    $failure = $_
} finally {
    if ($null -ne $app) {
        # Never locate/terminate applications by name. This Process owns only
        # the child created above; a forced stop is failure cleanup, not a pass.
        if (-not $app.HasExited) {
            $app.Kill()
            if (-not $app.WaitForExit(10000)) { Write-Warning 'Owned installed desktop did not exit after cleanup.' }
        }
        if ($null -ne $stderr -and $stderr.Wait(1000)) {
            $diagnostics = $stderr.GetAwaiter().GetResult()
            $diagnostics = [regex]::Replace($diagnostics, 'http://127\.0\.0\.1:\d+/[a-f0-9]{32}/?', '<local-app>')
            if ($failure -and $diagnostics) { Write-Warning "Installed desktop stderr: $diagnostics" }
        }
        $app.Dispose()
    }
    $client.Dispose()
}
if ($failure) { throw $failure }
