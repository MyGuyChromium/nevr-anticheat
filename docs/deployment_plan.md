# NEVR-Anticheat Deployment Plan

## Rollout Stages

### Stage 0: Local Development (Week 1-2)
- Run `nevr-ac analyze` on 10-20 known-clean replays from community matches
- Verify zero high-severity detections on clean data
- Run on 2-3 known-cheat replays (if available) to verify detections fire
- Fix any false positives discovered
- **Gate**: Zero false positives on clean corpus, at least one true positive on cheat corpus

### Stage 1: Shadow Mode — Single Server (Week 3-4)
- Deploy `nevr-server` on ONE community server in shadow mode (default)
- All detectors run, all events logged to SQLite, NO enforcement actions
- Monitor via `/health` endpoint and Prometheus metrics at `/metrics`
- Review daily: detection rates, detector firing distributions, score distributions
- **Gate**: Detection rate stable, no crash/OOM, <5% of players flagged above score 40
- **Rollback trigger**: >20% of players flagged, server CPU >10% increase, any crash

### Stage 2: Shadow Mode — Multiple Servers (Week 5-8)
- Expand to 3-5 servers across different regions
- Collect baseline data across skill brackets
- Begin baseline calibration (see Calibration Plan below)
- Compare detection rates across servers for consistency
- **Gate**: Cross-server detection rate variance <2x, baseline collection complete (5000+ matches)
- **Rollback trigger**: Cross-server variance >5x, storage growth >1GB/day

### Stage 3: Moderator Review (Week 9-12)
- Switch enforcement mode to `flag` on all servers
- Moderators review ALL flagged cases (score >= 60) manually
- Track moderator verdicts: confirmed_cheat / false_positive / inconclusive
- Compute per-detector false positive rates from verdicts
- **Gate**: Overall FPR < 5%, per-detector FPR < 10%, moderator workflow functional
- **Rollback trigger**: FPR > 15%, moderator queue backlog > 100 cases

### Stage 4: Active Review (Week 13-16)
- Switch to `review` mode: create review cases, moderators act on recommendations
- Promote ONLY detectors with FPR < 3% from shadow to review mode
- Keep remaining detectors in shadow
- Add automated alerts for score spikes and detection rate changes
- **Gate**: Promoted detectors FPR < 3% for 4 consecutive weeks
- **Rollback trigger**: Any promoted detector FPR > 5%, any false enforcement action

### Stage 5: Full Moderator Workflow (Week 17+)
- Switch to `enforce` mode: generate strongest recommendations for moderator review
- Restrict/ban actions require moderator confirmation in all cases
- Cross-match review cases surface repeat offenders across sessions
- Monitor continuously
- **Gate**: Zero false bans in 4 weeks of review mode data
- **Rollback trigger**: ANY false ban → immediate rollback to review mode

## Hard Stop Criteria (Immediate Rollback)

Any of these triggers immediate rollback to the previous stage:

1. **False enforcement action confirmed** → Roll back to review mode, investigate, disable offending detector
2. **FPR > 20%** for any detector for 1 week → Disable that detector
3. **Server crash or OOM** → Roll back, investigate memory leak
4. **Detection rate spikes 10x** in 1 hour → Likely game update broke assumptions, pause enforcement
5. **Player community reports false flags** → Investigate immediately, pause if confirmed

## Rollback Procedure

1. Change enforcement mode in config: `mode = "shadow"`
2. Restart server (or hot-reload if supported)
3. All pending enforcement actions are cancelled
4. Detection events continue logging (for analysis)
5. Review queue is frozen (no new cases created)
6. Investigate root cause before re-promoting

## Configuration Per Stage

| Stage | Mode | Detectors Active | Moderator Review | Recommendations |
|-------|------|-----------------|-----------------|-----------------|
| 0 | offline | all (shadow) | no cases | logging only |
| 1 | shadow | all (shadow) | no cases | logging only |
| 2 | shadow | all (shadow) | no cases | logging only |
| 3 | flag | calibrated only | manual review | flagging only |
| 4 | review | promoted only | review queue | flag + review cases |
| 5 | enforce | promoted only | review queue | strongest recommendations |

---

# Calibration Plan

## Phase 1: Data Collection (Stages 1-2)

### What to Collect
For every match processed in shadow mode, store:
- All detection events (detector ID, severity, confidence, evidence)
- Per-player per-match: max speed, max throw speed, avg throw speed, throw count, max hand speed, avg hand speed, max wrist angular rate
- Per-match: tick rate, frame count, duration, player count

### Sample Size Targets
- **Minimum**: 5,000 matches across all skill levels
- **Per-bracket minimum**: 500 matches (if skill brackets are available)
- **Throw events**: 50,000+ throws
- **Target**: 25,000 matches for full calibration

### Metrics to Inspect
For each metric, compute from clean-match data:
1. Mean, stddev, median, p90, p95, p99, p99.9, max
2. Per-skill-bracket distributions (if available)
3. Per-server/region distributions

### Key Calibration Metrics

| Metric | Source | Expected Legit Range |
|--------|--------|---------------------|
| Throw release speed | ThrowEvent.ReleaseSpeed | 0.5 - 18.0 m/s |
| Hand speed at release | ThrowEvent.HandSpeed | 0.5 - 12.0 m/s |
| Release angle | ThrowEvent.ReleaseAngle | 0 - 45 degrees |
| Player speed (sustained) | PlayerState.Speed (median over 30 frames) | 0 - 50 m/s |
| Wrist angular rate | PlayerState.LeftWristAngularRate | 0 - 25 rad/s |
| Hand jitter variance | ComputePositionVariance | 0.00005 - 0.01 m² |
| Stun recovery frames | StunStartFrame to StunEnd | 30 - 50 frames |

## Phase 2: Threshold Computation

For each metric:
1. Collect all observations from clean matches
2. Sort and compute percentiles
3. Set thresholds:
   - **Suspicious**: p99 (1 in 100 legit events)
   - **Extremely Improbable**: p99.9 (1 in 1000)
   - **Impossible**: p99.99 or physics cap (whichever is lower)
4. Widen thresholds by 15% if sample size < 5000
5. Compute 95% confidence intervals via bootstrap

## Phase 3: Detector Promotion

For each detector:
1. Compute FPR from moderator verdicts (Phase 3 of deployment)
2. If FPR < 3% for 4 weeks → promote to review mode
3. If FPR < 1% for 4 weeks after review → promote to enforce mode (strongest recommendations)
4. If FPR > 5% at any time → demote to shadow, re-calibrate

## Phase 4: Ongoing Tuning

- **Weekly**: Compute per-detector FPR from verdicts
- **Monthly**: Re-compute baseline percentiles from accumulated clean data
- **After game updates**: Re-run shadow mode for 1 week, re-validate thresholds
- **Threshold version bumps**: Create new version, A/B test in shadow before promoting

---

# Moderator Workflow

## Review Case Lifecycle

```
Detection Event → Score Accumulation → Review Threshold → Case Created → Queue
    ↓
Moderator Claims Case
    ↓
Reviews Evidence Package:
  - Detection timeline (which detectors fired, when, with what values)
  - Score breakdown (per-detector contributions)
  - Replay bundle (frames before/during/after event)
  - Player history (prior cases, prior verdicts, account age)
  - Threshold comparison (observed vs expected ranges)
    ↓
Renders Verdict:
  - confirmed_cheat → enforcement action (warn/restrict/ban)
  - false_positive → dismiss, feeds back to threshold tuning
  - inconclusive → enhanced monitoring
  - needs_more_data → flag for future match analysis
    ↓
Logs Decision (immutable audit trail)
```

## What Moderators See First

1. **Case Summary**: Player ID, match ID, suspicion score, severity level, recommended action
2. **Detection Timeline**: Chronological list of detector firings with timestamps and one-line descriptions
3. **Score Breakdown**: Which detectors contributed how much to the total score
4. **Threshold Comparison**: For each detection, the observed value vs. the threshold (e.g., "disc_speed: 22.3 m/s, threshold: 20.0 m/s")

## Replay Bundle Consumption

Moderators receive a JSON bundle containing:
- 3 seconds of frames before the detection event
- The frames during the event
- 3 seconds of frames after
- All player telemetry in those frames
- Disc telemetry

This can be loaded into a replay viewer (future tool) or inspected as raw data.

## Available Actions

| Action | When | Effect |
|--------|------|--------|
| `confirmed_cheat` | Clear evidence of cheating | Score locked, enforcement applied |
| `false_positive` | Legitimate gameplay flagged incorrectly | Score reset, case dismissed, feeds FPR tracking |
| `inconclusive` | Evidence ambiguous | Enhanced monitoring, no enforcement |
| `needs_more_data` | Insufficient evidence | Flag player for detailed analysis in future matches |
| `escalate` | Needs senior review | Case reassigned to senior moderator |

## Feedback Loop

Every moderator verdict is stored and used to:
1. Compute per-detector FPR weekly
2. Identify detectors needing threshold adjustment
3. Build a labeled dataset of confirmed-cheat vs false-positive cases
4. Train future threshold optimization

## Decision Quality Tracking

| Metric | Target | Alert |
|--------|--------|-------|
| Cases reviewed per week | Track (no target) | Alert if backlog > 50 |
| Median review time | < 5 minutes | Alert if > 15 minutes |
| Verdict distribution | <5% false_positive | Alert if >10% FP |
| Inter-moderator agreement | >90% on clear cases | Review if <80% |
