# 0.11.0 comprehensive hardening and acceptance record

This is a private test candidate, not a claim that every possible bug has been
eliminated or that cheating can be detected with 100% accuracy. No detector is
promoted by this change. Automatic enforcement remains disabled; ambiguous
telemetry, legal contacts and missing observations must not become convictions.

## Implemented and regression-tested

| Area | Concrete change and checked failure mode |
| --- | --- |
| Explain checks | Actual bounded branch counters for six detectors; pipeline gates and postprocessing for the catalog. Stale player context, raw emissions, merged/rate-limited results and retained observations remain distinct. No second detector simulation or invented explanation. |
| Playspace continuity | MOV_006 1.2.1 resets disconnected qualifying sequences. Synthetic 30 Hz bursts separated by one missing sample cannot manufacture sustained movement. No cap or severity threshold was loosened. |
| Legal-mechanics controls | Production-pipeline fixtures exercise leaning, game-authored boosting/stacking, small pushes, head contact, near-cap throws and sampled wrist motion, plus legal-contact ambiguity guards. Synthetic passes are not population validation. |
| Independent evidence workflow | Local bounded attachments are SHA-256 addressed, downloaded as attachments, and bound to exact player/window/candidate hashes. Two immutable, attested ballots precede explicit reveal. Disagreement stays uncertain. |
| Candidate identity | Analysis provenance records the actual executable SHA-256. Missing or different binary identity requires re-analysis before a new review session; matching version strings alone are insufficient. |
| Promotion integrity | Legacy textual references alone cannot pass. Current evidence, immutable ballots, candidate/window integrity, historical connected groups and representative legal/ping/capture strata are checked. Group ranges are descriptive, not confidence intervals. |
| Browser boundary | Same-origin validation, anti-framing and no-store headers, per-response script nonce, bounded headers and idle connections. Headerless local CLI clients remain supported. |
| Shutdown | New requests stop, active handlers and detached analysis are cancelled, and runtime/HTTP work drains before explicit database close. Timeout refuses premature store close. |
| Database recovery | Failed DB/WAL/SHM preservation or replacement restores every moved file or reports the surviving recovery copy. Unique recovery sets remain available. |
| Watch/recovery | Persistence failures remain retryable; failed settings saves do not activate unsaved values; empty enabled folders are rejected; scans honor cancellation; queue-root recovery preserves future intake. |
| Exact replay clips | Missing incident frames, corrupt ticks and overflowing indices refuse launch. Partial output is removed; absolute viewer arguments are passed without a shell. |
| Installer/rollback/uninstall | No name-wide process termination. Program-only snapshots use unique paths and verified manifests; partial-copy rollback, unsafe roots, linked paths and tampered snapshots fail safely. Evidence/configuration are not silently rolled back. |
| Soak and private regression | Isolated output paths, apostrophe-safe arguments, timeout/memory-limit accounting, executable/source hashes, malformed-manifest fuzzing, contradictory baseline rejection and cancellable file copying. |
| Review usability | Explain-check buttons, legal-context/diversity details, evidence workspace and visible modal errors; unknown values remain unavailable rather than displaying zero error. |

## Verification performed during this pass

- Full shipped-package Go tests and vet passed; the full race-enabled suite
  passed with approximately 75% statement coverage. Coverage is not an accuracy
  score and does not prove every platform path executes correctly.
- Sixteen embedded-page JavaScript tests cover escaping, blinded rendering,
  missing coverage, detector filters, real-decision trace rendering, metric
  availability and modal feedback.
- All five bounded fuzz targets passed: NDJSON, ZIP, Echo session mapping,
  legacy JSON and the private regression manifest.
- Ten private recordings were freshly analyzed through the production engine
  into isolated stores. The pinned baseline matched all 34 incidents and 35
  behavior-window contracts. Original sources and the prior baseline were not
  rewritten; these are behavior comparisons, not independently confirmed labels.
- The actual local browser page loaded under the nonce policy with no captured
  console errors. A synthetic attachment/session exercised field errors, two
  locked simulated ballots, explicit reveal, uncertain consensus, diversity
  details and persisted detector explanations. Those simulated reviewers are
  software fixtures, not independent human evidence.
- Thirteen program-snapshot safety checks, five soak-runner failure/isolation
  checks and a three-iteration synthetic CLI soak passed. Windows-specific
  storage/runtime/clip regressions also passed repeated race runs.
- Staticcheck, actionlint and reachable vulnerability checks passed. Security
  analysis uses the existing CI rule policy; the binary evidence download has
  a narrowly justified exception because it is forced to attachment-only,
  `application/octet-stream`, with `nosniff`. No global rule was weakened.

Machine-specific recordings, reports, test databases and compiled artifacts
stay in ignored local output directories. The PR/checks and hash-bound Windows
candidate report record final build results; this document does not claim a
local build has already become the published release.

## Before private testing

1. Keep a full database backup. Portable JSON carries annotations, not the new
   evidence attachment bytes and immutable session history.
2. Merge the PR only after its required checks pass. Install the resulting
   0.11.0 build, confirm its version, and re-upload a previously stored replay.
3. Check that the replay refreshes without a duplicate-file failure; compare
   retained notes and findings, then open **Explain checks** for a player.
4. Check a selected event in the installed Spark viewer against the actual
   intended frame. Automated handoff tests cannot verify your Spark installation.
5. Use the **Evidence review workspace** for actual short evidence attachments
   and real separate reviewers. Keep unexplained contacts or disagreements
   uncertain; do not use two aliases to simulate independence.

## External gates that code cannot complete alone

- A frozen candidate, independently adjudicated positive and legitimate
  opportunities, representative new participants/sessions, and a prospectively
  unseen evaluation cohort. The available regression corpus does not supply
  this by itself. Room-scale steps versus lean and aliased wrist measurements
  remain observability limitations, not solved physics.
- A disposable Windows VM or invited tester for real install, upgrade,
  uninstall, locked-file handling, one-click update and program rollback.
  Maintainer checks do not run Setup against the user's installed application.
- An installed Spark viewer visibly opening the exact intended clip.
- Publisher signing and reputation if broader distribution is desired. An
  unsigned candidate can still trigger SmartScreen; checksums are integrity,
  not publisher trust. Do not bypass Windows security to run it.
- Explicit approval to set up any public binary channel or external evaluation.
  The source repository remains private and the maintainer token is never
  embedded in the application or shared with testers.

See [detector decisions](detector_decision_tracing.md),
[evidence review](blind_review_workflow.md),
[calibration policy](calibration_workflow.md) and
[Windows hardening](windows_hardening.md) for details.
