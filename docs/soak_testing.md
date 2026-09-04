# Replay soak and storage testing

`scripts/soak-replays.ps1` repeatedly analyzes a replay directory against a new, isolated SQLite database. It never points at the normal evidence database and never modifies the source replays. Every replay is forced through a full replacement analysis on each iteration, exercising parsing, detection, transactional replacement, and database growth.

Build `nevr-ac.exe`, then run:

```powershell
.\scripts\soak-replays.ps1 -ReplayDirectory "D:\Spark Replays" -Executable ".\nevr-ac.exe" -Iterations 3 -HashInputs
```

For a quick smoke run, add `-MaxFiles 10`. The output folder contains `soak.db`, per-process logs, and `soak-report.json` with input inventory, exit codes, elapsed time, throughput, peak working set, and database size after every replay. Keep the JSON with the release evidence.

A release candidate passes only when all runs exit successfully, repeated forced analysis does not grow the database unexpectedly, peak memory remains bounded for the largest real replay, and a backup/restore of `soak.db` succeeds. Investigate any throughput collapse by replay rather than averaging it away.
