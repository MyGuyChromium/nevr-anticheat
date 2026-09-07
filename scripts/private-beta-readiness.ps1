#requires -Version 7.2
[CmdletBinding()]
param(
    [string]$DesktopExecutable = "",
    [string]$Installer = "",
    [string]$PackageZip = "",
    [string]$OutputDirectory = "",
    [switch]$SkipSourceTests
)

# Maintainer-only smoke checks. Never run Setup, install an update, launch Spark,
# contact GitHub, or open an existing evidence database from this script.
$ErrorActionPreference = "Stop"
$repoRoot = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot "..")).Path
if (-not $OutputDirectory) { $OutputDirectory = Join-Path $repoRoot "dist\private-beta" }
$outputParent = [IO.Path]::GetFullPath($OutputDirectory)
$runRoot = Join-Path $outputParent (([DateTime]::UtcNow.ToString("yyyyMMddTHHmmssZ")) + "-" + [Guid]::NewGuid().ToString("N").Substring(0, 8))
[IO.Directory]::CreateDirectory($runRoot) | Out-Null
$isolatedRoot = Join-Path ([IO.Path]::GetTempPath()) ("nevr-private-beta-" + [Guid]::NewGuid().ToString("N"))
[IO.Directory]::CreateDirectory($isolatedRoot) | Out-Null
$startedAt = [DateTime]::UtcNow
$script:checks = [Collections.Generic.List[object]]::new()
$script:artifacts = [Collections.Generic.List[object]]::new()
$script:app = $null
$script:appURL = ""
$script:testedVersion = ""
$script:installerVersion = ""
$script:zipDesktopHash = ""
$script:client = [Net.Http.HttpClient]::new()
$script:client.Timeout = [TimeSpan]::FromSeconds(45)
$script:revision = "unknown"
$script:dirty = $null

function Add-Check([string]$ID, [string]$Kind, [string]$Status, [string]$Detail) {
    $script:checks.Add([pscustomobject][ordered]@{ id = $ID; kind = $Kind; status = $Status; detail = $Detail })
    Write-Host "$Status $ID - $Detail"
}

function Run-Check([string]$ID, [scriptblock]$Action) {
    try {
        $detail = & $Action
        Add-Check $ID "automated" "pass" ([string]$detail)
    } catch {
        $detail = $_.Exception.Message.Replace($isolatedRoot, "<isolated-temp>").Replace($repoRoot, "<repo>")
        $detail = [regex]::Replace($detail, 'http://127\.0\.0\.1:\d+/[a-f0-9]{32}/?', '<local-app>')
        Add-Check $ID "automated" "fail" $detail
    }
}

function Assert-Beta([bool]$Condition, [string]$Message) {
    if (-not $Condition) { throw $Message }
}

function Has-Passed([string]$ID) { return @($script:checks | Where-Object { $_.id -eq $ID -and $_.status -eq "pass" }).Count -gt 0 }

function Record-Artifact([string]$Path, [string]$Role) {
    $item = Get-Item -LiteralPath $Path
    $script:artifacts.Add([pscustomobject][ordered]@{
        role = $Role; name = $item.Name; bytes = $item.Length
        sha256 = (Get-FileHash -LiteralPath $item.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
    })
}

function Send-BetaRequest([string]$Method, [string]$Path, [string]$UploadPath = "") {
    $request = [Net.Http.HttpRequestMessage]::new([Net.Http.HttpMethod]::new($Method), $script:appURL + $Path)
    $response = $null
    try {
        if ($UploadPath) {
            $multipart = [Net.Http.MultipartFormDataContent]::new()
            $request.Content = $multipart
            $part = [Net.Http.StreamContent]::new([IO.File]::OpenRead($UploadPath))
            $multipart.Add($part, "files", [IO.Path]::GetFileName($UploadPath))
        } elseif ($Method -eq "POST") {
            $request.Content = [Net.Http.StringContent]::new("{}", [Text.Encoding]::UTF8, "application/json")
        }
        $response = $script:client.SendAsync($request).GetAwaiter().GetResult()
        $raw = $response.Content.ReadAsStringAsync().GetAwaiter().GetResult()
        $body = if ($raw.TrimStart().StartsWith("{")) { $raw | ConvertFrom-Json } else { $null }
        return [pscustomobject]@{ status = [int]$response.StatusCode; body = $body }
    } finally {
        if ($null -ne $response) { $response.Dispose() }
        $request.Dispose()
    }
}

function Start-BetaDesktop([string]$Binary, [string]$Config) {
    $info = [Diagnostics.ProcessStartInfo]::new()
    $info.FileName = $Binary
    $info.WorkingDirectory = $isolatedRoot
    $info.UseShellExecute = $false
    $info.CreateNoWindow = $true
    $info.RedirectStandardOutput = $true
    $info.RedirectStandardError = $true
    foreach ($arg in @("--config", $Config, "--no-browser", "--port", "0")) { $info.ArgumentList.Add($arg) }
    $info.Environment["LOCALAPPDATA"] = Join-Path $isolatedRoot "localappdata"
    $info.Environment["APPDATA"] = Join-Path $isolatedRoot "appdata"
    $info.Environment["NEVR_REPLAY_VIEWER"] = Join-Path $isolatedRoot "missing-viewer.exe"
    $info.Environment.Remove("NEVR_GITHUB_TOKEN") | Out-Null
    $script:app = [Diagnostics.Process]::Start($info)
    $script:stderr = $script:app.StandardError.ReadToEndAsync()
    # The first line identifies the random loopback URL. No URL/token enters
    # the machine-readable report, and the timeout only owns this child.
    $lineTask = $script:app.StandardOutput.ReadLineAsync()
    Assert-Beta ($lineTask.Wait(15000)) "Desktop did not publish its local URL within 15 seconds."
    $line = $lineTask.GetAwaiter().GetResult()
    $match = [regex]::Match([string]$line, 'http://127\.0\.0\.1:\d+/[a-f0-9]{32}/')
    Assert-Beta $match.Success "Desktop startup did not produce an isolated loopback URL."
    $script:appURL = $match.Value
    $script:stdout = $script:app.StandardOutput.ReadToEndAsync()
}

function Stop-BetaDesktop {
    if ($null -eq $script:app) { return }
    if (-not $script:app.HasExited -and $script:appURL) {
        $null = Send-BetaRequest "GET" "quit"
    }
    Assert-Beta ($script:app.WaitForExit(10000)) "Desktop did not shut down within 10 seconds."
    Assert-Beta ($script:app.ExitCode -eq 0) "Desktop exited with code $($script:app.ExitCode)."
    $script:app.Dispose()
    $script:app = $null
    $script:appURL = ""
}

try {
    Push-Location $repoRoot
    try {
        if (Get-Command git -ErrorAction SilentlyContinue) {
            $rev = & git rev-parse HEAD 2>$null
            if ($LASTEXITCODE -eq 0) { $script:revision = ([string]$rev).Trim() }
            $status = & git status --porcelain 2>$null
            if ($LASTEXITCODE -eq 0) { $script:dirty = -not [string]::IsNullOrWhiteSpace(($status -join "")) }
        }

        if ($SkipSourceTests -or -not (Get-Command go -ErrorAction SilentlyContinue)) {
            Add-Check "source_regressions" "automated" "not_run" "Source tests require Go plus a C compiler; source-test execution was unavailable or explicitly skipped."
        } else {
            Run-Check "source_regressions" {
                $pattern = '^(TestDesktop_(AnalyzeFixture|BadUploads|FailureDiagnostics|MatchSummaryDownloads|QoLHealthMaintenanceAndCancel|Quit)|TestDesktop(SingleInstance|Instance|Settings).*|TestApplyPendingRestore.*|TestDownloadVerifiedUpdate|TestInstallUpdateRejectsActiveAnalysis|TestApplyStagedUpdate.*|TestWaitForDesktopExit|TestLaunchUpdateHelper.*|TestGitHubGet.*|TestValidateUpdateHelperPaths|TestSparkReplay.*|TestWatchScan.*|TestRecoveryReports.*|TestSupportBundleRepeated.*)$'
                $logPath = Join-Path $runRoot "source-tests.jsonl"
                & go test -json -count=1 -timeout=3m ./cmd/desktop -run $pattern 2>&1 | ForEach-Object { [string]$_ } | Set-Content -LiteralPath $logPath -Encoding utf8
                $testExit = $LASTEXITCODE
                $events = @(Get-Content -LiteralPath $logPath | ForEach-Object { if ($_.StartsWith("{")) { $_ | ConvertFrom-Json } })
                $passed = @($events | Where-Object { $_.Action -eq "pass" -and $_.Test -and -not $_.Test.Contains("/") } | ForEach-Object { $_.Test })
                foreach ($required in @("TestDesktop_AnalyzeFixture", "TestDesktop_BadUploads", "TestDesktop_MatchSummaryDownloads", "TestApplyPendingRestorePreservesAndReplacesDatabase", "TestApplyPendingRestoreRollsBackEveryMovedFile", "TestSparkReplayClipRequiresExactIncidentFrame", "TestWatchScanRetriesPersistenceFailure", "TestRecoveryReportsPersistenceFailureAndRetainsReplay", "TestDownloadVerifiedUpdate", "TestApplyStagedUpdateRefusesUnverifiedExit", "TestWaitForDesktopExit", "TestDesktopSingleInstanceDatabaseHashCollision")) {
                    Assert-Beta ($passed -contains $required) "Required regression did not pass: $required. See source-tests.jsonl."
                }
                Assert-Beta ($testExit -eq 0) "Source regression tests failed; see source-tests.jsonl."
                "$($passed.Count) regression tests passed: duplicate/malformed uploads, backup/restore, clip frame/content with mocked Spark launch, update integrity, Windows shutdown/refusal and database identity collisions."
            }
        }

        Run-Check "program_snapshot_safety" {
            & (Join-Path $PSScriptRoot "test-windows-snapshots.ps1") -OutputDirectory $runRoot
            $snapshotReport = Get-Content -Raw -LiteralPath (Join-Path $runRoot "windows-snapshot-tests.json") | ConvertFrom-Json
            Assert-Beta ($snapshotReport.failed -eq 0 -and $snapshotReport.passed -ge 13) "Program snapshot/rollback safety regressions failed."
            "$($snapshotReport.passed) isolated program snapshot, corruption, traversal, process-scope and partial-failure checks passed; no installer or real executable launched."
        }

        Run-Check "desktop_binary" {
            if (-not $DesktopExecutable) {
                Assert-Beta ([bool](Get-Command go -ErrorAction SilentlyContinue)) "Go is unavailable; supply -DesktopExecutable with the trusted beta executable."
                $script:binary = Join-Path $isolatedRoot "nevr-desktop.exe"
                & go build -o $script:binary ./cmd/desktop 2>&1 | Set-Content -LiteralPath (Join-Path $runRoot "build.log") -Encoding utf8
                Assert-Beta ($LASTEXITCODE -eq 0) "Desktop build failed; see build.log."
            } else {
                $script:binary = (Resolve-Path -LiteralPath $DesktopExecutable).Path
            }
            Record-Artifact $script:binary "tested_desktop"
            "Executable SHA-256 recorded; supplied binaries are tested as supplied, source-built binaries reflect the recorded working-tree state."
        }
        if ((Has-Passed "desktop_binary") -and (Get-Command go -ErrorAction SilentlyContinue)) {
            Run-Check "binary_build_provenance" {
                $info = & go version -m $script:binary 2>&1
                Assert-Beta ($LASTEXITCODE -eq 0) "Could not inspect desktop Go build metadata."
                $text = $info -join "`n"
                $revisionMatch = [regex]::Match($text, 'vcs\.revision=([a-f0-9]{40,64})')
                $dirtyMatch = [regex]::Match($text, 'vcs\.modified=(true|false)')
                Assert-Beta ($revisionMatch.Success -and $dirtyMatch.Success) "Desktop has no complete embedded VCS provenance; build with VCS stamping enabled."
                $artifact = $script:artifacts | Where-Object { $_.role -eq "tested_desktop" }
                $artifact | Add-Member -NotePropertyName source_revision -NotePropertyValue $revisionMatch.Groups[1].Value
                $artifact | Add-Member -NotePropertyName source_dirty -NotePropertyValue ($dirtyMatch.Groups[1].Value -eq "true")
                $embedded = [regex]::Match($text, 'main\.buildCommit=([a-zA-Z0-9_-]+)')
                $embeddedCommit = if ($embedded.Success) { $embedded.Groups[1].Value } else { "development" }
                $artifact | Add-Member -NotePropertyName embedded_build_commit -NotePropertyValue $embeddedCommit
                Assert-Beta (-not $artifact.source_dirty -or $embeddedCommit -eq "development") "Dirty binary advertises a clean Git revision; rebuild with the current private-candidate packaging script."
                "Embedded source dirty=$($artifact.source_dirty); build commit=$embeddedCommit. Dirty candidates cannot establish clean-release provenance."
            }
        } else { Add-Check "binary_build_provenance" "automated" "not_run" "Go or the test executable is unavailable; embedded build identity was not inspected." }
    } finally { Pop-Location }

    foreach ($artifact in @(@{ path = $Installer; role = "installer" }, @{ path = $PackageZip; role = "portable_zip" })) {
        if (-not $artifact.path) {
            Add-Check ($artifact.role + "_inventory") "automated" "not_run" "Artifact not supplied; its identity and contents were not checked."
            continue
        }
        Run-Check ($artifact.role + "_inventory") {
            $resolved = (Resolve-Path -LiteralPath $artifact.path).Path
            Record-Artifact $resolved $artifact.role
            if ($artifact.role -eq "installer") {
                $script:installerVersion = ([string][Diagnostics.FileVersionInfo]::GetVersionInfo($resolved).ProductVersion).Trim()
                $signature = Get-AuthenticodeSignature -LiteralPath $resolved
                $script:artifacts[$script:artifacts.Count - 1] | Add-Member -NotePropertyName signature_status -NotePropertyValue ([string]$signature.Status)
                $script:artifacts[$script:artifacts.Count - 1] | Add-Member -NotePropertyName product_version -NotePropertyValue $script:installerVersion
                $sidecar = $resolved + ".sha256"
                Assert-Beta (Test-Path -LiteralPath $sidecar -PathType Leaf) "Installer checksum sidecar is missing."
                $expected = ((Get-Content -Raw -LiteralPath $sidecar).Trim() -split '\s+')[0]
                Assert-Beta ($expected -match '^[a-fA-F0-9]{64}$' -and $expected -eq $script:artifacts[$script:artifacts.Count - 1].sha256) "Installer does not match its checksum sidecar."
                "SHA-256 matches sidecar; Authenticode status recorded as $($signature.Status). Setup was not run; hash agreement is not publisher verification."
            } else {
                $zip = [IO.Compression.ZipFile]::OpenRead($resolved)
                try {
                    $desktops = @($zip.Entries | Where-Object { [IO.Path]::GetFileName($_.FullName) -eq "nevr-desktop.exe" })
                    Assert-Beta ($desktops.Count -eq 1) "Portable ZIP must have exactly one desktop executable."
                    $prefix = $desktops[0].FullName.Substring(0, $desktops[0].FullName.Length - "nevr-desktop.exe".Length)
                    foreach ($required in @("nevr-desktop.exe", "nevr-ac.exe", "nevr-server.exe", "nevr-bridge.exe", "nevr-compat.exe", "configs/default.toml", "configs/shadow_deploy.toml", "README-WINDOWS.txt", "installer/Rollback-NEVR.cmd", "installer/Rollback-NEVR.ps1", "installer/Program-Snapshot.ps1")) {
                        $entry = $zip.GetEntry($prefix + $required)
                        Assert-Beta ($null -ne $entry -and $entry.Length -gt 0) "Portable ZIP is missing or has an empty $required."
                    }
                    $desktopStream = $desktops[0].Open()
                    $hasher = [Security.Cryptography.SHA256]::Create()
                    try { $script:zipDesktopHash = [Convert]::ToHexString($hasher.ComputeHash($desktopStream)).ToLowerInvariant() }
                    finally { $desktopStream.Dispose(); $hasher.Dispose() }
                    $script:artifacts[$script:artifacts.Count - 1] | Add-Member -NotePropertyName desktop_sha256 -NotePropertyValue $script:zipDesktopHash
                } finally { $zip.Dispose() }
                "Five executables, configurations, instructions and rollback tools present; archive was inspected without extraction or execution."
            }
        }
    }

    if (Has-Passed "desktop_binary") {
        $dbPath = Join-Path $isolatedRoot "beta.db"
        $configPath = Join-Path $isolatedRoot "beta.toml"
        $config = "[general]`ndb_path = " + (ConvertTo-Json -InputObject $dbPath -Compress) + "`nlog_level = 'error'`n"
        [IO.File]::WriteAllText($configPath, $config)
        [IO.File]::WriteAllText((Join-Path $isolatedRoot "nevr-desktop-settings.json"), '{"watch_enabled":false,"automatic_update_checks":false,"seen_files":{}}')
        $fixture = Join-Path $repoRoot "tests\fixtures\synthetic_session.echoreplay"
        $corrupt = Join-Path $isolatedRoot "corrupt.echoreplay"
        [IO.File]::WriteAllText($corrupt, "This is deliberately invalid replay data.")
        Run-Check "isolated_startup" {
            Start-BetaDesktop $script:binary $configPath
            $health = Send-BetaRequest "GET" "api/health"
            Assert-Beta ($health.status -eq 200 -and [IO.Path]::GetFullPath($health.body.database_path) -eq $dbPath -and $health.body.stored_matches -eq 0) "Desktop did not open the empty isolated database."
            $script:testedVersion = [string]$health.body.version
            "Desktop $($script:testedVersion) opened an empty temporary database on loopback without a browser."
        }
        if (Has-Passed "isolated_startup") {
            Run-Check "fixture_import" {
                $reply = Send-BetaRequest "POST" "api/analyze" $fixture
                Assert-Beta ($reply.status -eq 200 -and $reply.body.results.Count -eq 1 -and $reply.body.results[0].ok -and $reply.body.results[0].match_id -eq "SYN-FIXTURE-001") "Synthetic fixture import failed."
                "Synthetic replay imported successfully. This is a pipeline check, not detector calibration."
            }
            if (Has-Passed "fixture_import") {
                Run-Check "duplicate_import" {
                    $reply = Send-BetaRequest "POST" "api/analyze" $fixture
                    $history = Send-BetaRequest "GET" "api/matches"
                    Assert-Beta ($reply.status -eq 200 -and $reply.body.results[0].ok -and -not $reply.body.results[0].error -and $reply.body.results[0].match.replaced -and $history.status -eq 200 -and $history.body.matches.Count -eq 1) "Reimport did not refresh the existing match without an error/duplicate."
                    "The identical replay was accepted again, refreshed and retained as one stored match."
                }
                Run-Check "corrupt_upload_isolation" {
                    $reply = Send-BetaRequest "POST" "api/analyze" $corrupt
                    $health = Send-BetaRequest "GET" "api/health"
                    Assert-Beta ($reply.status -eq 200 -and $reply.body.results.Count -eq 1 -and -not $reply.body.results[0].ok -and [bool]$reply.body.results[0].error) "Corrupt replay was not reported as a per-file failure."
                    Assert-Beta ($health.status -eq 200 -and $health.body.stored_matches -eq 1) "Corrupt upload damaged the existing match or stopped the app."
                    "Corrupt file received a per-file error; the app and previously imported match remained usable."
                }
                Run-Check "database_backup" {
                    $reply = Send-BetaRequest "POST" "api/maintenance/backup"
                    Assert-Beta ($reply.status -eq 200 -and $reply.body.ok -and $reply.body.bytes -gt 0) "Database backup failed."
                    $backup = [IO.Path]::GetFullPath([string]$reply.body.path)
                    Assert-Beta ($backup.StartsWith($isolatedRoot + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) "Backup escaped the isolated temporary directory."
                    Assert-Beta (Test-Path -LiteralPath $backup -PathType Leaf) "Backup file is missing."
                    "Backup created inside the isolated directory; live evidence was never opened."
                }
                Run-Check "restart_preserves_evidence" {
                    Stop-BetaDesktop
                    Start-BetaDesktop $script:binary $configPath
                    $match = Send-BetaRequest "GET" "api/match/SYN-FIXTURE-001"
                    $health = Send-BetaRequest "GET" "api/health"
                    Assert-Beta ($match.status -eq 200 -and $match.body.players.Count -eq 4 -and $match.body.frames_processed -eq 120 -and $health.status -eq 200 -and $health.body.stored_matches -eq 1) "Stored replay did not survive a clean process restart."
                    "One match, four players and 120 frames survived a clean desktop shutdown/restart."
                }
            } else {
                foreach ($id in @("duplicate_import", "corrupt_upload_isolation", "database_backup", "restart_preserves_evidence")) { Add-Check $id "automated" "not_run" "Fixture import failed; dependent check could not run." }
            }
            Run-Check "clean_shutdown" { Stop-BetaDesktop; "Owned desktop process stopped with exit code zero." }
        } else {
            foreach ($id in @("fixture_import", "duplicate_import", "corrupt_upload_isolation", "database_backup", "restart_preserves_evidence", "clean_shutdown")) { Add-Check $id "automated" "not_run" "Isolated desktop startup failed." }
        }
    } else {
        Add-Check "isolated_desktop_smoke" "automated" "not_run" "No test executable was available."
    }
    if ($script:installerVersion -and $script:testedVersion) {
        Run-Check "installer_version_alignment" {
            Assert-Beta ($script:installerVersion -eq $script:testedVersion) "Installer version $($script:installerVersion) differs from tested desktop $($script:testedVersion); rebuild/select one candidate before beta distribution."
            "Installer metadata and running desktop report the same version; this does not by itself establish identical payload bytes."
        }
    } else { Add-Check "installer_version_alignment" "automated" "not_run" "Installer metadata or running desktop version was unavailable." }
    if ($DesktopExecutable -and $script:zipDesktopHash -and (Has-Passed "desktop_binary")) {
        Run-Check "candidate_binary_binding" {
            $testedHash = ($script:artifacts | Where-Object { $_.role -eq "tested_desktop" }).sha256
            Assert-Beta ($testedHash -eq $script:zipDesktopHash) "Tested executable differs from the desktop inside the supplied ZIP."
            "The real-process smoke check used the exact desktop bytes present in the candidate ZIP."
        }
    } else { Add-Check "candidate_binary_binding" "automated" "not_run" "Supply -DesktopExecutable extracted from the candidate -PackageZip to prove the smoke-tested executable is the distributed payload." }
} catch {
    Add-Check "runner" "automated" "fail" $_.Exception.Message.Replace($isolatedRoot, "<isolated-temp>").Replace($repoRoot, "<repo>")
} finally {
    if ($null -ne $script:app) {
        if (-not $script:app.HasExited) { $script:app.Kill(); $null = $script:app.WaitForExit(10000) }
        $script:app.Dispose()
    }
    $script:client.Dispose()
    foreach ($manual in @(
        @{ id = "second_pc_install"; detail = "Invited tester must download the exact hashed private beta and verify shortcuts/startup on a separate Windows account or PC." },
        @{ id = "installer_update_preservation"; detail = "Use disposable Windows account/VM to install older beta, seed synthetic evidence and settings, then install candidate; record before/after. Runner never executes Setup." },
        @{ id = "one_click_update"; detail = "Private authenticated release download, real installer handoff and relaunch require a disposable installed beta and two published private builds; source tests cover failure paths only." },
        @{ id = "spark_exact_clip"; detail = "Invited tester must compare player/frame/time in real Spark Replay Viewer. Source tests verify generated content/window using a mocked viewer launch." },
        @{ id = "publisher_and_smartscreen"; detail = "Record publisher identity and observed Windows prompts on the second PC. A matching hash does not imply a trusted signature or SmartScreen reputation." }
    )) { Add-Check $manual.id "manual" "not_run" $manual.detail }
    $failed = @($script:checks | Where-Object { $_.status -eq "fail" }).Count
    $missing = @($script:checks | Where-Object { $_.kind -eq "automated" -and $_.status -eq "not_run" }).Count
    $report = [ordered]@{
        schema_version = "nevr-private-beta-readiness/v1"
        started_at = $startedAt.ToString("o"); finished_at = [DateTime]::UtcNow.ToString("o")
        distribution = "private_invited_beta"; source_revision = $script:revision; source_dirty = $script:dirty
        tested_version = $script:testedVersion
        tested_binary_origin = if ($DesktopExecutable) { "supplied_binary" } else { "source_working_tree" }
        platform = [Environment]::OSVersion.VersionString; process_architecture = [Runtime.InteropServices.RuntimeInformation]::ProcessArchitecture.ToString()
        automated_status = if ($failed) { "fail" } elseif ($missing) { "incomplete" } else { "pass" }
        beta_status = if ($failed) { "fail" } else { "manual_validation_pending" }
        artifacts = @($script:artifacts); checks = @($script:checks)
        limits = @("Synthetic fixtures do not establish cheat detection accuracy or final thresholds.", "No installed app, live database, real installer, GitHub authentication or Spark process was used.")
    }
    $reportPath = Join-Path $runRoot "readiness-report.json"
    $report | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $reportPath -Encoding utf8
    # Only remove the exact unique scratch directory created above, after a
    # fresh absolute-path containment check. Reports and artifact inputs stay.
    $resolvedScratch = [IO.Path]::GetFullPath($isolatedRoot)
    $tempPrefix = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd([IO.Path]::DirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
    if ($resolvedScratch.StartsWith($tempPrefix, [StringComparison]::OrdinalIgnoreCase) -and [IO.Path]::GetFileName($resolvedScratch).StartsWith("nevr-private-beta-")) {
        Remove-Item -LiteralPath $resolvedScratch -Recurse -Force
    }
    Write-Host "Private beta report: $reportPath"
}
if ($failed -gt 0) { exit 1 }
if ($missing -gt 0) { exit 2 }
exit 0
