# Match Day Operator Checklist

## Pre-Launch (do once)

- [ ] Build binaries: `go build -o nevr-ac ./cmd/anticheat && go build -o nevr-server ./cmd/server && go build -o nevr-compat ./cmd/compat`
- [ ] Capture raw session: `curl http://127.0.0.1:6721/session > test_session.json`
- [ ] Run compat check: `./nevr-compat test_session.json` — verify 0 errors, review warnings
- [ ] Run strict check: `./nevr-compat --strict test_session.json` — review all strict failures
- [ ] Set auth token: `export NEVR_AC_AUTH_TOKEN=<secret>`
- [ ] Start server: `./nevr-server --config configs/shadow_deploy.toml --listen :8080 --metrics :9090 &`
- [ ] Verify health: `curl http://localhost:8080/health` — expect `{"status":"ok",...}`
- [ ] Verify metrics: `curl http://localhost:9090/metrics` — expect non-zero uptime

## During Match 1 (check every 60 seconds)

- [ ] Health check: `curl -s localhost:8080/health | python3 -m json.tool`
  - `connections` > 0
  - `frames_received` incrementing
  - `frames_rejected` < 5% of received
- [ ] No ERROR lines in logs: `grep '"level":"ERROR"' server.log | tail -5`
- [ ] No panics: `grep -i panic server.log`

## After Match 1

- [ ] Check DB exists and is non-empty: `sqlite3 nevr-ac-shadow.db "SELECT COUNT(*) FROM detection_events;"`
- [ ] Check per-detector event counts: `sqlite3 nevr-ac-shadow.db "SELECT detector_id, COUNT(*) FROM detection_events GROUP BY detector_id;"`
  - No single detector > 50 events
- [ ] Check max severity: `sqlite3 nevr-ac-shadow.db "SELECT detector_id, MAX(severity) FROM detection_events GROUP BY detector_id;"`
  - All severities look reasonable (0.0-1.0, not NaN)
- [ ] Check DB size: `ls -lh nevr-ac-shadow.db` — should be < 10MB

## After Match 5

- [ ] Same checks as Match 1
- [ ] Per-player event distribution: `sqlite3 nevr-ac-shadow.db "SELECT player_id, COUNT(*) FROM detection_events GROUP BY player_id ORDER BY COUNT(*) DESC LIMIT 10;"`
  - No single player > 100 total events across 5 matches
- [ ] Check for STATE_006 events (should be ZERO on clean matches): `sqlite3 nevr-ac-shadow.db "SELECT COUNT(*) FROM detection_events WHERE detector_id='STATE_006';"`
  - If > 0: **STOP — score tracking is broken**

## After Match 10

- [ ] All above checks
- [ ] DB size: `ls -lh nevr-ac-shadow.db` — should be < 50MB
- [ ] Memory usage: `ps aux | grep nevr-server` — RSS should be < 500MB
- [ ] Generate summary report: `./nevr-ac --config configs/shadow_deploy.toml flagged`
  - Review any flagged players — are they known cheaters or false positives?

## STOP Conditions (shut down immediately)

- [ ] Server crashes or panics
- [ ] Frame rejection > 20%
- [ ] Any single detector > 100 events per match
- [ ] STATE_006 fires on any match (broken score data)
- [ ] DB > 100MB after 10 matches
- [ ] Memory > 500MB
- [ ] All hand rotation data is zero (check compat output)

## Shutdown

```bash
kill $(pgrep nevr-server)
# Or: send SIGTERM to the server process (graceful shutdown)
```

## After Shutdown

- [ ] Copy `nevr-ac-shadow.db` for offline analysis
- [ ] Review detection event distribution
- [ ] Compare known-cheater matches vs known-clean matches
- [ ] Document any false positives with exact detector ID and evidence
