# NEVR-Anticheat Shadow Deployment Guide

## Detector Status for Shadow Deployment

### Enabled with Scoring Weight (10 detectors)

These detectors are production-ready. Their events contribute to suspicion scores (in shadow mode: scores are computed but not acted on).

| Detector | Weight | What It Catches | Why Safe |
|----------|--------|----------------|----------|
| THROW_001 | 0.8 | Disc speed above physics cap | Hard physics constraint; 1.3 m/s tolerance above 18.7 cap |
| THROW_006 | 0.8 | Disc trajectory bending post-release | Zero-G straight-line physics; dual detection (angle + alignment) |
| BIO_001 | 0.6 | Wrist rotation above 30 rad/s | Physical human limit ~25 rad/s; 2-frame consecutive required |
| BIO_002 | 0.6 | Hand speed above 15 m/s | Physical human limit ~12 m/s; 2-frame consecutive required |
| MOV_001 | 0.7 | Sustained speed above 55 m/s | Median over 30-frame window; resists single-frame spikes |
| MOV_002 | 0.8 | Instantaneous position teleport | Requires both >8m jump AND velocity mismatch; skips frame gaps |
| STATE_002 | 0.7 | Stun recovery under 20 frames | Requires 2+ incidents; 20 frames is half real stun duration |
| STATE_006 | 1.0 | Invalid score deltas | Deterministic: only valid deltas are {0,2,3,4,5,6} |
| PAT_004 | 0.9 | 3+ detector categories in one match | Meta-detector; inherently conservative |
| PAT_005 | 0.5 | Hand-to-head distance >1.6m sustained | 2x physical arm reach; 45-frame sustained requirement |

### Enabled for Observation Only (8 detectors, weight=0.0)

These detectors produce events but contribute ZERO to suspicion scores. Used for baseline data collection and threshold calibration.

| Detector | What It Observes | Why Observation Only |
|----------|-----------------|---------------------|
| THROW_002 | Disc acceleration at release | Pre-release frame data needs validation |
| THROW_003 | Release angle deviation | Wrist-flick threshold needs real distribution |
| THROW_005 | Target-line precision | Close-range shots need distance normalization |
| THROW_008 | Disc speed increase during flight | Arena physics interactions unvalidated |
| BIO_003 | Hand position jitter | Controller jitter baseline unknown |
| BIO_004 | Hand rotation wobble | Steady-aim baseline unknown |
| STATE_001 | Grab distance at possession | Network desync impact unquantified |
| STATE_007 | Punch range at stun | Per-frame stun count updates unconfirmed |

### Disabled (10 detectors)

| Detector | Reason |
|----------|--------|
| THROW_004 | UNSAFE: regrab playstyle false positives |
| THROW_007 | STUB: no implementation |
| MOV_003 | UNSAFE: wall bounce false positives |
| MOV_004 | No IsBoosting telemetry |
| MOV_005 | No IsBoosting + sequence counter bug |
| STATE_003 | ShieldActive field unconfirmed |
| STATE_004 | IsImmune field unconfirmed |
| STATE_005 | ShieldActive field unconfirmed |
| PAT_001 | UNSAFE: rhythmic play false positives |
| PAT_002 | UNSAFE: consistent form false positives |

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
| Score deltas | Log STATE_006 events | Zero events on clean matches | Events on every frame (broken delta tracking) |

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
| Score delta distribution | STATE_006 | All {0, 2, 3} | Any 1 or negative (score tracking broken) |

## Immediate Stop Conditions

**STOP the deployment immediately if ANY of these occur:**

1. **Crash or panic** in the server process
2. **>20% frame rejection rate** — adapter is rejecting too much data (likely format mismatch)
3. **>100 detection events per match** on clean matches — a detector is spamming
4. **Any clean-match player scores above 40** — false positive, investigate immediately
5. **STATE_006 fires on a clean match** — score tracking is broken (this is a hard rule with zero FP risk; any firing means the data is wrong)
6. **All hand rotation vectors are zero** — hand tracking data is not available, BIO detectors will false-positive
7. **Position values are all identical or all zero** — adapter is broken
8. **Disc velocity is always zero** — disc state is not properly mapped
9. **Database grows >100MB in 10 matches** — event spam, evidence storage out of control
10. **Memory usage exceeds 500MB** — likely a leak in per-match state
