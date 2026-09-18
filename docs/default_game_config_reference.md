# Default game settings versus replay evidence

This is a source audit, not detector promotion or independent accuracy validation.
The owner explicitly retained the **18.9 m/s configured review threshold** on
2026-09-18. No physics thresholds, tolerances, enabled detectors, scoring modes or
enforcement policy are changed by this audit. In particular, do not relabel that
threshold as an engine-enforced limit.

## Pinned reference

The reference is `thesprockee/nevr-cheat` revision
`8616ebf03d6f0f2c1071366d0aad6ee3403e9eb3`, specifically its default
`game_config/balance/` files. They are JSON-like source files with comments and
trailing commas, not strict JSON. Only plain game-setting facts are recorded here;
private moderation cases, identities, recordings, mechanisms and unpublished
empirical detector thresholds are not incorporated.

| Default file / field | Literal value | What it does not establish |
|---|---|---|
| `mp_frisbee_settings.json`: `use_grab_bubble`, `grab_range` | `true`, `0.25` m | A remote first-held hand/disc snapshot is not the geometry at acquisition. Timing, attachment snap, tracking origins and network delay still matter. |
| Same: `possesion_time` (source spelling) | `5.0` s | Possession/team ownership is not continuous hand attachment. |
| Same: `aim_assist.enable`, `target_choice_half_angle` | `true`, `22.5` degrees | Angle fields alone do not prove the implemented interpolation, triggering conditions or observed release direction. Native assistance must not be mistaken for an unauthorized setting. |
| Same: `aim_assist.min_angle`, `max_angle` | `10.0`, `45.0` degrees | These are not a permissible wrist-setting range or an automatic angle-cheat threshold. |
| `mp_arena_punch.json`: `left_hand_radius`, `right_hand_radius` | `0.06` m each | A sampled hand origin is not automatically the center of the punch collider at contact. |
| Same: `punch_hit_offset` | `(0, -0.04, 0)` | Applying the offset requires a verified coordinate transform and event-time pose. |
| Same: `head_hit_radius` | `0.15` m | Adding the radii does not produce a reliable remote-replay stun detector without attacker attribution and contact timing. |
| Same: `stun_length`, `stun_timing_window` | `2.0`, `0.25` s | Repeated stuns, missing state edges, build/config overrides and sampling can affect observed duration. Existing project's duration references are not silently replaced. |
| Same: `maximum_block_time`, `block_cooldown_time` | `4.5`, `1.25` s | Blocking and combat shields are different mechanics; do not replace unrelated cooldown settings. |

The files above contain **no throw-speed ceiling**. A proposed nominal 19.0 m/s
composition comes from separate reverse-engineering claims, not these JSON keys.
This pass neither republishes those private analyses nor treats them as a
universally verified rule. A shipped default also does not establish which
configuration a particular recorded match used.

The separately inspected `nevr-runtime-hardened` revision
`19a95a8643a864c4732ae9e473be29659d5bbc78` is a scaffold with design documentation,
not implemented server-side gameplay enforcement. It supplies no independently
verified event-time replay precision or authoritative throw/contact measurements.
Its presence is not evidence that a recording has become server-authoritative.

## Evidence-handling change

`THROW_001` is a **Reported Release Speed Review**. A first-free measurement,
later free-flight measurements and a local `last_throw` scalar are different
observations. Keep their raw values and timing separate. Never pick the maximum
and label the resulting mixture as the observed launch velocity.

Sample corroboration requires three consecutive known-free observations from a
consistent source with contact-counter and continuity checks. All three must
exceed the unchanged effective configured threshold. The retained median is
descriptive only; it neither replaces the raw first sample nor proves release
physics. An isolated spike, contact/gap/phase interruption, missing source or
incomplete window stays an uncorroborated observation, not a clean result or a
confirmed violation. A bound local-client report is contextual evidence, not an
independent witness. Uncorroborated and suspected-artifact observations have zero
enforcement weight, and this detector never stamps automatic-enforcement
permission. Review-only backend enforcement remains unchanged.

This review starts only after the shared extractor publishes a sampled release
(normally the second free tick). If the recording ends after only the first free
tick, the existing incomplete-release mechanics diagnostic remains available;
there is no published throw and no new `THROW_001` event. This pass does not
change that shared publication boundary or reconstruct missing samples.

Other detectors retain their existing release-event timing. Full original replay
data remains private and unchanged. Re-analyze a stored recording explicitly to
apply the new detector version; do not silently replace its immutable source.

## Outstanding validation

Use independently established event labels, representative legal contacts and
recordings held out from development. These source facts do not validate mags,
stun radius, wrist-setting detection or autopocket accuracy. Do not infer a
confirmed positive from an open moderation allegation or from a detector agreeing
with another detector. Unknown geometry and unobserved contacts must remain
explicit limitations.

## Reproducing the evidence regressions

From the repository root, the normal suite includes detector, full-pipeline and
SQLite close/reopen coverage for the new evidence fields:

```sh
go test -count=1 ./internal/detect/... ./internal/pipeline ./internal/storage/sqlite ./tests
go test -race -count=1 ./...
go build ./...
go vet ./...
node --test scripts/*.test.cjs
```

Tests cover an isolated spike versus three above-threshold samples, interruptions
and missing provenance, separate local reports, no automatic-enforcement flag,
redacted source identifiers, distinct incidents across recording-source changes,
and exact evidence persistence through database reopening. All constructed
measurements are synthetic; no player-specific exception is used.

For this pass, four authorized private recordings were also imported into fresh
disposable databases on the base and candidate versions, with unchanged input
hashes and no invalid emissions. No `THROW_001` event occurred on either version:
this is operational regression evidence, not a positive/negative accuracy test.
Recordings, identities, databases and detailed reports are not distributed.
Installer/visual acceptance, sustained-load qualification and independently
labelled detector accuracy are outside this bounded evidence-handling pass; do
not infer those approvals from a green unit-test run.
