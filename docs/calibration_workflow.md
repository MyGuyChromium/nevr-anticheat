# Replay calibration and promotion workflow

No detector is production-validated until independently reviewed real Echo VR examples show how often it fires, misses, and stays quiet. Synthetic fixtures prove code behavior; they do not validate a threshold. NEVR v0.9 therefore permits only a staged move from shadow mode to scored human review. Automatic enforcement remains disabled.

## Build ground truth

1. Keep separate source folders for expected-clean play, suspicious play, and confirmed cheats. Never infer truth from the anticheat score.
2. Import recordings through the desktop queue. Replays may contain multiple sessions, so review each stored match rather than trusting the file name.
3. Use **Known clean**, **Suspected**, or **Confirmed cheat** to index the match. Add a note naming the reviewer and supporting clip. A whole-match label organizes the library; it does not automatically label a detector.
4. Review emitted observations as **Correct**, **False positive**, or **Unsure**. Turn on **Blind review** and decide before revealing the detector whenever possible. Direct event labels measure precision, but cannot reveal a missed detection.
5. Use **Ground-truth window** to mark a specific player, detector, behavior, and frame range as confirmed cheating behavior, legitimate behavior, or uncertain. These opportunities supply the missing denominator: a positive window with no overlapping event is a false negative; a legitimate window with no event is a true negative.
6. Inspect every decisive sample in Spark and the Physics inspector. Possible head contact suppresses wrist conclusions. Game-authored velocity can contain legal boosts, grabs, block slaps, and stacking. Echo telemetry has no feet, guardian origin, or arena-object contact identifier, so room-scale steps and legal leans cannot always be separated from replay telemetry alone.
7. Re-upload stored replays after code or configuration changes. Desktop intake always re-analyzes them and preserves raw ticks and human labels. Use **Before / after** to investigate removed correct signals, new false positives, and intentional fixes.
8. Export the portable evidence library regularly. Version 2 includes match labels, direct event reviews, blind-review state, ground-truth windows, notes, bookmarks, and saved filters; it excludes raw telemetry.

## Dataset isolation

The validation dashboard assigns 60% of the library to training, 20% to validation, and 20% to holdout. Assignment is deterministic, and every connected group of matches sharing any player stays in one split. This prevents the same player from leaking into training and holdout through different matches.

- Use **training** for threshold exploration.
- Use **validation** to compare the chosen threshold with unseen examples.
- The threshold sweep does not run on **holdout**. Holdout is evaluated only by the promotion gate.

Newly imported matches can connect two previously separate player groups, so inspect the split assignments before a formal release decision and preserve the exported evidence library and application/config fingerprints used for that decision.

## Read the metrics

The dashboard reports a true confusion matrix for each detector:

- true positive: event overlaps a positive opportunity;
- false positive: event overlaps a legitimate opportunity;
- false negative: no event overlaps a positive opportunity;
- true negative: no event overlaps a legitimate opportunity.

Precision, recall, false-positive rate, false positives per 100 legitimate opportunities, and 95% Wilson intervals are shown overall and for training, validation, holdout, ping, capture-rate, and telemetry-quality strata. Uncertain labels remain visible but are excluded from the confusion matrix. One confirmed example proves that a behavior is possible; it does not prove detector precision.

## Tune and promote

1. Open **Investigation & reliability studio → Experiment matrix**.
2. Sweep one numeric parameter over the training split. Candidate events are evaluated in memory and are not persisted.
3. Run the chosen value on validation. Do not tune again from holdout results.
4. Save the value as a shadow candidate, restart NEVR, and re-analyze the library so the normal detector path produces durable evidence under that exact profile.
5. Review disagreements and quality strata. Add evidence instead of weakening the gate to make a candidate pass.
6. Promote only when the desktop gate marks the detector eligible.
7. Export the **calibration packet** before promotion and after the monitored review period. Verify its payload SHA-256 and retain it with the evidence-library export; this freezes the effective thresholds, physics constants, data splits, build revision, confidence intervals, and drift summary used for the decision.

The built-in gate requires at least 30 positive and 300 legitimate opportunities, 5 distinct players, 10 matches, at least 5 positive and 50 legitimate examples in each of validation and holdout, precision of at least 95%, recall of at least 50%, and false-positive rate of at most 1% overall, on validation, and on holdout. Every decisive sample must also have been re-analyzed by the current app build and detector-behavior configuration. Known-unsafe, stub, suspended, telemetry-dependent, cross-match, and meta detectors have explicit promotion blocks.

Promotion activates exactly one detector in scored **review** mode. Every other detector remains shadow-only, and `auto_enforce` is forced off. A review profile is honored only when its exact detector/profile pair has a stored gate approval; hand-written and imported profiles fail closed to shadow mode. A new label or deleted opportunity that makes the active detector fail the gate rolls it back to shadow immediately.

## Storage and provenance

Every successful analysis records the app version, build commit, active profile, config fingerprint, telemetry-quality grade, frame/event counts, and timing. Keep that provenance with any claimed calibration result.

Use **Archive raw** to create and checksum a full-fidelity restorable ZIP while retaining the active database copy. Use **Archive + remove active raw** only after the archive is backed up somewhere safe; the UI requires the exact match ID. Normalized frames and review work remain available, but mapper-level reprocessing and original-tick inspection require **Restore raw**. Full-fidelity archives retain player identity and should remain private.
