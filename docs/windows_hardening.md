# Windows private-beta hardening

This is software reliability evidence, not certified detector accuracy or
publisher trust. Keep the repository and candidate artifacts private. Do not
disable antivirus, SmartScreen, signing checks, or script-execution policy to
run these tests.

## Changes covered by regressions

- Scheduled database restore preserves the original database, WAL and SHM as
  one uniquely named `<database>.pre-restore-<random>/` recovery set. Any failed
  preservation or installation move rolls back every file already moved. If a
  rollback move also fails, the error identifies the surviving recovery copy;
  startup refuses to open a mixed database. Recovery sets are not deleted.
- A watch-folder replay whose analysis could not be persisted remains eligible
  for retry without changing the source recording. Recovery reports storage
  failures and retains the pending upload. Failed settings writes do not change
  active settings, and an empty enabled watch folder does not select the current
  working directory. Directory scans honor cancellation.
- An incident clip must contain the exact selected raw frame. Missing incident
  frames, overflow-sized indices, and invalid raw JSON cannot silently open
  another moment. Partially written clips are removed. Viewer arguments use
  absolute paths and no shell, and exited viewer processes are reaped.
- Support bundles receive unique names, including repeated clicks within one
  second. Packaging refuses to overwrite a pre-existing desktop resource from
  another build.

## Program rollback and installation

Setup no longer runs a name-wide `taskkill`. Its built-in
[CloseApplications handling](https://jrsoftware.org/ishelp/topic_setup_closeapplications.htm)
operates on applications using installation payload files. The ZIP installer
and rollback script refuse replacement while this installation's executables
are running and ask the user to use Quit; they never terminate other copies.
The ZIP uninstaller uses the same process guard and validates absolute,
non-root, non-linked installation/evidence targets before any removal.

Previous program snapshots contain five executables and allowed program
configuration/documentation assets, with a `SHA256SUMS.txt` manifest. The native
installer uses [GetSHA256OfFile](https://jrsoftware.org/ishelp/topic_isxfunc_getsha256offile.htm)
to verify each copied file and stops before replacement if snapshot creation
fails. Unique snapshot directories prevent collisions. Rollback validates
hashes, required payload files, relative names and reparse-point boundaries
before modification, preserves the current program set, and restores changed
files if a replacement fails. It never executes a snapshot to validate it.

`installed.toml`, evidence databases, replay recordings, desktop settings and
uninstaller/helper scripts are not rolled back. In particular, a program
rollback must not silently point the application at an older database path.
Legacy snapshots without a complete manifest remain available, but automated
rollback refuses them; retain them for maintainer-guided manual recovery rather
than fabricating a trusted checksum or deleting them. Local hashes detect
copy/integrity errors, not authenticity against an attacker with write access.

## Isolated maintainer checks

Run from the repository root in an already permitted PowerShell 7.2+ environment:

```powershell
go test -race ./cmd/desktop -run 'TestApplyPendingRestore|TestDesktopSettings|TestWatchScan|TestRecoveryReports|TestSupportBundleRepeated|TestSparkReplay' -count=3
pwsh -NoProfile -File scripts/test-windows-snapshots.ps1 -OutputDirectory dist/windows-audit
pwsh -NoProfile -File scripts/test-soak-runner.ps1 -OutputDirectory dist/windows-audit
```

The snapshot harness uses synthetic text files named `.exe`, never executable
payloads. It covers tampering, missing/duplicate/traversal manifest entries,
legacy refusal, reparse points, unrelated-process filtering, partial overwrite
rollback, and unchanged disposable database/configuration markers. The soak
harness builds a harmless child that validates isolated paths, exits with a
known failure, or waits until its resource limit. Neither harness runs Setup,
touches the installed app, opens a real database/replay, launches Spark, or
uses the network. Their machine-readable results are written under the supplied
ignored output folder; owned temporary test files are removed afterward.

The readiness runner includes these snapshot checks and the Go regressions.
For workload testing, `soak-replays.ps1` creates a fresh per-run child directory,
records the executable hash, safely quotes paths containing apostrophes, and
records timeout/memory-limit failures instead of waiting forever. A timeout
terminates only the child process tree started by that run. See
[soak_testing.md](soak_testing.md) and
[private_beta_readiness.md](private_beta_readiness.md).

## Remaining external gates

| Area | Automated scope | Still requires an invited tester or disposable Windows VM |
| --- | --- | --- |
| Installer | Compilation and payload inventory; snapshot helper failure injection | Fresh install, older-to-newer upgrade, uninstall, locked-file prompts |
| Program rollback | Verified synthetic payloads and partial-copy failure recovery | Actual version rollback and compatibility with the current evidence schema |
| One-click update | Download/hash/redirect, helper refusal and shutdown regressions | Private GitHub access, real Setup handoff and relaunch between installed builds |
| Spark | Raw clip contents and mocked exact argument handoff | Installed Spark viewer visibly opens the intended event/time |
| Workload | Synthetic repeat, corruption and resource-limit checks | Representative approved long replays and constrained hardware |
| Publisher | Signature status recorded honestly | Signing and actual SmartScreen reputation; no bypass or guarantee |

On this maintainer machine Windows PowerShell 5.1 script execution is restricted;
do not describe a PowerShell 7 harness pass as a 5.1 execution pass. Preserve
the distinction in release notes and use the normal Setup path for invited
users who do not have an already permitted scripting environment.
