# NEVR-Anticheat

Asynchronous, server-side cheat detection for Echo VR / Echo Arena on NEVR community servers (EchoTools / Nakama). It never runs inside a game server: telemetry is collected into a SQLite profiler database and 31 detectors analyse it there.

**Windows:** [Download NEVR-Anticheat-Setup.exe](https://github.com/MyGuyChromium/nevr-anticheat/releases/download/windows-latest/NEVR-Anticheat-Setup.exe), double-click it, and choose **Install**. No ZIP extraction, command prompt, administrator access, or manual folder selection is required.

**Validation status: 0 of 31 detectors have completed representative, independently labelled real-telemetry validation.** Every threshold still requires calibration, so the normal defaults and `configs/shadow_deploy.toml` keep every detector in shadow mode. Read `docs/production_readiness.md` before deploying anything.

Desktop 0.12.0 adds [autopocket catch review](docs/autopocket_review.md): sustained free-disc path changes before a confirmed catch, with moving-hand comparisons, contact exclusions and three trajectory projections in the physics inspector. This is permanently observation-only in this implementation; it cannot prove automated grip input or attribute the cause to the receiver. Re-import original replays to populate the new presence-aware telemetry; older normalized caches may not contain enough inputs.

**Automatic findings:** the installed app analyzes replays itself; no assistant,
cloud upload, admission list or player-name rules are needed. Each analyzed match
shows players with **Review needed**, detector-specific signal counts and Spark
replay buttons, including observation-only findings that add zero score. The
same `assessment` is included in player JSON, summary JSON, player CSV and case
reports. **No signals** does not mean verified fair play. These are review
findings, not automatic bans or independently confirmed cheating verdicts.

**0.11.0 private test candidate:** **Explain checks** records actual processing
decisions, including skipped checks and observation-only results. The new
[evidence review workspace](docs/blind_review_workflow.md) binds local attachments
and two locked reviewer decisions to an exact player/window and candidate.
Connected-group and legal-context metrics expose gaps in the evaluation data.
See the [hardening and acceptance record](docs/comprehensive_readiness.md) for
reliability fixes, verification scope, and the remaining real-world requirements.

**0.11.1 first-launch fix:** Setup creates the per-user evidence directory, and
desktop startup creates a missing parent for a normal configured database path.
Windows release checks now launch the actual installed app with its installed
configuration before preparing preservation-test data. See the
[first-launch regression record](docs/first_launch_regression.md).

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
  SQLite (source of truth) ──▶ feature extraction ──▶ 31 detectors ──▶ per-match scores
        ──▶ review cases (single-match RC-*, cross-match XM-*) ──▶ moderator verdicts ──▶ calibration
```

Design rules:

- **Raw data is truth.** Original Echo snapshots (`match_ticks`) are immutable source data. Normalized player frames (`telemetry_frames`) are a rebuildable cache; detection events, scores and cases are derived and can be recomputed at any time with `reprocess-*`.
- **Shadow-safe deployment.** A detector in `mode = "shadow"` stores its events with `is_shadow = 1` and they are never scored: no scores, no cases, no per-event log lines. Both the built-in defaults and `configs/shadow_deploy.toml` use that posture until labelled replay calibration supports promotion.
- **Evidence, not scores.** Every event carries typed evidence, observed vs expected values and a causal frame range; moderators review evidence, never a number.
- **Async, multi-pass.** Live inline detection exists for immediate feedback, but the canonical analysis is reprocessing stored telemetry.

## Toolchain

### Windows download with EXEs

GitHub's green **Code → Download ZIP** button is a source-code archive, so it intentionally does not contain compiled programs. Normal users should download [`NEVR-Anticheat-Setup.exe`](https://github.com/MyGuyChromium/nevr-anticheat/releases/download/windows-latest/NEVR-Anticheat-Setup.exe), double-click it, and choose **Install**. Setup installs for the current Windows account, creates the shortcuts, appears in Windows Installed apps, and launches NEVR. It does not need administrator access.

Installed evidence lives under `%LOCALAPPDATA%\NEVR-Anticheat`, separate from the replaceable program files. Upgrades and normal uninstall preserve it. The [`NEVR-Anticheat-Windows-x64.zip`](https://github.com/MyGuyChromium/nevr-anticheat/releases/download/windows-latest/NEVR-Anticheat-Windows-x64.zip) remains available for portable and advanced use.

Each successful master build publishes SHA-256 checksums, runs a clean install/uninstall preservation test, and scans the output with Microsoft Defender when it is available on the build runner. GitHub provenance attestations are added when the repository visibility supports them. Authenticode publisher identity is enabled automatically when the repository's signing-certificate secrets are configured; see [`docs/windows_release_trust.md`](docs/windows_release_trust.md).

The desktop's update card follows the verified `windows-latest` release tag. After this version has been installed once, **Install update** downloads the new setup package, verifies its revision-bound manifest, published SHA-256, and exact size, then closes NEVR, updates all five executables, and reopens it. The evidence database, labels, settings, and rollback snapshots stay outside the replaceable program files. Portable copies keep a manual-download fallback because safely updating an arbitrary portable folder is ambiguous.

Because this repository is private, the desktop cannot borrow credentials from a signed-in browser. Set a read-only GitHub token in `NEVR_GITHUB_TOKEN` for in-app checks and one-click installation; the manual-download button continues to use your signed-in browser. Remove that requirement by making the release publicly readable in the future—never embed a repository token in the program.

The installer can be shared directly and runs offline, but widespread downloads
and updates need a publicly readable release channel. Keeping the source private
and publishing binaries to a separate public release repository is possible;
that requires explicitly setting up the repository, publication permissions and
the updater's trusted release source. It is **not configured by this change**.
Do not distribute a maintainer token to end users or change source visibility
just to work around a download failure.

Maintainers can build that package locally from PowerShell with:

```powershell
.\scripts\package-windows.ps1
```

The result is `dist\NEVR-Anticheat-Windows-x64.zip` and includes all five Windows executables, both configs, and setup instructions.

- Go **1.26+** (see `go.mod`).
- **CGO is required** (`github.com/mattn/go-sqlite3`). Build with `CGO_ENABLED=1` and a C compiler on `PATH`: gcc on Linux, MinGW-w64 on Windows (for example `winget install BrechtSanders.WinLibs.POSIX.UCRT`). A binary built with `CGO_ENABLED=0` compiles but panics at the first `sql.Open`.

```bash
export CGO_ENABLED=1
go build -o nevr-ac      ./cmd/anticheat   # offline CLI + moderator tools
go build -o nevr-desktop ./cmd/desktop     # desktop app: upload replays in a local web page
go build -o nevr-server  ./cmd/server      # live ingestion server
go build -o nevr-bridge  ./cmd/bridge      # broadcaster → server bridge
go build -o nevr-compat  ./cmd/compat      # /session payload compatibility checker
```

## Quick start: offline (replays)

**Drag and drop:** drop one or more `.echoreplay` files (or a folder of them) onto `nevr-ac.exe` in Explorer. It runs `analyze` (or `batch`) on each, prints the flagged summary, and waits for Enter. Without `--config` the database `nevr-anticheat.db` is created next to the executable.

```bash
# Ingest one replay: stores telemetry + raw ticks, runs detection, stores events/scores/cases.
./nevr-ac analyze match.echoreplay
# A match that is already stored is skipped; --force refreshes normalized telemetry and replaces its derived outputs.
# A recording whose session id changes mid-file (a rematch in the same lobby) holds two
# matches: each is analyzed, stored and reported on its own, and the stored check is per match.
./nevr-ac analyze match.echoreplay --force

# Ingest a directory (parallel, general.max_workers), then run cross-match aggregation.
./nevr-ac batch ./replays/ [--force]

# Re-run the current mapper and detection on STORED data (no replay file needed).
./nevr-ac reprocess-match <match-id>
./nevr-ac reprocess-player <player-id>
./nevr-ac reprocess-timerange 2026-01-01T00:00:00Z 2026-03-01T00:00:00Z   # [since, until) on MATCH time

# Cross-match aggregation: decayed 0-100 score per player across matches, XM-* cases.
./nevr-ac cross-match

# Moderator workflow
./nevr-ac flagged                         # pending single-match (RC-*) and cross-match (XM-*) cases
./nevr-ac report <case-id>                # RC-<match>-<player>: evidence, stored events, decisions
./nevr-ac evidence-export <case-id> review.html
                                              # standalone visual replay + typed evidence
./nevr-ac evidence-export --match <match-id> --player <player-id> shadow.html --include-shadow
                                              # inspect shadow detections before promotion
./nevr-ac cross-match-report <case-id>    # XM-<player>: per-match evidence
./nevr-ac player-history <player-id>      # capped, decayed cross-match history from the DB
./nevr-ac verdict <case-id> confirmed_cheat --by <moderator> --detector THROW_006=yes --detector BIO_001=no
./nevr-ac calibration-report --since 30d  # per-detector confirmed / false-positive counts from verdicts
./nevr-ac observation-report --since 30d  # versioned event rates and confidence/severity tails
./nevr-ac backup backups/pre-review.db    # consistent, integrity-checked SQLite snapshot
./nevr-ac version
```

`reprocess-*` uses the current adapter and configured physics. When immutable `match_ticks` are available, it re-maps those snapshots and atomically refreshes `telemetry_frames` before replacing derived analysis; this lets possession, pose and velocity mapper fixes apply to old matches. Legacy matches without raw snapshots fall back to their stored normalized frames. A malformed or incomplete raw stream fails safely instead of silently mixing old and new telemetry.

Verdicts are `confirmed_cheat`, `false_positive`, `inconclusive` or `needs_more_data`. `--detector ID=yes|no|uncertain` records per-detector feedback that overrides the case verdict for that detector in the calibration report. Shadow events inside decided cases count on purpose: that is how a shadow detector earns promotion.

All CLI commands take `--config <file>` before the command; `--verbose` / `-v` (or `NEVR_AC_VERBOSE=1`) additionally reports the effective detector table at startup. Reports go to stdout, logs to stderr. Every command except `version` loads and validates the config first (unknown keys, detector IDs, params and wrong value types are errors); `version` prints the version without reading any file.

## Desktop app

`nevr-desktop` is the point-and-click front end of the offline path: start it, drop `.echoreplay` files onto the page, read the result.

```bash
go build -o nevr-desktop.exe ./cmd/desktop     # CGO_ENABLED=1, like every binary here
./nevr-desktop.exe                              # or double-click it
```

On start it opens the database (`nevr-anticheat.db` next to the executable, or `general.db_path` with `--config <file>`), listens on **127.0.0.1 only** at a random free port behind a random per-run token, and prints the URL. It opens that page in a dedicated Edge/Chrome app window when available, falling back to the default browser (`--no-browser` only prints the URL; `--port` pins the port). Starting a second copy for the same database hands off to the existing local session instead of competing for SQLite. Files or whole folders are analyzed through a visible, one-file-at-a-time queue with per-file progress, cancel, and retry controls. Browser intake streams recordings up to 4 GB directly into the durable recovery queue instead of making an extra operating-system temporary copy; larger recordings can be processed in place by the watched-folder workflow. Desktop uploads always re-analyze stored matches with the current detectors and replace their derived events, scores, and normalized cache; immutable raw ticks and human calibration labels remain preserved.

The desktop's **Regression & threshold lab** turns each event marked Correct or False positive into a local replay expectation, compares current output with the pre-reprocess snapshot, and previews numeric detector parameters against stored normalized telemetry. Both single-match previews and sweeps exclude reserved holdout and quarantined recordings. Reserved holdout is not an independently sealed prospective test: viewing findings freezes a clean candidate identity, and changing that candidate invalidates the exposed cohort. A selected value can be saved only as a shadow candidate. Match reports link to aggregate player history, and every event explains what crossed the boundary and whether it contributed score.

The **Automation, recovery & updates** panel can watch a local replay folder for new stable `.echoreplay` files, recover uploads durably spooled before an interruption, compare the release's embedded Git commit with the last successfully packaged `windows-latest` release, install a verified update with one click, and create a privacy-redacted support ZIP. Support bundles exclude raw ticks, normalized frames, the database, player identities and the watch-folder path. NEVR never silently installs an update: automatic checks only announce availability and the owner clicks **Install update**.

The health panel reports the complete SQLite footprint—the main database plus active `-wal` and `-shm` sidecars. **Check & checkpoint** first runs SQLite's quick integrity check and then folds committed WAL pages into the main file; it does not prune telemetry, labels, or evidence and does not require the extra database-sized workspace of a `VACUUM`.

Desktop v0.8 adds the **Investigation, reliability & calibration studio**: telemetry-quality gating, shared legal-motion context, uncertainty-banded throw reconstruction, grouped detector agreement, synchronized timelines/charts, chronological Spark playlists with keyboard review, durable notes/bookmarks, saved filters, review progress, standalone case reports, portable human-evidence libraries, per-run config provenance, shadow-safe named profiles, training/validation threshold matrices, player-grouped dataset splits, true confusion matrices, 95% confidence intervals, label disagreements, drift/performance monitoring, generated adversarial probes, a first-run checklist, and verified backup/restore. The exact feature contract and interpretation limits are documented in [the investigation suite guide](docs/investigation_suite.md).

Desktop v0.9 adds a hashed, revision-bound calibration packet; independent frame-level physics CSV export; bounded legacy replay parsing; native NDJSON/ZIP/session/legacy fuzz targets; a real server lifecycle integration test and coverage floor; replay/storage soak reporting; mandatory vulnerability and security scans; SBOM and dependency automation; and an OIDC-based Microsoft Artifact Signing path for identity-backed Windows releases.

Desktop v0.10 adds a verified one-click Windows updater. It refuses to interrupt active replay analysis, authenticates private-release requests without leaking the token across origins, validates a commit-bound release manifest and SHA-256 before execution, shuts down SQLite cleanly, updates the complete installed toolset through the existing preservation-tested installer, and relaunches NEVR automatically.

Desktop v0.10.1 fixes raw replay reads in databases containing both legacy and current tick storage, reports match-summary save failures, and strengthens updater redirect and shutdown checks. The Windows release workflow now runs the desktop test suite on Windows before packaging, including the Windows-only updater regressions.

Desktop v0.10.3 separates **Review needed**, **No signals**, and **Insufficient data**. Each analyzed player has persisted per-detector coverage, including phase/warmup dispatch counts and necessary-input checks for throw speed, wrist rotation and playspace motion. Disabled detectors and missing coverage never certify fair play; shadow findings remain visible at zero score. Coverage is stored atomically with the analysis and refreshed on re-import. Legacy results without coverage request re-analysis instead of silently implying success. The throw reconstruction margin is explicitly an unvalidated review guide, not a measurement confidence interval.

Private development now includes a [hash-pinned replay regression harness](docs/private_replay_regressions.md), [throw-measurement verification record](docs/throw_measurement_verification.md), and [isolated Windows beta readiness runner](docs/private_beta_readiness.md). These are maintainer tools; ordinary users still drop replays into the app. See the [six-track acceptance record](docs/private_readiness_acceptance.md) for implemented safeguards, local verification, and the independent evidence still required before wider release. Nothing in these workflows makes the repository public.

The [v0.9 hardening acceptance record](docs/hardening_v09.md) maps each of the eight tracks to its implemented evidence and clearly separates the remaining real-replay, independent-review, and publisher-identity gates.

On an update, the Windows installer snapshots the previous program files under `%LOCALAPPDATA%\NEVR-Anticheat\program-rollbacks`. Run `Rollback-NEVR.cmd` from the installed program folder to restore the newest snapshot; the evidence database is never replaced by program rollback.

Each match can be indexed as **Known clean**, **Suspected**, or **Confirmed cheat**, with a note. This organizes the library but does not automatically label a detector. Every observation can separately be labeled **Correct**, **False positive**, or **Unsure**. Use **Ground-truth window** for missed opportunities too; its default is uncertain, and it requires explicitly chosen frame boundaries. Promotion requires structured, corroborated, blinded review by two distinct reviewer identities and independent evidence references, not merely a label or admission. These identities are operator attestations, not authenticated reviewers. All labels survive re-analysis and portable evidence export. Persistent player-grouped assignments quarantine conflicting exposure; they cannot establish what a reviewer saw outside the app. The conservative promotion gate can move only one eligible detector into scored human-review mode, never automatically validate a production release. Automatic enforcement remains disabled and failed gates roll back to shadow. See [the calibration workflow](docs/calibration_workflow.md) and [collection protocol](docs/calibration_collection_protocol.md) for the exact requirements.

For an independent frame-by-frame physics comparison, run `nevr-compat --physics-audit physics-audit.csv match.echoreplay`. The export includes raw reported velocity, independently derived pose velocity, the production extractor result, playspace motion, legal-motion context, wrist rates, disc state, and releases for alignment with Spark.

Before a release, run the isolated [replay soak test](docs/soak_testing.md) on the largest available real replay library. Its machine-readable report captures throughput, peak memory, failures, and database growth without touching the normal evidence database.

The [security release gates](docs/security_release_gates.md) add reachable-vulnerability and source security scans, scheduled parser fuzzing, dependency updates, a CycloneDX SBOM, and conditional CodeQL/dependency review where GitHub licensing permits them.

The match card includes a **Physics inspector** for every detection and throw. It recomputes the same derived pose, hand, reported-game, and playspace values over a ±8-frame timeline, performs an independent finite-difference audit, highlights conservative hand/head-contact classification, and displays the original focus tick when present. It explicitly cannot prove feet, guardian-origin, or arena-object contact because Echo telemetry does not expose them. A **Diagnostic ZIP** captures this timeline, detector evidence, telemetry-health findings, app/build/schema versions, and the config fingerprint; player names and identifiers are redacted before export.

Raw-source schema health is checked automatically when a match is analyzed or reopened. Missing, intermittent, invalid, always-inactive, and newly unrecognized fields are surfaced without changing scores. Full-fidelity raw telemetry can be written to a verified local ZIP. **Archive + remove active raw** requires typing the exact match ID and only prunes raw ticks after checksum verification; normalized frames, detections, labels, cases, and summaries stay in the database, and **Restore raw** restores the newest verified archive. Full-fidelity archives contain player identity data and should remain private. Removed SQLite pages become reusable internally, although the database file may not immediately shrink.

**Open clip** reconstructs a short native `.echoreplay` around the exact detection or throw and starts it directly in Spark Replay Viewer—no browser evidence tab. Open Replay Viewer from Spark once so Spark installs `Documents\Replay Viewer\Replay Viewer.exe`; custom installs can be selected with `NEVR_REPLAY_VIEWER`. History can be searched, sorted, and filtered by detector, replay label, or pending flag. The health panel reports database, raw/normalized/archive sizes, Spark status, and offers verified database backup, data-folder access, and generated-clip cleanup. **Quit** on the page or Ctrl+C in the console stops it. Nothing is reachable from other machines and nothing but this page can reach the app.

The API behind the page (all under `/<token>/`): `POST api/analyze` (multipart `files[]`, optional legacy `force=1`; each `results[i]` carries the file's matches in `matches[]`), `POST api/analyze/cancel`, health/calibration/regression/threshold-lab/settings/recovery/update routes, ground-truth opportunity and gated-promotion routes, hashed calibration-report export, match comparison and player-history routes, match/event label routes, physics-inspector and redacted diagnostic-ZIP routes, verified raw archive/prune/restore routes, maintenance backup/data-folder/clip-cleanup/support-bundle actions, flagged/observation/history/match routes, summary JSON / player CSV exports, Spark viewer / detector clip / frame clip launch routes, legacy offline evidence-export routes, and `GET quit`.

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

Bridge defaults worth knowing: `--nakama-auth device` (creates/uses a bridge account through the server key; the raw server key alone cannot list matches), `--modes echo_arena` (prefix filter; `*` polls everything), `--session-check strict` (stops a poller whose `/session` `sessionid` is not the Nakama match id), `--broadcaster-allowlist` (the trust boundary: only listed IPs/CIDRs are polled; everything forwarded was pulled over plaintext HTTP from whatever host the match label advertises). Every batch and `match_start` carries `server_id = "<broadcaster_ip>:<api_port>"`; the server records it on the match context (`server_id`, persisted with `match_contexts` and shown wherever the match context is printed, e.g. `report`), not on every event row, so provenance is looked up per match. Each current bridge batch also carries the exact raw `/session` response as `raw_json`; the server stores it once per tick in the same transaction as the normalized player frames.

Expected log lines: server `telemetry server starting`, `game server connected`, `live match created`; bridge `connected to anticheat WebSocket (hello ok)`, `match_start`, periodic `bridge status` with `frames_acked`, and `match_end` with a reason (`post_match_detected`, `session_changed`, `broadcaster_unreachable`, `broadcaster_error`, `session_mismatch`, `cancelled`). Live matches are finalized on `match_end`, after `--stale-match-after` (30 min idle) or at shutdown.

### Wire protocol in one paragraph

Every message is JSON over the WebSocket. A message with a non-empty `type` is a control message, anything else is a `FrameBatch` `{match_id, server_id, timestamp, raw_json, frames[]}` (`raw_json` is optional for legacy/custom producers). After the upgrade with `Authorization: Bearer <token>` the server sends `{"type":"hello","auth":"ok"}`; on a bad token it sends `{"type":"error","reason":"unauthorized"}` and closes. While frames are pending it sends `{"type":"ack","accepted":N,"rejected":M,"ignored":K}` about once per second, the counts covering every frame since the previous ack (`ignored` = duplicate rows the store discarded). The producer sends `{"type":"match_start", match_id, server_id, game_mode, map, is_private, teams:{player_id:"blue"|"orange"}}` when a poller starts and `{"type":"match_end", match_id, reason}` when it stops. A producer must run a read loop: a peer that never reads fills its socket buffer and is disconnected on the next blocked ack write. Full field reference: `docs/telemetry_contract.md`.

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
| THROW_003 | Unnatural Release Angle | throw | 0.5 | Unverified — needs wrist-flick data; skips possible sampled headbutts |
| THROW_004 | Repeated Release Signatures | throw | 0.6 | **UNSAFE** — FPs on regrab playstyle |
| THROW_005 | Superhuman Target Precision | throw | 0.7 | Unverified — needs accuracy data |
| THROW_006 | Trajectory Correction (Mags) | throw | 0.8 | Physics-grounded |
| THROW_007 | Penalty Field Tampering | throw | 0.6 | **STUB** — no penalty field telemetry |
| THROW_008 | Speed-Distance Anomaly | throw | 0.5 | Unverified — needs arena physics data |
| BIO_001 | Impossible Wrist Rotation | bio | 0.6 | Unverified — rotation convention unconfirmed; unreachable at 15 Hz |
| BIO_002 | Impossible Hand Speed | bio | 0.6 | Physics-grounded (player-relative; 50 m/s = 4x human limit) |
| BIO_003 | Zero Hand Jitter | bio | 0.5 | Unverified — controller jitter baseline unknown |
| BIO_004 | Zero Aim Wobble | bio | 0.5 | Unverified — rotation variance baseline unknown |
| MOV_001 | Impossible Player Speed | movement | 0.7 | Physics-grounded |
| MOV_002 | Teleportation | movement | 0.8 | Physics-grounded |
| MOV_003 | Zero-Inertia Direction Change | movement | 0.5 | **UNSAFE** — FPs on wall bounces |
| MOV_004 | Boost Speed Cap Violation | movement | 0.5 | Disabled — needs is_boosting field |
| MOV_005 | Boost Spam | movement | 0.6 | Disabled — needs is_boosting field |
| MOV_006 | Physical Playspace Walking | movement | 0.75 | EchoTools-grounded translation candidate; shadow-only because legal leans cannot be excluded |
| STATE_001 | Impossible Grab Distance | state | 0.5 | Unverified — needs grab range data |
| STATE_002 | Stun Recovery Exploit | state | 0.7 | Physics-grounded |
| STATE_003 | Shield Duration Abuse | state | 0.6 | Disabled — needs shield_active field |
| STATE_004 | Damage Immunity Exploit | state | 0.8 | Disabled — needs is_immune validation |
| STATE_005 | Cooldown Bypass | state | 0.6 | Disabled — needs shield_active field |
| STATE_006 | Score Manipulation | state | 1.0 | **SUSPENDED** — no confirmed invariant |
| STATE_007 | Impossible Punch Range | state | 0.5 | Disabled — needs per-frame stun data |
| STATE_008 | Autopocket Catch Review | state | 0.0 | Experimental — immutable observation-only; cause and actor unverified |
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

Immutable source data: `match_ticks` (the raw profiler/session JSON once per tick for `.echoreplay` imports and current live bridge traffic). Rebuildable source cache: `telemetry_frames` (one normalized frame per player per tick) and `match_contexts` (roster, teams, current physics, source, `match_start_time`, the anchor for decay and `reprocess-timerange`). These rows are never pruned automatically; forced replay analysis and raw-backed reprocessing atomically replace only the normalized cache, never the raw snapshots.

`nevr-ac backup <output.db>` uses SQLite's consistent snapshot mechanism, verifies the copy with `PRAGMA quick_check`, and refuses to overwrite an existing artifact. Take a backup before reprocessing, threshold changes, moderator batches, or manual maintenance. Evidence exports are self-contained HTML or JSON files; they contain player identifiers and telemetry and should be handled as sensitive moderation data.

Derived data: `detection_events` (`is_shadow`, `analysis_source` = initial/reprocess, typed `evidence_json`), `suspicion_scores` (append-only snapshots, `scope` = match/cross_match), `review_cases`, `cross_match_review_cases`, `moderator_decisions` (verdicts + per-detector feedback), `enforcement_actions`. `match_summaries` caches the human-facing scoreboard, player stats, goals, throws, and suspicion view; older `.echoreplay` matches are rebuilt lazily from `match_ticks`. `nevr-server` prunes events older than 90 days and scores older than 30 days.

## Shadow Mode

`mode = "shadow"` means: the detector runs, its events are stored with `is_shadow = 1`, and nothing else happens. Shadow events are excluded from scoring, from single-match and cross-match cases, from `flagged`, `player-history` and PAT_003 history, and from per-event log lines. Both shipped configurations use this mode for all 31 detectors. `verdict` and `calibration-report` provide the calibration path before promoting anything. STATE_008 cannot be promoted by configuration or the calibration UI: its current observation does not establish a violation or identify the responsible actor.

Weights and `auto_enforce` come from config: the binaries build detectors through `detect.BuildAll`, which applies `enforcement_weight` via `SetWeight` and `auto_enforce` via `SetAutoEnforce`, so `MakeEvent` stamps the configured weight on every event.

## Testing

```bash
CGO_ENABLED=1 go test ./...
go test -race ./...
go test -bench=. ./tests/
```

## License

Proprietary. For authorized use by the NEVR community moderation team.
