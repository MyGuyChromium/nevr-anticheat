# THROW_001 measurement verification and limits

This note records what can be established from the available source and tests,
not a validated universal Echo VR throw-speed law. The configured 18.9 m/s cap,
detector thresholds, shadow posture and enforcement defaults are unchanged.

## Runtime source checked

The public EchoTools `nevr-runtime` checkout inspected was commit
`a692a304a79a75564f6b660ea81a96137b433357` (2026-08-05). Its snapshot definition
contains separate disc position and velocity arrays, with the velocity field at
offset `0x104`. The telemetry streamer copies the three velocity components
directly from game memory and serializes the same components; that path is not a
finite difference of sampled disc positions.
See [snapshot structure and offsets](https://github.com/EchoTools/nevr-runtime/blob/a692a304a79a75564f6b660ea81a96137b433357/src/runtime/server/telemetry_snapshot.h)
and [snapshot/serialization implementation](https://github.com/EchoTools/nevr-runtime/blob/a692a304a79a75564f6b660ea81a96137b433357/src/runtime/server/telemetry_streamer.cpp#L525).

The repository's symbol cache includes the name `maxthrowspeed`, but that is not
a numeric value or implementation of the release clamp.
See [symbol cache](https://github.com/EchoTools/nevr-runtime/blob/a692a304a79a75564f6b660ea81a96137b433357/src/runtime/log/symcache_data.cpp#L3183).
This checkout does not establish the exact cap/reference frame, the full legal
movement inheritance model, or whether every historical replay recorder used
this telemetry implementation. Do not infer those missing mechanics from a
symbol name or tune the cap to make unlabeled recordings agree.

## Distinguish the measurements

THROW_001 checks the sampled game-reported disc velocity magnitude, optionally
corroborated by a valid attributed engine `last_throw.total_speed`. The larger
scalar is retained by existing detection logic. Movement-aligned velocity and
player-relative disc speed are separate review context, not additional speed
allowances and not substitutes for a verified release law. Replay sampling still
affects whether the true release/contact was captured and who caused it, even
when the velocity itself is game-reported.

A reproduced evidence-only arithmetic bug was fixed in detector version 1.5.1:
when a valid engine scalar exceeded the sampled disc magnitude, the body-velocity
projection was divided by that unrelated scalar. For a sampled `(18,0,0)` m/s
disc vector, a 24 m/s engine scalar and `(4,0,0)` m/s player velocity, it displayed
3 m/s aligned movement instead of 4 m/s. The projection now uses the normalized
sampled disc vector. Parallel, opposing and perpendicular movement tests preserve
the independent sampled/engine values and do not mutate the tracked throw or
change the decision, threshold, severity, confidence or enforcement permission.

## Evidence still required

Before promoting this signal, independently review positive and negative release
windows, not only emitted events. Preserve the source hash, raw timestamp/frame,
nearest held and released samples, sample interval/gaps, possessor and attribution
confidence, disc vector and independent engine throw fields when present. Include
moving and stationary throws, different relative directions, legal block/slap
boosts, headbutts/slaps, deflections, and recorder/runtime variants. Missing
engine fields are unavailable evidence, not zero or proof of cheating.

The private regression runner can protect these windows from unintended changes,
but a preserved over-cap observation is not a verified cheat and a silent
self-reported fair-play recording is not a complete negative-control corpus.
No identity-based detector exemption is introduced.
