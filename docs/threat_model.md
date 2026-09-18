# Threat model: what the game server checks, and what it leaves to us

This note records what is known about the native Echo VR server's own
validation, so detector authors and reviewers know which checks exist nowhere
else. It changes no detector, threshold or status label.

**Scope limit (carry it with every claim below): Windows build 34.4.631547 only; Quest not checked.**

The facts come from a private reverse-engineering reference, EchoTools
`nevr-server-rs`. It is cited by path and section only; none of its text or
code is reproduced here. "Verified" below means the reference marks the claim
as read off the pinned binary; it has not been re-derived in this repository,
and nothing here has been validated against captured network payloads by us.

## 1. The server arbitrates ownership; it does not check grab geometry

Disc and object ownership is a request / grant-or-decline protocol decided by
one authority peer (the dedicated server).

Reference: `docs/re/ownership-and-possession.md`, section 6 "Authority-side
arbitration algorithm" (decision logic, stages 1 to 3), section 6.1 "The
Stage-2 permission gate", section 6.2.

What the arbiter checks, in order:

- the request belongs to this session, the sender handle is valid, the
  per-sender sequence number is exactly the next one, and the touch entry
  resolves (failures are dropped silently);
- the entity exists, the requested new owner is allowed to move the component
  (a per-team permission: non-playing roles are refused), and the requested new
  owner is not in a per-player blocking state;
- for a release, that the sender is the current owner; for a transfer, that the
  requester's view of the current owner is not stale and its stamp has not gone
  backwards.

What it does **not** check: distance between hand and disc, disc or hand
velocity, line of sight, or any position at all. No hand position and no disc
position is an input to the permission gate.

Do not read the per-team float constant inside that permission gate as a grab
radius. The reference establishes only that it separates playing from
non-playing roles; it is compared with a per-entity float, not with a measured
hand-to-disc distance.

**Consequence.** A client that asks for the disc from across the arena, with a
valid session, handle and sequence, is granted it. Geometry checks on our side
(STATE_001 grab geometry review, STATE_008 catch-approach review) are the only
line of defence against long-range grab and autopocket. They are also still
unverified review diagnostics: see each detector's own status.

## 2. "Physics validation" is a finiteness check

Reference: `docs/re/ownership-and-possession.md`, section 16, subsection "What
is not there".

The native routine whose name suggests physics-state validation only rejects
non-finite (NaN / infinite) floats in an incoming simulation state. It is not a
plausibility check: no speed cap, no position bound, no acceleration limit.

Reference: `nevr-server/src/game/goal_detect.rs` (module header),
`nevr-server/src/game/ownership.rs` (module header), and
`artifacts/nevr-composite-spec.md` (server authority section): clients simulate
physics, the owner's client authors the owned object's state, and the server
relays it. The server originates the score and arbitrates ownership; it does
not simulate the disc.

**Consequence.** Disc speed, throw speed and player movement limits are
enforced by nobody upstream of this project. THROW_001 and the movement
detectors are not a second opinion on a server check; they are the only check.

## 3. A cheater's client authors its own throw telemetry

`last_throw` (total speed, arm speed, wrist and movement contributions) is
computed by a game client and read through that client's own `/session` API.
This project only trusts it for throws by the recording client's own player, so
whenever it is used, the client that made the throw is also the client that
wrote the numbers describing it.

Reference: `data/captures/reference/README.md` (source of the `/session`
endpoint) and `data/captures/PROTOCOL_FLOW.md` (client HTTP API).

**Consequence.** Internally consistent engine throw values are not proof of
legitimacy: the same client that would forge a throw also writes the numbers
that describe it. THROW_001 version 2.0.0 therefore retains the local-client
report separately from sampled world-frame disc velocities; it does not select
the higher scalar or treat agreement as independent corroboration. Its
three-sample comparison describes repeated observed speeds, not client honesty
or a verified launch law. Neither an internally consistent report nor a missing
review event proves a release legitimate. See the
[current evidence contract](default_game_config_reference.md).

## 4. Direct holder-to-holder transfer is a native path

The arbiter's transfer path grants a change from one valid owner to another
without the object ever being unowned.

Reference: `docs/re/ownership-and-possession.md`, section 6 (stage 3, transfer)
and section 17.2.

**Consequence.** A recording in which possession moves straight from player A
to player B, with no free-flight sample between them, is something the native
protocol allows; it is not by itself evidence of tampering. STATE_001 keeps such
transfers as inconclusive records and invents no free-flight sample; that
remains correct.

## 5. Clients predict ownership, so possession can blip

On touch the client marks itself owner locally before the server answers, and
rolls back if it is declined.

Reference: `docs/re/ownership-and-possession.md`, section 7 "Client-side
prediction and reconcile" and section 16.

**Consequence.** A recording made on one client can show a brief possession
that the server never granted, followed by the real owner. Such a blip must not
be counted as two acquisitions, and a single-sample possession is weak evidence
of anything. Recordings are a client-local view, downstream of that client's
receive path, not the server's.

## 6. What this does not establish

- Nothing here measures how often any of these paths is abused.
- Nothing here validates a detector. Every detector keeps its current status
  label; shadow detectors stay shadow.
- Quest clients and other builds may differ:
  **Windows build 34.4.631547 only; Quest not checked.**
- The reference's ownership baseline comes from a single eight-player capture;
  it calls its own sample small.
