# NEVR-Anticheat Production Readiness Assessment

## 1. Telemetry Compatibility Audit

### Fields confirmed available in Echo VR

| Field | Echo VR Source | Status |
|-------|---------------|--------|
| PlayerID | `/session` → `name` | Available |
| Position | `/session` → `position` [3]float64 | Available |
| Rotation | `/session` → `forward/left/up` vectors (need quat conversion) | Available (needs conversion) |
| LeftHandPosition | `/session` → `lhand.pos` | Available |
| RightHandPosition | `/session` → `rhand.pos` | Available |
| LeftHandRotation | `/session` → `lhand` direction vectors | Available in replays; needs conversion from API |
| RightHandRotation | `/session` → `rhand` direction vectors | Available in replays; needs conversion from API |
| IsStunned | `/session` → `stunned` | Available |
| HasPossession | `/session` → `possession` | Available |
| Disc.Position | `/session` → `disc.position` | Available |
| Disc.Velocity | `/session` → `disc.velocity` | Available |
| BlueScore/OrangeScore | `/session` → `blue_points`/`orange_points` | Available |
| Goals/Stuns | `/session` → per-player stats | Available |

### Fields NOT reliably available

| Field | Impact | Detectors Affected | Mitigation |
|-------|--------|-------------------|------------|
| IsBoosting | MOV_004, MOV_005 dead | Boost abuse undetectable | Infer from velocity spikes (future) |
| ShieldActive | STATE_003, STATE_005 dead | Shield abuse undetectable | Cannot infer; needs game server mod |
| IsImmune | STATE_004 dead | God mode undetectable via this field | Infer from spawn timing |
| EstimatedPingMs | No lag compensation | All timing detectors slightly more strict | Default 0 is safe (strict) |
| InPenaltyField | THROW_007 stub | No penalty field detection | Needs geometric derivation |

### Fields with conversion needed

| Field | Issue | Fix Required |
|-------|-------|-------------|
| Rotation | API gives direction vectors, not quaternion | Add `DirectionVectorsToQuat()` converter in replay adapter |
| HandRotation | Same as Rotation | Same converter |
| DeltaTime | Not in raw API; derived from timestamps | Already handled by feature extractor |
| Disc.Speed | Not raw; derived from velocity magnitude | Already handled by `convertFrame()` |

## 2. Detector Viability Matrix

**IMPORTANT**: No detector has been validated against real Echo VR telemetry. All
thresholds are derived from game physics documentation and synthetic test data.
"PHYSICS_GROUNDED" means the detection logic has a sound physics basis and
conservative thresholds, NOT that it has been proven correct on real data.

| Detector | Status | Safe for Shadow? | Safe for Enforce? | Primary Risk |
|----------|--------|-------------------|-------------------|-------------|
| THROW_001 | **PHYSICS_GROUNDED** | Yes | Not until tolerances validated | Physics cap is real; tolerance/ping values are UNVERIFIED |
| THROW_002 | **BROKEN** | No (bad data) | No | Pre-release frame bug: all snapshots have same disc velocity |
| THROW_003 | **UNVERIFIED** | Yes (observation only) | After calibration | Wrist-flick throws exceed 45° legitimately; threshold guessed |
| THROW_004 | **UNSAFE** | Log only | No | Regrab playstyle produces low variance naturally |
| THROW_005 | **UNVERIFIED** | Yes (observation only) | After calibration | Close-range shots have inherently low deviation; threshold guessed |
| THROW_006 | **PHYSICS_GROUNDED** | Yes | Not until thresholds validated | Straight-line physics is a hard constraint; angle thresholds UNVERIFIED |
| THROW_007 | **STUB** | N/A | N/A | Evaluate() returns nil; no telemetry for penalty fields |
| THROW_008 | **UNVERIFIED** | Yes (observation only) | After calibration | Tight tolerance; arena geometry may cause speed bumps |
| BIO_001 | **UNVERIFIED** | Yes (if rotation format correct) | No | Hand rotation format UNCONFIRMED — may receive garbage input from live API |
| BIO_002 | **PHYSICS_GROUNDED** | Yes | Not until threshold validated | 50 m/s threshold is 4× human limit; hand positions generally available |
| BIO_003 | **UNVERIFIED** | Yes (observation only) | After calibration | Controller jitter baseline unknown; resting controllers trigger zero-jitter |
| BIO_004 | **UNVERIFIED** | Yes (if rotation format correct) | After calibration | Same rotation format risk as BIO_001 + wobble baseline unknown |
| MOV_001 | **PHYSICS_GROUNDED** | Yes | Not until threshold validated | Median filter + 30-frame window is robust; 55 m/s threshold is generous |
| MOV_002 | **PHYSICS_GROUNDED** | Yes | Not until network desync quantified | Dual-condition + 5-incident minimum is defensive; desync behavior UNVERIFIED |
| MOV_003 | **UNSAFE** | Log only | No | Wall bounces produce legitimate 180° reversals |
| MOV_004 | **TELEMETRY_DEPENDENT** | Only if IsBoosting present | No | IsBoosting not in standard API |
| MOV_005 | **TELEMETRY_DEPENDENT** | Only if IsBoosting present | No | Same + sequence counter design limitation |
| STATE_001 | **UNVERIFIED** | Yes (observation only) | After calibration | Network desync inflates grab distance; grab range unquantified |
| STATE_002 | **PHYSICS_GROUNDED** | Yes | Not until stun duration validated | 2-incident minimum is conservative; real stun duration UNVERIFIED |
| STATE_003 | **TELEMETRY_DEPENDENT** | Only if ShieldActive present | No | ShieldActive not confirmed in .echoreplay format |
| STATE_004 | **TELEMETRY_DEPENDENT** | Only if IsImmune present | No | IsImmune not confirmed in .echoreplay format |
| STATE_005 | **TELEMETRY_DEPENDENT** | Only if ShieldActive present | No | ShieldActive not confirmed (same as STATE_003) |
| STATE_006 | **SUSPENDED** | No | No | No confirmed impossible score invariant; delta=1 proved legitimate in real profiler data |
| STATE_007 | **TELEMETRY_DEPENDENT** | Only if per-frame stun count updates | After validation | Per-frame stun count granularity UNCONFIRMED; may be round-end only |
| PAT_001 | **UNSAFE** | Log only | No | Regrab rhythm produces low CoV naturally |
| PAT_002 | **UNSAFE** | Log only | No | Consistent throwing form produces <2cm spread |
| PAT_003 | **CROSS_MATCH_DEPENDENT** | After history collected | After validation | Wired in cmd/anticheat + cmd/server; needs min_matches of prior data in DB |
| PAT_004 | **PHYSICS_GROUNDED** | Yes | Not until upstream detectors validated | Meta-detector; accuracy depends entirely on upstream detector quality |
| PAT_005 | **PHYSICS_GROUNDED** | Yes | Not until validated | 1.6m threshold is 2× physical arm reach; sustained 30-frame window |

### Summary: 0 confirmed, 7 physics-grounded (unvalidated), 6 unverified, 4 unsafe, 6 telemetry-dependent, 1 cross-match-dependent, 1 stub, 1 broken, 1 suspended, 1 meta (physics-grounded)

### Recommended Initial Enable List (Shadow Mode)

**Enable in shadow with scoring weight (physics-grounded, conservative thresholds):**
THROW_001, THROW_006, BIO_002, MOV_001, MOV_002, STATE_002, PAT_004, PAT_005

**Enable in shadow at weight=0 (observation only — collect baseline data):**
THROW_003, THROW_005, THROW_008, BIO_001, BIO_003, BIO_004, STATE_001

**Disable until telemetry fields confirmed:**
MOV_004, MOV_005, STATE_003, STATE_004, STATE_005, STATE_007

**Disable until calibrated with real data:**
THROW_004, MOV_003, PAT_001, PAT_002

**Disable until sufficient match history collected:**
PAT_003 (wired in cmd/anticheat + cmd/server; needs min_matches of prior DB data)

**Disable (suspended — no confirmed invariant):**
STATE_006 (delta=1 proved legitimate; no impossible score delta confirmed)

**Disable (broken / stub):**
THROW_002 (pre-release frame data wrong), THROW_007 (Evaluate returns nil; no penalty field data)

## 3. Integration Gap Analysis

### Critical gaps between implementation and real Echo VR data

| Gap | Impact | Severity |
|-----|--------|----------|
| **Coordinate frame assumption**: Code assumes Y-up right-handed coordinates. Echo VR uses Y-up but with specific axis conventions for forward/left/up that may differ from the assumed quaternion representation. | All spatial calculations could be wrong if axes are flipped. | **CRITICAL** — must be validated with first real data |
| **Hand rotation format**: Echo VR API provides hand pose as `{pos, forward, left, up}` direction vectors, not quaternions. The replay adapter `convertFrame()` directly casts `[4]float64` to Quat, which only works if the source is already a quaternion (true for .echoreplay protobuf, false for live API). | BIO_001 and BIO_004 receive garbage data from live API. | **HIGH** |
| **Disc state during possession**: When a player holds the disc, does Echo VR still report disc position/velocity? If disc velocity is zero while held and only set at release, the feature extractor's throw detection may work. If disc position matches player position while held, the hand-to-disc distance is wrong. | Throw detection may fail or produce wrong evidence. | **HIGH** — needs empirical validation |
| **Score update frequency**: Code assumes per-frame score updates. If Echo VR only updates score on goal events (not every tick), the score delta between frames where a goal happened could be 0,0,0,...,2 instead of smooth updates. | STATE_006 works correctly with sparse updates (delta=2 is valid). No issue. | **LOW** |
| **Stun count granularity**: STATE_007 assumes per-frame stun count increments. If stun stats are only updated at round/match end, the detector is dead. | STATE_007 may be non-functional on live data. | **HIGH** |
| **Replay protobuf schema**: The `FrameParser` interface abstracts the actual replay format, but no concrete protobuf parser exists. The `JSONFrameParser` is for testing only. | Cannot process real .echoreplay files without implementing the protobuf parser. | **CRITICAL** for offline mode |

## 4. Real Replay Adapter

The adapter interface is already defined in `internal/replay/reader.go`:

```go
type FrameParser interface {
    Open(path string) error
    Close() error
    Header() (*ReplayHeader, error)
    NextFrame() (*RawFrame, bool, error)
}
```

### What needs to be implemented for real Echo VR data:

1. **EchoReplayParser** — implements FrameParser for `.echoreplay` protobuf files
   - Requires: nevr-common/v4 protobuf definitions
   - Maps: protobuf player entries → `RawPlayerFrame`
   - Maps: protobuf disc state → `RawDiscFrame`
   - Handles: quaternion extraction from protobuf rotation fields

2. **LiveAPIAdapter** — implements FrameParser-like interface for polling `/session` endpoint
   - Polls at configurable interval (default 67ms = 15fps)
   - Maps: JSON player objects → `RawPlayerFrame`
   - Converts: direction vectors → quaternions via `DirectionVectorsToQuat(forward, left, up)`
   - Derives: delta_time from wall-clock polling interval

### Unknown mapping points (must be validated with real data):

| Source Field | Target Field | Uncertainty |
|-------------|-------------|-------------|
| `.echoreplay` player rotation format | `Quat [4]float64` | Is it (x,y,z,w) or (w,x,y,z)? |
| `/session` hand pose `forward/left/up` | Hand rotation `Quat` | Which axis convention? |
| Disc velocity when held | `DiscState.Velocity` | Zero or player velocity? |
| `possession` array format | Per-player `HasPossession` bool | Array of player IDs or single holder ID? |
| Boost state availability | `IsBoosting` | Available in API? In replays? |

## 5. Why This Could Still Fail in Production

### Telemetry Drift
Echo VR's API has changed between versions. Field names, value ranges, and available data have shifted without notice. If a game update adds, removes, or renames a field, the ingestion layer will silently produce zero-valued or missing data, and detectors will either go silent (false negatives) or fire on garbage (false positives). **There is no telemetry schema validation that alerts on unexpected field changes.**

### Coordinate Space Mismatch
The entire codebase assumes a specific coordinate convention (Y-up, right-handed). If Echo VR uses a different convention (e.g., Z-up, or left-handed), every distance calculation, velocity computation, and angle comparison is wrong. This cannot be detected from synthetic data. **It will only be discovered on the first real replay.**

### Replay Inaccuracy
`.echoreplay` files are client-side recordings, not server-authoritative. They contain interpolated positions, not raw server state. The interpolation introduces smoothing that makes legitimate movement look smoother than it actually was on the server, and can create artifacts (position jumps, velocity spikes) at interpolation boundaries. **Every threshold calibrated on replay data is calibrated on interpolated data, not ground truth.**

### Timing Artifacts
Frame timestamps in replays are not guaranteed to be uniform. The spec says ~15 FPS but actual frame intervals vary (50ms to 200ms). The feature extractor handles this via dt clamping, but velocity and acceleration computed from variable-dt frames have higher noise than uniform-dt data. **Detectors calibrated on synthetic data with uniform 67ms dt will behave differently on real variable-dt data.**

### Detector Overfitting to Synthetic Data
All 60 tests pass on synthetic telemetry generated by `synthetic.go`. The synthetic generators use sinusoidal motion, deterministic jitter, and idealized throw mechanics. Real Echo VR gameplay involves:
- Chaotic multi-player physics interactions (bumping, bouncing, grabbing through bodies)
- Network prediction/reconciliation artifacts
- VR tracking occlusion (hands disappear briefly)
- Playspace-dependent hand positions (tall vs short players, room-scale vs seated)
- Controller-specific tracking noise profiles (Quest 2 vs Index vs Rift S)

**None of these are modeled in synthetic data.** The first real match will likely reveal false positive patterns not seen in testing.

### UNSAFE Detectors That Look Safe
THROW_004 (signature repeat), PAT_001 (throw timing), PAT_002 (release points), and MOV_003 (direction change) all pass their tests — but their tests explicitly avoid triggering them on legitimate data by excluding them or using parameters that don't exercise the risky code paths. **These detectors WILL false-positive on real skilled players.** They must remain in shadow mode indefinitely until calibrated against 5000+ real matches.

### Moderator Misuse
The system produces evidence packages and recommended actions for moderator review. If moderators treat "score >= 80" as proof of cheating without reviewing evidence, innocent players will be punished. The recommendation engine requires multi-category + multi-match confirmation for the strongest recommendations, but moderator manual actions bypass these safeguards. **A moderator who acts on score alone, without reviewing evidence, defeats the entire false-positive prevention architecture.**

### Single Point of Failure: Feature Extractor
All 29 detectors depend on the feature extractor computing derived state correctly. A single bug in velocity computation (like the hand velocity bug found and fixed during audit) silently breaks every detector downstream. **There is no cross-validation of derived features against independent computation.**

### Pre-Release Frame Data Bug (THROW_002)
The feature extractor stores the same current-frame disc velocity in every pre-release snapshot, making THROW_002's acceleration analysis meaningless. This detector appears to work but produces wrong results.

### Storage Growth
At 15 FPS × 8 players × ~200 bytes per detection event, a match generating many detections can produce megabytes of evidence. Over thousands of matches, the SQLite database will grow unboundedly. **There is no retention policy, no archival, and no pruning of old detection events.**
