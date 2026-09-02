# NEVR-Anticheat Shadow Deployment Guide

## What a shadow deployment produces

With `configs/shadow_deploy.toml` every detector runs in `mode = "shadow"`. That means, precisely:

- Detection events **are** stored in `detection_events` with `is_shadow = 1`, with full typed evidence.
- Shadow events are **never scored**: `suspicion_scores` stays empty, no `review_cases` / `cross_match_review_cases` rows are created, `nevr-ac flagged` prints `No flagged players.`, `nevr-ac player-history` reports no events, and PAT_003/PAT_004 receive nothing.
- Nothing is logged per event (the pipeline logs only scored detections). `nevr_ac_shadow_events_total{detector}` and `nevr_ac_detection_events_total{detector}` count them.
- `auto_enforce` is `false` for every detector, so `detection_events.auto_enforce` must be 0 everywhere.

The output of a shadow deployment is therefore **the `detection_events` table**, and this guide is about reading it. Scores and cases appear only when a detector is promoted (`mode = "review"`), which happens offline first (see "Promotion loop").

## Detector status in `shadow_deploy.toml`

The file is an overlay on `configs/default.toml`; every param keeps its calibrated default. `enforcement_weight` has no effect in shadow mode; the values are what would apply after promotion.

### Enabled, weight set for promotion (8)

| Detector | Weight | What it catches | Physics basis | UNVERIFIED aspect |
|----------|--------|-----------------|---------------|-------------------|
| THROW_001 | 0.8 | Release speed above the 18.7 m/s cap (+1.3 m/s tolerance, + ping) | Engine-enforced cap | Tolerances; releases > 37.4 m/s are `disc_speed_artifact` at severity 0.2 |
| THROW_006 | 0.8 | Disc bending in free flight | Zero-G straight-line physics | Angle thresholds; needs 5 sustained violation frames |
| BIO_002 | 0.6 | Hand speed above 50 m/s | Human limit ~12 m/s | Threshold is 4× the limit |
| MOV_001 | 0.7 | Sustained speed above 55 m/s | Physics speed cap | Median window on real variable-dt data |
| MOV_002 | 0.8 | Instantaneous position jump (8–12 m) | Position continuity | Network desync false-positive rate |
| STATE_002 | 0.7 | Stun shorter than 20 frames (1.33 s at 15 Hz) | Game stun mechanic | Real stun duration |
| PAT_004 | 0.9 | 3+ detector categories in one match | Meta-detector | Receives nothing while everything upstream is shadow |
| PAT_005 | 0.5 | Hand-to-head distance > 1.6 m for 30 frames | Physical arm reach ~0.8 m | Threshold is 2× reach |

### Enabled for observation only, weight 0.0 (8)

| Detector | What it observes | Why observation only |
|----------|------------------|----------------------|
| THROW_003 | Release angle deviation | Wrist-flick distribution unknown |
| THROW_005 | Goal-line precision | Distance normalisation unvalidated; correlation gate needs ≥ 30 pairs |
| THROW_008 | Disc speed increase in flight | Arena physics interactions unvalidated |
| BIO_001 | Wrist rotation rate | **Cannot exceed 50 rad/s on 15 Hz data** (π/dt = 46.9 rad/s). Enabled as a rotation-pipeline probe: any event on bridge data means garbage rotations. |
| BIO_003 | Hand position jitter | Controller jitter baseline unknown |
| BIO_004 | Hand rotation wobble | Rotation convention unconfirmed; wobble baseline unknown |
| STATE_001 | Grab distance at possession | Network desync impact unquantified |
| STATE_007 | Punch range at stun | Validates whether per-frame stun counts update at all |

### Disabled (13)

THROW_002 (BROKEN label pending re-test of the v2.0.0 mechanism), THROW_004 (UNSAFE), THROW_007 (STUB), MOV_003 (UNSAFE), MOV_004 / MOV_005 (need `is_boosting`), STATE_003 / STATE_005 (need `shield_active`), STATE_004 (needs `is_immune`), STATE_006 (SUSPENDED), PAT_001 / PAT_002 (UNSAFE), PAT_003 (needs 3+ matches of non-shadow history).

## Build

```bash
export CGO_ENABLED=1          # go-sqlite3 needs a C compiler (gcc / MinGW-w64)
go build -o nevr-ac     ./cmd/anticheat
go build -o nevr-server ./cmd/server
go build -o nevr-bridge ./cmd/bridge
go build -o nevr-compat ./cmd/compat
```

## Step 1: validate the adapter against a real payload

```bash
curl http://<broadcaster>:6721/session > first_session.json
./nevr-compat first_session.json            # field-by-field mapping report
./nevr-compat --strict first_session.json   # strict bounds: |X| <= 12.5, |Y| <= 12.5, |Z| <= 82, ping <= 1000
# For a recorded replay:
./nevr-compat --replay --max-frames 500 match.echoreplay
```

STOP if strict mode reports errors you cannot explain. Lost hand tracking (zero direction vectors) is reported as a warning, not an error.

## Step 2: analyse one replay offline

```bash
./nevr-ac --config configs/shadow_deploy.toml analyze match.echoreplay
```

Expected output: `Parsed N player-frames`, `Frames: N processed, M invalid` with M a few percent at most, `Detections: K (K stored)`, `Review cases: 0` (everything is shadow), no player score lines. The startup log shows the effective detector table; check that the modes are all `shadow`. Re-running prints `already stored`; add `--force` to replace the events.

## Step 3: batch-analyse 5–10 known-clean matches

```bash
mkdir clean_matches/ && cp <trusted replays> clean_matches/
./nevr-ac --config configs/shadow_deploy.toml batch clean_matches/
```

Then look at the events (shadow events are the whole output):

```bash
sqlite3 nevr-ac-shadow.db "SELECT detector_id, COUNT(*) FROM detection_events WHERE is_shadow = 1 GROUP BY detector_id;"
sqlite3 nevr-ac-shadow.db "SELECT match_id, detector_id, COUNT(*) FROM detection_events GROUP BY match_id, detector_id ORDER BY 3 DESC LIMIT 20;"
```

**STOP CONDITION**: a detector with more than ~20 events in a known-clean match is miscalibrated. Disable it (`enabled = false`) before going live, and record the event samples for calibration.

## Step 4: start the ingestion server

```bash
export NEVR_AC_AUTH_TOKEN='<long random secret>'
./nevr-server --config configs/shadow_deploy.toml --listen :8080 --metrics :9090 2> server.log
```

The server refuses to start without the token unless `--allow-unauthenticated` (or `NEVR_AC_ALLOW_UNAUTH=1`) is passed deliberately; it then logs a SECURITY warning. Logs are JSON on stderr. Flags mirror the `[server]` section of the config (`--max-matches`, `--max-players`, `--max-connections`, `--max-message-bytes`, `--max-frame-rate`, `--idle-timeout`, `--stale-match-after`, `--persist-interval`); a flag wins over the file.

```bash
curl -s localhost:8080/health
# {"status":"ok","connections":0,"frames_received":0,"frames_rejected":0,"frames_rate_limited":0,"frames_ignored":0,"active_matches":0}
```

## Step 5: start the bridge — probe, once, continuous

The bridge is the only live telemetry producer. Without it `connections` stays 0 forever.

```bash
# Probe: discover matches, fetch + map ONE /session, write debug artifacts, exit. Nothing is sent.
./nevr-bridge --nakama-url http://127.0.0.1:7350 --nakama-server-key <key> \
    --broadcaster-allowlist <ip-or-cidr> --probe --dump-dir ./dump
```

Read `dump/<match>_manifest.json`:

| Manifest field | Expect | Else |
|----------------|--------|------|
| `session_matches_match_id` | `true` | the `/session` `sessionid` is not the Nakama match id: check `--api-port`; `--session-check warn` forwards anyway |
| `players_seen` | 2–8 | 0 = wrong host or lobby |
| `spectators_seen` | any | spectators/moderators are dropped, not forwarded |
| `mapped_frames` | = players_seen | 0 with players → non-active `game_status`, zero positions, or all spectators |
| `errors_count` / `warnings_count` | 0 / few | inspect `dump/<match>_session_raw.json` |
| `server_id` | `<broadcaster_ip>:6721` | provenance stamp on every batch |

```bash
# Once: full cycle for one match (discover → fetch → map → send match_start + batch + match_end → wait for ack).
./nevr-bridge --nakama-url http://127.0.0.1:7350 --nakama-server-key <key> \
    --anticheat-url ws://127.0.0.1:8080/telemetry --anticheat-token "$NEVR_AC_AUTH_TOKEN" \
    --broadcaster-allowlist <ip-or-cidr> --once --dump-dir ./dump
```

Exit code 0 and `anticheat_send_succeeded: true`, `frames_acked > 0`, `frames_rejected: 0` in the manifest mean the server received the `hello`, accepted the batch and acked it. `ANTICHEAT AUTH FAILED` in the log means the tokens differ.

```bash
# Continuous, one match only for the first live run:
./nevr-bridge --nakama-url http://127.0.0.1:7350 --nakama-server-key <key> \
    --anticheat-url ws://127.0.0.1:8080/telemetry --anticheat-token "$NEVR_AC_AUTH_TOKEN" \
    --broadcaster-allowlist <ip-or-cidr> --match-id <nakama-match-id> 2> bridge.log
# Later: drop --match-id to poll every active echo_arena match (--modes '*' for all modes).
```

Log lines to expect: `connected to anticheat WebSocket (hello ok)`, `match_start`, `poller status` every 30 s, `bridge status` every discovery cycle with `frames_sent`/`frames_acked`/`frames_rejected`/`frames_dropped`, and `match_end` with a reason when the lobby ends. On SIGTERM the bridge prints a `nevr-bridge shutdown summary` with every counter (`frames_mapped`, `total_frames_forwarded_acked`, `frames_rejected_by_ingest`, `frames_unacked_lost`, `session_mismatches`, ...).

## Step 6: monitor

```bash
curl -s localhost:8080/health                       # connections > 0, frames_received incrementing
curl -s localhost:9090/metrics | grep nevr_ac_       # see below
grep '"level":"ERROR"' server.log | tail -5
grep -i panic server.log bridge.log
```

Metrics that matter (all exist on `/metrics`):

| Metric | Expect | STOP if |
|--------|--------|---------|
| `nevr_ac_active_connections` | ≥ 1 while the bridge runs | 0 for > 60 s with the bridge up (token / URL) |
| `nevr_ac_frames_received_total` | incrementing ~15 × players per second | flat |
| `nevr_ac_frames_invalid_total` and `nevr_ac_frames_invalid_reason_total{reason}` | < 5 % of received | > 20 % (a `reason` label tells you which field) |
| `nevr_ac_frames_ratelimited_total` | 0 | growing (producer faster than 30 Hz per player) |
| `nevr_ac_frames_rebased_total` | 0, or one burst per bridge restart | growing every batch |
| `nevr_ac_auth_failures_total` | 0 | > 0 |
| `nevr_ac_batches_rejected_total{reason}` | 0 | `oversized_message` growing (batch > 64 KB) |
| `nevr_ac_shadow_events_total{detector}` | spread across detectors, tens per match | one detector > 80 % of events or > 100 per match |
| `nevr_ac_store_errors_total` | 0 | > 0 |
| `nevr_ac_active_matches` | number of live lobbies | stuck at 64 (cap) |

## First 3 matches: required checks

**After match 1**

- [ ] `/health`: `frames_received` > 0 and incrementing; `frames_rejected` < 5 % of received.
- [ ] Bridge log shows `match_start` for the match and periodic `bridge status` with `frames_acked` rising.
- [ ] Events exist and are all shadow:
  `sqlite3 nevr-ac-shadow.db "SELECT is_shadow, COUNT(*) FROM detection_events GROUP BY is_shadow;"` — every row must have `is_shadow = 1`.
- [ ] Per-detector counts: `sqlite3 nevr-ac-shadow.db "SELECT detector_id, COUNT(*) FROM detection_events GROUP BY detector_id;"` — no detector > 50 in one match.
- [ ] **BIO_001**: `sqlite3 nevr-ac-shadow.db "SELECT COUNT(*) FROM detection_events WHERE detector_id='BIO_001';"` — must be **0** on 15 Hz bridge data (46.9 rad/s saturation < 50). Any event means the rotation conversion is producing garbage: disable BIO_001 and BIO_004 and inspect the `basis_reflected` / `hand_tracking_lost` warnings in the bridge log.
- [ ] Severities are finite: `sqlite3 nevr-ac-shadow.db "SELECT detector_id, MIN(severity), MAX(severity), AVG(confidence) FROM detection_events GROUP BY detector_id;"`
- [ ] No auto-enforce flags: `sqlite3 nevr-ac-shadow.db "SELECT COUNT(*) FROM detection_events WHERE auto_enforce = 1;"` — must be 0 (every detector has `auto_enforce = false`).
- [ ] No scores, no cases: `sqlite3 nevr-ac-shadow.db "SELECT COUNT(*) FROM suspicion_scores; SELECT COUNT(*) FROM review_cases;"` — both 0.
- [ ] Match context persisted with a start time: `sqlite3 nevr-ac-shadow.db "SELECT match_id, match_start_time, frame_count FROM match_contexts;"`

**After match 3**

- [ ] Re-check BIO_001 (still 0) and the per-detector spread.
- [ ] Look at THROW_001 samples: `sqlite3 nevr-ac-shadow.db "SELECT observed_value, expected_range, severity FROM detection_events WHERE detector_id='THROW_001' ORDER BY severity DESC LIMIT 20;"` — `disc_speed_artifact` rows (severity 0.2) are releases above 37.4 m/s; a large share of them means the disc velocity is not what the detector assumes.
- [ ] Reprocess one match offline with the same config and confirm the event counts match the live run: `./nevr-ac --config configs/shadow_deploy.toml reprocess-match <match-id>`.

## What to watch during the first 10 matches

### Matches 1–2: telemetry validation

| Check | Where | Expected | STOP if |
|-------|-------|----------|---------|
| Frames received | `/health`, `nevr_ac_frames_received_total` | > 0, incrementing | zero after 30 s |
| Rejection rate | `nevr_ac_frames_invalid_reason_total{reason}` | < 5 % | > 20 %; `out_of_arena_bounds` dominant = coordinate problem, `zero_position` dominant = mapping problem |
| Players per match | bridge `poller status` / manifest `players_seen` | 2–8 | 0 |
| Hand tracking | bridge manifest `warnings_count`, `hand_tracking_lost` in mapper stats | occasional | most frames |
| Handedness | bridge log `basis_reflected` warning | logged once (either way is fine) | mixed per frame |
| Possession | THROW_001/THROW_003 events exist at all after a few throws | some | none across 2 matches with visible throws → disc/possession not mapped |
| Disc velocity | THROW_001 `observed_value` | 5–20 m/s typical | always 0 or > 100 |
| Ping | `estimated_ping_ms` in `telemetry_frames.frame_json` | 10–200 ms | all 0 (field absent) |

### Matches 3–5: detector behaviour

| Check | Where | Expected | STOP if |
|-------|-------|----------|---------|
| Events per match | `detection_events` grouped by `match_id` | 0–50 for clean matches | > 100 (spam) |
| Events per detector | `nevr_ac_shadow_events_total{detector}` | spread | one detector > 80 % |
| THROW_001 | events | rare | > 5 per clean match |
| THROW_006 | events | rare | > 3 per clean match |
| MOV_002 | events | rare | > 2 per clean match (frame-gap / reset handling) |
| BIO_003 / BIO_004 | events | 0–2 | > 10 (jitter / wobble thresholds wrong) |

### Matches 6–10: baseline collection

Use the stored evidence, not scores: `evidence_json` carries the measured values (release speed, hand speed, stun seconds, ...). Export per detector with `sqlite3 -json nevr-ac-shadow.db "SELECT detector_id, player_id, observed_value, evidence_json FROM detection_events WHERE detector_id = 'THROW_001';"` and build distributions. Bimodal release-speed distributions suggest a cheater in the sample; > 50 % of hand speeds above 12 m/s suggests a coordinate/scale problem.

## Immediate stop conditions

1. Crash or panic in `nevr-server` or `nevr-bridge`.
2. Frame rejection > 20 % (`nevr_ac_frames_invalid_total` / `nevr_ac_frames_received_total`).
3. > 100 shadow events per match on clean matches.
4. Any BIO_001 event on 15 Hz bridge data.
5. All hand rotations zero (`hand_tracking_lost` on every frame) — hand detectors are blind.
6. Positions all identical or zero — adapter broken.
7. Disc velocity always zero — disc state not mapped.
8. `nevr_ac_auth_failures_total` > 0 or `ANTICHEAT AUTH FAILED` in the bridge log.
9. Database > 100 MB after 10 matches (telemetry + ticks are stored for every frame; budget it, but a jump means event spam).
10. `nevr-server` RSS > 500 MB.

## Promotion loop: how a detector leaves shadow

1. Collect ≥ 10 matches in shadow. Review the `detection_events` samples for the candidate detector.
2. **Copy the database** (`cp nevr-ac-shadow.db calib.db`) and write a calibration overlay that scores only the candidate:

   ```toml
   [general]
   db_path = "./calib.db"
   [detector.THROW_001]
   mode = "review"          # scored; everything else stays shadow from shadow_deploy.toml
   ```

   Load it *on top of* the shadow file by merging the two files into one (the loader reads a single file); the overlay semantics make that a copy of `shadow_deploy.toml` plus these lines.
3. Reprocess the collected matches with it: `./nevr-ac --config calib.toml reprocess-timerange 2026-09-01T00:00:00Z 2026-10-01T00:00:00Z` (half-open on match time). This **replaces** the events of those matches in `calib.db`; the candidate's events are now scored, players at or above `review_threshold` (60) get `RC-<match>-<player>` cases, and `cross-match` builds `XM-<player>` cases.
4. Moderators review: `./nevr-ac --config calib.toml flagged`, `report <case-id>`, then `verdict <case-id> confirmed_cheat|false_positive|inconclusive|needs_more_data --by <mod> --detector THROW_001=yes|no`.
5. `./nevr-ac --config calib.toml calibration-report` shows per-detector confirmed / false-positive counts and precision. Promote a detector to `mode = "review"` in the live config only when its precision is acceptable over a meaningful number of decided cases; demote (`mode = "shadow"`) at the first confirmed false positive.
6. Restart `nevr-server` after every config change (there is no hot reload). The startup effective-config table is the confirmation.
