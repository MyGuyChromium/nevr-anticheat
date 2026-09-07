# Replay soak and storage testing

`scripts/soak-replays.ps1` repeatedly analyzes a replay directory against a new, isolated SQLite database. It never points at the normal evidence database and never modifies the source replays. Every replay is forced through a full replacement analysis on each iteration, exercising parsing, detection, transactional replacement, and database growth.

Build `nevr-ac.exe`, then run:

```powershell
.\scripts\soak-replays.ps1 -ReplayDirectory "D:\Spark Replays" -Executable ".\nevr-ac.exe" -Iterations 3 -HashInputs
```

For a quick smoke run, add `-MaxFiles 10`. The output folder contains `soak.db`, per-process logs, and `soak-report.json` with input inventory, exit codes, elapsed time, throughput, peak working set, and database size after every replay. Keep the JSON with the release evidence.

Use PowerShell 7.2 or newer. `-OutputDirectory` names the output **parent**;
each invocation creates a timestamp/random-ID child so an existing `soak.db`
or report is never reused. Each process has a default ten-minute limit; adjust
`-RunTimeoutSeconds` for approved long recordings. An optional
`-MaxWorkingSetMiB 2048` records a failure and stops the owned process tree if
its sampled peak memory exceeds 2 GiB (`0`, the default, disables this memory
limit). This is sampled process memory, not a hard OS allocation quota.
Reports include the executable SHA-256, whether it changed during the run,
per-run `pass`/`fail`, and explicit timeout/memory/analysis failure reasons.

`scripts/test-soak-runner.ps1` checks isolation, apostrophe-containing paths,
repeat invocation, failure reporting, timeout and memory limits using a tiny
synthetic helper only. It does not establish real replay performance.

A release candidate passes only when all runs exit successfully, repeated forced analysis does not grow the database unexpectedly, peak memory remains bounded for the largest real replay, and a backup/restore of `soak.db` succeeds. Investigate any throughput collapse by replay rather than averaging it away.
