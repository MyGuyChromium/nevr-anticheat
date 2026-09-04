# NEVR-Anticheat Telemetry Contract

## Overview

This document is the normative JSON schema between telemetry producers (cmd/bridge, or any game-server-side profiler) and the NEVR-Anticheat ingestion server (cmd/server, `internal/ingest`). It matches the decoder exactly: the server unmarshals batches into `model.PlayerTelemetryFrame`, control messages into `model.ControlMessage`, and `internal/model/telemetry_test.go` decodes every JSON example in this file to prove it. Where a value is accepted in more than one spelling the alias is stated; anything not listed here is silently ignored by the decoder.

## Transport

| Property | Value | Behaviour when exceeded |
|----------|-------|-------------------------|
| Protocol | WebSocket (`ws://` or `wss://`), endpoint `/telemetry` | — |
| Auth | `Authorization: Bearer <token>` on the upgrade request; token = `NEVR_AC_AUTH_TOKEN` of the server | server sends `{"type":"error","reason":"unauthorized"}` and closes; counted in `nevr_ac_auth_failures_total` |
| Message format | one JSON object per WebSocket text message | undecodable JSON: message dropped, `nevr_ac_batches_malformed_total` |
| Max message size | 1,048,576 bytes / 1 MiB (`[server] max_message_bytes`, `--max-message-bytes`) | sized for a 256 KiB raw `/session` document after JSON string escaping plus normalized player frames; the message is **discarded and the connection stays open** when exceeded, counted as one rejected frame and `nevr_ac_batches_rejected_total{reason="oversized_message"}` |
| Max frames per batch | 100 | whole batch rejected, `nevr_ac_batches_rejected_total{reason="oversized_batch"}` |
| Rate limit | 30 frames/s sustained per (match, player) (`max_frame_rate_per_player`), measured so that a reconnect backlog is tolerated: a producer that replays queued batches after a link hiccup loses nothing as long as the burst covers at most 10 s of frames at that rate (the limiter follows telemetry timestamps / a token bucket, not the arrival wall clock) | frames beyond the sustained rate plus burst are rejected, **not stored**, reported in the next `ack.rejected`, `nevr_ac_frames_ratelimited_total` |
| Identifier bounds | `match_id`, `player_id` ≤ 128 bytes, no control characters | frame/batch rejected (`invalid_match_id`, `invalid_player_id`) |
| Idle timeout | 5 min without any message (`idle_timeout`, `--idle-timeout`) | connection closed |
| Concurrency | 100 connections, 64 live matches, 16 players per match | connection refused with `too_many_connections`; frames for a 65th match / 17th player rejected |
| Health | `GET /health` on the telemetry port | `{"status":"ok","connections":N,"frames_received":N,"frames_rejected":N,"frames_rate_limited":N,"frames_ignored":N,"active_matches":N}` |

Timestamps in this document: `FrameBatch.timestamp` is an ISO-8601/RFC3339 wall-clock string; frame `timestamp`/`delta_time` are seconds relative to the match.

## Message types

Every message is a JSON object. A message with a non-empty `type` field is a control message; anything else is a frame batch.

### 1. Frame batch (producer → server)

Sent on every producer tick (the bridge polls `/session` at ~15 Hz). One batch carries the frames of **one match** at one sample time, one frame per player.

```json
{
  "match_id": "string (required, Nakama match id or replay session id)",
  "server_id": "string (required; the bridge sends \"<broadcaster_ip>:<api_port>\")",
  "timestamp": "2026-09-02T17:30:22Z",
  "raw_json": "{\"sessionid\":\"...\",\"game_status\":\"playing\",\"teams\":[...]}",
  "frames": [
    {
      "player_id": "echovr:PLR-001",
      "team": "blue",
      "frame_index": 1234,
      "timestamp": 82.345,
      "delta_time": 0.067,
      "position": [1.5, 1.6, -3.2],
      "rotation": [0.0, 0.707, 0.0, 0.707],
      "left_hand_position": [1.2, 1.9, -3.0],
      "right_hand_position": [1.8, 1.9, -3.4],
      "left_hand_rotation": [0.0, 0.1, 0.0, 0.995],
      "right_hand_rotation": [0.0, -0.1, 0.0, 0.995],
      "is_stunned": false,
      "is_boosting": false,
      "shield_active": false,
      "is_immune": false,
      "has_possession": false,
      "game_last_throw": {
        "arm_speed": 12.4,
        "total_speed": 19.91,
        "off_axis_spin_deg": 3.2,
        "wrist_throw_penalty": 0.4,
        "rot_per_sec": 8.1,
        "pot_speed_from_rot": 2.5,
        "speed_from_arm": 12.0,
        "speed_from_movement": 4.2,
        "speed_from_wrist": 3.71,
        "wrist_align_to_throw_deg": 4.5,
        "throw_align_to_movement_deg": 7.5,
        "off_axis_penalty": 0.2,
        "throw_move_penalty": 0.1
      },
      "estimated_ping_ms": 45.0,
      "game_phase": "playing",
      "blue_score": 4,
      "orange_score": 2,
      "goals": 1,
      "stuns": 3,
      "disc": {
        "position": [5.0, 2.1, 0.0],
        "velocity": [19.91, 0.0, 0.0],
        "possessor_id": "",
        "is_held": false
      }
    }
  ]
}
```

#### Batch envelope

| Field | Type | Required | Notes |
|-------|------|----------|-------|
| `match_id` | string | **yes** | ≤ 128 bytes. All frames in the batch belong to it. |
| `server_id` | string | desired | Provenance of the data. The bridge sends `"<broadcaster_ip>:<api_port>"` (the host it polled over plaintext HTTP). The server records it on the **match context** (`MatchContext.ServerID`, persisted with `match_contexts` and printed wherever the match context is shown, e.g. evidence reports); it is not copied onto individual frame or event rows. |
| `timestamp` | RFC3339 string | desired | Wall-clock time the producer sampled the state. |
| `raw_json` | string containing JSON | bridge: **yes**; other producers: optional | Exact `/session` response bytes that produced the normalized frames, including whitespace and fields the current mapper does not know. The server validates the inner JSON and stores it once in `match_ticks`. When present, every frame in the batch must have the same `frame_index`; invalid, empty-frame or multi-index raw batches are rejected as a unit. |
| `frames` | array | **yes** | 1–100 player frames. |

#### Player frame fields

| Field | Type | Unit | Required | Valid range / behaviour | Notes |
|-------|------|------|----------|-------------------------|-------|
| `player_id` | string | — | **yes** | non-empty, ≤ 128 bytes, no control chars | Stable across sessions. The bridge/adapter emits `echovr:<userid>`. |
| `team` | string | — | desired | `"blue"` or `"orange"` (case-insensitive); anything else is ignored | Builds `match_contexts.team_assignments` for live matches and `PlayerState.Team` (STATE_007 team filter). |
| `frame_index` | int | — | **yes** | ≥ 0, monotonic per (match, player) | Restart detection is per (match, player): a player's new index ≤ that player's last stored index means the producer restarted, and the whole batch is re-based (one shared offset per match, applied to `frame_index` and `timestamp`; warning, `nevr_ac_frames_rebased_total`). A batch that repeats the match's last index for a *different* player (one player per batch, same tick) is not a restart. Duplicate `(match, player, frame_index)` rows are ignored by the store and reported in `ack.ignored`. |
| `timestamp` | float64 | seconds | **yes** | ≥ 0, finite | Seconds since the producer's first sample of the match (the bridge stamps the real HTTP sample time). Negative or NaN → `invalid_timestamp`. |
| `delta_time` | float64 | seconds | desired | `0` = unknown (first frame); `0 < dt < 0.005` (`pipeline.min_frame_dt / 2`) or `dt > 60` (`pipeline.MaxProducerDt`, a clock jump) → frame rejected (`dt_out_of_range`) | Seconds since **this player's** previous frame; report the real sample spacing. A long gap (stall, reconnect) is accepted: the feature extractor treats a known dt above `pipeline.max_frame_dt` (0.5 s) as a gap, updating raw state but clearing kinematics and histories, and clamps smaller known values to [0.005, `max_frame_dt`] for finite differences. `max_frame_dt` never rejects a frame. |
| `position` | [3]float64 | metres | **yes** | not the zero vector, finite; `|X| ≤ 21`, `|Y| ≤ 15`, `|Z| ≤ 82` | Body/head centre, Y-up. Bounds are `DefaultPhysics` arena extents (32 × 20 × 154 m, measured on real recordings) plus 5 m tolerance; real data spans X ±16, Y −9..+9.5, Z ±78 with goals at Z ≈ ±36.078. Zero → `zero_position`, out of bounds → `out_of_arena_bounds`. |
| `rotation` | [4]float64 | quaternion (x,y,z,w) | **yes** | unit; near-unit is normalised (`|q| > 0.1`), NaN/Inf → zeroed | Body/head orientation. |
| `left_hand_position` | [3]float64 | metres | **yes** | finite | Left controller in arena coordinates. **Tracking loss = the zero vector** (NaN/Inf is replaced by it). |
| `right_hand_position` | [3]float64 | metres | **yes** | finite | Right controller. Same tracking-loss sentinel. |
| `left_hand_rotation` | [4]float64 | quaternion | desired | unit, or **`[0,0,0,0]` for tracking loss** | Do **not** send the identity when tracking is lost: identity is a real orientation and would be measured by BIO_001/BIO_004; the zero quaternion is skipped. |
| `right_hand_rotation` | [4]float64 | quaternion | desired | as above | |
| `is_stunned` | bool | — | **yes** | — | STATE_002, STATE_007. |
| `is_boosting` | bool | — | optional | — | No known source supplies it; MOV_004/MOV_005 stay inert when it is always false. |
| `shield_active` | bool | — | optional | — | STATE_003/STATE_005; unconfirmed in every known source. |
| `is_immune` | bool | — | optional | — | STATE_004; unconfirmed in every known source. |
| `has_possession` | bool | — | **yes** | — | A `true → false` transition is a throw (see §4). |
| `game_last_throw` | object | m/s and degrees | optional | `total_speed > 0`; every value finite; invalid objects are dropped (`invalid_game_last_throw`) | Echo VR's engine-authored local-client `last_throw` record. Emit it only on the release snapshot where the record changes and only on that local player's frame. See below. |
| `disc` | object | — | desired | see below | Omit only when the disc is unknown; every throw/disc detector needs it. |
| `estimated_ping_ms` | float64 | ms | desired | `[0, 1000]`; invalid/negative becomes 0, values above 1000 are clamped (`invalid_ping`) | Values above `pipeline.high_ping_threshold_ms` (150) set `IsHighPing`. Absent/0 = strictest tolerance. |
| `game_phase` | string | — | desired | see §5 | Absent/empty = active play. |
| `blue_score` | int | points | desired | ≥ 0 | MOV_002 goal cooldown and goal-side learning. |
| `orange_score` | int | points | desired | ≥ 0 | |
| `goals` | int | count | desired | ≥ 0 | This player's goals. |
| `stuns` | int | count | desired | ≥ 0 | This player's stun count; STATE_007 attributes punches from its increments. |

#### Disc fields

| Field | Type | Unit | Required | Notes |
|-------|------|------|----------|-------|
| `position` | [3]float64 | metres | **yes** | Disc centre. NaN/Inf in any disc field drops the disc from the frame (frame kept, `invalid_disc`). |
| `velocity` | [3]float64 | m/s | **yes** | Free-flight velocity. Magnitude < 100 in strict mode. |
| `speed` | float64 | m/s | optional | Derived as `|velocity|` when absent or 0; an explicit non-zero value wins. |
| `possessor_id` | string | — | desired | `player_id` of the holder, `""` when free. |
| `is_held` | bool | — | desired | `true` while a player holds the disc. |
| `holder_id` | string | — | alias | Accepted instead of `possessor_id`/`is_held`: a non-empty `holder_id` sets `possessor_id` to it and `is_held` to `true`; empty means free. |

Every player's frame in a tick should carry the same disc state (the adapter emits one `DiscState` per tick and copies it onto each frame). The throw detectors pick the possessor's copy, else the first player in sorted `player_id` order.

### 2. Control messages

Any message with a non-empty `type` is decoded as `model.ControlMessage`. Unknown types are logged at debug level and ignored. Field reference (all optional except `type`; each message uses the subset shown):

| Field | Type | Used by |
|-------|------|---------|
| `type` | string | all: `hello`, `ack`, `error`, `match_start`, `match_end` |
| `match_id` | string | `match_start`, `match_end`, `error` |
| `server_id` | string | `match_start`, `match_end` (producer provenance; `match_start` sets it on the match context) |
| `reason` | string | `match_end` (why the producer stopped), `error` (`unauthorized`, `too_many_connections`) |
| `auth` | string | `hello` (`"ok"`) |
| `accepted` | int | `ack`: frames the pipeline processed since the previous ack |
| `rejected` | int | `ack`: frames refused (validation, rate limit, caps) since the previous ack |
| `ignored` | int | `ack`: frames the store discarded as duplicates since the previous ack |
| `game_mode` | string | `match_start` (e.g. `Echo_Arena`) |
| `map` | string | `match_start` |
| `is_private` | bool | `match_start` |
| `teams` | object | `match_start`: `player_id → "blue" | "orange"` |

**Server → producer.** Immediately after a successful upgrade:

```json
{"type": "hello", "auth": "ok"}
```

On authentication failure, followed by close:

```json
{"type": "error", "reason": "unauthorized"}
```

At most once per second while any counter is non-zero (`accepted + rejected + ignored` equals the frames received since the previous ack):

```json
{"type": "ack", "accepted": 8, "rejected": 0, "ignored": 0}
```

A producer **must read** these messages. One that never reads fills its socket buffer and is disconnected when an ack write blocks for 10 s. The bridge treats a missing `hello` within 5 s or no ack within `--ack-timeout` (30 s) as a dead link and reconnects.

**Producer → server.** When a poller starts (or as soon as metadata is known):

```json
{
  "type": "match_start",
  "match_id": "6bef4ca8-2f1b-4d7a-9c1e-0a9d5c3b2e11",
  "server_id": "10.0.0.7:6721",
  "game_mode": "Echo_Arena",
  "map": "mpl_arena_a",
  "is_private": false,
  "teams": {"echovr:PLR-001": "blue", "echovr:PLR-002": "orange"}
}
```

The server applies `game_mode`, `map`, `is_private` and `teams` to the live match context (creating the match if needed) and persists it. Team entries for unknown players are added to the roster up to the player cap.

When the poller stops:

```json
{"type": "match_end", "match_id": "6bef4ca8-2f1b-4d7a-9c1e-0a9d5c3b2e11", "reason": "post_match_detected"}
```

The server finalizes the match: closes open dedup incidents, persists remaining events and scores, the context and a summary. Bridge reasons: `post_match_detected`, `session_changed` (a new session appeared under the same match id; a fresh `match_start` follows), `broadcaster_unreachable`, `broadcaster_error` (30 consecutive failed polls), `session_mismatch` (`--session-check strict`), `cancelled` (shutdown), `once` (`--once` mode). A match that never receives `match_end` is finalized after `--stale-match-after` (30 min idle) or at server shutdown.

### 3. Frame identity and ordering

- `frame_index` is match-relative and monotonic per (match, player). Frames are grouped by index and processed in ascending order; only indices present in a batch are visited.
- Restart detection is **per (match, player)**: a batch in which some player's new `frame_index` is ≤ that player's last stored index is a producer restart and is **re-based** with a warning — one shared offset for the whole match is added to every `frame_index` in the batch, and `timestamp` is re-based with the same rule (offset = last timestamp + nominal dt − the batch's minimum timestamp), so a restarted producer never overwrites stored rows and its clock continues from where the match left off. A batch that merely repeats the match's last index for a player who has not sent that index yet (a producer delivering one player per batch) is **not** a restart and is stored as-is.
- A row with an already stored `(match_id, player_id, frame_index)` is ignored by the store and reported as `ignored`.
- `timestamp` and `delta_time` are the producer's responsibility. The bridge stamps both from the real HTTP sample time per match (`delta_time` = 0 on a player's first frame; a negative delta is reported as 0).

### 4. Example: throw transition

A throw is detected by the feature extractor when `has_possession` goes `true → false` between a player's consecutive frames during an active phase. The producer never sends a derived throw event; it may attach the game-authored `game_last_throw` observation to the release frame.

For raw Echo VR/Spark snapshots, the adapter derives `has_possession` from
`holding_left` / `holding_right` when those fields are present because the
separate `possession` boolean can remain true for the last carrier after the
disc is released. Older/custom sources without hand-held item fields fall back
to `possession`.

Frame N (holding the disc):

```json
{"player_id": "PLR-001", "frame_index": 300, "timestamp": 20.0, "position": [1.0, 1.6, 10.0], "has_possession": true, "disc": {"position": [1.2, 1.7, 10.1], "velocity": [0, 0, 0], "possessor_id": "PLR-001", "is_held": true}}
```

Frame N+1 (released):

```json
{"player_id": "PLR-001", "frame_index": 301, "timestamp": 20.067, "delta_time": 0.067, "position": [1.0, 1.6, 10.0], "has_possession": false, "disc": {"position": [1.8, 1.8, 10.9], "velocity": [15.2, 2.1, -0.8], "possessor_id": "", "is_held": false}}
```

The same transition written with the alias:

```json
{"player_id": "PLR-001", "frame_index": 300, "timestamp": 20.0, "position": [1.0, 1.6, 10.0], "has_possession": true, "disc": {"position": [1.2, 1.7, 10.1], "velocity": [0, 0, 0], "holder_id": "PLR-001"}}
```

Possession drops during a non-active phase (round reset, pre/post match) are not throws.
The throwing hand is selected against the disc position from frame N (the last
held sample), not the already-moving free disc in frame N+1. If either prior
hand position is missing, the thrower can still be possession-attributed but
the left/right hand choice remains `unknown`; hand-dependent detectors do not
invent a hand.

Echo VR's top-level `last_throw` belongs only to `client_name`. The bridge
compares that record with the preceding snapshot, copies it to
`game_last_throw` only when it changes, and attaches it only to the matching
local player's frame. THROW_001 evaluates the higher of `total_speed` and the
simultaneous disc-velocity magnitude, so one source cannot conceal a faster
reading; both are retained in evidence. The remaining twelve fields are preserved,
including the engine's arm, movement and wrist contributions and its alignment
and penalty values. Remote players do not have this engine breakdown; their
release speed continues to use `|disc.velocity|`.

### 5. Game phase values

`game_phase` gates the detectors: the feature extractor always updates player state, but detectors run only during active phases.

| Value | Active? | Source |
|-------|---------|--------|
| `"playing"`, `""` (absent) | yes | `/session` `game_status` `playing`; empty defaults to active |
| `"round"`, `"overtime"`, `"sudden_death"` | yes | overtime is reported as `sudden_death` by Echo VR |
| `"round_start"`, `"round_over"` | no | players teleport to spawn; `score` from `/session` is mapped to `round_over` |
| `"pre_match"`, `"post_match"` | no | lobby / scoreboard |
| `"pre_sudden_death"`, `"post_sudden_death"`, any other string | no | unknown strings pass through and are treated as inactive |

### 6. Example: stun transition

Frame N: `"is_stunned": false`. Frame N+1: `"is_stunned": true` (player stunned). Frame N+45 (~3 s later at 15 Hz): `"is_stunned": false`. STATE_002 measures the stun length in seconds using the match tick rate; STATE_007 attributes the stun to a puncher whose `stuns` counter incremented within `attribution_window_frames`.

### 7. Hand tracking loss

Send the zero vector for a lost hand position and the zero quaternion `[0,0,0,0]` for a lost hand rotation. The adapter does exactly this when a `/session` hand pose has any zero direction vector or a degenerate basis (`MapperStats.HandTrackingLost`). Consumers skip zero hands: BIO_001/BIO_004 break their windows, BIO_003 drops that hand's window, PAT_005 skips the frame, and a release with either prior hand position unavailable records `throwing_hand = "unknown"` so THROW_003/THROW_004/PAT_002 skip its hand-dependent evidence. NaN/Inf hand data received on the wire is converted to the same sentinels by the pipeline validator (`nan_hand_position`, `invalid_rotation`).

### 8. Graceful degradation

| Missing / constant field | Effect |
|--------------------------|--------|
| hand rotations (zero quaternions) | BIO_001, BIO_004 never measure; THROW_004 signature loses the wrist dimension |
| `estimated_ping_ms` | ping tolerance 0 (strictest); no `IsHighPing` confidence reduction |
| `shield_active` | STATE_003, STATE_005 inert |
| `is_immune` | STATE_004 inert |
| `is_boosting` | MOV_004, MOV_005 inert |
| `blue_score` / `orange_score` | MOV_002 has no goal cooldown; goal side cannot be learned (throw goal selection falls back to release direction) |
| `stuns` | STATE_007 inert (it needs per-frame stun count increments) |
| `game_phase` | detectors run during resets and lobbies; expect MOV_002 false positives |
| `team` | `team_assignments` empty for live matches; STATE_007 cannot exclude teammates |
| `disc` | every throw/disc detector inert (THROW_001–THROW_008, STATE_001) |

### 9. Server metrics

Exported in Prometheus text format on the metrics port (`--metrics :9090`, `/metrics`):

`nevr_ac_uptime_seconds`, `nevr_ac_frames_received_total`, `nevr_ac_frames_processed_total`, `nevr_ac_frames_invalid_total`, `nevr_ac_frames_invalid_reason_total{reason}` (reasons: `missing_player_id`, `invalid_player_id`, `zero_position`, `invalid_position`, `invalid_timestamp`, `negative_frame_index`, `dt_out_of_range`, `out_of_arena_bounds`), `nevr_ac_frames_ratelimited_total`, `nevr_ac_frames_ignored_total`, `nevr_ac_frames_rebased_total`, `nevr_ac_batches_received_total`, `nevr_ac_batches_malformed_total`, `nevr_ac_batches_rejected_total{reason}` (`invalid_match_id`, `oversized_batch`, `oversized_message`), `nevr_ac_control_messages_total{type}`, `nevr_ac_auth_failures_total`, `nevr_ac_detection_events_total{detector}` (shadow and non-shadow), `nevr_ac_shadow_events_total{detector}`, `nevr_ac_events_deduplicated_total`, `nevr_ac_events_ratelimited_total`, `nevr_ac_events_invalid_total`, `nevr_ac_store_errors_total`, `nevr_ac_matches_created_total`, `nevr_ac_matches_ended_total`, `nevr_ac_score_snapshots_total`, `nevr_ac_active_connections`, `nevr_ac_active_matches`.
