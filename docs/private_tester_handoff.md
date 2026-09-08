# Invited tester: review-only candidate

This is a private replay-review test, not a validated anticheat release. A
finding is a reason to inspect evidence, never a confirmed cheating verdict.
No automatic punishment is available in this build. Do not change thresholds
or calibration settings while reproducing a UI or installation bug.

## Supported controlled scope

Windows x64, offline local replay analysis, and one operator are the scope of
this candidate. The current verification environments are Windows 11 locally
and Windows Server 2025 in CI; this is not a claim of testing every Windows
version, architecture, multi-user configuration or live-server deployment.
Do not connect an external punitive adapter that consumes review recommendations.

Install and run this candidate **only in a disposable Windows VM/account with
no existing NEVR data or production credentials**. An ordinary Setup launch
uses that account's normal application storage; the isolated test scripts do
not make a separate ordinary installation safe for an existing production
profile. Do not install or upgrade this controlled candidate into the profile
holding your important evidence. Use synthetic/disposable evidence for all
installation, update, rollback and uninstall checks.

## Before installing

Get the candidate link, app version, exact Git revision, Setup/ZIP SHA-256 and
current verification status from the maintainer's PR handoff. A green source
test does not establish that a downloaded installer is that exact candidate.
Do not use **Code → Download ZIP** as an application installer. Keep the
repository private and use only your own invited GitHub account; never send
tokens, passwords, or a signed-in browser profile to another person.

An unsigned installer may show an unknown-publisher or SmartScreen warning.
Record the prompt and stop if it differs from the expected candidate. Do not
disable Windows protection or treat a matching checksum as publisher trust.
Remain in the disposable Windows account/VM throughout this controlled test.
The existing general-installation guide is not authorization to upgrade a
production profile with this candidate.

## Five-minute first check

1. Start the supplied candidate in that disposable account/VM. In **Health & maintenance → Build & configuration
   identity**, record app/schema version, linked/source revision, modified state,
   executable SHA-256, configuration fingerprint and enforcement policy. The
   policy should be `review-only-v1`. A development/unverified build is labeled
   as such; do not describe it as a verified clean revision.
2. Use the maintainer-provided synthetic fixture or a replay you are authorized
   to review. Import it twice. The second import should refresh it without
   rejecting it as a duplicate. Restart and confirm the match and notes remain.
3. Open a case, inspect its limitations and select an event. Open the Spark clip
   and manually check the player and exact incident/time. Record **not_run** if
   Spark is unavailable; a generated clip is not proof of correct playback.
4. Save a harmless test note and reopen the case. Try a long name/explanation,
   narrow window, larger text and keyboard focus. Check that a failed save or
   disconnected engine is clear, retains unsaved text where offered, and gives
   a recovery action. Do not stop the engine while important analysis is active.
5. Record expected versus actual results using the private bug template. “No
   signals” is not a pass/fail accuracy label and must not be relabeled “clean”.

`tests/fixtures/synthetic_session.echoreplay` is committed generated test data.
The operator UI fixture is a maintainer-only temporary synthetic database; it
is not a live feed or independent cheating/fair-play evidence. Original player
recordings are not bundled as sample data. Request sharing permission before
substituting any third party's recording.

## What to send, and what to keep private

- Start with reproducible steps, candidate identity and a tightly cropped
  screenshot with player names, IDs, local paths and private match details
  removed. Send through the agreed private channel, not a public screenshot
  host or social-media post.
- A **Support bundle** excludes raw frames, original recordings and the database.
  Inspect its entries before sharing; aggregate match timing can still be
  identifying context. It is created locally, not automatically uploaded.
- A **Diagnostic ZIP** includes source/evidence information. Known identity
  keys are redacted, but unknown fields, map keys and free text may retain
  identifying data. It is **not guaranteed anonymous**. Inspect every entry and
  share only with explicitly authorized reviewers.
- Case reports, summary/player exports, the evidence library, raw archives and
  backups can contain player identities, reviewer notes or full telemetry. Do
  not attach them by default. Obtain permission and agree on the minimum
  necessary excerpt first. Never send credentials or an installed database as
  an ordinary bug report.

## Identity and result limits

Health and diagnostic runtime provenance identifies the application producing
the report, not necessarily the application that generated a stored finding.
Case reports list historical run records separately. Missing historical
provenance is unknown, not an invitation to substitute the current build.
Historical schema versions were not saved per analysis run.

New display fingerprints use `nevr-runtime-config/v2:` and include effective
detectors, physics, scoring, pipeline timing and project rules. Earlier short
fingerprints remain as recorded and are not directly comparable. Calibration
fingerprints and historical review bindings are not rewritten by this change.

Passing these checks establishes reproducible application behavior only. It
does not establish detector accuracy, verified Echo mechanics, publisher trust,
or public-release readiness. Separate-PC installation, a real private update
and Spark alignment require actual tester evidence; leave missing checks
**not_run**. Maintainer automation and the longer acceptance checklist are in
[private beta readiness](private_beta_readiness.md).

## Maintainer verification scope

The controlled candidate entrypoint is `scripts/verify-test-release.ps1`, given
the actual supplied Setup/ZIP, their checksum sidecars and exact `ExpectedCommit`.
Its report combines artifact checks, selected source regressions, actual
temporary desktop backup/restore and an isolated CLI workload. The optional
explicit `ReplayFiles` list adds a timed desktop workload. It does **not** run
the whole repository's build, vet, race, fuzzing and dependency gates; it does
not execute Setup, update or Spark. Omitted work stays **NOT TESTED**. Passing
that wrapper alone is not whole-suite or installation acceptance.

Bind these separate same-commit CI results to the candidate as well:

- **CI:** `go build ./...`, `go vet ./...`, full tests/coverage, race tests
  including `scripts/testdata`, Node UI/policy regressions, Staticcheck,
  Actionlint, Govulncheck and Gosec.
- **Security and parser fuzzing:** the NDJSON, ZIP, Echo-session, legacy JSON
  and regression-manifest fuzz targets, dependency vulnerability/static scans,
  and SBOM. Private-repository dependency-review/CodeQL jobs may be deliberately
  unavailable/skipped; do not count skipped jobs as passed.
- **Windows package:** Windows tests and disposable CI install/upgrade/uninstall
  checks, including installed-file hashes compared with the supplied ZIP.
  Local wrapper results always leave that installed-payload binding **NOT
  TESTED**; attach the matching CI evidence separately.

Check each job's actual completed status. A green earlier commit or a compile-only
result does not pass these checks for the candidate. No checklist automatically
authorizes publication, signing, detector promotion or enforcement.
