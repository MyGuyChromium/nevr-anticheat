# Detector decision tracing

Decision tracing explains what this build actually did with a recording. It is
not a cheat verdict, calibrated accuracy, proof of fair play, or a reconstruction
of motion the recorder did not sample. Thresholds, shadow modes, scoring order
and enforcement policy are unchanged by the observer.

## Stored contract

Each `PlayerCoverage.detectors[]` record can contain `decision_trace`:

```json
{
  "version": 1,
  "internal_branches": true,
  "reasons": [
    {
      "code": "release_at_or_below_cap",
      "description": "Observed release speed did not exceed the configured effective cap",
      "count": 4,
      "first_frame": 100,
      "last_frame": 500
    }
  ],
  "overflow_count": 0
}
```

The additive field travels with the existing coverage JSON. Old analyses with
no trace have **no recorded explanation**; re-analysis is necessary. Do not
invent reasons for them or infer a failed guard from a zero score.

The synchronous optional `detect.DecisionObservable` interface records fixed
codes inside the branches being executed. It does not run detector guards a
second time or alter telemetry. Detectors without that interface remain
compatible. `internal_branches: false` explicitly means only pipeline stages
were observed; absence of internal reasons is not evidence that every detector
input was usable.

Traces store at most 48 unique reason codes per player/detector. Further or
malformed codes increment `overflow_count`. Counts update in place; there is no
per-frame trace history or raw telemetry in a reason. Sorting is descending
count, then code. Observer references detach on success and cancellation, so a
later match cannot mutate an earlier result.

## Reading the counts

- `frame_rejected`, `inactive_phase`, `warming_up` and `detector_evaluated`
  describe actual validation and dispatch, not estimated opportunity coverage.
- `no_raw_emission` means that evaluation returned none for that player. It
  does not mean the detector could observe every possible behavior.
- `raw_emission_returned` is **before** event validation, source-quality/contact
  handling, merging, rate limits and scoring.
- `invalid_emission_dropped`, `emission_merged` and `incident_rate_limited`
  explain distinct reasons a raw emission did not become another final incident.
- `quality_confidence_reduced`, `quality_forced_shadow`, and the `context_*`
  reasons name actual processing branches. Context flags are possible legal
  explanations, not authoritative collision/contact identification.
- `incident_retained_shadow` / `incident_retained_review` count final incidents
  returned by the pipeline. **They are not database-write receipts.** The
  persisted event list and shared assessment remain the source of displayed
  review signal totals.
- `incident_scored` / `incident_not_scored` record the scorer's actual acceptance
  result. Shadow observations can require review while adding zero score.

Counts are not mutually exclusive, and they are not TP/FP/TN/FN opportunities.
Two hand checks can increment a wrist reason twice on one frame. Some throw
detectors inspect retained player context as well as fresh players; these visits
use the separate `stale_player_context` reason, never a sampled `no_current_release`.
Delayed track flushes can return an emission after the frame it describes.
`first_frame` / `last_frame` are source-frame bounds associated with a reason;
emission/incident stages use the event's source frame, not the flush-call time.

For complete offline `ProcessMatch`, the trace includes final track/dedup flush.
In live mode it describes only that `ProcessMatch` batch, not all prior batches.
The existing separate live `Finalize` result does not carry coverage; callers
must not present a single batch trace as a complete live-match accounting.
Failed/cancelled analysis is not a completed trace.

## Detailed detector scope

| Detector | Actual branches named |
| --- | --- |
| THROW_001 | Current release, usable speed, attributed engine measurement availability, hand-ratio availability, sampled artifact band, above/below effective cap |
| THROW_002 | Current release, pre-release snapshot availability/ordering, configured speed-delta condition |
| THROW_003 | Current release, hand attribution/kinematics, possible head contact, minimum motion, angle condition, body-translation guard |
| BIO_001 | Stun/immunity/interval guards, per-hand threshold and streak/emission conditions |
| MOV_001 | Window warmup, median/burst conditions |
| MOV_006 | Residual availability/speed/displacement, tracked hands, observed pose speed, coherence, ping, continuity, duration, repeat-burst suppression |

Every detector has pipeline-stage accounting when dispatched. The stable code
descriptions live in `internal/detect/diagnostics.go`; new detailed branch support
must set `TraceBranches` only when actual evaluation paths are instrumented.

## Conservative continuity fix

MOV_006 version 1.2.1 also fixes a demonstrated decision bug: qualifying samples
on either side of a missing observation could previously accumulate into one
"sustained" burst. It now restarts that burst on nonconsecutive frame indices.
No speed, displacement, duration, confidence, shadow or enforcement threshold
was changed. A production-pipeline synthetic 30 Hz regression has two separate
nine-frame runs (each only 0.267 seconds), separated by one omitted observation;
the missing sample no longer supplies the time needed to cross the 0.3-second
duration rule. This test uses the real extractor, whose gap is still short
enough to reconstruct a residual, not just prebuilt detector state.

## Legal mechanics and unresolved evidence

Tests cover matching game velocity (including synthetic stacking, a small
game-authored push and boost), a short coherent lean, possible head contact,
unattributed/slap-like hand motion, brief wrist transients, body translation and
near-cap releases. They assert the actual guard or confidence reduction, so
passing due solely to missing inputs or warmup cannot masquerade as protection.
An expressible 60 Hz wrist fixture avoids mistaking the 15 Hz sampling ceiling
for successful wrist discrimination.

These are regression invariants, **not** independent real-world labels. In
particular:

- Recorded poses have no feet or guardian origin, so MOV_006 cannot prove
  physical walking instead of a lean/lunge.
- A sampled hand rotation is bounded by pi / sample interval. A 50 rad/s
  threshold is unreachable at 15 Hz. Tracing does not recover aliased motion.
- No authoritative wall/block/contact telemetry proves that a particular
  observed impulse was a slap, headbutt, stack or push. Current context handling
  is deliberately conservative and incomplete.
- The configured disc cap and movement/throw inheritance model still require
  independently reviewed, sufficiently sampled evidence. A sampled disc velocity
  can already include contact after release. No threshold was promoted here.

Verification:

```powershell
go test -race ./internal/model ./internal/detect/... ./internal/pipeline ./tests
```
