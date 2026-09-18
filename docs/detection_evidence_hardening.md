# Detection evidence hardening

This pass fixes reproduced input, continuity and evidence-integrity defects. It
does not establish a new Echo physics law or calibrate detector accuracy. The
18.9 m/s project reference is unchanged, playspacing remains paused, and the
backend remains review-only. Mags, shot-targeting, free-flight and catch-path
observations do not become verified cheating verdicts.

## Corrected boundaries

| Area | Previously possible failure | Hardened behavior |
| --- | --- | --- |
| Pipeline admission | A rejected or stale peer's phase could stop checks for valid players; rejected/unbound samples could corroborate a shared orientation fault | Only admitted phase context affects dispatch; corroboration requires valid, advancing, frame-bound observations. Negative frame identities cannot seed history. |
| Release continuity | A held sample after missing frames, a long gap or a phase transition could inherit old possession warmup and pre-release motion | Fresh uninterrupted held samples are required; release snapshots retain only the observed contiguous active suffix. Evicted frame metadata is not reconstructed with invented indices. |
| Legal-motion context | Missing previous velocity could masquerade as measured rest and fabricate a possible contact; a fallback identity rotation could appear tracked | Contact context needs an observed velocity pair across a valid interval. Rotation availability follows explicit observation metadata. |
| Release/wrist evidence | Contradictory cached angles or hand speeds could generate candidates; malformed confirmation metadata could pass when release metadata was absent | Derived caches are checked against raw vectors, attribution values are bounded, and supplied confirmation metadata must bind to the current observation. |
| Shot/free-flight evidence | An old-source release could enter current statistics; contradictory possession could appear to be free flight | Release frame/time/source binding is required. Conflicting holder fields and possible head contact interrupt free-flight review. |
| Mags evidence | Explicit possession conflicts or duplicate player identities could confirm an acquisition | Contradictory ownership becomes unavailable; identity sets are validated before any pending acquisition can be confirmed. |
| Catch-path evidence | Exact retries could destroy pending evidence; inactive phases could leave old approach history alive | Identical usable snapshots are idempotent, altered duplicates still interrupt continuity, and phase changes close pending diagnostics without producing a detection. |
| Long-session memory | Recent throw context grew for the entire match without a bound | In-memory recent throws use the configured history window; every release publication and total throw count are preserved independently. This is not a limit on stored findings. |

Reported engine release speed remains a separate observation from sampled disc
velocity: their equality is **not** required by the cache-consistency checks.
The numerical comparison tolerance covers floating-point round trips, not an
allowance for legal gameplay or measurement uncertainty.

## Verification and limitations

Each corrected class has synthetic regression coverage, including positive
controls. The focused suites are:

```powershell
go test -count=1 ./internal/pipeline ./internal/detect/throw ./internal/detect/state
go test -race -count=1 ./internal/pipeline ./internal/detect/throw ./internal/detect/state
go test -count=1 -timeout 40m ./...
```

New tests live in `admitted_context_test.go`,
`feature_extractor_hardening_test.go`, `evidence_integrity_test.go` and
`catch_integrity_test.go` in their respective packages. Detector version bumps
identify changed review behavior; source commit identity also matters because
shared extraction affects more than one detector. Re-analyze recordings to
collect current evidence; saved old findings are not silently relabeled.

These are implementation tests, not independently labeled legal/cheating
recordings. Follow [controlled comparison recordings](controlled_comparison_recordings.md)
for independent validation. Missing contact geometry, active game settings and
authoritative release/catch timing remain limitations. A conservative abstention
means insufficient evidence, not proof of fair play. Backend review-only policy
and the outstanding installer, viewer and recovery acceptance gates are unchanged.

Nonblocking backlog: an identical retry with already-degraded motion input can
still close an insufficient-data catch diagnostic as a sample gap. This cannot
confirm a catch candidate or create a scored finding; the usable-snapshot retry
path is covered separately. Do not interpret that diagnostic as a new incident.

Two older regression contracts were deliberately tightened: negative indices
now require rejection on both ingest and offline paths, and the snapshot-label
fixture now includes two fresh held samples after its missing frame. It still
asserts exact frame/time labels, with the unsupported pre-gap window excluded.
Separate negative controls require a one-held-sample post-gap release to abstain.
