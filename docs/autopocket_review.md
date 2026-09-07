# Autopocket catch review (STATE_008)

Desktop 0.12.0 adds a conservative **trajectory observation**, not a verified autopocket verdict. It finds a particular pattern: a steady free disc makes sustained, position-and-velocity-supported corrections that bring it closer to a moving receiver's hand, followed by possession confirmed in two consecutive samples. It does not detect every meaning of “autopocket.” A grip macro that performs an otherwise normal catch may leave no observable trace.

The receiver is an index for reviewing the catch, **not an attribution of who caused the path change**. A thrower's modification, legal contact, unseen geometry or network prediction/correction could affect an innocent receiver's disc approach. The implementation always emits shadow events with zero enforcement weight and no automatic enforcement. Configuration and the calibration promotion API reject attempts to turn this detector into scored enforcement. Changing that policy requires a separately reviewed implementation and independently validated evidence, not just a threshold tweak.

## What the sources establish—and what they do not

- EchoTools' runtime reads the disc position, velocity and bounce counter from the game and samples telemetry periodically. Those are observations, not authoritative collision impulses or a complete physics solver. See the pinned [telemetry reader](https://github.com/EchoTools/nevr-runtime/blob/a692a304a79a75564f6b660ea81a96137b433357/src/runtime/server/telemetry_streamer.cpp) and [snapshot representation](https://github.com/EchoTools/nevr-runtime/blob/a692a304a79a75564f6b660ea81a96137b433357/src/runtime/server/telemetry_snapshot.h).
- The public [engine HTTP schema](https://github.com/EchoTools/nevr-proto/blob/2450c39d820bf496913e6150ef2e368266987905/engine/v1/engine_http.proto) describes the available disc and player snapshots. The richer [telemetry schema](https://github.com/EchoTools/nevr-proto/blob/2450c39d820bf496913e6150ef2e368266987905/telemetry/v2/echo_arena.proto) does not establish remote grip-button truth for arbitrary replay players; capture-client input fields are not a substitute.
- The runtime's [symbol inventory](https://github.com/EchoTools/nevr-runtime/blob/a692a304a79a75564f6b660ea81a96137b433357/src/runtime/log/symcache_data.cpp) names catch/prediction parameters. A symbol name does not supply its runtime value, exact catch law or legal acceleration limit. Nakama's [telemetry stream service](https://github.com/EchoTools/nakama/blob/a52ae6b6f1ab4e89f2f757064c4c06d6a3b0251b/server/evr_telemetry_streams.go) transports observations; it is not that missing solver.

Consequently, the comparison below is an engineering heuristic with explicit abstentions. It is not a reconstructed exact Echo VR simulation, probability of cheating, or substitute for Spark/video review.

## Comparison and exclusions

1. Require a complete fresh sampled roster (at most 16 players), finite disc data, known nonconflicting possession, an explicitly present bounce counter, and tracked head/body/both hands for every player. Missing is unknown, including a missing bounce counter; it is never silently treated as zero/no-contact. Spectators are excluded. A rejected player sample leaves the roster incomplete.
2. Collect at least four steady free-disc samples. Fit the position progression against the mean reported velocity. The reference then continues at constant velocity.
3. Require at least two sustained directional corrections supported by both sampled positions and reported velocities. Reject single discontinuities, position/velocity disagreement, large abrupt turns, speed jumps, sampling gaps/jitter, roster changes and tracking discontinuities.
4. Compare each relevant free-disc segment with simultaneously moving hands, heads and bodies. A possible contact inside the conservative margin discards the comparison, including contacts between sample endpoints. A bounce-counter change also discards it. Geometry and nonlinear motion between samples remain unknown.
5. Compare actual and reference paths against each moving receiver hand, at matching times. Require a material reduction in closest approach and an approaching final free sample, then the same unique holder in two consecutive samples. Recent releases/regrabs are excluded.
6. Store a bounded free-disc trajectory, moving hand tracks, metrics, attribution limitations and branch reasons. Never use the first held-disc attachment displacement or velocity to measure a correction. No catch confirmation means no retained observation.

History is capped at 32 snapshots per detector, independent of replay length. Playback/re-analysis starts with fresh detector state. Limits intentionally sacrifice sensitivity when the available data cannot support a useful comparison; missing a finding does not establish fair play.

## Provisional configuration

All distances are metres and times seconds. These defaults and constructor bounds are sampling/review filters, **not verified legal limits**. They are centrally documented in `internal/config/params.go` and included in both shipped TOML files.

| Parameter | Default | Constructor bounds |
| --- | ---: | --- |
| `baseline_samples` | 4 | 4–12 |
| `min_correction_samples` | 2 | 2–8 |
| `max_sample_gap_s` | 0.12 | 0.02–0.20 |
| `max_window_s` | 1.5 | 0.25–2.0 |
| `max_step_error_m` | 0.20 | 0.01–0.50 |
| `min_lateral_deviation_m` | 0.30 | 0.10–3.0 |
| `min_correction_angle_deg` | 4 | 1–10 |
| `max_turn_angle_deg` | 20 | 10–30 |
| `contact_margin_m` | 0.65 | 0.50–2.0 |
| `min_miss_improvement_m` | 0.50 | 0.25–3.0 |
| `max_catch_approach_m` | 1.5 | 0.50–2.0 |
| `regrab_grace_s` | 0.35 | 0.25–1.0 |

Additional fixed conservative filters include minimum free-disc speed 2 m/s, adjacent speed ratio 0.75–1.25, interval ratio at most 2, stable-reference direction tolerance 3°, and two-sample holder confirmation. They are not cheat thresholds. Out-of-range finite parameter inputs are bounded by the constructor; nonfinite direct constructor inputs fall back safely. The effective config table displays configured input values, not a substitute for reading those bounds. Prefer staying within the documented ranges.

## Reviewing a result

Re-import the **original replay** after updating. Re-analysis of an older normalized-only cache may lack newly preserved head/bounce/possession metadata and must abstain. No database schema migration or rewriting of original replay files is needed for these additive JSON fields.

Filter detector observations by `STATE_008` / Autopocket Catch Review, inspect the event's physics and open its existing Spark replay clip. The inspector shows X/Y, X/Z and Y/Z projections of the free disc, steady-motion reference, and both receiver hands. Each projection uses equal horizontal/vertical metre scales but may have a different span from the other projections. The larger disc marker is the last **free** sample, not the held attachment. Raw typed evidence remains available alongside the plot. Trajectory plots are hidden in blinded review.

Coverage reports `catch_inputs_ready` only when the detector's complete-roster checks actually pass. That is input availability, not proof a catch opportunity existed. `catch_approach_evaluated` reports a completed reference/catch comparison after the second possession sample; it is not an independently labelled statistical denominator. Other branch reasons explain contact exclusions, unavailable inputs, discontinuities and insufficient correction. Do not interpret silence as “autopocket checked and cleared.”

## Verification and calibration boundary

Regression tests exercise a synthetic sustained curve, straight/moving-hand catches, ordinary held attachment, single deflections, bounces, possible hands/head/body contact (including between samples), regrabs, missing/contradictory metadata, timestamp gaps, corrupt numbers, deterministic reset, bounded history and multiple sampling rates. Adapter, persistence, pipeline, promotion and rendering tests protect the surrounding safety contracts.

Synthetic positives demonstrate that the code recognizes its constructed pattern, **not that real autopocket necessarily produces it**. Deployment accuracy still needs independently confirmed autopocket examples, exact event windows, source/version metadata, and a diverse legal holdout set covering leans, slaps, headbutts, interceptions, regrabs and network artifacts. Never whitelist a player's identity or label everyone in a “fair” player's match as fair. Do not tune and report accuracy on the same replay set. No precision, recall or calibrated confidence claim is made by this release.
