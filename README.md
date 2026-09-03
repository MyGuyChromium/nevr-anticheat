# NEVR-Anticheat

Asynchronous, server-side cheat detection for Echo VR / Echo Arena on NEVR community servers (EchoTools / Nakama). It never runs inside a game server: telemetry is collected into a SQLite profiler database and 29 detectors analyse it there.

**Validation status: 0 of 29 detectors have been validated on real Echo VR telemetry.** Every threshold is derived from game physics constants and synthetic data. All detectors ship in shadow mode. Read `docs/production_readiness.md` before deploying anything.

## Architecture

```
LIVE PATH
  Echo VR broadcaster (/session HTTP API, ~15 Hz)
        │  polled by
        ▼
  nevr-bridge  (cmd/bridge: Nakama match discovery → /session poll → adapter mapping)
        │  WebSocket /telemetry, bearer token, FrameBatch + match_start/match_end
        ▼
  nevr-server  (cmd/server: internal/ingest → SQLite telemetry_frames, inline detection)

OFFLINE PATH
  .echoreplay / legacy JSON replay ──▶ nevr-ac analyze|batch (internal/adapter + internal/replay)

BOTH PATHS
  SQLite (source of truth) ──▶ feature extraction ──▶ 29 detectors ──▶ per-match scores
        ──▶ review cases (single-match RC-*, cross-match XM-*) ──▶ moderator verdicts ──▶ calibration
```

Design rules:

- **Database is truth.** Telemetry frames (`telemetry_frames`) and match context are immutable source data; detection events, scores and cases are derived and can be recomputed at any time with `reprocess-*`.
- **Shadow mode by default.** A detector in `mode = "shadow"` stores its events with `is_shadow = 1` and they are never scored: no scores, no cases, no per-event log lines. Promotion out of shadow is a config change backed by moderator verdicts.
- **Evidence, not scores.** Every event carries typed evidence, observed vs expected values and a causal frame range; moderators review evidence, never a number.
- **Async, multi-pass.** Live inline detection exists for immediate feedback, but the canonical analysis is reprocessing stored telemetry.

## Toolchain

- Go **1.26+** (see `go.mod`).
- **CGO is required** (`github.com/mattn/go-sqlite3`). Build with `CGO_ENABLED=1` and a C compiler on `PATH`: gcc on Linux, MinGW-w64 on Windows (for example `winget install BrechtSanders.WinLibs.POSIX.UCRT`). A binary built with `CGO_ENABLED=0` compiles but panics at the first `sql.Open`.

```bash
export CGO_ENABLED=1
go build -o nevr-ac     ./cmd/anticheat   # offline CLI + moderator tools
go build -o nevr-server ./cmd/server      # live ingestion server
go build -o nevr-bridge ./cmd/bridge      # broadcaster → server bridge
go build -o nevr-compat ./cmd/compat      # /session payload compatibility checker
```

## Quick start: offline (replays)

```bash
# Ingest one replay: stores telemetry + raw ticks, runs detection, stores events/scores/cases.
./nevr-ac analyze match.echoreplay
# A match that is already stored is skipped; --force clears its derived outputs and re-analyses.
./nevr-ac analyze match.echoreplay --force

# Ingest a directory (parallel, general.max_workers), then run cross-match aggregation.
./nevr-ac batch ./replays/ [--force]

# Re-run detection on STORED telemetry (no replay file needed; replaces derived outputs).
./nevr-ac reprocess-match <match-id>
./nevr-ac reprocess-player <player-id>
./nevr-ac reprocess-timerange 2026-01-01T00:00:00Z 2026-03-01T00:00:00Z   # [since, until) on MATCH time

# Cross-match aggregation: decayed 0-100 score per player across matches, XM-* cases.
./nevr-ac cross-match

# Moderator workflow
./nevr-ac flagged                         # pending single-match (RC-*) and cross-match (XM-*) cases
./nevr-ac report <case-id>                # RC-<match>-<player>: evidence, stored events, decisions
./nevr-ac cross-match-report <case-id>    # XM-<player>: per-match evidence
./nevr-ac player-history <player-id>      # capped, decayed cross-match history from the DB
./nevr-ac verdict <case-id> confirmed_cheat --by <moderator> --detector THROW_006=yes --detector BIO_001=no
./nevr-ac calibration-report --since 30d  # per-detector confirmed / false-positive counts from verdicts
./nevr-ac version
```

Verdicts are `confirmed_cheat`, `false_positive`, `inconclusive` or `needs_more_data`. `--detector ID=yes|no|uncertain` records per-detector feedback that overrides the case verdict for that detector in the calibration report. Shadow events inside decided cases count on purpose: that is how a shadow detector earns promotion.

All CLI commands take `--config <file>` before the command; `--verbose` / `-v` (or `NEVR_AC_VERBOSE=1`) additionally reports the effective detector table at startup. Reports go to stdout, logs to stderr. Every command except `version` loads and validates the config first (unknown keys, detector IDs, params and wrong value types are errors); `version` prints the version without reading any file.

## Quick start: live (bridge + server)

Both binaries must share one bearer token. The server refuses to start without a token unless `--allow-unauthenticated` (or `NEVR_AC_ALLOW_UNAUTH=1`) is given deliberately.

```bash
export NEVR_AC_AUTH_TOKEN='<long random secret>'

# 1. Server: listens on :8080 (/telemetry WebSocket, /health JSON) and :9090 (/metrics).
./nevr-server --config configs/shadow_deploy.toml --listen :8080 --metrics :9090

# 2. Bridge, step by step. Probe: discover a match, fetch + map one /session, no send.
./nevr-bridge --nakama-url http://127.0.0.1:7350 --nakama-server-key <key> \
    --probe --dump-dir ./dump
#    Read dump/<match>_manifest.json: session_matches_match_id must be true, players_seen > 0,
#    mapped_frames > 0, errors_count 0.

# 3. Once: full cycle for one match (discover → fetch → map → send → wait for the ack).
./nevr-bridge --nakama-url http://127.0.0.1:7350 --nakama-server-key <key> \
    --anticheat-url ws://127.0.0.1:8080/telemetry --anticheat-token "$NEVR_AC_AUTH_TOKEN" \
    --once --dump-dir ./dump
#    Exit 0 and anticheat_send_succeeded=true in the manifest mean the server acked >= 1 frame.

# 4. Continuous: poll every active echo_arena match; restrict to one match first.
./nevr-bridge --nakama-url http://127.0.0.1:7350 --nakama-server-key <key> \
    --anticheat-url ws://127.0.0.1:8080/telemetry --anticheat-token "$NEVR_AC_AUTH_TOKEN" \
    --broadcaster-allowlist 10.0.0.0/24 --match-id <nakama-match-id>
```

Bridge defaults worth knowing: `--nakama-auth device` (creates/uses a bridge account through the server key; the raw server key alone cannot list matches), `--modes echo_arena` (prefix filter; `*` polls everything), `--session-check strict` (stops a poller whose `/session` `sessionid` is not the Nakama match id), `--broadcaster-allowlist` (the trust boundary: only listed IPs/CIDRs are polled; everything forwarded was pulled over plaintext HTTP from whatever host the match label advertises). Every batch and `match_start` carries `server_id = "<broadcaster_ip>:<api_port>"`; the server records it on the match context (`server_id`, persisted with `match_contexts` and shown wherever the match context is printed, e.g. `report`), not on every event row, so provenance is looked up per match.

Expected log lines: server `telemetry server starting`, `game server connected`, `live match created`; bridge `connected to anticheat WebSocket (hello ok)`, `match_start`, periodic `bridge status` with `frames_acked`, and `match_end` with a reason (`post_match_detected`, `session_changed`, `broadcaster_unreachable`, `broadcaster_error`, `session_mismatch`, `cancelled`). Live matches are finalized on `match_end`, after `--stale-match-after` (30 min idle) or at shutdown.

### Wire protocol in one paragraph

Every message is JSON over the WebSocket. A message with a non-empty `type` is a control message, anything else is a `FrameBatch` `{match_id, server_id, timestamp, frames[]}`. After the upgrade with `Authorization: Bearer <token>` the server sends `{"type":"hello","auth":"ok"}`; on a bad token it sends `{"type":"error","reason":"unauthorized"}` and closes. While frames are pending it sends `{"type":"ack","accepted":N,"rejected":M,"ignored":K}` about once per second, the counts covering every frame since the previous ack (`ignored` = duplicate rows the store discarded). The producer sends `{"type":"match_start", match_id, server_id, game_mode, map, is_private, teams:{player_id:"blue"|"orange"}}` when a poller starts and `{"type":"match_end", match_id, reason}` when it stops. A producer must run a read loop: a peer that never reads fills its socket buffer and is disconnected on the next blocked ack write. Full field reference: `docs/telemetry_contract.md`.

## Configuration

`configs/default.toml` is the reference and is identical to the compiled defaults (a test enforces it). A config file is **overlaid key by key** onto those defaults: a two-line

```toml
[detector.MOV_001]
enabled = true
```

keeps MOV_001 in shadow mode with its calibrated params, and a `[detector.X.params]` section changes only the keys it names. Unknown keys, unknown detector IDs, unknown params and wrong value types are startup errors; keys documented by earlier versions but no longer read are dropped with a warning (the renamed/removed list is at the end of `default.toml`). To see what an edit did, read the effective per-detector table (id, enabled, mode, weight, auto_enforce, resolved params): `nevr-server` always reports it at startup; `nevr-ac` only with `--verbose` / `-v` or `NEVR_AC_VERBOSE=1` (report output on stdout stays clean otherwise). With `log_format = "json"` the table is one JSON log record per detector (`"msg":"effective detector"`), with `"text"` a fixed-width block; both go to stderr. `[server]` durations must be strings (`"5m"`, `"300s"`): a bare number is rejected at startup because it would be read as nanoseconds.

Per detector: `enabled`, `enforcement_weight` (0-1, multiplies every event's score contribution, this is the weight scoring uses), `auto_enforce` (only THROW_001 has a rule that can stamp `AutoEnforce` on an event, and only when this is true), `mode` (`shadow` = stored, never scored; `review`/`enforce` both mean scored, nothing else is wired), `params` (units per key, frames not milliseconds). `configs/shadow_deploy.toml` is the first-deployment overlay.

## Detector Catalog

Weight is the `enforcement_weight` from `configs/default.toml`, which is what scoring applies. Status labels are those of `docs/production_readiness.md` (the single source for them); none is validated on real data. The one label that changed since the original assessment is THROW_002: its BROKEN multi-frame mechanism was removed in the v2.0.0 rewrite (commit 3ab978b) and the replacement is unvalidated, hence Unverified rather than an upgrade to any validated status.

| ID | Name | Category | Weight | Status |
|----|------|----------|--------|--------|
| THROW_001 | Impossible Release Velocity | throw | 0.8 | Physics-grounded |
| THROW_002 | Impossible Disc Acceleration | throw | 0.7 | Unverified — v2.0.0 single-delta approach, needs real-data calibration |
| THROW_003 | Unnatural Release Angle | throw | 0.5 | Unverified — needs wrist-flick data |
| THROW_004 | Repeated Release Signatures | throw | 0.6 | **UNSAFE** — FPs on regrab playstyle |
| THROW_005 | Superhuman Target Precision | throw | 0.7 | Unverified — needs accuracy data |
| THROW_006 | Trajectory Correction (Mags) | throw | 0.8 | Physics-grounded |
| THROW_007 | Penalty Field Tampering | throw | 0.6 | **STUB** — no penalty field telemetry |
| THROW_008 | Speed-Distance Anomaly | throw | 0.5 | Unverified — needs arena physics data |
| BIO_001 | Impossible Wrist Rotation | bio | 0.6 | Unverified — rotation convention unconfirmed; unreachable at 15 Hz |
| BIO_002 | Impossible Hand Speed | bio | 0.6 | Physics-grounded (50 m/s = 4x human limit) |
| BIO_003 | Zero Hand Jitter | bio | 0.5 | Unverified — controller jitter baseline unknown |
| BIO_004 | Zero Aim Wobble | bio | 0.5 | Unverified — rotation variance baseline unknown |
| MOV_001 | Impossible Player Speed | movement | 0.7 | Physics-grounded |
| MOV_002 | Teleportation | movement | 0.8 | Physics-grounded |
| MOV_003 | Zero-Inertia Direction Change | movement | 0.5 | **UNSAFE** — FPs on wall bounces |
| MOV_004 | Boost Speed Cap Violation | movement | 0.5 | Disabled — needs is_boosting field |
| MOV_005 | Boost Spam | movement | 0.6 | Disabled — needs is_boosting field |
| STATE_001 | Impossible Grab Distance | state | 0.5 | Unverified — needs grab range data |
| STATE_002 | Stun Recovery Exploit | state | 0.7 | Physics-grounded |
| STATE_003 | Shield Duration Abuse | state | 0.6 | Disabled — needs shield_active field |
| STATE_004 | Damage Immunity Exploit | state | 0.8 | Disabled — needs is_immune validation |
| STATE_005 | Cooldown Bypass | state | 0.6 | Disabled — needs shield_active field |
| STATE_006 | Score Manipulation | state | 1.0 | **SUSPENDED** — no confirmed invariant |
| STATE_007 | Impossible Punch Range | state | 0.5 | Disabled — needs per-frame stun data |
| PAT_001 | Frame-Perfect Timing | pattern | 0.6 | **UNSAFE** — FPs on skilled players |
| PAT_002 | Identical Release Points | pattern | 0.6 | **UNSAFE** — FPs on consistent form |
| PAT_003 | Cross-Match Consistency | pattern | 0.8 | Needs 3+ matches of DB history |
| PAT_004 | Composite Multi-Cheat | pattern | 0.9 | Meta-detector (depends on upstream) |
| PAT_005 | Playspace Abuse | pattern | 0.5 | Physics-grounded |

**Status key**: Physics-grounded = based on game physics constraints (thresholds unvalidated). Unverified = needs real-data calibration. STUB = non-functional. UNSAFE = known FPs on legitimate play. SUSPENDED = disabled, no confirmed detection rule. Disabled = required telemetry field absent from every known source.

## Scoring

A non-shadow event contributes `severity × confidence × enforcement_weight × 100` points, capped at `max_single_contribution` (15), at most `max_contrib_per_detector_per_match` (2) events per detector per match, subject to a per-detector frame cooldown, and diminished by `same_category_diminishing` (0.8) per prior event in the same category. Players firing in ≥ 2 independent categories get a correlation bonus of `(categories − 1) × 5`, capped at 15 (PAT_003/PAT_004 are meta-detectors and never count as a category). Total = min(100, base + bonus).

Bands are `model.LevelTable`, populated from `[scoring]`; `review_threshold` **is** the `high_risk` boundary:

| Score | Level | Meaning |
|-------|-------|---------|
| 0-19 | clean | No action |
| 20-39 | informational | Visible in dashboards |
| 40-59 | suspicious | Monitoring |
| 60-79 | high_risk | Review case created (`review_threshold = 60`) |
| 80-94 | critical | Urgent moderator review |
| 95-100 | action_worthy | Action recommended with hard evidence (`auto_enforce_threshold = 95`) |

The per-match score stored in `suspicion_scores` (`scope = 'match'`) is **not** decayed. Decay (`decay_half_life_hours`, 168 h) is applied by cross-match aggregation (`cross-match`, `player-history`): every stored non-shadow event is re-capped, decayed from its match start time, and summed into a 0-100 score on the same scale as a match score (`scope = 'cross_match'`). A single-match case `RC-<match>-<player>` is created for a player whose match score reaches `review_threshold`; a cross-match case `XM-<player>` for a decayed score at or above it with events in at least `min_matches_for_cross_match` (3) matches.

## Database

SQLite in WAL mode (expect `.db-wal` / `.db-shm` sidecars). Every timestamp column is UTC RFC3339 with a literal `Z`.

Immutable source data: `telemetry_frames` (one normalized frame per player per tick), `match_ticks` (the raw profiler/session JSON once per tick, `.echoreplay` sources only), `match_contexts` (roster, teams, physics, source, `match_start_time`, the anchor for decay and `reprocess-timerange`). Never pruned automatically.

Derived data: `detection_events` (`is_shadow`, `analysis_source` = initial/reprocess, typed `evidence_json`), `suspicion_scores` (append-only snapshots, `scope` = match/cross_match), `review_cases`, `cross_match_review_cases`, `moderator_decisions` (verdicts + per-detector feedback), `enforcement_actions`. `nevr-server` prunes events older than 90 days and scores older than 30 days.

## Shadow Mode

`mode = "shadow"` (the default for all 29 detectors) means: the detector runs, its events are stored with `is_shadow = 1`, and nothing else happens. Shadow events are excluded from scoring, from single-match and cross-match cases, from `flagged`, `player-history` and PAT_003 history, and from per-event log lines. The only output of an all-shadow deployment is the `detection_events` table, which is the calibration surface: `verdict` on cases that do exist counts shadow events, and `calibration-report` shows per-detector precision. A detector is promoted by changing its `mode` in config.

Weights and `auto_enforce` come from config: the binaries build detectors through `detect.BuildAll`, which applies `enforcement_weight` via `SetWeight` and `auto_enforce` via `SetAutoEnforce`, so `MakeEvent` stamps the configured weight on every event.

## Testing

```bash
CGO_ENABLED=1 go test ./...
go test -race ./...
go test -bench=. ./tests/
```

## License

Proprietary. For authorized use by the NEVR community moderation team.
