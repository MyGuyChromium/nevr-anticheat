# Echo VR connectome agent

## Current milestone

The development-only fly-agent research path now has five offline or
observation-only pieces:

1. a deterministic compiler for a bounded MaleCNS v1.0
   visual/proprioceptive-to-descending-neuron corridor;
2. a Go verifier that binds the compiler's build manifest, copied source
   registry, exact topology bytes, graph semantics, attribution, and runtime
   population contract;
3. a deterministic closed-loop zero-gravity arena that trains only bounded
   sensory/readout adapter values while leaving connectome nodes, edges,
   weights, and biases frozen;
4. replay inference and local performance benchmarking; and
5. an observation-only private-session runner that reads a local `/session`
   endpoint and writes abstract intents to a dry-run trace.

None of this is evidence that the model has learned Echo VR, reproduces a
biological fly, or is ready for a public match. There is no controller-input
sink, matchmaking client, installed/observed compatible Echo runtime, operator
grant, or sanctioned bot slot in the current environment. The repository's
execution policy continues to reject live actuation.

## Build and verify a MaleCNS corridor

The source is **MaleCNS v1.0**, the adult male *Drosophila melanogaster* brain
and ventral nerve cord connectome published by FlyEM at HHMI Janelia with the
University of Cambridge, MRC Laboratory of Molecular Biology, Google Research,
and collaborators. It contains about 166,700 eligible annotated neurons and
roughly 125 million synaptic contacts; contacts are not the same thing as the
released neuron-pair edges used here. The dataset is CC BY 4.0. Cite Berg et
al., *Cell* (2026), DOI
[`10.1016/j.cell.2026.08.015`](https://doi.org/10.1016/j.cell.2026.08.015),
and retain the [MaleCNS attribution and download
information](https://male-cns.janelia.org/download/).

Raw Feather files and every generated artifact must stay in ignored storage
such as `fly-agent-data/`; do not commit them. Download these exact official
filenames into `fly-agent-data\malecns\v1.0\raw`:

- `body-annotations-male-cns-v1.0-minconf-0.5.feather`
- `body-neurotransmitters-male-cns-v1.0.feather`
- `connectome-weights-male-cns-v1.0-minconf-0.5-significant-only.feather`

Then run from the repository root in PowerShell:

```powershell
py -m venv fly-agent-data\malecns\.venv
& fly-agent-data\malecns\.venv\Scripts\python.exe -m pip install `
  -r tools\malecns_compiler\requirements.txt

& fly-agent-data\malecns\.venv\Scripts\python.exe -m tools.malecns_compiler `
  --data-dir fly-agent-data\malecns\v1.0\raw `
  --output-dir fly-agent-data\malecns\v1.0\corridor

& fly-agent-data\malecns\.venv\Scripts\python.exe -m unittest `
  tools.malecns_compiler.test_compiler -v
go test ./internal/flyagent -run MaleCNSArtifact -count=1
```

The pinned registry records exact filenames, sizes, row counts, GCS
generations, MD5 metadata, SHA-256 hashes, and required columns. The compiler
rehashes every input before importing PyArrow, preserves `body_pre ->
body_post`, applies explicitly recorded presynaptic neurotransmitter-sign
assumptions, refuses silent sampling or pruning, and never replaces an existing
output file.

The Go APIs `flyagent.VerifyMaleCNSArtifact` and
`flyagent.LoadVerifiedMaleCNSArtifact` strictly verify the build/source
manifests and topology together. They reject ambiguous JSON, unsafe or linked
references, byte-count/hash changes, wrong schemas or attribution, population
partition errors, graph-count/sign-profile disagreement, and a topology that
fails the agent contract. A SHA-256 proves byte identity relative to the pinned
registry; it is not a publisher signature or biological validation.

An exact build performed from the pinned official files on 2026-09-15 produced:

| Evidence | Value |
|---|---:|
| retained neurons | 16,550 |
| directed edges | 312,673 |
| topology bytes | 17,341,543 |
| topology-file SHA-256 | `baad3dcd0737bb17ec89bef85da1ece56ddbad1cbe0b75e2b418aca81582fcdc` |
| source-manifest SHA-256 | `3653f582dc66fe276d879be52bb65d0159aec9de0e212204d2e54bf9c68594e4` |
| build-manifest SHA-256 | `ff39c8a5acdce7356144d63995d2a236b187e13c25b31941da5157d9199ee95d` |

Two independent compilations from those inputs were byte-identical. This is
artifact-reproduction evidence only.

## Closed-loop arena and bounded adapter training

`cmd/fly-agent-arena` supplies a deterministic synthetic zero-gravity arena in
which an action changes the next observation. Its checkpoints include all
environment and RNG state. Training uses disjoint deterministically derived
training and held-out seeds, bounded coordinate search, and exactly 12 sensory
gain/readout gain/readout threshold parameters. The connectome is never
trained.

The official corridor is large enough that the command's generic training
defaults exceed its hard work cap. Use a small explicit reproduction first:

```powershell
$flyDir = 'fly-agent-data\malecns\v1.0\corridor'
$topology = Join-Path $flyDir 'male-cns-v1.0-corridor.topology.json'
$training = Join-Path $flyDir 'male-cns-v1.0-corridor.training-smoke.json'
$build = Join-Path $flyDir 'male-cns-v1.0-corridor.build-manifest.json'

go run ./cmd/fly-agent-arena `
  --mode train `
  --topology $topology `
  --training-episodes 1 `
  --evaluation-episodes 1 `
  --passes 1 `
  --max-steps 25 `
  --output $training

go run ./cmd/fly-agent-arena `
  --mode compare-trained `
  --topology $topology `
  --adapter-report $training `
  --baseline weight `
  --max-steps 25 `
  --output (Join-Path $flyDir 'male-cns-v1.0-corridor.weight-control.json')

go run ./cmd/fly-agent-arena `
  --mode compare-trained `
  --topology $topology `
  --adapter-report $training `
  --baseline rewired `
  --max-steps 25 `
  --output (Join-Path $flyDir 'male-cns-v1.0-corridor.rewired-control.json')

go run ./cmd/fly-agent-readiness `
  --build-manifest $build `
  --adapter-report $training `
  --controls=true `
  --output (Join-Path $flyDir 'male-cns-v1.0-corridor.readiness.json')
```

The weight control permutes the exact weight multiset while holding directed
connectivity fixed. The rewired control performs a degree-preserving endpoint
permutation, retains the exact weights, biases, node IDs, per-node in/out
degrees, I/O populations, and protected shortest actionable motor paths, and
fails if it cannot produce a genuinely changed compatible graph. Reports bind
the arena, topology, model, seed plan, adapter values/digests, attempt
trajectory, planned work, and paired episode results. Loading an adapter report
for replay or private dry-run re-executes the deterministic training run before
issuing a topology/model-bound capability.
Trained-control reports additionally identify the verified-trained-adapter
experiment mode and bind the SHA-256 of the exact adapter-report bytes; their
arena configuration and evaluation seeds must exactly match that report.

The readiness command verifies the actual three-file MaleCNS artifact, exactly
reproduces an optional adapter report, and, with `--controls=true`, reruns both
paired synthetic controls. Its output records `controls_executed` explicitly;
an intentionally skipped run contains an empty `controls` array and a matching
limitation. It always reports `public_match_ready=false` while the external
gates below remain unsatisfied; it does not turn a local check into a deployment
authorization.

The one-episode/one-pass, 25-step reproduction performed during this milestone
did **not** improve the adapter: its training objective stayed at `0.046261`
and its held-out objective was `-0.027121`. In the readiness rerun, the
candidate tied both the shuffled-weight and rewired controls to displayed
precision; the rewired graph did change connectivity while preserving the
reported parity constraints and 24 protected path edges. One seed and 25
synthetic steps cannot establish either equivalence or an advantage; these
numbers demonstrate reproducible plumbing, not learned skill.

## Benchmark the compiled graph

```powershell
$flyDir = 'fly-agent-data\malecns\v1.0\corridor'
$topology = Join-Path $flyDir 'male-cns-v1.0-corridor.topology.json'

go run ./cmd/fly-agent-benchmark `
  --topology $topology `
  --warmup 16 `
  --steps 160 `
  --output (Join-Path $flyDir 'male-cns-v1.0-corridor.benchmark.json')
```

One Windows/amd64 Go 1.27 measurement with `GOMAXPROCS=14` compiled the
16,550-node/312,673-edge topology in 212.973 ms with a 67,565,672-byte heap
allocation delta. Across 160 measured steps at a `1/15`-second model interval
in ten batches of 16, the mean was 0.635 ms and the nearest-rank batch-mean
p50/p95/p99 were 0.531/1.056/1.056 ms.
These are local wall-clock measurements affected by the Go runtime, scheduler,
host load, and batching. The heap delta is a process snapshot and the reported
core-storage value is only a lower bound. This is neither real-match latency
nor biological/policy validation.

## Run an offline replay trace

The attacking end is required because the available Echo telemetry does not
authoritatively identify which goal the selected player's team is attacking.
Once a display name resolves, its stable player ID and team remain bound for
the whole input; a silent replacement or team switch fails the run.

```powershell
New-Item -ItemType Directory -Force tmp\fly-agent | Out-Null

go run ./cmd/fly-agent `
  --replay tests\fixtures\synthetic_session.echoreplay `
  --player BlueOne `
  --attack-goal positive-z `
  --max-ticks 120 `
  --output tmp\fly-agent\blue-one-actions.jsonl
```

For the official topology and a verified training report, add `--topology
$topology --adapter-report $training`. The command copies the bounded regular
replay into a private temporary snapshot while hashing the exact copied bytes,
parses that snapshot, and places its SHA-256 in every record. This avoids
parsing a different byte sequence after an ordinary source-path change. The
temporary copy is removed after the run. It is not a sandbox or adversarial
integrity boundary: another hostile process running as the same local OS user
may be able to modify the temporary file, output, or process memory.

Every trace record carries the observation/state/topology/model digests and
the exact policy-adapter digest. When an adapter report is supplied, it also
carries the SHA-256 of the exact report bytes that were strictly decoded and
reproduced. Outputs can retain stable player IDs; keep them private. Existing
outputs are never replaced, and partial outputs are removed on failure.

The rate update remains a deterministic engineering model:

```text
x[t+1] = (1-a)x[t] + a * clip01(Wnorm*x[t] + B*sensory[t] + bias)
```

where `a = 1-exp(-dt/tau)`. Directed connectome weights and signs are frozen,
incoming absolute weight is normalized per destination node, and sensory
currents are clamped. Synapses alone do not supply neuron dynamics, sensory
tuning, a body, or an Echo VR policy; all four remain explicit adaptations.

## Observe an authorized private session without actuation

`cmd/fly-agent-private` polls exactly an HTTP loopback-IP `/session` URL. It
does not use proxy settings or redirects, caps every response at 1 MiB, hashes
the exact response body, requires explicit top-level private/session evidence,
binds the session and uniquely resolved local player, deduplicates identical
samples, and waits for 2--10 distinct private confirmations. It accepts only a
dry-run JSONL sink: there is no controller, keyboard, process-injection, or
matchmaking capability.

With an Echo-compatible runtime already serving the expected private session:

```powershell
New-Item -ItemType Directory -Force tmp\fly-agent | Out-Null

go run ./cmd/fly-agent-private `
  --session-url http://127.0.0.1:6721/session `
  --expected-session-id '<exact-private-session-id>' `
  --player '<local-player-id-or-exact-name>' `
  --attack-goal positive-z `
  --session-limit 5m `
  --stale-after 750ms `
  --private-confirmations 3 `
  --topology $topology `
  --adapter-report $training `
  --output tmp\fly-agent\private-dry-run.jsonl
```

The runner stops on Ctrl+C, its mandatory limit (at most 30 minutes),
post-match, changed session/player evidence, stale telemetry (at most five
seconds), or any safety/output failure. Every exit attempts a final bounded
neutralization event before closing the dry-run sink. That event is only an
audit record; it sends no command to the game.

No compatible Echo executable/runtime or responding local `/session` endpoint
was observed on the development host during this milestone, so only synthetic
and HTTP-fixture behavior has been verified.

## Public-match authorization boundary

The repository contains a strict verifier for an expiring, Ed25519-signed
operator authorization grant. The signature binds the trusted key ID and the
grant, and verification binds all of these to the intended deployment:

- community server, bot account, disclosed display name, and sanctioned bot
  slot;
- named observation and controller APIs;
- activation/expiry, maximum session duration, and maximum action rate; and
- explicit permission for public matchmaking, human-equivalent observations,
  operator monitoring, and an immediate operator stop.

Trusted public keys are supplied out of band. Strict parsing rejects unknown,
duplicate, case-aliased, trailing, oversized, tampered, inactive, or mismatched
documents. Its `GrantForUse` method rechecks expiry before releasing the grant;
future consumers must call it at the point of admission. This verifier is only
a future admission gate. It does not provide a public runner,
controller API, matchmaking implementation, or operator permission.

Public work is therefore blocked until all of the following exist outside this
repository and are then integrated in a separately reviewed change:

1. an installed and observed Echo-compatible runtime with a documented,
   supported controller/bot API;
2. explicit community-server operator approval represented by a valid signed
   grant;
3. a sanctioned, disclosed bot account and bot slot with human-equivalent
   observations, rate/population limits, monitoring, and operator stop; and
4. held-out authorized-private evaluation showing acceptable behavior against
   baseline agents.

Never bypass NEVR or another anti-cheat, inject through an undocumented input
path, conceal the bot, or run an unattended bot that degrades public matches.
Synthetic-arena, replay, regression, and benchmark passes do not satisfy these
external gates.
