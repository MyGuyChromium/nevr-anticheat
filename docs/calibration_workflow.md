# Replay calibration workflow

No detector is production-validated until it has enough independently reviewed, real Echo VR examples. Synthetic fixtures prove code behavior; they do not prove a threshold is accurate.

## Build the library

1. Keep separate source folders for expected-clean play, suspicious play, and confirmed cheats. Do not infer a label from an anticheat score.
2. Use the desktop folder queue to ingest the recordings. Replays may contain multiple sessions; label each stored match, not merely the file name.
3. Open the match and choose **Known clean**, **Suspected**, or **Confirmed cheat**. Add a note describing who reviewed it and which clip or behavior supports the label. The app records its version and effective-config fingerprint with the label.
4. Inspect each detector observation in Spark and in the Physics inspector. Mark it **Correct**, **False positive**, or **Unsure**. A whole-match label never automatically becomes a detector label.
5. Download a redacted Diagnostic ZIP when an event needs comparison or a bug report. Keep full-fidelity raw archives private; they retain player identity data so they can restore the source exactly.

## Interpret the evidence

- The Physics inspector shows source values, extractor output, and an independent finite-difference audit. `Audit Δ` should normally be zero. A difference means the extractor path needs investigation; it does not mean the player cheated.
- A possible head contact suppresses hand/wrist conclusions. Game-authored velocity can include legal boosting, grabs, block slaps, and stacking. Echo telemetry has no feet, guardian origin, or arena-object contact identifier, so room-scale steps and legal leans cannot always be separated from a replay alone.
- Schema warnings mean detector results from that field are untrustworthy. Fix or understand the source before using those observations in calibration.

## Promotion rule

Run `nevr-ac calibration-report` against the same database or a verified backup. Promote only one candidate detector at a time, only after a meaningful number of reviewed events across multiple players and matches, and only when known-clean examples show an acceptable false-positive rate. One confirmed example proves possibility, not precision. Keep unsafe, stubbed, suspended, and telemetry-blocked detectors disabled as listed in `production_readiness.md`.

## Storage

Use **Archive raw** to create and checksum a full-fidelity restorable ZIP while retaining the active database copy. Use **Archive + remove active raw** only after the archive path is backed up somewhere safe; the UI requires the exact match ID. Normalized frames and all review work remain available, but mapper-level reprocessing and original-tick inspection require **Restore raw**. Deleted SQLite pages are reusable by SQLite and may not immediately reduce the file's size on disk.
