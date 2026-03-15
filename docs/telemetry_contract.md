# NEVR-Anticheat Telemetry Contract

## Overview

This document defines the exact JSON schema that game servers must send to the NEVR-Anticheat system. All field names, units, and valid ranges are normative.

## Transport

- **Protocol**: WebSocket (ws:// or wss://)
- **Endpoint**: `/telemetry`
- **Auth**: `Authorization: Bearer <token>` header on WebSocket upgrade
- **Message format**: JSON
- **Batching**: Frames are sent in batches per match tick (typically 1-5 frames per message)
- **Max message size**: 64KB
- **Max frame rate**: 30 frames/second per player (server enforced)

## Message Types

### 1. Frame Batch (primary telemetry message)

Sent every server tick (~15 FPS). Contains all player states for one match at one point in time.

```json
{
  "match_id": "string (required, unique match identifier)",
  "server_id": "string (required, server identifier)",
  "timestamp": "ISO-8601 datetime",
  "frames": [
    {
      "player_id": "string (required, unique player identifier)",
      "frame_index": 1234,
      "timestamp": 82.345,
      "delta_time": 0.067,
      "position": [12.5, 1.6, -3.2],
      "rotation": [0.0, 0.707, 0.0, 0.707],
      "left_hand_position": [12.2, 1.9, -3.0],
      "right_hand_position": [12.8, 1.9, -3.4],
      "left_hand_rotation": [0.0, 0.1, 0.0, 0.995],
      "right_hand_rotation": [0.0, -0.1, 0.0, 0.995],
      "is_stunned": false,
      "is_boosting": false,
      "shield_active": false,
      "is_immune": false,
      "has_possession": false,
      "estimated_ping_ms": 45.0,
      "game_phase": "playing",
      "blue_score": 4,
      "orange_score": 2,
      "goals": 1,
      "stuns": 3,
      "disc": {
        "position": [5.0, 2.1, 0.0],
        "velocity": [12.5, 1.0, -0.5],
        "holder_id": ""
      }
    }
  ]
}
```

### Field Reference

#### Player Frame Fields

| Field | Type | Unit | Required | Valid Range | Notes |
|-------|------|------|----------|-------------|-------|
| `player_id` | string | — | **yes** | non-empty | Stable identifier across sessions |
| `frame_index` | int | — | **yes** | >= 0 | Monotonically increasing per match |
| `timestamp` | float64 | seconds | **yes** | >= 0 | Seconds from match start |
| `delta_time` | float64 | seconds | no | [0.01, 0.5] | Time since previous frame. If absent, derived from timestamps. |
| `position` | [3]float64 | meters | **yes** | X: [-45, 45], Y: [-20, 20], Z: [-20, 20] | Player body/head center in arena coordinates (right-handed, Y-up) |
| `rotation` | [4]float64 | quaternion | **yes** | unit quaternion (x,y,z,w) | Player body/head orientation |
| `left_hand_position` | [3]float64 | meters | **yes** | within 2m of position | Left controller/hand in arena coordinates |
| `right_hand_position` | [3]float64 | meters | **yes** | within 2m of position | Right controller/hand in arena coordinates |
| `left_hand_rotation` | [4]float64 | quaternion | desired | unit quaternion | Left hand orientation. If unavailable, send identity [0,0,0,1]. |
| `right_hand_rotation` | [4]float64 | quaternion | desired | unit quaternion | Right hand orientation. If unavailable, send identity [0,0,0,1]. |
| `is_stunned` | bool | — | **yes** | — | Player is in stun state |
| `is_boosting` | bool | — | **yes** | — | Player is actively boosting |
| `shield_active` | bool | — | desired | — | Player shield/block is active. If unavailable, send false. |
| `is_immune` | bool | — | desired | — | Player has post-stun immunity or spawn protection. If unavailable, send false. |
| `has_possession` | bool | — | **yes** | — | Player holds the disc |
| `estimated_ping_ms` | float64 | milliseconds | desired | [0, 500] | Player's estimated round-trip latency. 0 if unknown. |
| `game_phase` | string | — | desired | see below | Current game phase. Default "playing" if unknown. |
| `blue_score` | int | points | desired | >= 0 | Current blue team score |
| `orange_score` | int | points | desired | >= 0 | Current orange team score |
| `goals` | int | count | desired | >= 0 | This player's goal count this match |
| `stuns` | int | count | desired | >= 0 | This player's stun count this match |

#### Game Phase Values

| Value | Meaning |
|-------|---------|
| `"playing"` | Active gameplay (default) |
| `"round_start"` | Round start countdown |
| `"round_over"` | Round just ended |
| `"pre_match"` | Pre-match lobby |
| `"post_match"` | Post-match scoreboard |
| `"overtime"` | Overtime play |

#### Disc Fields

| Field | Type | Unit | Required | Valid Range | Notes |
|-------|------|------|----------|-------------|-------|
| `position` | [3]float64 | meters | **yes** | arena bounds | Disc center position |
| `velocity` | [3]float64 | m/s | **yes** | magnitude < 100 | Disc velocity vector |
| `holder_id` | string | — | **yes** | player_id or "" | Player holding disc, or empty if free |

### 2. Match Start Event (optional)

Sent once when a match begins. If not sent, the system auto-discovers match context from frame data.

```json
{
  "type": "match_start",
  "match_id": "MTX-20260315-173022-EU2",
  "server_id": "eu-west-2",
  "map": "mpl_arena_a",
  "game_mode": "Echo_Arena",
  "is_ranked": true,
  "is_private": false,
  "players": [
    {"player_id": "PLR-001", "team": "blue", "display_name": "PlayerOne"},
    {"player_id": "PLR-002", "team": "orange", "display_name": "PlayerTwo"}
  ],
  "start_time": "2026-03-15T17:30:22Z"
}
```

### 3. Match End Event (optional)

```json
{
  "type": "match_end",
  "match_id": "MTX-20260315-173022-EU2",
  "final_score": {"blue": 10, "orange": 6},
  "duration_seconds": 522.5
}
```

### 4. Example: Throw Transition

A throw is detected by the pipeline when `has_possession` transitions from `true` to `false` between consecutive frames. The game server does NOT need to send throw events explicitly.

Frame N (holding disc):
```json
{"player_id": "PLR-001", "has_possession": true, "disc": {"holder_id": "PLR-001", "velocity": [0,0,0]}}
```

Frame N+1 (disc released):
```json
{"player_id": "PLR-001", "has_possession": false, "disc": {"holder_id": "", "velocity": [15.2, 2.1, -0.8]}}
```

### 5. Example: Stun Transition

Frame N: `"is_stunned": false`
Frame N+1: `"is_stunned": true` (player got stunned)
Frame N+45 (~3 seconds later): `"is_stunned": false` (stun ended)

### 6. Graceful Degradation

The system is designed to operate with partial telemetry:

| Missing Field | Impact | Detectors Affected |
|--------------|--------|-------------------|
| hand rotations | BIO_001, BIO_004 disabled | Wrist rotation, aim wobble |
| ping | Lag tolerance uses 0ms (strictest) | All timing-sensitive |
| shield_active | STATE_003, STATE_005 disabled | Shield duration, cooldown |
| is_immune | STATE_004 disabled | God mode detection |
| blue/orange_score | STATE_006 disabled | Score manipulation |
| goals/stuns | STATE_002, STATE_007 reduced | Stun recovery, punch range |
| game_phase | All detectors active always | May fire during non-play phases |
