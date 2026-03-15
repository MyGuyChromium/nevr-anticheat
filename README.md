# NEVR-Anticheat

Server-side anticheat for Echo VR / Echo Arena community-hosted servers.

## Overview

NEVR-Anticheat is a **server-authoritative, telemetry-based** detection and enforcement system. It analyzes replay files and server telemetry to detect impossible, implausible, or suspicious gameplay behavior — without any client-side component.

### Architecture

```
Replay/.echoreplay → Ingest → Validate → Feature Extract → Detect → Score → Review
```

The pipeline processes telemetry frame-by-frame through 29 detectors across 5 categories, accumulates suspicion scores, and surfaces flagged players for moderator review.

### Design Principles

- **Server-side only** — the client is untrusted
- **Evidence-based** — every flag backed by measurable evidence
- **Low false positives** — observe → score → threshold → escalate → review → enforce
- **Shadow mode default** — all detectors start in logging-only mode
- **Explainable** — moderators see exactly why someone was flagged

## Quick Start

```bash
# Build
go build -o nevr-ac ./cmd/anticheat

# Analyze a single replay
./nevr-ac analyze replay.json

# Batch analyze a directory
./nevr-ac batch ./replays/

# List flagged players
./nevr-ac flagged

# Generate moderator report
./nevr-ac report RC-20260315-abc12345
```

## Configuration

Copy `configs/default.toml` and customize. All thresholds are config-driven.

```bash
./nevr-ac --config myconfig.toml analyze replay.json
```

## Detector Catalog

| ID | Name | Category | Weight |
|----|------|----------|--------|
| THROW_001 | Impossible Release Velocity | throw | 0.8 |
| THROW_002 | Impossible Disc Acceleration | throw | 0.7 |
| THROW_003 | Unnatural Release Angle | throw | 0.5 |
| THROW_004 | Repeated Release Signatures | throw | 0.6 |
| THROW_005 | Superhuman Target Precision | throw | 0.7 |
| THROW_006 | Trajectory Correction (Mags) | throw | 0.8 |
| THROW_007 | Penalty Field Tampering | throw | 0.6 |
| THROW_008 | Speed-Distance Anomaly | throw | 0.5 |
| BIO_001 | Impossible Wrist Rotation | bio | 0.6 |
| BIO_002 | Impossible Hand Speed | bio | 0.6 |
| BIO_003 | Zero Hand Jitter | bio | 0.5 |
| BIO_004 | Zero Aim Wobble | bio | 0.5 |
| MOV_001 | Impossible Player Speed | movement | 0.7 |
| MOV_002 | Teleportation | movement | 0.8 |
| MOV_003 | Zero-Inertia Direction Change | movement | 0.5 |
| MOV_004 | Boost Speed Cap Violation | movement | 0.5 |
| MOV_005 | Boost Spam (Infinite Battery) | movement | 0.6 |
| STATE_001 | Impossible Grab Distance | state | 0.5 |
| STATE_002 | Stun Recovery Exploit | state | 0.7 |
| STATE_003 | Shield Duration Exploit | state | 0.6 |
| STATE_004 | Damage Immunity Exploit | state | 0.8 |
| STATE_005 | Cooldown Bypass | state | 0.6 |
| STATE_006 | Score Manipulation | state | 1.0 |
| STATE_007 | Punch Range Exploit | state | 0.5 |
| PAT_001 | Frame-Perfect Throw Timing | pattern | 0.6 |
| PAT_002 | Identical Release Points | pattern | 0.6 |
| PAT_003 | Cross-Match Consistency | pattern | 0.8 |
| PAT_004 | Composite Multi-Cheat | pattern | 0.9 |
| PAT_005 | Playspace Abuse | pattern | 0.5 |

## Scoring

Detection events accumulate into per-player suspicion scores:

| Score | Level | Action |
|-------|-------|--------|
| 0-19 | Clean | Log only |
| 20-39 | Informational | Dashboard visibility |
| 40-59 | Suspicious | Shadow flag |
| 60-79 | High Risk | Moderator review queue |
| 80-94 | Critical | Auto-restrict + urgent review |
| 95-100 | Action-Worthy | Auto-ban eligible (with hard evidence) |

## Shadow Mode

All detectors default to **shadow mode**: they run, generate events, and log results, but do not contribute to enforcement scores. This allows safe validation before enabling enforcement.

## Testing

```bash
go test ./...
go test -race ./...
go test -bench=. ./...
```

## License

Proprietary. For authorized use by the NEVR community moderation team.
