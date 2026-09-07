# Private evidence review and connected-group evaluation

This local workflow strengthens evidence integrity and preserves review order. It does **not** authenticate reviewers, prove that they worked independently, or make a prospective holdout sealed. The local owner controls the application and can see findings elsewhere. `sealed_holdout`, `production_validated`, `release_eligible` and automatic enforcement remain false.

## Collect and review a specific opportunity

1. Freeze and identify the candidate; re-analyze the match using that app and configuration. Every new analysis records the running executable's SHA-256, not just its linked Git revision/version. Creating a bound review requires that stored hash to exactly match the current binary; older/missing-hash runs and different dirty builds require re-analysis. If executable hashing fails, the successful replay analysis remains available, the failure is logged, and an empty hash blocks bound review rather than fabricating identity. Keep originals unchanged. Select the player and exact frame window using the blind workspace's match picker; it requests no detector findings, review labels or scores.
2. With permission to retain the recording locally, choose the actual independent evidence file and confirm consent. The app accepts local file bytes only, never an arbitrary server path or a URL. Prefer short synchronized clips or controlled-trial artifacts, not detector output as ground truth.
3. Create a review session with the detector opportunity, behavior, player/frame bounds, evidence method and legal-context category. The app freezes SHA-256 of the uploaded bytes, the selected stored player-window content, and the candidate (actual executable hash, app/build identity and behavior configuration). Missing player samples or oversized windows are rejected. Sample presence does not prove detector observability.
4. The first reviewer records `positive`, `negative` or `uncertain` with a note. Each ballot must include the explicit server-required `pre_reveal_attestation=true`: the reviewer attests to inspecting the attachment before the detector finding or other ballot. This attestation is stored immutably; it is not machine proof of independence. A different reviewer records their independent ballot before any detector reveal in this workflow. Reviewer IDs are trimmed and case-normalized for uniqueness; placeholders are rejected. Names and votes cannot be replaced. The pre-reveal API exposes only the count, not either reviewer's vote or name.
5. Explicitly reveal after exactly two ballots. Agreement produces a ground-truth window; disagreement or an uncertain vote produces `uncertain`, never an automatic positive/negative. The detector observations are then available for comparison. A reported admission, player reputation, free-text reference or simply typing two reviewer names cannot satisfy the new promotion evidence gate.
6. Retain disagreements. A correction requires a new session; never rewrite a prior ballot. If an old annotation overlaps, export/retain its notes and deliberately remove that annotation before completing the new reveal. Sessions, attached evidence and ballots are retained when a replay or its calibration annotation is removed.

Reveal is idempotent and does not duplicate annotations. It fails if the candidate, stored player window or attachment integrity changed. Existing sessions retain their history but cease to supply current promotion evidence after those changes. Re-analysis with unchanged deterministic window/candidate preserves the binding. A deleted calibration annotation is not silently restored by revealing again.

Ordinary legacy review labels remain readable and editable as before, but their free-text two-reviewer attestations alone no longer qualify for promotion. Portable library imports cannot recreate trusted local bindings. Database backups preserve actual attachments and immutable ballots; keep backups private because they include player identities and the attached media. There is no automatic media upload or public sharing.

## Resource and attachment safety

- One file per consented upload; 1 byte to 32 MiB. Maximum 128 unique files / 256 MiB payload per library. Identical content is deduplicated by hash.
- At most two concurrent evidence transfers; one review window spans at most 9001 frame indices and at most 32 MiB of normalized player telemetry. A library holds at most 10,000 sessions; the workspace lists the most recent 500.
- Filenames are display metadata, never filesystem paths. Downloads always use `application/octet-stream`, attachment disposition and `nosniff`; uploaded HTML/SVG is never executed inline by the app. The app does not establish that arbitrary attached files are safe to open in another application.
- Actual bytes are rehashed when verified. Batch calibration verifies each shared artifact once per dashboard, bounded by total library capacity. There is no remote evidence fetching.
- Capacity errors preserve existing evidence; use a backed-up separate review library for additional collection. No automatic deletion or irreversible overwrite clears a capacity limit.

## Connected-group evaluation

Multiple throws from the same people are not independent trials. The app groups matches connected by **any** current or historical player identity, including links retained after deleting a replay, replacing its roster or importing older labels. These current connected-group keys are separate from immutable split-assignment IDs.

Alongside existing pooled opportunity metrics and Wilson intervals, each metric reports:

- Number of connected groups, and groups contributing positive/legitimate opportunities.
- Largest group's share of all opportunities.
- The observed minimum/maximum recall and false-positive rate across groups with the relevant denominator, plus equal-group-weighted averages.

These ranges are **descriptive observed heterogeneity, not confidence intervals**. Ten identical favorable groups do not prove perfect population performance. Unknown or omitted contexts, selection bias, correlated hardware/recorders and unseen legal mechanics remain evaluation limitations. A formal population claim still requires a pre-registered representative prospective evaluation and an appropriate independently reviewed statistical design.

Additional conservative **human-review policy** blocks promotion unless:

- Overall there are at least 10 connected groups; validation and reserved holdout each have at least five. Positive and legitimate opportunities each occur in at least three groups in each evaluated summary.
- No group supplies more than 25% of overall opportunities or 40% within either evaluation split.
- Each required ping and capture-rate category contains at least 20 legitimate opportunities from at least three groups. Required categories are low/medium/high ping and low/standard/high capture cadence, using the existing dashboard bands.
- Required legal-context controls meet the same 20-negative/three-group floor: throwing uses normal, stacking, block pushes, slaps and headbutts; movement uses normal, leaning, stacking and block pushes; other detector families use normal and transitions. Context is an explicit human attestation, not automatically inferred ground truth. Controls apply to the proposed deployment envelope; insufficient coverage means stay in shadow, not relax the gate without a separately specified validation design.

All earlier count, diversity, current-provenance, quality, independent-window, isolation and Wilson policy checks still apply. `MOV_006` and `BIO_001` remain blocked by their observability limits. Synthetic tests of these checks establish software behavior only; none of these policy values is presented as empirical detector accuracy.
