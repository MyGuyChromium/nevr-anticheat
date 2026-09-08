# Private readiness: six-track acceptance record

Target: a useful, reproducible, conservatively evaluated replay-review product.
This is not a claim of perfect detection, zero false positives, or public-release approval.
The repository, original recordings, labels and generated reports remain private.

| Track | Implemented locally | Remaining external acceptance |
| --- | --- | --- |
| 1. Calibration isolation | Durable/versioned player-group assignments, historical exposure retention, quarantine of cross-split links, single-match and matrix experiment exclusions, candidate locking on first holdout exposure; dirty/unknown/stale builds cannot qualify. | A genuinely prospective, independently reviewed holdout collected after a clean candidate is frozen. The in-app holdout is reserved, not sealed/blinded. |
| 2. Throw-speed validation | Production-pipeline comparisons, explicit sampled-versus-engine measurement semantics, regression tests, corrected direction-projection arithmetic without cap retuning. Heuristic margins no longer claim certainty. | Authoritative release/contact/reference-frame evidence for representative legitimate and confirmed illegal throws. Public runtime serialization does not prove a universal cap or inheritance formula. |
| 3. Automatic replay regression | Versioned manifests pin replay hashes, configuration, match structure and incident behavior; exact player/detector windows report missed signals and unexpected findings; confirmed evidence is separated from reported or uncertain claims. Original sources are read-only. | More diverse independently corroborated examples, including legal leans, stacking, pushes, slaps, headbutts, latency, capture cadence and transitions. Never baseline a false positive as verified fair behavior. |
| 4. Promotion measurement | Stricter point estimates, Wilson bounds, sample/diversity floors, independent evidence requirements, provenance/quality/isolation checks, explicit replay-only promotion blocks for MOV_006 and BIO_001. | Representative prospective evaluation, cluster-aware uncertainty and independent verification. Opportunity-level Wilson intervals do not prove population accuracy for correlated throws. No detector is declared production-validated. |
| 5. Honest assessment | Review needed / no signals / insufficient data across app/API/summary/CSV/report; actual phase/warmup dispatch and selected necessary-input counters persist atomically with events. Missing coverage, disabled checks, poor quality and unreachable input conditions cannot certify fairness. | Detector-specific opportunity instrumentation beyond the initial three checks and richer telemetry. Input counters are necessary conditions, not independently validated opportunities or guarantees that internal guards passed. |
| 6. Private beta | Isolated actual-executable smoke runner, version/hash-bound artifacts, source regressions, duplicate/corrupt import, backup/restart/shutdown tests, private tester checklist with pass/fail/not-run reporting. | Other Windows PCs, real installer upgrade/rollback on a disposable copy, Spark installation and exact clip positioning, authorized private-update download and publisher signing/trust. No invited testers have been contacted automatically. |

## Repeatable local verification

- `go test ./...` and `go vet ./...` cover the source repository; private local audit programs under ignored `dist/` may also be discovered by Go and are not shipped source.
- `go test -race ./cmd/... ./internal/... ./tests/...` checks the shipped packages for data races.
- `node --test scripts/desktop-review.test.cjs` checks the actual embedded page's assessment, filters, insufficient-data handling, blinding and escaping.
- Run the commands in [private replay regressions](private_replay_regressions.md) against a pinned private manifest after each detector change. A captured baseline is behavior history, not ground truth or proof of accuracy.
- Run [private beta readiness](private_beta_readiness.md) against the exact candidate ZIP/installer/executable. An all-green automated section does not turn the manual `not_run` entries green.

This is a historical acceptance record for development candidate **0.10.3**, not the identity or acceptance status of the current candidate. Use the current PR's commit/hash-bound verification report and the installed app's runtime provenance for a newer build. Machine-specific verification outputs are kept in ignored `dist/`, never mixed into the user's installed database or original replay files. Uncommitted development artifacts must not be described as published, signed or verified clean-revision releases.

## Release decision

The calibration dashboard/packet explicitly reports `production_validated=false`, `sealed_holdout=false`, and `release_eligible=false`. Those flags are intentionally not flipped by synthetic tests, a familiar player's name, a reported admission, a large count of correlated signals, or passing local smoke tests. Scored human review and a validated public anticheat release are separate decisions.

Do not weaken antivirus, bypass publisher verification, enable automatic punishment, create a public feed, change repository visibility, or publish private replay identities to clear these gates. Collect and review the missing evidence instead.
