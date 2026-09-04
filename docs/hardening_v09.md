# NEVR v0.9 hardening acceptance record

This release completes the engineering needed for the eight production-hardening tracks. It does not manufacture replay labels, claim detector validation without real data, or claim a publisher identity before Microsoft validates one.

| Track | Implemented acceptance evidence | External evidence still required |
|---|---|---|
| 1. Real calibration library | Blind event reviews, detector opportunity windows, portable evidence library, player-safe splits, detailed collection protocol, library counts in the hashed packet | Independently reviewed clean and confirmed replay opportunities |
| 2. Threshold calibration | Training-only sweeps, validation comparison, locked holdout, Wilson intervals, stratified metrics, stale-provenance rejection, single-detector promotion, automatic rollback, full threshold/physics export | Enough current-build labels to satisfy each detector's gate |
| 3. Parser fuzzing | Native fuzz targets for raw NDJSON, ZIP selection/decompression, Echo session mapping, and legacy JSON; line, decompression, upload, and legacy-document bounds | Continued scheduled fuzz time and any future real malformed samples |
| 4. End-to-end coverage | Real server migration/listen/auth/shutdown tests, desktop upload/API tests, batch replacement tests, race testing, retained coverage profile, 72% repository floor | Future features must keep the floor green and add path-specific tests |
| 5. CI security gates | `govulncheck`, medium/high `gosec`, Staticcheck, vet, actionlint, race tests, Dependabot, SBOM, conditional CodeQL/dependency review | GitHub Code Security entitlement or public visibility for CodeQL/dependency-review uploads |
| 6. Performance/storage soak | Isolated replay-library harness with forced re-analysis, hashes, per-run logs, throughput, peak working set, DB growth, and a synthetic smoke test | Multi-iteration run against the largest real Spark library on the main PC |
| 7. Derived-physics validation | `nevr-compat --physics-audit` CSV compares raw game velocity, an independent pose derivative, production extraction, playspace residual, legal context, wrist motion, disc state, and releases | Frame alignment and sign-off against Spark/known controlled motions |
| 8. Distribution trust | Defender scanning, clean install/upgrade/uninstall, hashes, provenance where available, PFX signing, and OIDC Microsoft Artifact Signing with signature verification | Validated Artifact Signing profile (or PFX certificate) and accumulated SmartScreen reputation |

All detectors remain shadow-only unless their real-data gate passes. None of the engineering checks converts synthetic fixtures into detector validation, and no automatic enforcement path is enabled.
