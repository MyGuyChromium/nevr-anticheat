# Replay calibration and promotion workflow

No detector is production-validated until independently reviewed real Echo VR examples show how often it fires, misses, and stays quiet. Synthetic fixtures prove code behavior; they do not validate a threshold. NEVR therefore permits only a staged move from shadow mode to scored human review. Automatic enforcement remains disabled.

## Build ground truth

1. Keep separate source folders for expected-clean play, suspicious play, and confirmed cheats. Never infer truth from the anticheat score.
2. Import recordings through the desktop queue. Replays may contain multiple sessions, so review each stored match rather than trusting the file name.
3. Use **Known clean**, **Suspected**, or **Confirmed cheat** to index the match. Add a note naming the reviewer and supporting clip. A whole-match label organizes the library; it does not automatically label a detector.
4. Review emitted observations as **Correct**, **False positive**, or **Unsure**. Turn on **Blind review** and decide before revealing the detector whenever possible. Direct event labels measure precision, but cannot reveal a missed detection.
5. Use **Ground-truth window** to mark a specific player, detector, behavior, and frame range as confirmed cheating behavior, legitimate behavior, or uncertain. These opportunities supply the missing denominator: a positive window with no overlapping event is a false negative; a legitimate window with no event is a true negative. For scored-review eligibility, record a blinded primary reviewer, a distinct second reviewer agreeing with the window's truth, and a specific independent artifact (`controlled_reproduction`, `synchronized_video`, or `authoritative_telemetry`). An allegation, player-level admission, or NEVR's own measured speed is not sufficient independent window evidence. Existing labels remain visible but are not automatically verified.
6. Inspect every decisive sample in Spark and the Physics inspector. Possible head contact suppresses wrist conclusions. Game-authored velocity can contain legal boosts, grabs, block slaps, and stacking. Echo telemetry has no feet, guardian origin, or arena-object contact identifier, so room-scale steps and legal leans cannot always be separated from replay telemetry alone.
7. Re-upload stored replays after code or configuration changes. Desktop intake always re-analyzes them and preserves raw ticks and human labels. Use **Before / after** to investigate removed correct signals, new false positives, and intentional fixes.
8. Export the portable evidence library regularly. Version 2 includes match labels, direct event reviews, blind-review state, ground-truth windows, notes, bookmarks, and saved filters; it excludes raw telemetry.

## Dataset isolation

Isolation policy version 1 allocates new disconnected player groups with a deterministic 60/20/20 training/validation/reserved-holdout target; actual proportions depend on connected group sizes. Assignments and historical player membership persist in the database. A new recording sharing an existing player inherits that group's split. When new matches or revised rosters connect different splits, every historically connected match is permanently quarantined from split metrics, experiments, and promotion. Deleting a replay cannot erase its exposure history.

An upgrade reserves all existing matches and evidence as training exposure. Portable evidence imports also remain training-only (or quarantine a previously reserved held-out match); importing labels does not transfer an independently sealed evaluation history. Legacy labels, raw data, and review notes are preserved. Collect fresh, disconnected cohorts rather than deleting metadata to manufacture a new holdout.

- Use **training** for threshold exploration.
- Use **validation** to compare the chosen threshold with unseen examples.
- Both experiment-matrix and single-match preview routes reject **reserved holdout** and quarantined matches, including `scope=match`.

Reserved holdout is **not a sealed/blinded prospective test**. Ordinary replay views and validation metrics can reveal detector findings. At first exposure, the app freezes the exact app version, clean VCS revision, and detector-behavior configuration for the cohort. A different candidate, stale analysis, unknown/development build, or dirty VCS state permanently quarantines that cohort; re-analysis does not erase previous exposure. Unknown build provenance is not treated as clean merely because a revision was supplied as a linker flag. Build and choose a candidate before gathering the prospective cohort.

This prevents silent reuse after changing a candidate, but cannot establish what a human has previously seen or authenticate reviewer independence. The dashboard and calibration packet explicitly emit `sealed_holdout=false`, `production_validated=false`, and `release_eligible=false`. Before a public release claim, arrange a genuinely prospective external evaluation: freeze the build/configuration and opportunity definition; independently collect unseen participants/sessions; establish ground truth without detector output; evaluate the fixed candidate once; retain every result, including misses and disagreements. If tuning resumes, use a new prospective test cohort.

## Read the metrics

The dashboard reports a true confusion matrix for each detector:

- true positive: event overlaps a positive opportunity;
- false positive: event overlaps a legitimate opportunity;
- false negative: no event overlaps a positive opportunity;
- true negative: no event overlaps a legitimate opportunity.

Precision, recall, false-positive rate, false positives per 100 legitimate opportunities, and 95% Wilson intervals are shown overall and for training, validation, reserved holdout, ping, capture-rate, and telemetry-quality strata. Uncertain labels remain visible but are excluded from the confusion matrix. New windows with no stored samples for the selected player are rejected. Imported/legacy windows with no player samples remain preserved but count as unobservable/uncertain, block promotion, and never become quiet true negatives or missed false negatives. Sample presence alone does not establish telemetry sufficiency. One confirmed example proves that a behavior is possible; it does not prove detector precision. Wilson intervals here describe labeled opportunities and assume independent trials: correlated throws within players/matches invalidate a population-level interpretation. A future external evaluation needs cluster-aware uncertainty, representative opportunity sampling, and explicit performance by capture/ping/legal-contact strata.

## Tune and promote

1. Open **Investigation & reliability studio → Experiment matrix**.
2. Sweep one numeric parameter over the training split. Candidate events are evaluated in memory and are not persisted.
3. Run the chosen value on validation. Do not tune again from holdout results.
4. Save the value as a shadow candidate, restart NEVR, and re-analyze the library so the normal detector path produces durable evidence under that exact profile.
5. Review disagreements and quality strata. Add evidence instead of weakening the gate to make a candidate pass.
6. Promote only when the desktop gate marks the detector eligible.
7. Export the **calibration packet** before promotion and after the monitored review period. Verify its payload SHA-256 and retain it with the evidence-library export; this freezes the effective thresholds, physics constants, data splits, build revision, confidence intervals, and drift summary used for the decision.

The built-in **human-review gate** uses conservative proposed policy limits, not empirically validated Echo thresholds:

- Overall: at least 160 positive and 800 legitimate opportunities, 10 players, and 20 matches.
- Validation and reserved holdout separately: at least 80 positive and 400 legitimate opportunities, five players, and five matches each.
- Overall and each evaluation split: point precision at least 95%, recall at least 90%, and false-positive rate at most 1%; Wilson 95% lower bounds of at least 95% precision and 80% recall, and a false-positive upper bound at most 1%.
- Every decisive sample: matching current app/build/behavior provenance, good or excellent ungated telemetry, no isolation conflict, and the structured independent two-reviewer window attestation described above. Direct event labels alone cannot pass this evidence gate. Count floors alone are not enough to satisfy the confidence bounds.

Known-unsafe, stub, suspended, telemetry-dependent, cross-match, and meta detectors have explicit promotion blocks. `MOV_006` (walking versus legal leaning without feet/guardian context) and `BIO_001` (sample-rate-limited/aliased wrist speed) are also blocked regardless of favorable numerical metrics until a separate observability-validation design exists.

Promotion activates exactly one detector in scored **human-review** mode; it is not a release approval, cheating verdict, or permission for automatic punishment. Every other detector remains shadow-only, and `auto_enforce` is forced off. A review profile is honored only when its exact detector/profile pair has a stored gate approval; hand-written and imported profiles fail closed to shadow mode. A new label or deleted opportunity that makes the active detector fail the gate rolls it back to shadow immediately.

## Storage and provenance

Every successful analysis records the app version, build commit, active profile, config fingerprint, telemetry-quality grade, frame/event counts, and timing. Keep that provenance with any claimed calibration result.

Use **Archive raw** to create and checksum a full-fidelity restorable ZIP while retaining the active database copy. Use **Archive + remove active raw** only after the archive is backed up somewhere safe; the UI requires the exact match ID. Normalized frames and review work remain available, but mapper-level reprocessing and original-tick inspection require **Restore raw**. Full-fidelity archives retain player identity and should remain private.
