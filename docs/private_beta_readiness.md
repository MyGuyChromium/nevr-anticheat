# Private beta readiness

NEVR remains a private, invited beta. This checklist does not publish anything,
change repository visibility, promote detectors, or establish that a player
cheated. Passing synthetic checks establishes specific software behavior;
real replay labels and independent review are still required for calibration.

## Maintainer: run the isolated checks

Use Windows and PowerShell 7.2 or newer. Source checks/builds also require the
Go version in `go.mod`, CGO enabled, and a C compiler, as described in README.

From the repository root:

```powershell
pwsh -NoProfile -File scripts/private-beta-readiness.ps1
```

This builds a temporary desktop executable, runs the existing targeted source
regressions, and exercises a real desktop process with synthetic replay data.
It leaves a timestamped `dist/private-beta/<run>/readiness-report.json` and local
test logs. It uses an exclusively owned temporary database, disables watch and
automatic update checks, removes the GitHub token from the child environment,
and launches with `--no-browser`. It never opens the installed evidence database,
runs Setup, triggers an update, launches Spark, or contacts the release service.
The unique scratch database and process are cleaned up after the run.

A source-only run is intentionally incomplete for distribution: no candidate
installer/ZIP has been identified or checked. To check an actual candidate,
reuse the existing packaging script:

```powershell
pwsh -NoProfile -File scripts/package-windows.ps1 -OutputDirectory dist/beta-candidate -BuildInstaller
Expand-Archive -LiteralPath dist/beta-candidate/NEVR-Anticheat-Windows-x64.zip -DestinationPath dist/beta-candidate/programs
pwsh -NoProfile -File scripts/private-beta-readiness.ps1 -DesktopExecutable dist/beta-candidate/programs/nevr-desktop.exe -Installer dist/beta-candidate/NEVR-Anticheat-Setup.exe -PackageZip dist/beta-candidate/NEVR-Anticheat-Windows-x64.zip
```

Use a new output directory for each candidate, or choose an unused extraction
directory. Local packages contain files directly at ZIP root; workflow packages
have a `NEVR-Anticheat-Windows-x64` subdirectory, so adjust `-DesktopExecutable`
when checking a downloaded workflow artifact. The packaging command compiles an
installer but does not install it. Inno Setup is required for this build step.

The runner records SHA-256, size, version, source revision and dirty-tree state,
including the tested binary's own Go VCS metadata. Local packaging also writes
`NEVR-Anticheat-Build.json` with source identity and executable hashes. Dirty
source packages embed `buildCommit=development`; clean packages retain their
exact Git revision. This prevents an uncommitted candidate from masquerading
as a verified release or being used for trusted detector promotion. Development
candidates use manual installer updates until a clean private release is built.
It checks the Setup checksum sidecar, inspects ZIP contents without executing
them, compares Setup version with the running app, and compares the tested
desktop's hash with the desktop inside the ZIP. A source build is not silently
treated as the exact packaged payload. Source regressions always describe the
current checkout; the real-process smoke checks describe the recorded binary.

For an invited technical tester who already has the source script and trusted
candidate executable but no Go installation, add `-SkipSourceTests`. The report
then explicitly records source checks as `not_run`, not as passed.

## What the report proves

Each check has `pass`, `fail`, or `not_run`, with its scope and explanation.
`automated_status` is `pass`, `fail`, or `incomplete`; `beta_status` remains
`manual_validation_pending` until the separate tester checklist is completed.
An automated pass must not be described as complete beta acceptance.

| Check | Automatic evidence | Remaining manual evidence |
| --- | --- | --- |
| First/repeated import | Actual process accepts the fixture twice and stores one refreshed match | Tester reimports their approved replay through the UI |
| Corrupt file | Per-file failure; existing match and app remain usable | Clear UI explanation for a failed file among valid uploads |
| Evidence persistence | Temporary backup and match survive process restart; source tests exercise restore preservation | Older-to-newer installed upgrade and uninstall preserve disposable test evidence/settings |
| Update safety | Manifest/hash/size, redirect, active-analysis and shutdown failure-path regressions | Private release download, actual Setup handoff and relaunch between two installed betas |
| Spark clip | Source tests verify incident/throw frame windows and native clip contents with a mocked viewer launch | Real Spark opens the correct player/incident/time on the tester PC |
| Candidate identity | Hashes, required ZIP payload, Setup checksum/version and exact tested desktop binding | Confirm privately downloaded files match the maintainer's recorded hashes |
| Publisher trust | Authenticode status is recorded without changing it | Record actual publisher and Windows/SmartScreen prompts on another PC |
| Detection quality | No claim made from these synthetic smoke checks | Labeled real replays and independent review/calibration |

Exit codes: `0` means the automated checks passed, `1` means a check failed,
and `2` means automated evidence is incomplete. Required manual rows remain
`not_run` in all automated reports. An unsigned artifact is recorded as
`NotSigned`; a matching checksum does not establish a trusted publisher and the
runner never bypasses Windows security prompts. A stale installer version or
a different ZIP desktop is a failure, even when every source test passed.

Keep detailed logs local unless they have been inspected. The JSON report has
no GitHub token, loopback access token or real replay data; its artifact inventory
uses filenames instead of full paths. Source logs and failure messages may
contain local filesystem paths. Send only the report and the relevant redacted
failure details through the agreed private channel.

## Invited tester: normal installation

1. Accept the maintainer's private repository invitation. Sign into that invited
   GitHub account in your browser, then open the private release link supplied
   by the maintainer. If it is unavailable, ask for access or the approved
   installer through the existing private channel; do not make the repo public.
2. Download `NEVR-Anticheat-Setup.exe` from that release, not **Code → Download
   ZIP**. Confirm the version/hash supplied by the maintainer. Never receive,
   send, paste, or embed somebody else's GitHub token or password.
3. Run Setup and choose **Install**. It installs for your Windows account,
   creates shortcuts, and opens NEVR. Record any publisher/security prompt;
   stop and check with the maintainer if it differs from what was expected.
4. Drop an approved replay into NEVR. Upload the same file again and confirm
   that it is accepted and refreshed. Close/reopen NEVR and confirm the match
   remains. Do not use important evidence for destructive test scenarios.
5. Open Replay Viewer once in Spark, then use an incident's **Open clip** or a
   throw's replay button. Check the displayed player and time against the
   selected incident. A generated file alone is not proof the viewer opened
   at the correct moment.
6. For a newer beta, use the private signed-in browser to download Setup again
   and run it normally. In-app update requests do not borrow browser sign-in;
   a private-access message is not a request to send the maintainer a token.
   Real one-click updating is a separate maintainer-run authenticated test.

## Record the checks that need a second PC

Use a disposable Windows account or VM for install/update/uninstall testing.
Install the previous approved beta, import the synthetic fixture, save one
setting and review note, and create an in-app backup. Record the version, one
stored match, setting/note and backup filename. Install the candidate, verify
all those values again, and verify a rollback snapshot exists. Then test
uninstall preservation in that disposable account. This runner deliberately
does not perform those operations on the developer's installed app.

Record one private acceptance entry per candidate with these fields:

```text
Candidate version and Setup SHA-256:
Readiness report run identifier:
Tester alias and Windows version:
Separate PC/account used:
Private download / first install: pass | fail | not_run
Duplicate and corrupt UI uploads: pass | fail | not_run
Upgrade evidence/settings/backup preservation: pass | fail | not_run
Actual one-click update and relaunch: pass | fail | not_run
Real Spark player/incident/time alignment: pass | fail | not_run
Uninstall evidence preservation (disposable account): pass | fail | not_run
Observed publisher and security prompts:
Failure steps and expected/actual behavior:
```

Keep pending items pending. Reuse the existing release-workflow install and
uninstall smoke results as additional evidence, but bind them to the candidate
commit/hash and do not substitute them for a separate tester PC or actual
Spark verification. Broader local replay soak checks remain available in
`scripts/soak-replays.ps1` when approved replay data is ready.
