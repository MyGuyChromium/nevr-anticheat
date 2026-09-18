# Source-aware mechanics evidence

This update improves review evidence, not detector calibration or enforcement.
The 18.9 m/s project reference is unchanged. Playspace checks remain paused.
Production punishment remains unavailable under the backend review-only policy.

## What changed

| Area | Recorded evidence | What it does not establish |
| --- | --- | --- |
| Mags / disc acquisition | Up to eight same-source pre-acquisition samples, original first-held geometry, a second consecutive same-holder sample, explicit interrupted/unconfirmed outcomes | Exact acquisition tick, server acceptance, legal grab volume, or a range violation |
| Default rule profile | Literal configuration values pinned to source revision `8616ebf03d6f0f2c1071366d0aad6ee3403e9eb3`, retained with analysis coverage and available in app health | Active match overrides, executable/platform applicability, exact engine equations, or a speed ceiling |
| Release direction | Original release and confirmation frames, hand/body-motion context, source consistency, legal contact/assistance limitations | WristAngleOffset setting, macro identity, or an independently validated cheating verdict |
| Behavior taxonomy | Separate release-direction, shot-targeting, free-flight and receiver-associated catch-path explanations | A single identifiable mechanism called “autopocket,” or receiver responsibility for observed motion |
| Stun review | Explicit observed false-to-true victim interval and all qualifying opposing counter-change candidates with raw endpoints | Proven attacker, authoritative collision, permissible stun radius, or a detection event |
| Controlled comparisons | Versioned private provenance, measured alignment anchors, development/holdout overlap guards | Authenticity of operator assertions, an automatically sealed holdout, or ground-truth labels |

The stun review is independent of the legacy `STATE_007` heuristic, which remains
disabled by default. Its diagnostic records live under that check's **Explain
checks → Mechanics assessments** panel, including players without scored
findings. The short association window is an engineering collection budget, not
the default game's counter-punch timing rule or a latency allowance. Candidate
counts are not independent incidents or accuracy denominators.

Missing/null stun fields stay unknown rather than becoming observed false/zero.
Sparse native-capture counters cannot pretend to be fresh observations on every
displayed frame. A missing known baseline can prevent a stun review entirely;
an empty review log does not establish fair play or complete stun coverage.

Non-play disc transfers can be retained as unscored attachment observations,
separately labelled from active play. A one-sample possession flicker is retained
as unconfirmed rather than silently becoming a confirmed catch.

## Reviewer use

1. Re-analyze a permitted recording to collect the new evidence. Older reports
   without provenance are not silently re-labelled with current configuration.
2. Select the player and open **Explain checks**. Choose the relevant check.
3. Read the observation kind, source, interval and limitations before the raw
   metrics. Second-sample possession confirmation is not cheating confirmation.
4. Use the existing **Inspect samples** and **Open in Spark** actions. Stun raw
   samples identify victim and candidate roles explicitly; no nearest attacker
   is automatically selected.
5. Save independent observations, exact windows and permitted evidence. Use
   blinded review before revealing detector output for an independent label.

No new storage migration is required: additions are version-compatible JSON in
existing analysis coverage/evidence. Reprocessing replaces derived diagnostics
through the existing atomic workflow. Back up before updating; an older binary
may omit new diagnostic fields on rewrites and is not a database rollback.

## Repeatable verification

From the repository root:

```powershell
go build ./...
go vet ./...
go test -count=1 -timeout 40m ./...
go test -race -count=1 -timeout 40m ./...
node --test scripts/*.test.cjs
node scripts/ui-render-check.cjs --shots dist/visual-game-evidence
```

Use the pinned analysis commands in `.github/workflows/ci.yml` for static,
vulnerability and security scans. The existing
`scripts/verify-test-release.ps1` checks an exact clean-commit Windows package,
isolated startup/import/reopen, backup restoration and sustained workload. Do
not substitute a development-server pass for a packaged application pass.

See [controlled comparison recordings](controlled_comparison_recordings.md) for
the private capture and evaluation protocol and
[default configuration reference](default_game_config_reference.md) for source
limitations. Synthetic regression tests verify implementation contracts only.
Independent accuracy remains blocked until permitted, independently adjudicated,
representative synchronized recordings are available. Do not publish recordings,
private manifests, player-identifying findings, databases, or calibration reports.
