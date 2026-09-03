# Match Day Operator Checklist

Two processes make a live deployment: `nevr-server` (ingestion + storage) and `nevr-bridge` (the only telemetry producer). Both log JSON to **stderr**, one record per line (the effective detector table `nevr-server` reports at startup is one `"msg":"effective detector"` record per detector in that stream); redirect it or you have no log file.

## Pre-launch (once)

- [ ] Toolchain: Go 1.26+, `CGO_ENABLED=1`, gcc / MinGW-w64 on `PATH`.
- [ ] Build: `CGO_ENABLED=1 go build -o nevr-ac ./cmd/anticheat && go build -o nevr-server ./cmd/server && go build -o nevr-bridge ./cmd/bridge && go build -o nevr-compat ./cmd/compat`
- [ ] Capture a raw session: `curl http://<broadcaster>:6721/session > test_session.json`
- [ ] Compat check: `./nevr-compat test_session.json` — 0 errors, review warnings.
- [ ] Strict check: `./nevr-compat --strict test_session.json` — explain every strict failure before continuing.
- [ ] Config check: `./nevr-ac --verbose --config configs/shadow_deploy.toml flagged` loads and validates the file (every unknown key, detector ID, param or wrong value type is listed and the command exits non-zero) and reports the effective detector table on stderr; it also opens (creates if missing) the configured `db_path`, which is the shadow database anyway. Do **not** use `version` for this: it prints the version without reading any file. Every mode in the table must be `shadow`: with the file's `log_format = "json"` run it as `2> check.log` and `grep '"msg":"effective detector"' check.log | grep -v '"mode":"shadow"'` must print nothing.
- [ ] Token: `export NEVR_AC_AUTH_TOKEN=<long random secret>` in the server's environment; the same value goes to the bridge's `--anticheat-token`.
- [ ] Start the server: `./nevr-server --config configs/shadow_deploy.toml --listen :8080 --metrics :9090 2> server.log &`
- [ ] Startup table: `grep '"msg":"effective detector"' server.log | grep -v '"mode":"shadow"'` prints nothing (every detector is in shadow) and `grep -c '"msg":"effective detector"' server.log` is 29.
- [ ] Health: `curl -s localhost:8080/health` → `{"status":"ok","connections":0,...}`
- [ ] Metrics: `curl -s localhost:9090/metrics | grep nevr_ac_uptime_seconds`
- [ ] Bridge probe (no send): `./nevr-bridge --nakama-url <url> --nakama-server-key <key> --broadcaster-allowlist <cidr> --probe --dump-dir ./dump` — manifest has `session_matches_match_id: true`, `players_seen > 0`, `mapped_frames > 0`, `errors_count: 0`.
- [ ] Bridge once (send + ack): same flags plus `--anticheat-url ws://127.0.0.1:8080/telemetry --anticheat-token "$NEVR_AC_AUTH_TOKEN" --once` — exit 0, manifest `anticheat_send_succeeded: true`, `frames_acked > 0`.
- [ ] Start the bridge continuously, first match only: `... --match-id <nakama-match-id> 2> bridge.log &` (drop `--match-id` after match 1).

## During match 1 (every 60 s)

- [ ] `curl -s localhost:8080/health | python3 -m json.tool`
  - `connections` ≥ 1
  - `frames_received` incrementing
  - `frames_rejected` < 5 % of `frames_received`; `frames_rate_limited` = 0
- [ ] `curl -s localhost:9090/metrics | grep -E 'nevr_ac_(active_connections|frames_received_total|frames_invalid_total|auth_failures_total|shadow_events_total)'`
- [ ] Bridge: `grep '"msg":"bridge status"' bridge.log | tail -1` — `frames_acked` rising, `frames_rejected` 0, `anticheat_connected: true`
- [ ] No errors: `grep '"level":"ERROR"' server.log bridge.log | tail -5`
- [ ] No panics: `grep -i panic server.log bridge.log`

## After match 1

- [ ] Preserve a verified snapshot before analysis: `./nevr-ac --config configs/shadow_deploy.toml backup backups/after-match-1.db` (choose a new filename each time; existing backups are never overwritten).
- [ ] Bridge log has `match_end` with reason `post_match_detected` (or the server finalized after 30 min idle).
- [ ] Events exist, all shadow: `sqlite3 nevr-ac-shadow.db "SELECT is_shadow, COUNT(*) FROM detection_events GROUP BY is_shadow;"` — only `1|N`.
- [ ] Per-detector counts: `sqlite3 nevr-ac-shadow.db "SELECT detector_id, COUNT(*) FROM detection_events GROUP BY detector_id;"` — no detector > 50.
- [ ] For each player with events, export a visual spot check: `./nevr-ac --config configs/shadow_deploy.toml evidence-export --match <match-id> --player <player-id> review.html --include-shadow`.
- [ ] **BIO_001 must be 0** on bridge data: `sqlite3 nevr-ac-shadow.db "SELECT COUNT(*) FROM detection_events WHERE detector_id='BIO_001';"` — if > 0, disable BIO_001 and BIO_004 and restart.
- [ ] Severities finite: `sqlite3 nevr-ac-shadow.db "SELECT detector_id, MAX(severity), MAX(confidence) FROM detection_events GROUP BY detector_id;"` — all within 0–1.
- [ ] No auto-enforce: `sqlite3 nevr-ac-shadow.db "SELECT COUNT(*) FROM detection_events WHERE auto_enforce=1;"` — 0.
- [ ] No scores / cases (shadow never scores): `sqlite3 nevr-ac-shadow.db "SELECT COUNT(*) FROM suspicion_scores; SELECT COUNT(*) FROM review_cases; SELECT COUNT(*) FROM cross_match_review_cases;"` — 0, 0, 0.
- [ ] Match context stored: `sqlite3 nevr-ac-shadow.db "SELECT match_id, match_start_time, frame_count FROM match_contexts;"` — start time set (UTC RFC3339 `Z`).
- [ ] Telemetry stored: `sqlite3 nevr-ac-shadow.db "SELECT match_id, COUNT(*) FROM telemetry_frames GROUP BY match_id;"` — roughly 15 × players × seconds.
- [ ] Exact live ticks stored: `sqlite3 nevr-ac-shadow.db "SELECT match_id, COUNT(*) FROM match_ticks GROUP BY match_id;"` — roughly 15 × seconds for current bridge traffic; 0 means an outdated/custom producer omitted `raw_json`.
- [ ] DB size: `ls -lh nevr-ac-shadow.db*` (WAL sidecars included) — telemetry for one match is a few MB.

## After match 5

- [ ] All match-1 checks.
- [ ] Per-player spread: `sqlite3 nevr-ac-shadow.db "SELECT player_id, COUNT(*) FROM detection_events GROUP BY player_id ORDER BY 2 DESC LIMIT 10;"` — no single player > 100 events across 5 matches.
- [ ] Offline/live agreement: `./nevr-ac --config configs/shadow_deploy.toml reprocess-match <match-id>` — event counts comparable to the live run.
- [ ] STATE_006 stays disabled (suspended; no verified invariant).

## After match 10

- [ ] All checks above.
- [ ] DB size and memory: `ls -lh nevr-ac-shadow.db*`; `ps -o rss= -p $(pgrep nevr-server)` < 500 MB.
- [ ] Create a verified database backup and start the promotion loop in `docs/shadow_deployment_guide.md` (reprocess with a review-mode overlay on the copy, `flagged`, `report`, `evidence-export`, `verdict`, `calibration-report`).

## STOP conditions (shut down immediately)

- [ ] `nevr-server` or `nevr-bridge` crashes or panics
- [ ] Frame rejection > 20 % (`nevr_ac_frames_invalid_total` / `nevr_ac_frames_received_total`)
- [ ] Any single detector > 100 events in one match
- [ ] Any BIO_001 event on bridge data
- [ ] `nevr_ac_auth_failures_total` > 0 or `ANTICHEAT AUTH FAILED` in `bridge.log`
- [ ] All hand rotations zero (compat output / bridge `hand_tracking_lost`)
- [ ] DB > 100 MB after 10 matches, or RSS > 500 MB

## Shutdown

```bash
kill -TERM $(pgrep nevr-bridge)   # sends match_end for every polled match, prints "nevr-bridge shutdown summary"
kill -TERM $(pgrep nevr-server)   # closes connections, finalizes live matches (context, summary, scores), then closes the store
```

Stop the bridge first so the server receives `match_end` and finalizes cleanly; a server stopped first finalizes its live matches itself.

## After shutdown

- [ ] Run `./nevr-ac --config configs/shadow_deploy.toml backup backups/shutdown-<date>.db` for a consistent, integrity-checked offline copy. Do not copy only the `.db` file from a running WAL database.
- [ ] Review the detection event distribution per detector and per player.
- [ ] Compare known-cheater matches against known-clean matches using `evidence_json`.
- [ ] Document every suspected false positive with detector ID, `event_id` and the evidence, then feed it through the promotion loop (`verdict --detector ID=no`).
