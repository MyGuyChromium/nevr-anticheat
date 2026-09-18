# Controlled comparison recordings

Use the existing [private replay regression command](private_replay_regressions.md)
to retain correlated recordings, independently established labels, and measured
alignment evidence. This protocol collects evidence; it does not tune detectors,
enable enforcement, verify a runtime build, or certify accuracy. The application
remains review-only and the 18.9 m/s project reference is unchanged.

## Collect deliberately, with permission

Arrange a private, consenting test session. Preserve the original recordings and
recording-tool versions. Obtain permission before sharing any player's recording,
identity, video, voice, or configuration. Keep recordings, manifests, reports,
configuration snapshots, and databases outside Git or under ignored `dist/`.
Never attach private evidence to a public pull request or CI job.

Capture the subject's local view and at least one other participant's remote view
of the same action. Include a server-relay view when it is actually available and
authorized. A relay can forward client reports: **server-relay is not synonymous
with authoritative simulation telemetry**. Record actual availability rather than
inventing a missing view. Recorder/subject identities are provenance assertions,
not authenticated facts.

For legal comparisons, vary one factor at a time and retain failed attempts too:

- stationary and translating throws, documented native-assistance settings;
- normal releases, slaps, headbutts, and ambiguous contacts;
- near/far catches, hand swaps, brief regrabs and possession flicker;
- unobstructed flight and known bounces or player contacts;
- known capture rates and deliberately observed loss/disconnect conditions.

Do not run unauthorized tools in public matches. Independently confirmed positive
examples need exact event windows and permitted evidence; an allegation or an
entire player's reputation does not label every action. Retain uncertain windows
as uncertain. Compare release-direction/shot-targeting behavior separately from
receiver-associated catch paths; neither name identifies a macro or setting.

## Freeze provenance before looking at results

Capture a behavior baseline with the existing `replay-regression capture` command,
then retain it unchanged. Prepare a separate, versioned private manifest copy with
`capture_provenance` on **every** case. Its schema remains
`nevr-private-replay-regression/v1`; legacy manifests without this extension work
unchanged. Once any case uses the extension, unassigned cases are refused.

Each case records:

- `protocol`: `nevr-controlled-comparison/v1`.
- `session_group`: a stable pseudonymous session-family ID shared by all clips
  and viewpoints from that session. Renaming clips does not create new data.
- `split`: `development` or `holdout`; assign the whole session before inspection.
- `view`: `local`, `remote`, or `server_relay` relative to `subject_player_id`.
  Local has the same `recorder_player_id`; remote has a different roster player.
  Relay leaves the recorder absent. These claims cannot change detector inputs.
- `game_build` and `game_config`: explicit `{ "status": "unknown" }`, or
  `status: recorded` with `value`. A recorded build is an identifier; a recorded
  configuration value is the SHA-256 of the separately retained settings artifact.
  **Recorded is not verified active configuration.** Shipped defaults do not
  establish this match's overrides, precise engine behavior, or build applicability.

The following synthetic `capture_provenance` value is exercised by a test against
the real replay engine with generated fictional telemetry. It is not a complete
manifest or a real recording, and must not be pasted into a real player's case:

```json
{
  "protocol": "nevr-controlled-comparison/v1",
  "session_group": "synthetic-session-a",
  "split": "development",
  "view": "local",
  "subject_player_id": "echovr:1",
  "recorder_player_id": "echovr:1",
  "game_build": { "status": "unknown" },
  "game_config": { "status": "unknown" }
}
```

Use the existing `windows[].provenance` for independent event labels. `capture`
only freezes current output; it does not create ground truth. A `confirmed` window
requires the existing two-reviewer, decisive-label, pinned-artifact and note
requirements. Hide detector results during initial adjudication. Keep the broader
[calibration collection protocol](calibration_collection_protocol.md), including
player correlation and opportunity-quality restrictions.

## Measured alignment, not guessed latency

Optional `alignment_anchors` identify the same visible or instrumented marker
across a session's recordings. Each anchor has a stable `id`, a `match_id` present
in that case, `method` (`visual_event` or `instrumented_marker`),
`recording_time_seconds`, strictly positive `uncertainty_seconds`, and `evidence`
containing a private `path` plus SHA-256. Paths resolve relative to the manifest.
Keep measurement notes and the marker/video in that pinned artifact.

Times are elapsed seconds from each source recording's start, **not** game-clock
values. An anchor may be at time zero. Bound uncertainty from the actual sampling
interval, marker identification, frame/video resolution, and clock measurement.
Do not manufacture exactness with zero uncertainty or estimate an offset as half
the player's ping. The checker verifies artifact hashes; it cannot verify that a
human measured them correctly. Anchors are retained as metadata and never
automatically resample telemetry, correct trajectories, or change detector inputs.

There are at most 32 anchors per case, seven days of elapsed time, one hour of
positive uncertainty per anchor, 128-character stable identifiers and 4,096 cases
in an extended manifest. Nonfinite times, ambiguous identities, missing evidence,
duplicate anchors and unsupported methods are refused rather than silently fixed.

## Split discipline and repeatable execution

Keep one combined provenance manifest for the eventual comparison so cross-split
checks can see both sets. A shared source digest, session group, or observed match
ID crossing development/holdout is rejected before a run directory is created.
Local/remote/relay views and clips from one session stay together even when bytes
differ. Develop using development recordings only; do not run the combined
manifest until the candidate and independent expectations are frozen and the
holdout evaluation is authorized. `check` analyzes **all** listed cases.

The guard only examines the supplied manifest. It cannot discover omitted or
renamed sessions, earlier viewing, disguised derivative recordings, shared players
in other manifests, or dishonest labels. It is not a sealed holdout and does not
replace the application's stricter calibration/promotion gates. After inspecting
a holdout to tune a detector, it is development data; collect a new prospective
cohort rather than relabeling the old one as untouched.

```powershell
go build -o dist/private-regression/replay-regression.exe ./cmd/replay-regression
.\dist\private-regression\replay-regression.exe check --manifest dist/private-regression/comparison-v1.json --out dist/private-regression/comparison-run-001
go test ./internal/regression ./cmd/replay-regression
```

Create the parent directory first; the output directory must not already exist.
For nondefault analysis settings pass the same `--config` used for capture.
The existing analysis-configuration pin remains separate from the recorded game
configuration. Reports preserve capture provenance, build/configuration pins and
behavior outcomes. A changed alignment artifact, replay, or analysis configuration
fails preflight. Zero signals does not establish a fair-play opportunity; a passed
regression is not a detector-accuracy result. No new synchronized recordings or
independent validation results are claimed by this implementation.
