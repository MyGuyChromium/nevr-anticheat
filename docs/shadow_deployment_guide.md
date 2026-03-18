# NEVR-Anticheat Shadow Deployment Guide

## Detector Status for Shadow Deployment

### Enabled with Scoring Weight (8 detectors)

These detectors are physics-grounded with conservative thresholds. Their events
contribute to suspicion scores (in shadow mode: scores are computed but not
acted on). **None have been validated against real Echo VR telemetry.**

| Detector | Weight | What It Catches | Physics Basis | UNVERIFIED Aspect |
|----------|--------|----------------|---------------|-------------------|
| THROW_001 | 0.8 | Disc speed above physics cap | Engine-enforced 18.7 m/s cap | Tolerance and ping compensation values |
| THROW_006 | 0.8 | Disc trajectory bending post-release | Zero-G straight-line physics | Angle change thresholds (8°/frame, 130° cumulative) |
| BIO_002 | 0.6 | Hand speed above 50 m/s | Physical human limit ~12 m/s | Threshold is 4× limit; generous but unvalidated |
| MOV_001 | 0.7 | Sustained speed above 55 m/s | Physics speed cap | Median window behavior on real variable-dt data |
| MOV_002 | 0.8 | Instantaneous position teleport | Position continuity | Network desync false-positive rate |
| STATE_002 | 0.7 | Stun recovery under 20 frames | Game stun mechanic | Real stun duration in telemetry frames |
| PAT_004 | 0.9 | 3+ detector categories in one match | Meta-detector (3+ categories) | Accuracy depends on upstream detector quality |
| PAT_005 | 0.5 | Hand-to-head distance >1.6m sustained | Physical arm reach ~0.8m | Threshold is 2× reach with 30-frame sustain |

### Enabled for Observation Only (6 detectors, weight=0.0)

These detectors produce events but contribute ZERO to suspicion scores. Used for baseline data collection and threshold calibration.

| Detector | What It Observes | Why Observation Only |
|----------|-----------------|---------------------|
| THROW_003 | Release angle deviation | Wrist-flick threshold needs real distribution |
| THROW_005 | Target-line precision | Close-range shots need distance normalization |
| THROW_008 | Disc speed increase during flight | Arena physics interactions unvalidated |
| BIO_003 | Hand position jitter | Controller jitter baseline unknown |
| STATE_001 | Grab distance at possession | Network desync impact unquantified |

### Enabled for Observation Only — Format-Dependent (2 detectors, weight=0.0)

These detectors depend on hand rotation arriving as quaternions. If the replay format provides direction vectors instead, these detectors receive garbage input and their output is meaningless.

| Detector | What It Observes | Format Risk |
|----------|-----------------|-------------|
| BIO_001 | Wrist rotation rate | UNCONFIRMED: .echoreplay may use direction vectors, not quaternions |
| BIO_004 | Hand rotation wobble | Same rotation format dependency as BIO_001 |

### Enabled for Observation Only — Telemetry-Dependent (1 detector, weight=0.0)

| Detector | What It Observes | Dependency |
|----------|-----------------|------------|
| STATE_007 | Punch range at stun | Per-frame stun count granularity UNCONFIRMED; may update only at round end |

### Disabled (14 detectors)

| Detector | Classification | Reason |
|----------|---------------|--------|
| THROW_002 | **BROKEN** | Pre-release frame data in real replays has identical disc velocities across snapshots |
| THROW_004 | **UNSAFE** | Regrab playstyle produces low variance naturally; false-positives on skilled players |
| THROW_007 | **STUB** | Evaluate() returns nil; no penalty field telemetry exists |
| MOV_003 | **UNSAFE** | Wall bounces produce legitimate 180° reversals |
| MOV_004 | **TELEMETRY_DEPENDENT** | IsBoosting field absent from standard API and unconfirmed in replays |
| MOV_005 | **TELEMETRY_DEPENDENT** | Same IsBoosting dependency + sequence counter design limitation |
| STATE_003 | **TELEMETRY_DEPENDENT** | ShieldActive field unconfirmed in .echoreplay format |
| STATE_004 | **TELEMETRY_DEPENDENT** | IsImmune field unconfirmed in .echoreplay format |
| STATE_005 | **TELEMETRY_DEPENDENT** | ShieldActive field unconfirmed (same as STATE_003) |
| STATE_006 | **SUSPENDED** | No confirmed impossible score invariant; delta=1 proved legitimate |
| PAT_001 | **UNSAFE** | Regrab rhythm produces low CoV naturally; false-positives on skilled players |
| PAT_002 | **UNSAFE** | Consistent throwing form produces <2cm spread naturally |
| PAT_003 | **CROSS_MATCH_DEPENDENT** | Wired in cmd/server but needs min_matches of prior DB history; enable after data collected |

## First 3 Matches: Required Validation Checks

Run these checks immediately after deploying. Do not leave the deployment unattended until all pass.

**After Match 1:**

- [ ] **Telemetry ingestion**: `curl -s localhost:8080/health` — `frames_received` must be >0 and incrementing. If zero after 60s, adapter is broken.
- [ ] **BIO_001 sanity check**: `sqlite3 nevr-ac-shadow.db "SELECT COUNT(*) FROM detection_events WHERE detector_id='BIO_001';"` — If >50 events in one match, rotation format is wrong. **Disable BIO_001 immediately** by setting `enabled = false` in the config and restarting. If 0 events, this is expected (50 rad/s threshold is generous). Re-evaluate after match 3.
- [ ] **Event volume**: `sqlite3 nevr-ac-shadow.db "SELECT detector_id, COUNT(*) FROM detection_events GROUP BY detector_id;"` — No single detector should have >50 events per match. If any does, it's spamming — disable it.
- [ ] **Shadow-only confirmation**: `sqlite3 nevr-ac-shadow.db "SELECT COUNT(*) FROM detection_events WHERE auto_enforce=1;"` — Must be 0. All events are shadow-only. If >0, something is misconfigured.
- [ ] **No enforcement actions**: Verify no player has been banned, kicked, or flagged for moderator review. Shadow mode produces scores for calibration only.

**After Match 3:**

- [ ] **BIO_001 re-check**: If BIO_001 has produced a small number of events (1-10) with severity <1.0, rotation format is likely correct. If still zero events, the detector may be dead — investigate hand rotation data via `./nevr-compat --strict`.
- [ ] **Score sanity**: `sqlite3 nevr-ac-shadow.db "SELECT player_id, score FROM suspicion_scores ORDER BY score DESC LIMIT 5;"` — All scores should be <20 on clean matches. If any score >40, a detector is miscalibrated.
- [ ] **PAT_003 readiness**: After 3+ matches, PAT_003 can be enabled at weight=0.0 for observation. It will only fire for players with detections across multiple prior matches.

## Launch Commands

### Build

```bash
cd /home/developer/nevr-anticheat
go build -o nevr-ac ./cmd/anticheat
go build -o nevr-server ./cmd/server
go build -o nevr-compat ./cmd/compat
```

### Step 1: Validate Adapter Against First Real Payload

```bash
# Capture a raw session from the game server
curl http://127.0.0.1:6721/session > first_session.json

# Run compatibility check
./nevr-compat first_session.json

# Run strict mode check
./nevr-compat --strict first_session.json

# STOP if strict mode shows unexpected errors.
# Review every error before proceeding.
```

### Step 2: Analyze a Single Replay

```bash
./nevr-ac --config configs/shadow_deploy.toml analyze replay.json
```

Verify output shows:
- Frames processed > 0
- No crashes or panics
- Detection count is reasonable (0-20 for a clean match)

### Step 3: Batch Analyze 5-10 Known-Clean Matches

```bash
mkdir clean_matches/
# Copy 5-10 replays from trusted players
./nevr-ac --config configs/shadow_deploy.toml batch clean_matches/
```

**STOP CONDITION**: If ANY player in known-clean matches scores above 40 (Suspicious level), investigate the detections before proceeding.

### Step 4: Start Live Server (Shadow Mode)

```bash
NEVR_AC_AUTH_TOKEN="<secret>" ./nevr-server \
    --config configs/shadow_deploy.toml \
    --listen :8080 \
    --metrics :9090
```

### Step 5: Monitor

```bash
# Check health
curl http://localhost:8080/health

# Check metrics
curl http://localhost:9090/metrics

# Check flagged players
./nevr-ac --config configs/shadow_deploy.toml flagged

# View a specific case
./nevr-ac --config configs/shadow_deploy.toml report <case-id>
```

## What to Watch During the First 10 Matches

### Match 1-2: Telemetry Validation

| Check | How | Expected | STOP If |
|-------|-----|----------|---------|
| Frames received | `/health` endpoint | >0, incrementing | Zero frames after 30 seconds |
| Frame rejection rate | `/metrics` → `frames_invalid` | <5% of total | >20% rejection |
| Player count per match | Log output | 2-8 players | 0 players (adapter broken) |
| Position values | Log sample at debug level | Within arena bounds (±40m) | All zeros or all identical |
| Hand rotation presence | Adapter diagnostics | >90% non-zero `lhand.forward` | >50% zero (tracking data missing) |
| Possession tracking | Adapter diagnostics | Alternates between players | Always false or always true |
| Disc velocity | Adapter diagnostics | 0-20 m/s typical | >100 m/s or always zero |
| Ping values | Adapter diagnostics | 10-200ms typical | All zero (field not present) |
| Score deltas | ~~STATE_006 disabled~~ — monitor via raw telemetry | N/A (detector suspended) | N/A |

### Match 3-5: Detector Behavior

| Check | How | Expected | STOP If |
|-------|-----|----------|---------|
| Total events per match | DB query or flagged output | 0-20 for clean matches | >100 (detector spam) |
| Events per detector | Metrics → `detection_events_total` | Spread across detectors | One detector >80% of events |
| THROW_001 fires | Check events | 0 on clean matches | >5 on clean (threshold too tight) |
| THROW_006 fires | Check events | 0 on clean matches | >3 on clean (trajectory noise) |
| MOV_002 fires | Check events | 0 on clean matches | >2 on clean (frame gap handling) |
| BIO_003/004 fires | Check events | 0-2 observation events | >10 (jitter threshold wrong) |
| Max player score | DB query | <20 for clean matches | >40 on any clean player |

### Match 6-10: Baseline Collection

| Check | How | Expected | STOP If |
|-------|-----|----------|---------|
| Throw speed distribution | Observation events from THROW_001 | 5-16 m/s with long tail | Bimodal (two peaks = cheater in data) |
| Hand speed distribution | Observation events from BIO_002 | 0-10 m/s | >50% above 12 m/s (coordinate issue) |
| Stun duration distribution | STATE_002 observation | 40-50 frames | All <20 (stun field broken) |
| Score delta distribution | Raw telemetry query (STATE_006 suspended) | Collect baseline — no confirmed invalid delta | N/A |

## Immediate Stop Conditions

**STOP the deployment immediately if ANY of these occur:**

1. **Crash or panic** in the server process
2. **>20% frame rejection rate** — adapter is rejecting too much data (likely format mismatch)
3. **>100 detection events per match** on clean matches — a detector is spamming
4. **Any clean-match player scores above 40** — false positive, investigate immediately
5. ~~STATE_006 fires on a clean match~~ — **STATE_006 is suspended** (no confirmed impossible score invariant). If re-enabled in future, any firing on a clean match means the invariant is wrong.
6. **All hand rotation vectors are zero** — hand tracking data is not available, BIO detectors will false-positive
7. **Position values are all identical or all zero** — adapter is broken
8. **Disc velocity is always zero** — disc state is not properly mapped
9. **Database grows >100MB in 10 matches** — event spam, evidence storage out of control
10. **Memory usage exceeds 500MB** — likely a leak in per-match state
