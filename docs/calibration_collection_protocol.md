# Calibration collection protocol

NEVR cannot be made trustworthy by guessing better thresholds. Detector promotion requires replay evidence whose ground truth was decided independently of the detector output. This protocol is the collection contract for that evidence.

## Non-negotiable rules

1. Keep every detector in `shadow` while collecting. Never use a NEVR score as the ground-truth label.
2. Preserve the original `.echoreplay`; work on a copy and keep player identities private.
3. Review the synchronized Spark clip before revealing the detector when the desktop offers blind review.
4. Label a detector opportunity, not an entire player. A player can have both legal and illegal windows in one match.
5. Use `positive` only when the behavior is independently confirmed. Use `negative` only when the same detector had a real opportunity and the behavior was legal. Use `uncertain` for occlusion, missing telemetry, ambiguous contact, or reviewer disagreement.
6. A distinct second reviewer must confirm every decisive positive or negative before it can count toward scored-review eligibility. Fill `reviewer_id`, `verifier_id`, `verified_ground_truth`, `evidence_method`, and `evidence_reference`, and record blinded primary review. Use `uncertain` for disagreement. Both IDs and references are recorded attestations, not authenticated identities or automatic verification of the artifact. A participant-level admission cannot label every throw by that player.
7. Do not copy one player across training, validation, and holdout. NEVR groups connected matches that share any player and exports the assignments.

## What to collect

For each detector, the conservative human-review policy requires at least 160 positive and 800 legitimate opportunities, 10 players, and 20 matches overall. Validation and reserved holdout each need at least 80 positives, 400 legitimate opportunities, five players, and five matches. Precision/recall/false-positive point limits and Wilson bounds must also pass; the count floors are not evidence that a detector is safe. See [the calibration workflow](calibration_workflow.md) for exact policy limits and correlated-opportunity limitations.

The legitimate library should deliberately cover:

- low, normal, and high ping;
- low, standard, and high capture rates;
- clean and degraded tracking;
- leaning and short room-scale steps;
- legal boosting, stacking, block pushes, slaps, and headbutts;
- catches, drops, goals, respawns, round transitions, and spectator changes;
- slow moving throws as well as the fastest legal throws;
- repeated uploads/re-analysis of the same source.

The confirmed-positive library should identify how the behavior was confirmed (controlled reproduction, trusted witness/video, or another independent artifact). A detector agreeing with itself is not confirmation.

## Collection workflow

1. Import the replay into the desktop. Re-analysis is always enabled; an existing match is refreshed without turning the duplicate into an error.
2. Check **Telemetry & schema health** first. If a required field is missing or the sample is quality-gated, record the window as `uncertain`.
3. Open the exact frame or event in Spark. Use the physics inspector for pose, hand, disc, and reported-velocity context.
4. Add a **Ground-truth window** around the complete opportunity, including a small lead-in and follow-through. Select the detector, player, positive/negative/uncertain truth, behavior type, and a precise note.
5. When an event exists, use blind event review first and reveal the detector afterward. Ground-truth windows supersede overlapping event labels so a case is never double-counted.
6. Resolve reviewer disagreements outside the score. Keep unresolved examples `uncertain`.
7. Export both the **Portable evidence library** and the **calibration packet**. Store them beside the replay manifest in a private, versioned location.

## Release decision

The calibration packet includes the application revision, configuration and calibration fingerprints, physics constants, effective detector settings, player-grouped assignments, confusion matrices, Wilson 95% intervals, drift summary, and promotion state. Its SHA-256 covers the exact payload.

A candidate may enter scored human-review mode only when the in-app gate passes on current-provenance, independently corroborated windows. Promote one detector at a time, monitor fresh false positives and drift, and roll it back to shadow on a gate failure. Automatic punishment remains disabled. `MOV_006` and `BIO_001` cannot be promoted through this gate while their observability limitations remain unresolved.

Reserved holdout is not proof of an untouched test set: match views and metrics can reveal findings. First exposure locks the candidate's app/build/behavior fingerprint; changing it or lacking trustworthy provenance permanently quarantines that cohort. The app explicitly reports no sealed holdout and no production validation. Before claiming release readiness, freeze a clean identifiable candidate, prospectively collect an external unseen cohort, independently adjudicate windows, evaluate once, and retain a complete signed-off decision record. Further tuning requires a new prospective cohort. Do not claim population-level accuracy from opportunity-level Wilson intervals when throws share players or matches.

## Independent physics comparison

Export a replay audit with:

```powershell
.\nevr-compat.exe --physics-audit physics-audit.csv match.echoreplay
```

The CSV puts raw game velocity, an independent pose derivative, the production extractor result, reconstructed playspace residuals, legal-motion context, wrist rates, disc state, and releases on one row per player-frame. Align `frame_index` and `timestamp_s` with Spark. A disagreement must be explained before changing a threshold; it is not itself proof of cheating.
