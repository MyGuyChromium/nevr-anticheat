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

The ZIP uninstaller is installed into the program folder, which is the only
place a user can start it from, and a folder cannot be deleted while it is the
current directory of the `cmd.exe` wrapper and of the PowerShell running the
script. Earlier copies therefore removed the shortcuts, failed with "in use"
and left every program file behind. `Uninstall-NEVR.ps1` now copies itself and
`Program-Snapshot.ps1` to a fresh `%TEMP%\nevr-uninstall-<guid>` folder,
continues from there, retries the folder removal for up to 20 s while the
wrapper exits, and removes the shortcuts only after the program folder is gone,
so a failed uninstall never leaves a working program with no way to start it.
The preflight order is unchanged (roots validated and processes checked before
any removal). `Uninstall-NEVR.cmd` forwards its arguments, so
`Uninstall-NEVR.cmd -RemoveEvidence` reaches the script; without it evidence is
always kept. The relocation is covered in a scratch profile by
`packaging/Test-ProgramSnapshots.ps1`; it has not been exercised on a real
installed copy (see the external gates below).

Previous program snapshots contain five executables and allowed program
configuration/documentation assets, with a `SHA256SUMS.txt` manifest. The native
installer uses [GetSHA256OfFile](https://jrsoftware.org/ishelp/topic_isxfunc_getsha256offile.htm)
to verify each copied file and stops before replacement if snapshot creation
fails. Unique snapshot directories prevent collisions. Both writers name
snapshot folders in UTC: Setup writes `yyyymmdd-hhnnssZ-N` and
`Program-Snapshot.ps1` writes `yyyyMMdd-HHmmss-fffffff-<guid>`. Setup used to
name folders in local time, so "newest by name" compared two clocks and
Rollback could pick the wrong build. A UTC-format name is authoritative; a
legacy local-time name is resolved through the folder's own creation time.
Rollback walks the snapshots newest first and skips, and reports, any folder
that fails verification, so one interrupted snapshot no longer blocks every
rollback.

Both writers keep the newest 3 verified snapshots (about 40 MB each; the
constant `SnapshotsToKeep` in `NEVR-Anticheat.iss` and the `-Keep` default in
`Program-Snapshot.ps1`). Pruning counts only snapshots that verify, never
removes the snapshot a rollback is restoring from or the one just written,
deletes only manifest-listed files plus the then-empty folders (no recursive
delete), leaves a folder with foreign content or a reparse point untouched, and
is never fatal to the install. Unverifiable and legacy folders are never
deleted automatically. Rollback validates
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

## Windowed desktop build, log file and auto-exit

`nevr-desktop.exe` is released as a GUI-subsystem program (`-H=windowsgui`
with `-X main.guiSubsystem=true`), by `scripts/package-windows.ps1` and by the
release workflow alike; `scripts/release-gate.test.cjs` fails when the two
disagree. The four command-line programs stay console programs. The package job
reads the PE subsystem of the shipped files and records the measured value as
`desktop_subsystem` in `NEVR-Anticheat-Update.json` (the local packager records
the same field in `NEVR-Anticheat-Build.json`).

A windowed process has no console to close, so the build is refused unless the
embedded page sends `api/heartbeat`. The server then quits by itself:

- about 8 s after the page announced that it is leaving (window closed); any
  later request, such as a reload, cancels that;
- after 90 s of silence otherwise. The limit is deliberately above 60 s because
  Chromium throttles the timers of a minimized or covered window to about one
  wake-up per minute;
- never before a first heartbeat was seen (`--no-browser`, scripts), never
  while an analysis, recovery, watch-folder import, update or any other API
  request such as a backup or restore is running, and never with
  `--no-auto-exit`.

Without a console the app writes what it would have printed to
`<data dir>\logs\nevr-desktop.log` (rotated at 5 MB, three older files kept),
where the data directory is the one holding the database:
`%LOCALAPPDATA%\NEVR-Anticheat` for an installed copy. Fatal Go runtime
crashes go to `nevr-desktop-crash.log` beside it, and a startup failure is also
shown in a message box. The per-run access URL is kept out of the log; a parent
process that redirected stdout still receives it, which is what the installed
startup smoke test relies on. `--console` prints to the starting terminal
instead, and `--log-file` makes a console build log to the file. The release
smoke test requires the installed app to have written the log file, which also
proves that the `-X main.guiSubsystem` linker setting took effect.

## Update ordering

The rolling `windows-latest` release is replaced in place and every build
carries the same version, so the manifest states the committer time of its
source (`commit_time`). The updater installs a release only when that time is
strictly after the installed build's; a missing or unparseable time is refused.
The publish job is serialised per release and refuses to move the rolling tag
to anything but the published commit or a descendant of it. The only override
is `nevr-desktop.exe --allow-update-downgrade`: it lasts one process, cannot be
reached from the page or from a release, skips the ordering rule only (the
size, SHA-256 and tag checks still run) and is stripped before the update
helper relaunches NEVR. This orders honest releases; it is not authentication.
Updates are integrity-checked, not publisher-verified, unless a signing
identity is configured (see [windows_release_trust.md](windows_release_trust.md)).

One-click update is refused before any network request while another NEVR
program runs from the installation folder (nothing is terminated), and on an
installation whose database was moved off `%LOCALAPPDATA%\NEVR-Anticheat`,
because the staging directory must be the profile-local
`%LOCALAPPDATA%\NEVR-Anticheat\updates` and not a junction or symlink.

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
| Installer | Compilation and payload inventory; snapshot helper failure injection; hosted-runner silent install, same-build upgrade and uninstall | Interactive install, upgrade from a real previous release, locked-file prompts |
| ZIP uninstaller | Relocation, shortcut order and evidence handling in a scratch profile; argument forwarding of the `.cmd` wrapper | Uninstall of a real ZIP installation started by double-click from its program folder |
| Program rollback | Verified synthetic payloads, partial-copy failure recovery, UTC naming and keep-3 pruning (including Setup's compiled code) | Actual version rollback and compatibility with the current evidence schema |
| One-click update | Download/hash/redirect, downgrade refusal, helper refusal and shutdown regressions | Token access if the feed is private, real Setup handoff and relaunch between installed builds, Restart Manager behaviour |
| Windowed build | PE subsystem of the shipped files; log file written by the installed app on the hosted runner; heartbeat watchdog unit tests | Closing the real Edge/Chrome app window ends the process; a window left minimized for a long time keeps it alive |
| Spark | Raw clip contents and mocked exact argument handoff | Installed Spark viewer visibly opens the intended event/time |
| Workload | Synthetic repeat, corruption and resource-limit checks | Representative approved long replays and constrained hardware |
| Publisher | Signature status recorded honestly | Signing and actual SmartScreen reputation; no bypass or guarantee |

On this maintainer machine Windows PowerShell 5.1 script execution is restricted;
do not describe a PowerShell 7 harness pass as a 5.1 execution pass. Preserve
the distinction in release notes and use the normal Setup path for invited
users who do not have an already permitted scripting environment.
