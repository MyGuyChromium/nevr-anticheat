# Investigation, reliability, and calibration suite (desktop v0.8)

This release implements the 30-item investigation/QoL pass as one evidence-only workflow. It does not promote any detector out of shadow mode and does not add automatic punishment.

1. **Telemetry quality gate** — grades normalized source data before detection, reduces confidence when quality falls, and hard-gates unusable timing.
2. **Legal-action context engine** — one shared classifier covers game locomotion, boosts, coherent leans, playspace steps, possible wall/block slaps or pushes, possible head contact, and tracking loss. It can only reduce confidence.
3. **Physics uncertainty envelopes** — throw review shows speed tolerance derived from source quality and ping; the 18.9 m/s engine cap itself is unchanged.
4. **Throw reconstruction view** — every summarized release is shown with its frame, player, speed, envelope, boundary state, and Spark action.
5. **Detector agreement system** — temporally overlapping signals for one player are grouped into incidents and show the number of distinct detectors. Agreement is review priority, not proof.
6. **Automatic threshold recommendations** — direct-label sample size and precision drive conservative collect/tighten/hold recommendations.
7. **True confusion dashboard** — direct event reviews plus ground-truth behavior windows measure TP, FP, FN, TN, precision, recall, false-positive rate, and 95% Wilson intervals.
8. **Leakage-resistant replay library** — deterministic 60/20/20 training, validation, and holdout partitions keep every connected group of matches sharing a player in one split.
9. **Adversarial synthetic replay generator** — six deterministic generated telemetry streams probe clean glide, playspace stepping, slap/push ambiguity, tracking loss, reversed timestamps, and an over-cap disc release.
10. **Detector drift monitoring** — recent quality, event rate, and pipeline runtime are compared with the preceding recorded-analysis window.
11. **Unified match timeline** — sampled player/body/game/disc facts share a match-relative clock.
12. **Synchronized physics charts** — pose, game-authored, and disc speed render together.
13. **Side-by-side player comparison** — match counts, signals, throws, fastest releases, and history can be compared without changing verdicts.
14. **Incident grouping** — overlapping detector events are a single review unit with their full event IDs and frame range.
15. **Replay clip playlist** — detector and throw anchors are chronological and open exact frames in Spark Replay Viewer.
16. **Review keyboard shortcuts** — J/down and K/up move through the playlist; Enter opens the selected frame.
17. **Bookmarks and investigator notes** — match/frame bookmarks and notes are durable in SQLite.
18. **Saved filter presets** — the current History search, detector, label, sort, and flagged-only state can be named and restored.
19. **Review progress tracking** — each investigation reports reviewed, remaining, and percent complete.
20. **One-click case report** — a standalone HTML report captures quality, config fingerprint, assessment, and grouped incidents.
21. **Portable evidence library** — versioned JSON export/import carries human match labels, direct event reviews, blind-review state, ground-truth windows, notes/bookmarks, and saved filters; raw telemetry is excluded.
22. **Detector/config provenance** — successful analyses record app/build/config/profile, quality, frames, event count, and wall/pipeline time.
23. **Named configuration profiles** — current detector settings can be saved and selected for the next restart; imported application is forced shadow-only with zero enforcement weight.
24. **Experiment matrix** — 1–12 numeric candidates run in memory over one match, training, or validation and return confusion metrics without persistence; holdout is never swept.
25. **Label disagreement finder** — known-clean matches that still contain observations are surfaced for review.
26. **First-run setup wizard** — checks evidence-folder writes, Spark discovery, shadow safety, stored matches, and calibration counts.
27. **Backup and restore wizard** — verified backups are listed; restore verifies the source, creates a safety backup, stages a verified copy, and preserves the replaced database.
28. **Update rollback** — Windows installs snapshot previous program files, and `Rollback-NEVR.cmd` restores the newest snapshot without touching evidence.
29. **Queue manager** — upload, watch-folder, and crash-recovery analyses share visible status, result count, errors, and duration.
30. **Performance diagnostics** — per-analysis wall/pipeline timings and queue averages support regression and drift review.

## Interpretation limits

Echo replay telemetry does not expose a guardian/feet pose or identify which arena object, player, head, or hand produced a collision. Legal context is therefore conservative and never treated as proof. A dedicated playspace-abuse detector is not suppressed merely because the motion resembles a physical step; repeated/sustained behavior still remains reviewable. Dataset assignment does not create ground truth—human labels are still required. The promotion gate only enables one scored human-review detector and never enables automatic punishment.
