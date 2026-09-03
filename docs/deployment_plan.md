# NEVR-Anticheat Deployment Plan

Every stage below uses only features that exist in the binaries today. Where the plan needs something that is not implemented, it says so.

## What exists

- Detection: 29 detectors, all shipping in `mode = "shadow"` (events stored with `is_shadow = 1`, never scored). Scoring happens only for detectors whose mode is `review` or `enforce` (both simply mean "scored"; there is no separate enforcement path).
- Cases: single-match `RC-<match>-<player>` cases for match scores ≥ `review_threshold` (60), cross-match `XM-<player>` cases for decayed scores ≥ 60 across ≥ 3 matches (`nevr-ac cross-match`).
- Moderator tools: `flagged`, `report`, `cross-match-report`, `player-history`, `verdict` (with per-detector feedback), `calibration-report`.
- Reprocessing: `reprocess-match`, `reprocess-player`, `reprocess-timerange` (half-open on match time) replace derived outputs from stored telemetry under any config.
- Evidence export: `nevr-ac evidence-export` writes either the archival JSON bundle or a self-contained offline HTML reviewer with a top-down X/Z replay, event navigation, hand/disc state and typed evidence. Case exports contain scored events; `--match ... --player ... --include-shadow` supports pre-promotion review.
- Observation dashboard: `nevr-ac observation-report` and the desktop app report event volume, shadow/scored counts, match/player spread and confidence/severity tails separately for every detector version. These are calibration inputs, never validation claims.
- Database preservation: `nevr-ac backup <output.db>` makes a consistent SQLite snapshot, verifies it, and refuses to replace an existing snapshot.
- Raw-source preservation: `.echoreplay` imports and the current live bridge store the original session JSON once per tick in `match_ticks`, alongside normalized `telemetry_frames`, so future mapper changes and disputed detections can be re-examined.
- Not implemented / not wired: automatic enforcement (`enforce.Engine` exists but no binary constructs it), hot config reload, per-server dashboards.

## Rollout stages

### Stage 0: local development
- Run `nevr-ac analyze` on 10–20 known-clean replays and 2–3 known-cheat replays (if available) with `configs/shadow_deploy.toml`.
- Inspect `detection_events` per detector; there are no scores in shadow.
- **Gate**: no detector produces more than ~20 events on any clean replay; THROW_001/THROW_006 fire on the cheat replays.

### Stage 1: shadow mode, single server
- One `nevr-server` and one `nevr-bridge` with `--broadcaster-allowlist` and, for the first match, `--match-id`.
- Follow `docs/operator_checklist.md`. Nothing is scored; the product is the `detection_events` table.
- **Gate**: 10 matches without a stop condition; per-detector event rates stable; BIO_001 at 0 on bridge data.
- **Rollback trigger**: any stop condition (crash, > 20 % rejection, detector spam, auth failures).

### Stage 2: shadow mode, multiple servers
- One `nevr-server`; one `nevr-bridge` per Nakama instance (or one bridge with a wider allowlist). Provenance lives on the match context: the `server_id` the bridge sends on every batch and `match_start` is recorded and persisted with `match_contexts` (and shown by `report`), so join events to their match to see which broadcaster produced them; events do not carry it individually.
- Collect baseline distributions from `evidence_json` across skill brackets.
- **Gate**: per-detector event rates within 2× across servers; ≥ 100 matches stored.
- **Rollback trigger**: cross-server variance > 5×; storage growth beyond the disk budget (telemetry is never pruned automatically).

### Stage 3: offline calibration and moderator review
- Copy the database. Reprocess the collected matches with a calibration overlay that sets `mode = "review"` for one candidate detector at a time (see the promotion loop in `docs/shadow_deployment_guide.md`).
- Moderators work the resulting cases with `flagged` / `report` / `cross-match-report` and record `verdict` with `--detector ID=yes|no|uncertain`.
- `calibration-report` gives per-detector confirmed / false-positive counts and precision.
- **Gate**: ≥ 30 decided cases per candidate detector; precision acceptable to the moderation team; no confirmed false positive that the detector's evidence could not have explained.
- **Rollback trigger**: precision below the team's floor → keep the detector in shadow and recalibrate its params.

### Stage 4: live scoring for promoted detectors
- In the live config set `mode = "review"` for the promoted detectors only; everything else stays shadow. Restart `nevr-server`.
- Live matches now produce scores and `RC-*` cases for those detectors; `nevr-ac cross-match` (run on a schedule) produces `XM-*` cases.
- Moderators review every case; `verdict` keeps feeding `calibration-report`.
- **Gate**: 4 weeks with no confirmed false positive from a promoted detector.
- **Rollback trigger**: any confirmed false positive → set that detector back to `mode = "shadow"` and restart.

### Stage 5: enforcement recommendations (not implemented)
- Automatic recommendations (`enforce.Engine`/`Policy`: review_queue, restrict, temp_ban proposals) exist as code but are not wired into any binary, and `auto_enforce` stays `false` for every detector. Until that wiring lands and is validated, every action is a moderator decision recorded with `verdict --action <warn|temp_ban|...>`.

## Hard stop criteria (immediate rollback)

1. A moderator confirms a false positive from a promoted detector → demote it to shadow.
2. A detector's precision in `calibration-report` drops below the team's floor for a week → demote.
3. Server crash, OOM or `nevr_ac_store_errors_total` > 0 → stop, investigate.
4. Event rate for any detector spikes 10× in an hour → a game update probably changed the telemetry; stop the bridge, re-run `nevr-compat --strict`.
5. Player reports of false flags → investigate the case evidence; demote on confirmation.

## Rollback procedure

1. Edit the live config: set `mode = "shadow"` (or `enabled = false`) for the affected detector(s).
2. Restart `nevr-server` (no hot reload). Confirm the effective detector table it reports at startup (`grep '"msg":"effective detector"' server.log`; `nevr-ac` shows the same table only with `--verbose`).
3. Existing cases stay in the database with their status; nothing is cancelled automatically. Close or dismiss them with `verdict`.
4. Events keep being stored in shadow for analysis.
5. Do not re-promote without a new calibration round.

## Configuration per stage

| Stage | Detectors scored (`mode = "review"`) | Cases | Moderator work |
|-------|--------------------------------------|-------|----------------|
| 0 | none | none | inspect events |
| 1 | none | none | inspect events |
| 2 | none | none | inspect events, build baselines |
| 3 | one candidate at a time, offline on a DB copy | RC-*/XM-* in the copy | verdicts, calibration report |
| 4 | promoted detectors, live | RC-*/XM-* live | review every case |
| 5 | (not implemented) | — | — |

---

# Calibration Plan

## Phase 1: data collection (stages 1–2)

Every shadow match from `.echoreplay` or the current bridge stores normalized telemetry (`telemetry_frames`), exact source JSON once per tick (`match_ticks`), the match context and every detector's events with typed evidence. Per-player summaries (max/avg speed, throw speeds, hand speeds, wrist rates) are recomputable from the telemetry by reprocessing; nothing further needs to be collected. Legacy/custom WebSocket producers should add `raw_json` to reach the same standard.

Sample size targets: minimum 500 matches before any threshold change, 5,000 for a threshold anyone will defend, 50,000+ throws for the throw detectors.

## Phase 2: threshold computation

For each detector metric (from `evidence_json` of clean matches): mean, stddev, median, p90/p95/p99/p99.9, max; per skill bracket and per server if available. Suspicious = p99, extremely improbable = p99.9, impossible = min(p99.99, physics cap). Widen by 15 % when n < 5,000. Record the commit hash and config used next to every number.

| Metric | Source | Expected legit range (to be measured) |
|--------|--------|---------------------------------------|
| Throw release speed | THROW_001 evidence `release_speed` | 0.5 – 18.7 m/s |
| Hand speed at release | THROW_001/THROW_003 evidence `hand_speed` | 0.5 – 12 m/s |
| Release angle | THROW_003 evidence | 0 – 45° |
| Sustained player speed | MOV_001 evidence (median over 30 frames) | 0 – 50 m/s |
| Wrist angular rate | BIO_001 evidence (bounded by 46.9 rad/s at 15 Hz) | 0 – 25 rad/s |
| Hand jitter variance | BIO_003 evidence | 0.00005 – 0.01 m² |
| Stun duration | STATE_002 evidence (seconds) | 2.5 – 3.5 s |

## Phase 3: detector promotion

1. Precision from `calibration-report` over ≥ 30 decided cases.
2. Promote to `mode = "review"` when the moderation team accepts the precision; demote at the first confirmed false positive.
3. There is no enforce tier to promote into yet.

## Phase 4: ongoing tuning

- Weekly: `calibration-report --since 7d`.
- Monthly: re-derive percentiles from the accumulated clean matches.
- After a game update: bridge back to `--probe`/`--once`, `nevr-compat --strict` on a fresh `/session`, one week of shadow before re-enabling scoring.
- Threshold changes: new config file, `reprocess-timerange` on a DB copy, compare event counts before applying live.

---

# Moderator Workflow

## Case lifecycle (as implemented)

```
scored detection events → per-match score ≥ 60 → RC-<match>-<player> case (pending)
stored events across ≥ 3 matches → cross-match decayed score ≥ 60 → XM-<player> case (pending)
    ↓
nevr-ac flagged                     list pending cases
nevr-ac report <RC-id>              evidence summary, every stored event with typed evidence, prior decisions
nevr-ac evidence-export <RC-id> review.html
                                     offline frame-by-frame visual evidence artifact
nevr-ac cross-match-report <XM-id>  per-match evidence
nevr-ac player-history <player>     capped, decayed history
    ↓
nevr-ac verdict <case-id> <confirmed_cheat|false_positive|inconclusive|needs_more_data> --by <mod> \
    [--detector ID=yes|no|uncertain ...] [--action warn|temp_ban|none] [--notes ...]
    ↓
moderator_decisions row (immutable), case status → decided
nevr-ac calibration-report          per-detector confirmed / false-positive counts, precision
```

Case statuses: `pending`, `assigned`, `in_review`, `decided`, `appealed`, `closed`. A cross-match case whose status a moderator changed is refreshed but never reopened by re-aggregation.

## What moderators see

1. Case summary: player, match, score, severity level, recommended action (informational).
2. Every stored detection event for the player in the match: detector, version, frame range, timestamp, severity, confidence, weight, observed vs expected, causal key, typed evidence JSON.
3. Prior decisions on the case.

`evidence-export` includes a configurable frame window around each event (`--before` / `--after`, 45 frames per side by default), every player in those windows, match provenance, score metadata and typed evidence. HTML exports are fully offline and make no network requests. JSON exports preserve the same bundle for tooling. Both formats contain player identifiers and must be stored with the same access controls as the database.

## Verdicts

| Verdict | Meaning | Effect |
|---------|---------|--------|
| `confirmed_cheat` | evidence supports cheating | counts as confirmed for every detector that fired (or per `--detector` feedback) |
| `false_positive` | legitimate play | counts as a false positive for every detector that fired (or per feedback) |
| `inconclusive` | ambiguous | counted as inconclusive |
| `needs_more_data` | insufficient evidence | counted; case can be revisited |

`--detector ID=yes|no|uncertain` overrides the case verdict for that detector only. Shadow events inside a decided case count on purpose: that is how a shadow detector accumulates calibration data.

## Decision quality tracking

| Metric | Target | Alert |
|--------|--------|-------|
| Pending cases (`flagged`) | track | > 50 backlog |
| False-positive share (`calibration-report`) | < 5 % | > 10 % |
| Decided cases per promoted detector | ≥ 30 before promotion | — |
