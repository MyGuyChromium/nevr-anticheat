# NEVR-Anticheat Production Readiness Assessment

**Bottom line: 0 of 29 detectors are validated on real Echo VR telemetry.** Every threshold comes from game physics constants, community documentation and synthetic test data. The system is ready for a *shadow* deployment whose purpose is to collect real data; it is not ready to act on anyone.

## 1. Telemetry Compatibility Audit

### Fields confirmed available in Echo VR

| Field | Echo VR Source | Status |
|-------|---------------|--------|
| PlayerID | `/session` → `name` (emitted as `echovr:<userid>`) | Available |
| Team | `/session` → `teams[].team` (`BLUE`/`ORANGE`, case-insensitive) | Available; SPECTATORS and any other team are dropped |
| Position | `/session` → `position` [3]float64 | Available |
| Rotation | `/session` → `forward/left/up` vectors | Available, converted to a quaternion |
| LeftHandPosition / RightHandPosition | `/session` → `lhand.pos` / `rhand.pos` | Available |
| LeftHandRotation / RightHandRotation | `/session` → `lhand`/`rhand` direction vectors | Available, converted; handedness convention **unconfirmed** |
| IsStunned | `/session` → `stunned` | Available |
| HasPossession | `/session` → `possession` | Available |
| Disc.Position / Disc.Velocity | `/session` → `disc.position` / `disc.velocity` | Available; one `DiscState` per tick copied onto every player's frame |
| BlueScore / OrangeScore | `/session` → `blue_points` / `orange_points` | Available |
| Goals / Stuns | `/session` → per-player stats | Available; per-frame update granularity **unconfirmed** |
| Sample time | HTTP response time (bridge) / line timestamp (`.echoreplay`) | Available; frames carry real intervals |

### Fields NOT reliably available

| Field | Impact | Detectors Affected | Mitigation |
|-------|--------|-------------------|------------|
| IsBoosting | MOV_004, MOV_005 inert | Boost abuse undetectable | Infer from velocity spikes (future) |
| ShieldActive | STATE_003, STATE_005 inert | Shield abuse undetectable | Cannot infer; needs game server mod |
| IsImmune | STATE_004 inert | God mode undetectable via this field | Infer from spawn timing (future) |
| EstimatedPingMs | No lag compensation | All timing detectors slightly stricter | Default 0 is safe (strict) |
| InPenaltyField | THROW_007 stub | No penalty-field detection | Needs geometric derivation |

### Conversions

| Field | Issue | Status |
|-------|-------|--------|
| Rotation / HandRotation | API gives `{forward, left, up}` direction vectors, not quaternions | **DONE**: `model.QuatFromDirectionVectorsChecked` normalises forward, Gram-Schmidts up and rebuilds left = up × forward, so the result is always a proper rotation. It measures the handedness of the input (`BasisProper` vs `BasisReflected`, counted in `MapperStats.BasesProper/BasesReflected`) and handles both conventions exactly. **Which convention Echo VR really uses is still unconfirmed**: every fixture in this repo has det −1 (reflected). The first real `/session` capture settles it (the bridge logs the `basis_reflected` warning once). A degenerate basis (zero or collinear vectors) yields the **zero quaternion** (tracking loss), never a silent identity. |
| DeltaTime | Not in the raw API | **DONE**: the bridge stamps `timestamp`/`delta_time` from the real HTTP sample time per match; the replay parser uses line timestamps. dt ≤ 0 means unknown; known dt is clamped to [0.005, 0.5] s. |
| Disc.Speed | Not raw | **DONE**: derived as `|velocity|` by the adapter and, for wire producers that omit it, by the decoder. |
| holder_id | Contract used a different disc holder spelling | **DONE**: `DiscState.UnmarshalJSON` accepts `holder_id` as an alias of `possessor_id`/`is_held`. |

## 2. Detector Viability Matrix

**IMPORTANT**: no detector has been validated against real Echo VR telemetry. "PHYSICS_GROUNDED" means the detection logic has a sound physics basis and conservative thresholds, NOT that it has been proven correct on real data. Status labels below are unchanged from the original assessment; changing one requires real-data evidence.

| Detector | Status | Safe for Shadow? | Safe to score? | Primary Risk |
|----------|--------|-------------------|----------------|-------------|
| THROW_001 | **PHYSICS_GROUNDED** | Yes | Not until tolerances validated | Physics cap is real; tolerance/ping values are UNVERIFIED. Releases > 2× cap are reported as `disc_speed_artifact` at severity 0.2 and excluded from cap-riding; the old dt > 0.05 s gate is gone, so it fires on 15 Hz data. |
| THROW_002 | **BROKEN** | No (until re-tested) | No | Label retained pending data. The mechanism the label described (multi-frame acceleration over snapshots with identical velocities) was **replaced in v2.0.0** by a single delta: last pre-release disc speed vs release speed (`max_speed_delta` 22 m/s), and snapshots now exclude the release frame. The README lists it as Unverified for that reason; re-test on real replays before changing this row. |
| THROW_003 | **UNVERIFIED** | Yes (observation only) | After calibration | Wrist-flick throws exceed 45° legitimately; threshold guessed. Frame-of-reference guard skips throws where body speed ≥ hand speed. |
| THROW_004 | **UNSAFE** | Log only | No | Regrab playstyle produces low variance naturally; with the 1e-8 threshold even varied human throws fire. Degenerate dimensions are now excluded from the product, which removes one collapse mechanism but does not make the threshold meaningful. |
| THROW_005 | **UNVERIFIED** | Yes (observation only) | After calibration | "Faster throws deviate more" premise unvalidated. Correlation gate now needs ≥ 30 pairs and a Fisher-z 95 % CI upper bound < 0.1. Goal side is learned per match from the first score increment. |
| THROW_006 | **PHYSICS_GROUNDED** | Yes | Not until thresholds validated | Straight-line physics is a hard constraint; angle thresholds UNVERIFIED. Needs **5** sustained violation frames (not 7 as earlier revisions of this document said) and 5 tracked frames; the goal is fixed at release. |
| THROW_007 | **STUB** | N/A | N/A | Evaluate() returns nil; no telemetry for penalty fields |
| THROW_008 | **UNVERIFIED** | Yes (observation only) | After calibration | Tight tolerance; arena geometry may cause speed bumps. Uses game `DistanceFromThrower` when present. |
| BIO_001 | **UNVERIFIED** | Yes (rotation-format probe) | No | Rotation convention UNCONFIRMED. **Saturation:** the wrist-rate metric is bounded by π/dt = 46.9 rad/s at 15 Hz, so the 50 rad/s threshold is unreachable on bridge data (`Bio001.Reachable(0.067) == false`). Any event on 15 Hz data means the rotation pipeline is broken. |
| BIO_002 | **PHYSICS_GROUNDED** | Yes | Not until threshold validated | 50 m/s is 4× the human limit; hand positions generally available |
| BIO_003 | **UNVERIFIED** | Yes (observation only) | After calibration | Controller jitter baseline unknown; resting controllers trigger zero-jitter (activity gate mitigates) |
| BIO_004 | **UNVERIFIED** | Yes (rotation-format probe) | After calibration | Same rotation dependency as BIO_001 + wobble baseline unknown |
| MOV_001 | **PHYSICS_GROUNDED** | Yes | Not until threshold validated | Median filter + 30-frame window; 55 m/s is generous; burst branch tied to `physics.max_player_speed` |
| MOV_002 | **PHYSICS_GROUNDED** | Yes | Not until network desync quantified | Dual condition + 5-incident minimum; jumps > 12 m (`max_displacement`) are treated as resets and are invisible; desync behaviour UNVERIFIED |
| MOV_003 | **UNSAFE** | Log only | No | Wall bounces produce legitimate 180° reversals (collision radius + heading confirmation help, not enough) |
| MOV_004 | **TELEMETRY_DEPENDENT** | Only if IsBoosting present | No | IsBoosting not in standard API |
| MOV_005 | **TELEMETRY_DEPENDENT** | Only if IsBoosting present | No | Same; counts activations, `max_consecutive` limit unvalidated |
| STATE_001 | **UNVERIFIED** | Yes (observation only) | After calibration | Network desync inflates grab distance; grab range unquantified |
| STATE_002 | **PHYSICS_GROUNDED** | Yes | Not until stun duration validated | 2-incident minimum; real stun duration UNVERIFIED (measured in seconds against tick rate) |
| STATE_003 | **TELEMETRY_DEPENDENT** | Only if ShieldActive present | No | ShieldActive not confirmed in any source |
| STATE_004 | **TELEMETRY_DEPENDENT** | Only if IsImmune present | No | IsImmune not confirmed in any source |
| STATE_005 | **TELEMETRY_DEPENDENT** | Only if ShieldActive present | No | Same as STATE_003 |
| STATE_006 | **SUSPENDED** | No | No | No confirmed impossible score invariant; delta=1 proved legitimate in real profiler data |
| STATE_007 | **TELEMETRY_DEPENDENT** | Only if per-frame stun count updates | After validation | Per-frame stun count granularity UNCONFIRMED; may be round-end only |
| PAT_001 | **UNSAFE** | Log only | No | Regrab rhythm produces low CoV naturally |
| PAT_002 | **UNSAFE** | Log only | No | Consistent throwing form produces < 2 cm spread |
| PAT_003 | **CROSS_MATCH_DEPENDENT** | After history collected | After validation | Reads prior non-shadow, non-meta events; nothing until `min_matches` of history |
| PAT_004 | **PHYSICS_GROUNDED** | Yes | Not until upstream detectors validated | Meta-detector fed only with non-shadow events ≥ 0.7 confidence; in an all-shadow deployment it receives nothing |
| PAT_005 | **PHYSICS_GROUNDED** | Yes | Not until validated | 1.6 m threshold is 2× arm reach; 30-frame sustain |

### Summary

0 validated · 8 physics-grounded (incl. the PAT_004 meta-detector) · 7 unverified · 4 unsafe · 6 telemetry-dependent · 1 cross-match-dependent · 1 stub · 1 broken · 1 suspended = 29.

### Recommended initial enable list (shadow mode)

In shadow mode `enforcement_weight` has **no effect**: shadow events are never scored, whatever the weight. The split below is about which weight *would* apply after promotion, and it is what `configs/shadow_deploy.toml` encodes.

- **Enabled, weight as configured for promotion:** THROW_001, THROW_006, BIO_002, MOV_001, MOV_002, STATE_002, PAT_004, PAT_005
- **Enabled at weight 0.0 (observation only):** THROW_003, THROW_005, THROW_008, BIO_001, BIO_003, BIO_004, STATE_001, STATE_007
- **Disabled until telemetry fields confirmed:** MOV_004, MOV_005, STATE_003, STATE_004, STATE_005
- **Disabled until calibrated:** THROW_004, MOV_003, PAT_001, PAT_002
- **Disabled until history exists:** PAT_003
- **Disabled (suspended / broken / stub):** STATE_006, THROW_002, THROW_007

## 3. Integration Gap Analysis

| Gap | Impact | Severity |
|-----|--------|----------|
| **Coordinate frame / handedness.** The converter handles both handedness conventions exactly and counts which one it sees, but the convention Echo VR uses has not been observed on real data; all fixtures are reflected. | If the reflected count is 100 % on real data the fixtures were right and nothing changes; if it is mixed, the source is inconsistent and hand-rotation detectors must stay observation-only. | **MEDIUM** — resolved by the first real capture |
| **Disc state during possession.** The adapter now emits one disc per tick with `possessor_id`/`is_held` from the first possessing player in team order (multi-holder ticks counted as `PossessionConflicts`). Whether Echo VR reports disc velocity as zero, player velocity or stale while held is still unknown. | Release speed (THROW_001/002) and hand-to-disc distance could be wrong. | **HIGH** — needs empirical validation |
| **Stun count granularity.** STATE_007 needs per-frame `stuns` increments. | STATE_007 may be dead on live data. | **HIGH** |
| **Replay format.** The `.echoreplay` parser exists (`internal/adapter/replay_parser.go`): NDJSON lines of `timestamp\tJSON`, optionally inside a ZIP, streamed per tick. It is what `analyze`/`batch` use. The `internal/replay` `FrameParser` interface and its JSON parser remain for legacy JSON replays only. | None for offline mode; comments that still mention protobuf are stale. | **LOW** |
| **Live path statefulness.** The pipeline keeps per-player state across batches (`SetSkipReset`), so inline detection works even with one frame per batch. Inline results are `analysis_source = initial`; reprocessing stays canonical. | None; earlier revisions of this document called inline detection non-functional. | **LOW** |
| **Trust boundary.** Everything the bridge forwards was pulled over plaintext HTTP from whatever host the Nakama match label advertises. Every batch/control message carries `server_id = "<ip>:<port>"`, and `--broadcaster-allowlist` restricts polling to known hosts. There is no authentication of the broadcaster itself. | A hostile or misconfigured broadcaster can inject arbitrary telemetry (and therefore detection events) for real player IDs. | **HIGH** — always run with an allowlist; treat evidence from unknown `server_id` values as untrusted |

## 4. Producers

| Producer | Status |
|----------|--------|
| **EchoReplayParser** (`internal/adapter`) | DONE. NDJSON/ZIP `.echoreplay`, real line timestamps, spectators dropped, team names mapped, one disc per tick, diagnostics report (`nevr-compat --replay`). |
| **Live bridge** (`cmd/bridge`) | DONE. Nakama device-auth discovery, `/session` polling with real sample times (`MapSessionAt`), per-match monotonic frame epochs, hello/ack protocol, bounded send queue, `match_start`/`match_end` with team rosters, `--probe`/`--once` validation modes, `--broadcaster-allowlist`. |
| **Legacy JSON replay reader** (`internal/replay`) | Kept for the old JSON layout; physics from `DefaultPhysics`. |

Unknown mapping points that only real data can settle: hand-basis handedness, disc velocity while held, whether `possession` is ever true for more than one player, whether `stuns` updates per frame, whether `is_boosting`/`shield_active`/`is_immune` exist anywhere.

## 5. Why This Could Still Fail in Production

### Telemetry drift
Echo VR's API has changed between versions. A renamed or removed field makes the ingestion layer emit zero-valued data, and detectors go silent (false negatives) or fire on garbage (false positives). `nevr-compat --strict` and the bridge manifest (`errors_count`, `warnings_count`, `mapped_frames`) are the only schema checks; they are manual.

### Coordinate space
Y-up is confirmed from real replays (X ±5 m, Y −4..+7 m, Z ±77 m, goals at Z ≈ ±36.078). Handedness of the direction-vector bases is handled either way but unconfirmed (see §3).

### Replay inaccuracy
`.echoreplay` files are client-side recordings with interpolated positions. **Every threshold calibrated on replay data is calibrated on interpolated data, not server truth.**

### Timing
Producers now report real sample intervals (bridge: HTTP sample time; replays: line timestamps), dt ≤ 0 is treated as unknown and known values are clamped to [0.005, 0.5] s, so the "dt clamping handles variable intervals" claim is now true. Kinematics from variable-dt frames are still noisier than uniform synthetic data, and **the bridge's 15 Hz poll bounds what can be observed**: BIO_001 cannot exceed 46.9 rad/s, single-frame teleports shorter than 67 ms are invisible, and THROW_002's pre-release snapshot is one 67 ms sample.

### Overfitting to synthetic data
The test suite passes on synthetic telemetry (`internal/testutil/synthetic.go`: sinusoidal motion, deterministic jitter, idealised throws). Real gameplay has chaotic collisions, network reconciliation artifacts, tracking occlusion, playspace-dependent hand positions and controller-specific noise. **None of these are modelled.** The first real match will reveal false-positive patterns not seen in testing.

### UNSAFE detectors that look safe
THROW_004, PAT_001, PAT_002 and MOV_003 pass their tests because the tests avoid triggering them on legitimate data. **They will false-positive on real skilled players.** Keep them disabled until calibrated on thousands of real matches.

### Moderator misuse
Scores are evidence pointers, not verdicts. The single-match and cross-match cases put the evidence in front of a moderator; `verdict` records the decision and `calibration-report` turns decisions into per-detector precision. **A moderator who acts on a score alone defeats the false-positive prevention design.** No automatic action exists: `enforce.Engine` and `enforce.Policy` are implemented but not wired into any binary.

### Single point of failure: feature extractor
All 29 detectors depend on the feature extractor. A single bug in velocity or throw derivation silently breaks everything downstream. There is no independent cross-validation of derived features.

### THROW_002
The v2.0.0 mechanism (last pre-release speed vs release speed) is untested on real data. The label stays BROKEN until a real replay shows the pre-release snapshot carries a genuine, non-identical disc velocity.

### Storage growth
Telemetry is never pruned automatically; `nevr-server` prunes detection events after 90 days and score snapshots after 30 days. Raw `.echoreplay` ticks are stored once per tick in `match_ticks`. Budget disk accordingly.

## 6. Calibration Findings from Real Data (historical)

**Data source**: nevr-anticheat.db — 17,498 detection events across 8 matches, 43 players, produced by an **earlier build**. The numbers are kept as a record of what was learned; they must be regenerated with the current binary (`nevr-ac reprocess-timerange <since> <until>` over the same matches, recording the commit hash and config next to the results) before any threshold decision.

| Detector | Events/Match (old build) | What changed since |
|----------|-------------------------|--------------------|
| BIO_002 | ~913 | Generated with the old 15 m/s threshold; current config is 50 m/s. Reprocess. |
| BIO_003 | ~280 | Activity gate and log-ratio severity added; needs baseline calibration. |
| BIO_004 | ~238 | Same, plus zero-quaternion tracking-loss handling. |
| THROW_006 | ~137 | Bounce filter narrowed, alignment gate 0.7, minimum violation frames **5**, goal fixed at release. |
| THROW_008 | ~118 | Uses game distance-from-thrower; first 3 frames not judged. |
| MOV_002 | ~91 | Jumps > 12 m treated as resets; distinct-player cluster suppression; goal cooldown. |
| BIO_001 | ~80 | 3-frame sustained floor; note the 15 Hz saturation. |
| THROW_001 | ~60 | dt gate removed; releases > 2× cap (37.4 m/s) now reported as `disc_speed_artifact` at severity 0.2 instead of being dropped; cap-riding once per 900 frames. |
| STATE_004 | ~54 | Escalating re-fire with tolerance. |

Key conclusions that still hold: THROW_001's over-cap events (22–96 m/s against a ~20 m/s effective cap) are genuinely impossible *if the velocity is real*; the 2×-cap artifacts are now visible rather than hidden so that question can be answered. One match (`6BEF4CA8`) produced ~90 % of all events; a broader sample is required.

## 7. Decisions pending (owner)

Collected from the quality-pass fixer reports. Each has a conservative default in place.

1. **Goal side per team.** No convention found; the extractor learns the side per match from the first score increment. Confirm from a replay and, if fixed, set `FeatureExtractor.SetBlueGoalSide(±1)` via a config key.
2. **THROW_001 auto-enforcement** stays off (`auto_enforce = false`). Enabling it makes any possession-tracked release > ~25 m/s carry `AutoEnforce`. Keep off until the 22–96 m/s events are explained.
3. **Releases > 2× cap** are `disc_speed_artifact` at severity 0.2. Decide after re-running calibration whether they are timing artifacts (keep low) or injection (route to the disc_speed path).
4. **THROW_005 correlation gate** (n ≥ 30, CI upper < 0.1) is deliberately conservative; keep observation-only until measured on real players.
5. **THROW_004** remains UNSAFE/disabled; recalibrate the 1e-8 threshold or retire the detector.
6. **BIO_001 at 15 Hz**: keep 50 rad/s (unreachable on bridge data, observation-only) or calibrate a 15 Hz-attainable value < 46.9 rad/s from real data. Kept at 50.
7. **MOV_002 `max_displacement`** kept at 12 m (teleports beyond that are invisible); raising it to ~40 m extends coverage and relies on the cluster/goal filters. Needs a calibration re-run.
8. **BIO_001 `min_violation_frames`**: the code floors it at 3; the config now says 3 so the effective table is truthful. Remove the floor if 2-frame streaks are wanted.
9. **PAT_002 `min_release_speed`** is 0 (drops sampled like throws); 3–5 m/s would exclude drops but is a real-data choice.
10. **MOV_005 `max_consecutive`** stays at the code default 5 activations; the old 8-frame limit was a different unit and was not applied.
11. **Review threshold** is now 60 (= high_risk) everywhere; cross-match cases therefore start at the high_risk tier. `auto_enforce_threshold` moved to 95 (= action_worthy) because it must not be below the review threshold.
12. **Case creation tier** is high_risk (`review.WithMinLevel` can lower it to suspicious for "monitoring" cases).
13. **Cross-match case status**: a moderator-set status is never reopened by re-aggregation, even when new matches add evidence.
14. **Calibration counts shadow events** inside decided cases (that is how shadow detectors earn promotion); add `AND is_shadow = 0` if you want the opposite.
15. **Decay fallback**: events whose match has no start time are decayed from storage time (count reported); the stricter alternative is no decay for them.
16. **Change detection** (`Mapper.SetDedupeIdentical`) is on for the bridge and off for replay parsing; decide whether `analyze`/`batch` should enable it.
17. **Reflected basis**: fixtures are reflected; no threshold was changed on that basis. The first real capture decides.
18. **Unnamed team entries** fall back to array index (0 = blue, 1 = orange); real payloads always carry `team`.
19. **Bridge defaults**: `--session-check strict`, `--modes echo_arena`, `--nakama-auth device` (creates a `nevr-anticheat-bridge` account), `--ack-timeout 30s`, `--idle-give-up 10m`. Confirm each against the target Nakama.
20. **Ingest defaults**: idle deadline 5 min, ack interval 1 s, persist interval 2 min, 64 matches, 16 players, stale finalization 30 min, dedup merge window 20 frames; NaN disc data nils the disc, NaN hands become the tracking-loss sentinel (flip to rejection if preferred).
21. **Live score snapshots** are written on tier change and at match end; never for score ≤ 0.
22. **Rate limiter** is first-N-wins per player/detector; a severity-aware top-K would need to hold events until match end.
23. **Anti-evasion window randomizer** is unused/EXPERIMENTAL; production wiring should use `NewWindowRandomizerWithSecret`.
24. **`sudden_death`** is active; `pre_sudden_death`/`post_sudden_death` are not. Confirm against real status strings.
25. **`enforce` temp-ban recommendation** (7 days) stays unreachable until auto-enforce and cross-match inputs are supplied — deliberate.
26. **`tests.test`** (a 12 MB `go test -c` binary) was removed from the index and `*.test` is now ignored; nothing further to do.
