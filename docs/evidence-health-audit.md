# Evidence, source health and policy hardening

Audit baseline: `9964bbf` (merged PR #32). Catalog version:
`evidence-contract-2026-09-08-v1`. This is an implementation audit, not a
claim of verified gameplay mechanics or real-world accuracy.

## Existing capabilities retained

The project already had one production parser/mapper/extractor/detector path,
typed player IDs, explicit attachment and rotation validity, source-bound
release reconstruction, per-incident deduplication, scoring caps, review
overturns, blind review, evidence retention settings, detector disable/shadow
configuration, version/config-pinned replay comparisons, grouped calibration
data, and Windows packaging tests. This pass does not replace those systems.

`STATE_001`, `THROW_005`, `THROW_006` and `STATE_008` already had immutable
review-only promotion guards. No production call site wires the separate
`internal/enforce.Engine` kick/ban callbacks into live ingestion. Live processing
creates review recommendations, not automatic bans. Authentication cannot make
a compromised sender's gameplay claims true.

## Confirmed defects and changed boundaries

| Baseline defect | Implementation boundary / fix | Regression evidence |
| --- | --- | --- |
| Snapshot hashing converted exact int64 IDs to float64 and omitted observed state | `adapter.sessionFingerprint`: exact integer bytes, presence and mapped state fields | adjacent IDs above 2^53; state-only changes |
| Source clock reversal became plausible next-frame cadence | `Mapper.tickTimestamp`: local source epoch, explicit rebased time basis, zero boundary dt | clock discontinuity and epoch identity tests |
| Live retries/reordered old frames rebased into fresh evidence | `MatchManager.HandleFramesWithRaw`: reject stale/retry observations, retain consistent raw/frame association | duplicate, reorder, retry, loss, jitter, restart tests |
| Project rules absent from regression config pin | `regression.ConfigFingerprint` includes project rules | rule-only change invalidates fingerprint |
| Broadcaster allowlist checked only initial destination | bridge HTTP policy rejects redirects, including discovery/auth paths | cross-destination redirects, configured private address tests |
| WebSocket upgrade could outlive TCP connect deadline | bridge sender uses bounded context for the complete dial/upgrade | stalled-upgrade cancellation/shutdown test |
| Server CLI overrides applied after validation | effective server config revalidated before DB/listening | invalid effective flag tests |
| One shared ingest token could submit unrelated matches/players | operator-owned principal grants; remote startup fails closed without grants | token/server/match/player and control-hijack tests |
| Arbitrary control types and roster keys grew bookkeeping | fixed metric labels, roster admission and bounded rate/warning maps | unknown controls and limit tests |
| Live score could include an event that failed to persist | atomic events+scores live append; persistence-failure latch withholds later scores/cases/callbacks, raw capture continues | transaction failure/rollback, restart marker, healthy finalization tests |
| Live processing assigned 100 quality to each batch | incremental per-player/source data health and explicit source-scoped live report | one-frame batches, faults/recovery, independent-source tests |
| Cross-match summaries could revive overturned/zero-weight evidence and bypass scoring cooldowns | reviewed evidence filtering and shared scorer contribution rules | cross-match cooldown/review/zero-weight regressions |
| Derived composite findings could score underlying evidence twice | derived pattern scoring protection | scorer pattern regressions |

See the current PR for the head-specific test results. A named regression is
implementation evidence; it is not a labeled Echo gameplay trial.

## Capability and rule catalog

Every production catalog entry has `detect.CapabilityFor(ID)` containing its
actual declared review inputs, source trust, timing/validity requirements,
missing behavior, versioned rule meaning/units/provenance/applicability,
exceptions, test locations and missing enforcement observables. Analysis
coverage snapshots the configured detector parameters rather than substituting
later defaults. The catalog-completeness test fails when a newly registered
detector has no contract.

All 31 current detectors are **ineligible for automatic enforcement** in this
pipeline. That is independent of healthy telemetry and operator threshold
settings. No current feed supplies an independently attested build/ruleset and
adequately validated enforcement model. Existing descriptive findings remain
available; source-affected findings are shadow/zero-weight. Stateful detectors
still receive observations so they can close tracks and record abstentions;
dispatch is not a successfully evaluated event opportunity.

| IDs | Available review behavior | Missing enforcement prerequisites / legal alternatives |
| --- | --- | --- |
| THROW_001 | sampled attributed release speed | exact release model, cap/reference frame, contact impulses, allowed contributions |
| THROW_002 | sampled release acceleration | contact-free interval and verified acceleration/impulse model |
| THROW_003 | hand-motion/disc direction disagreement | release geometry; slaps, headbutts and contacts; not a settings measurement |
| THROW_004 | repeated release signatures | independent legitimate distribution and input observability |
| THROW_005 | targeting statistics with misses retained | actual target geometry and validated aim model |
| THROW_006 | free-flight trajectory diagnostics | authoritative forces, collisions and integration model |
| THROW_007 | penalty-field heuristic | actual collision/penalty geometry and current build behavior |
| THROW_008 | sampled speed/distance anomaly | contact history, frame of reference and verified free-flight model |
| MOV_001 | sampled speed envelope anomaly | regrabs, stacking, launches, playspace motion and corrections |
| MOV_002 | sampled position discontinuity | authorized teleport, respawn and source correction events |
| MOV_003 | sampled direction change | wall slaps, regrabs and collision impulses |
| MOV_004 | boost-state speed check where boost is explicitly known | carrying/stacking context and verified boost rules |
| MOV_005 | boost-state timing where explicitly known | trusted input edges and actual cooldown |
| MOV_006 | residual physical playspace motion | feet, playspace origin and contacts; legal leans/lunges remain alternatives |
| STATE_001 | sampled disc acquisition geometry | grabbing hand/event time, true interaction geometry and verified rule |
| STATE_002 | sampled stun state heuristic | presence-valid stun edges, duration and transition/respawn rules |
| STATE_003 | sampled shield duration heuristic | presence-valid blocking edges and actual eligibility/duration |
| STATE_004 | sampled immunity heuristic | presence-valid immunity and proof that a contact should succeed |
| STATE_005 | sampled shield cooldown heuristic | trusted shield edges and current cooldown rules |
| STATE_006 | sampled score consistency | authoritative goals and reset/scoring semantics |
| STATE_007 | sampled punch/stun association | actual hit geometry, event attribution, blocking and immunity |
| STATE_008 | pre-catch trajectory review | grip inputs, collision impulses and verified ownership model |
| BIO_001 | observed wrist angular-rate anomaly | adequate sampling and independent legal slap/headbutt/tracking validation |
| BIO_002 | hand-speed anomaly | calibrated tracking, legal contacts/regrabs and physical motion |
| BIO_003 | low hand jitter | unsmoothed tracking and legitimate distribution |
| BIO_004 | low aim wobble | unsmoothed orientation and legitimate distribution |
| PAT_001 | sampled release regularity | trusted grip edges; cannot establish frame-perfect input automation |
| PAT_002 | repeated release locations | independent events and trusted release/hand attribution |
| PAT_003 | historical review evidence pattern | independent reviewed incidents and original config/contribution provenance |
| PAT_004 | composite review signal | underlying event independence; derived evidence must not score twice |
| PAT_005 | reach/playspace heuristic | playspace calibration, actual boundary/feet observations and legal reach |

Common units: positions/distances in metres, velocity in m/s, acceleration in
m/s², wrist rate in rad/s, source intervals in seconds; parameters explicitly
named as frames or counts remain those units. Existing per-detector degree/
radian conversion and squared variance units remain in their implementation.
Configured values are review thresholds, not measurements or probabilities.
No gameplay threshold was increased to make an example pass.

### Corrected 19 / 4.7 relationship

The [original Kronos preview](https://medium.com/echo-games-blog/echo-arena-kronos-patch-preview-c1a91f1ea487)
describes a 4.7 m/s clamp for a chain carrying the disc. It does **not** establish
that every 19 m/s throw requires 4.7 m/s of body movement. Its 0.25 regrab timer
is seconds, not evidence of a 0.25 metre grab radius. Historical patch behavior
does not independently verify the current build.

The existing owner configuration values 0.25 m, 19 m/s, 4.7 m/s and the separate
18.9 m/s legacy cap are preserved for compatibility, not newly validated. No
production evaluator has audited knowledge that activates the proposed 19/4.7
relationship. It must stay out of enforcement. Synthetic hypothetical-rule
tests cannot establish that Echo implements that rule.

## Data health and recovery

`PlayerCoverage.DataHealth` independently reports healthy/degraded/blind, active
reason codes, affected detector IDs, cumulative sample counts, last observed
frame, monotonic revision and recovery samples remaining. Eight consecutive
usable observations clear each operational fault; a new fault restarts that
fault's window. This is a conservative engineering policy, not game physics.
Live batches retain state. Stored snapshots replace by watermark; retrying one
does not add its counters again. Rejected old input cannot rewind the watermark.
Delayed/rolling findings are also checked against a bounded per-detector last
affected-frame watermark after recovery. Findings beginning before that boundary
remain shadow. This deliberately abstains some retrospective findings that were
entirely before a later fault; it does not claim exact interval overlap. Correct
causal ranges must cover all supporting samples for any history-based detector.

Missing source context or rejected/non-advancing observations are blind. Missing
disc data affects disc checks, not unrelated movement; missing reported velocity
affects physical playspace separation; missing/invalid hand data affects relevant
hand checks. Source/timing discontinuities break continuity. Simultaneous large
orientation jumps in at least three distinct players on the same source/epoch
cause an operator-review health fault for rotation checks, not cheating findings
or a declaration that the feed is corrupt. Quaternion sign is invariant. This
heuristic can abstain during coordinated legitimate turns; evidence is retained.

Desktop **Explain checks** exposes these states, counts and contracts. A final
healthy sample does not erase earlier blind/degraded counts. Unrecorded legacy
health remains unknown. Processing/storage failure is separately latched blind;
repair storage and perform offline re-analysis before relying on new scores.
An ingest health aggregate exposes incomplete analysis without player identities.

No additional background network alert, external notification, telemetry-mandatory
competition penalty, automatic ban or rollback executor was enabled. A live feed
that stops entirely still needs the existing idle/disconnect monitoring; sample
health cannot observe packets that never arrive. Processing backlog and source
truthfulness remain separate from numeric sample health.

## Evaluation and remaining limits

Production-path tests cover deterministic replay perturbations, exact IDs,
source epochs, retries, geometry/time/name invariance and health isolation.
Existing private regression comparisons assert behavior, not ground truth.
Player-frame counts are not detector opportunity counts. Geometry/catch logs
retain their actual eligible/excluded/inconclusive transitions; generic checks
do not silently receive invented opportunity denominators.

Before promoting any check, use independent reviewers and a pinned engine build,
retain related incidents/alternate recordings/transformed copies in the same
split, and evaluate disjoint matches, disjoint players and later-time recordings.
The [grouped validation guidance](https://scikit-learn.org/stable/modules/cross_validation.html#cross-validation-iterators-for-grouped-data)
explains why related observations must not be split across tuning and evaluation.
Do not tune on a held-out recording and then report it as held out. The supplied
self-labeled fair-play examples are regression references, not an independent
false-accusation-rate estimate. No new labeled positive corpus or game build
attestation was supplied for this pass.

Recommended rollout remains offline → shadow → reviewer-only. Narrow enforcement
requires new independent evidence, not more lines of code. Existing per-detector
disable/shadow controls and versioned comparisons support rollback of analysis;
historical evidence should be reprocessed after a fix. A human overturn is not
automatically a backend unban because no such backend is wired here.

Security references: [Nakama gameplay validation responsibility](https://heroiclabs.com/docs/nakama/concepts/multiplayer/authoritative/),
[WebSocket authorization/replay protection](https://cheatsheetseries.owasp.org/cheatsheets/WebSocket_Security_Cheat_Sheet.html),
[SSRF allowlists and redirect control](https://cheatsheetseries.owasp.org/cheatsheets/Server_Side_Request_Forgery_Prevention_Cheat_Sheet.html).
Field presence alone does not establish enforceability: [community Echo API schema](https://github.com/Ajedi32/echovr_api_docs).
Go-specific checks follow the documented [JSON reuse/null behavior](https://pkg.go.dev/encoding/json),
[serialized time limitations](https://pkg.go.dev/time), and [race coverage limits](https://go.dev/doc/articles/race_detector).

## Verification log

Pending head-specific full verification; update before handoff. Local focused
pipeline and catalog tests pass after the initial capability/health changes.
No accuracy percentage, signing guarantee or production source attestation is
inferred from compilation, synthetic regressions or vulnerability scanning.
