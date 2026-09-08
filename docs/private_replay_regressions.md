# Private replay regression contracts

`replay-regression` is an opt-in local developer/operator command. It runs the
same `replay.Engine.AnalyzeFileAll` ingestion, detectors, shadow routing, scoring
and persistence used by the desktop app and `nevr-ac`. It does not send replays
anywhere, change the desktop database, tune detectors, create exemptions or
promote a detector. It is not an automatic update or a scheduled background job.

## Capture and repeat a private baseline

From the repository, keep all generated files under ignored `dist/` (or another
private directory outside the checkout). Use a new manifest version and new
output directory for every capture; existing outputs are deliberately refused.
The parent directory must already exist.

```powershell
New-Item -ItemType Directory -Path dist/private-regression
go run ./cmd/replay-regression capture --manifest dist/private-regression/v1.json --out dist/private-regression/capture-001 "C:\PrivateReplays\example.echoreplay"
go run ./cmd/replay-regression check --manifest dist/private-regression/v1.json --out dist/private-regression/check-001
```

Pass `--config path/to/config.toml` to both commands to test a particular
configuration. Without it, both use the application's built-in defaults. All
flags go before replay paths. Multiple replay paths are supported; multi-session
recordings are analyzed as all of their matches, never just the first one.

For a reproducible build with Git revision metadata, build once instead of
`go run`, then invoke that executable:

```powershell
go build -o dist/private-regression/replay-regression.exe ./cmd/replay-regression
```

Exit codes: `0` = all declared behavior checks passed; `1` = behavior/window
regression; `2` = invalid input, changed hashes/configuration or execution error.
Each completed run writes `report.json` and isolated per-source databases. Failed
runs retain their partial diagnostics when an output directory was created.
Original sources are read only. The runner hashes sources before analysis and
verifies a temporary staging copy so an edited source cannot silently change the
analyzed bytes. Its temporary copy is removed when that case finishes.

## What is pinned

The versioned `nevr-private-replay-regression/v1` manifest contains source
SHA-256s, an analysis-configuration SHA-256, expected match IDs, frame and player
frame counts, and the complete detector-incident multiset. Incident identity
includes match, player, detector, emitted frame, causal start/end, anomaly type,
shadow status and auto-enforcement status. Random event IDs are excluded, so two
fresh analyses compare meaningfully. Additional, missing, reassigned, duplicated,
promoted or newly auto-enforcing incidents fail the contract. A different Git
revision is expected during a code regression run and is recorded, not rejected.
Triggered detector versions, quality grades and build details are in the report.

Changing a replay, pinned evidence artifact or analysis configuration is a
preflight refusal, not a new successful baseline. Capture a separately reviewed
manifest version for an intentional change; retain old results. Capturing current
behavior does not demonstrate that current behavior is correct. In particular,
zero expected signals is not a verified fair-play label.

## Behavior windows and evidence strength

Each replay case can include `windows` such as this **illustrative** object.
Replace the match/player IDs and inclusive frame range with values from that
case; it is not a ready-to-use real-player label:

```json
{
  "id": "release-review-01",
  "match_id": "example-match",
  "player_id": "example-player",
  "detector_id": "THROW_001",
  "start": 120,
  "end": 140,
  "expectation": "observe",
  "provenance": {
    "kind": "uncertain",
    "truth": "unknown",
    "note": "Release attribution and legal contact require review."
  }
}
```

`signal` catches a missing expected incident; `quiet` catches an unwanted
incident; `observe` records the outcome without inventing a behavioral assertion.
Only incidents for that player and detector whose causal frame interval overlaps
the inclusive window count. A player with no stored samples in that window is
reported as unobservable, never as a true negative or false negative. Sample
presence does not prove usable release-time measurements: reviewers must still
inspect phase, loss, sampling gaps, quality gates, contact and attribution.
Every new window result therefore reports
`opportunity_coverage: "unresolved_opportunity_coverage"`. Its `assertion`
reports the requested signal/quiet behavior separately. `samples` is a count of
stored player frames, never a detector-opportunity denominator.

Provenance categories are deliberately separate:

- `behavior_only` and `uncertain` require `truth: "unknown"`.
- `synthetic` is generated behavior, not measured production accuracy.
- `user_reported` can preserve a player's positive/negative claim. A disagreement
  is `user_reported_contradiction`, not a confirmed false positive or false negative.
- `confirmed` requires positive/negative truth, two distinct reviewer identifiers
  (case-insensitive; `local-owner` placeholders do not count),
  a review note and at least one local evidence artifact with its SHA-256. Only
  the label is retained; its outcome remains `unresolved_opportunity_coverage`,
  not a true/false positive/negative. These labels are operator assertions;
  the runner cannot authenticate reviewer independence or
  prove that their conclusion is correct. Review identity and evidence through
  the application's calibration process before treating a label as promotion data.

Pinned evidence is represented as `{"path":"review-notes.txt","sha256":"..."}`
in `provenance.artifacts`. Relative replay/artifact paths resolve from the manifest
directory. Do not use another player's self-report as a blanket exemption; labels
are checked only after detection has run, and never enter detector configuration.
Use separate incident windows, including expected-but-missing behavior, instead
of labeling only existing events. Keep ambiguous slaps, headbutts, releases and
playspace movement uncertain until the required telemetry/context is available.

The report intentionally does not turn correlated windows, overlapping replays,
user reports or synthetic examples into accuracy percentages. It does not feed
detector promotion automatically. Passing these contracts provides change
protection; independently reviewed held-out data and promotion gates are still
required for calibration and enforcement decisions.

### Report migration

The v1 manifest format remains readable. Newly generated reports no longer emit
confusion-matrix outcomes for confirmed windows: old `true_positive`,
`true_negative`, `false_positive`, and `false_negative` outcomes lacked a valid
opportunity gate. Do not aggregate those historical outcomes as accuracy data.
`passed` now reflects the explicit `signal`/`quiet` assertion (or `observe`),
plus source/sample/structure checks; a review label alone does not change it.
Retain old reports and generate a new run rather than overwriting them.
Configuration pins now also include `project_rules`, so old pins require an
explicit new baseline capture; they are not silently accepted or rewritten.

## Privacy and tests

Manifests/reports include source paths, match/player identifiers and incident
coordinates; per-source databases include raw telemetry. Treat the whole output
directory as private. Do not commit it or attach it to public CI. Public tests
generate fictional player telemetry and use the committed synthetic replay only.

```powershell
go test ./internal/regression ./cmd/replay-regression ./internal/detect/throw
```

The tests exercise real ingestion/persistence, below/above-cap generated throws,
multi-session replay handling, configuration/source/evidence tampering, immutable
baselines, identity and shadow/enforcement changes, expected misses/unwanted
signals, absent samples, strict schema parsing, provenance and CLI exit codes.
