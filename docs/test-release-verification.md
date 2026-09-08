# Controlled review-only test release verification

Use the existing Windows package workflow's **NEVR-Anticheat-Windows** artifact from the exact candidate run. Keep the repository private. Do not run the installer on a maintainer's working account merely to verify it, and do not dispatch the workflow on `master` or a version tag for a routine PR: those successful runs can publish the rolling release. A PR/codex-branch artifact does not require publication.

The single local entrypoint reuses the existing readiness, snapshot and CLI workload tools:

```powershell
$env:GOTOOLCHAIN = 'go1.26.6'
pwsh -NoProfile -File scripts/verify-test-release.ps1 `
  -PackageZip 'C:\candidate\NEVR-Anticheat-Windows-x64.zip' `
  -Installer 'C:\candidate\NEVR-Anticheat-Setup.exe' `
  -ExpectedCommit '<actual 40-character artifact build commit>' `
  -ReplayDirectory 'C:\explicitly-selected-private-replays' `
  -Iterations 3 -RunTimeoutSeconds 600 -MaxWorkingSetMiB 2048
```

Both artifacts require their original, filename-bound `.sha256` sidecars. `ExpectedCommit` is mandatory: for a pull-request workflow it can be the checkout **merge SHA**, not the PR branch head. Obtain it from that run's manifest/build evidence; do not substitute the current working-tree SHA. The verifier rejects dirty builds, a revision mismatch, mixed ZIP roots, ambiguous/traversing/linked members, or a checksum mismatch before executing candidate code. A checksum proves consistency with the supplied sidecar, not a trusted publisher: obtain the whole candidate from the known private workflow, not an unknown download.

The verifier does not build replacement executables. It extracts only the desktop and CLI from the supplied ZIP into a uniquely owned temporary directory. It checks embedded VCS identity, observes the desktop's runtime build identity, and binds the real-process smoke/workload results to the exact extracted hashes. Setup is inventoried, never executed locally.

Existing readiness checks run the supplied desktop against an isolated database, including first launch, original/duplicate/corrupt synthetic imports, backup, restart and shutdown. The backup test now restores through the production restore API and startup path: a pre-backup note returns, a later note disappears from the restored database, the replay survives, and both notes remain readable in the preserved pre-restore database. It never opens installed user data.

The CLI workload uses the caller-selected replay directory, explicit per-run time/memory limits, a fresh database and hashes of inputs. Its report includes actual process counts, durations, peak working set and database growth. Repeated CLI processes are **not** a long-lived desktop soak; record desktop responsiveness, memory and database behavior separately. Neither test establishes independent detector accuracy or final thresholds.

To also run the timed desktop workload within this entrypoint, invoke the script from PowerShell with `-ReplayFiles @('C:\selected\first.echoreplay', 'C:\selected\second.echoreplay')`. It passes those exact paths through a private JSON manifest to `test-desktop-release-workload.ps1`; the default minimum desktop duration is 120 seconds (`-DesktopMinDurationSeconds` changes it). Without explicit `ReplayFiles`, the desktop soak remains `NOT TESTED`. Child processes have session, bridge and service credential environment variables removed; the parent environment is unchanged.

Outputs are written beneath ignored `dist/test-release-verification/` by default. The report uses `PASS`, `FAIL`, `BLOCKED` and `NOT TESTED`; skipped source tests stay untested and cause an incomplete automated result. Exit codes are 0 for automated pass, 1 for failure and 2 for incomplete prerequisites/checks. Manual acceptance remains `BLOCKED`, even when automated checks pass. Private workload reports contain replay paths and must not be committed.

Still record separately:

- The disposable Windows CI installer smoke result and hashes proving its installed executables match the packaged ZIP. Matching version strings alone do not establish payload identity.
- Real previous-version update/relaunch and settings/evidence preservation in a disposable account/VM. The existing CI same-candidate reinstall is not a two-version update test.
- Exact incident player/frame/time in actual Spark Replay Viewer, and a separate Windows account/PC startup check.
- Actual signing/SmartScreen observations. Unsigned artifacts are not trusted-publisher releases; Defender availability/skips must be reported honestly.
- Long-lived desktop behavior and independently labeled detector acceptance evidence. No automatic enforcement is authorized by this verifier.

Verifier contract regressions (synthetic bytes only):

```powershell
pwsh -NoProfile -File scripts/test-test-release-verifier.ps1
```

Do not bypass `test-installed-startup.ps1`'s disposable GitHub-hosted Windows guard or alter local environment variables to impersonate that runner.
