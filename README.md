# NEVR-Anticheat

Async cheat detection engine for Echo VR / Echo Arena, backed by a profiler database.

## Architecture

NEVR-Anticheat is a **profiler-database-backed, asynchronous** cheat detection system. It does NOT run on game servers. Game servers only expose telemetry to a profiler. The profiler stores all telemetry in a database. This engine runs separately, analyzing stored telemetry to detect impossible, implausible, or suspicious gameplay behavior.

```
Game Servers → Profiler/Telemetry Collection → Database (canonical source of truth)
                                                   ↓
                              Async Detection Engine → Score → Review Cases
                                                   ↓
                                          Moderator Review Queue
```

### Two-part system

1. **Profiler / Ingestion** — collects telemetry from matches and stores it in the database.
   Sources: `.echoreplay` files, legacy JSON replays, live WebSocket telemetry.
   The database retains all telemetry indefinitely as the canonical source of truth.

2. **Async Detection Engine** — reads from the database, runs 29 detectors across 5 categories,
   accumulates suspicion scores, and surfaces flagged players for moderator review.
   Can run after matches, in batch, or continuously in the background.
   Can make multiple passes over the same data. Can correlate behavior across sessions.

### Design Principles

- **Async, not live** — detection runs against stored profiler data, not during matches
- **Database is truth** — all telemetry is persisted; replay files are an ingestion format, not the primary source
- **Multi-pass** — the same telemetry can be reprocessed with updated detectors or thresholds
- **Cross-match** — players are tracked across sessions, not just within a single match
- **Evidence-based** — every flag is backed by measurable evidence with full provenance
- **Shadow mode default** — all detectors start in logging-only mode
- **Explainable** — moderators see exactly why someone was flagged

## Quick Start

```bash
# Build
go build -o nevr-ac ./cmd/anticheat

# Ingest and analyze a replay (stores telemetry to DB + runs detection)
./nevr-ac analyze match.echoreplay

# Batch ingest a directory of replays
./nevr-ac batch ./replays/

# Re-run detection on stored telemetry (no replay file needed)
./nevr-ac reprocess-match <match-id>
./nevr-ac reprocess-player <player-id>
./nevr-ac reprocess-timerange 2026-01-01T00:00:00Z 2026-03-01T00:00:00Z

# Cross-match aggregation (decayed scoring across all matches)
./nevr-ac cross-match

# Moderator workflow
./nevr-ac flagged                      # list all flagged players
./nevr-ac report <case-id>             # single-match case detail
./nevr-ac cross-match-report <case-id> # cross-match case detail
./nevr-ac player-history <player-id>   # full player history from DB
```

## Configuration

Copy `configs/default.toml` and customize. All thresholds are config-driven.

```bash
./nevr-ac --config myconfig.toml analyze replay.echoreplay
```

## Detector Catalog

| ID | Name | Category | Weight | Status |
|----|------|----------|--------|--------|
| THROW_001 | Impossible Release Velocity | throw | 0.8 | Physics-grounded |
| THROW_002 | Impossible Disc Acceleration | throw | 0.7 | **BROKEN** — pre-release frame data invalid |
| THROW_003 | Unnatural Release Angle | throw | 0.5 | Unverified — needs wrist-flick data |
| THROW_004 | Repeated Release Signatures | throw | 0.6 | **UNSAFE** — FPs on regrab playstyle |
| THROW_005 | Superhuman Target Precision | throw | 0.7 | Unverified — needs accuracy data |
| THROW_006 | Trajectory Correction (Mags) | throw | 0.8 | Physics-grounded |
| THROW_007 | Penalty Field Tampering | throw | 0.6 | **STUB** — no penalty field telemetry |
| THROW_008 | Speed-Distance Anomaly | throw | 0.5 | Unverified — needs arena physics data |
| BIO_001 | Impossible Wrist Rotation | bio | 0.6 | Unverified — needs rotation format validation |
| BIO_002 | Impossible Hand Speed | bio | 0.6 | Physics-grounded (50 m/s = 4x human limit) |
| BIO_003 | Zero Hand Jitter | bio | 0.5 | Unverified — controller jitter baseline unknown |
| BIO_004 | Zero Aim Wobble | bio | 0.5 | Unverified — rotation variance baseline unknown |
| MOV_001 | Impossible Player Speed | movement | 0.7 | Physics-grounded |
| MOV_002 | Teleportation | movement | 0.8 | Physics-grounded |
| MOV_003 | Zero-Inertia Direction Change | movement | 0.5 | **UNSAFE** — FPs on wall bounces |
| MOV_004 | Boost Speed Cap Violation | movement | 0.5 | Disabled — needs IsBoosting field |
| MOV_005 | Boost Spam (Infinite Battery) | movement | 0.6 | Disabled — needs IsBoosting field |
| STATE_001 | Impossible Grab Distance | state | 0.5 | Unverified — needs grab range data |
| STATE_002 | Stun Recovery Exploit | state | 0.7 | Physics-grounded |
| STATE_003 | Shield Duration Exploit | state | 0.6 | Disabled — needs ShieldActive field |
| STATE_004 | Damage Immunity Exploit | state | 0.8 | Disabled — needs IsImmune validation |
| STATE_005 | Cooldown Bypass | state | 0.6 | Disabled — needs ShieldActive field |
| STATE_006 | Score Manipulation | state | 1.0 | **SUSPENDED** — no confirmed invariant |
| STATE_007 | Punch Range Exploit | state | 0.5 | Disabled — needs per-frame stun data |
| PAT_001 | Frame-Perfect Throw Timing | pattern | 0.6 | **UNSAFE** — FPs on skilled players |
| PAT_002 | Identical Release Points | pattern | 0.6 | **UNSAFE** — FPs on consistent form |
| PAT_003 | Cross-Match Consistency | pattern | 0.8 | Needs 3+ matches of DB history |
| PAT_004 | Composite Multi-Cheat | pattern | 0.9 | Meta-detector (depends on upstream) |
| PAT_005 | Playspace Abuse | pattern | 0.5 | Physics-grounded |

**Status key**: Physics-grounded = based on game physics constraints (thresholds unvalidated). Unverified = needs real data calibration. BROKEN/STUB = non-functional. UNSAFE = known FPs on legitimate play. SUSPENDED = disabled, no confirmed detection rule.

## Scoring

Detection events accumulate into per-player suspicion scores (time-decayed):

| Score | Level | Meaning |
|-------|-------|---------|
| 0-19 | Clean | No action |
| 20-39 | Informational | Visible in dashboard |
| 40-59 | Suspicious | Shadow flag for monitoring |
| 60-79 | High Risk | Enters moderator review queue |
| 80-94 | Critical | Urgent moderator review |
| 95-100 | Action-Worthy | Moderator action recommended with hard evidence |

Scores decay over time (configurable half-life, default 168h). A player who was suspicious months ago but clean since will naturally return to clean status.

## Database as Source of Truth

The database stores two categories of data:

**Immutable source data** (never modified after ingestion):
- `telemetry_frames` — raw and normalized telemetry frames from the profiler
- `match_contexts` — match metadata for reprocessing

**Derived analysis outputs** (recomputable from source data):
- `detection_events` — detector outputs, replaced on reprocessing
- `suspicion_scores` — append-only scoring snapshots
- `cross_match_review_cases` — aggregated review cases, replaced on re-aggregation

Telemetry is never pruned by default. Detection events and scores may be pruned as maintenance.

## Shadow Mode

All detectors default to **shadow mode**: they run, generate events, and log results, but do not affect scoring. This allows safe validation before enabling detection for scoring.

## Testing

```bash
go test ./...
go test -race ./...
go test -bench=. ./...
```

## License

Proprietary. For authorized use by the NEVR community moderation team.
