# Echo VR connectome agent

## Current milestone

`cmd/fly-agent` is an offline action-trace prototype. It streams the repository's
existing `.echoreplay` or native `.tape` parser, selects one player, rotates the
scene into that player's body frame, stimulates a frozen recurrent rate network,
and writes bounded abstract action intents.

It does **not** currently:

- simulate the MaleCNS dataset shipped by Janelia;
- train or measure an Echo VR policy;
- change a replay's next frame (replay inference is open-loop);
- expose a live `/session` source, matchmaking client, VR-controller pose sink,
  reset/rejoin loop, or anti-cheat exception;
- enter private or public matches.

The built-in `nevr-synthetic-reflex` topology is intentionally small and marked
`synthetic=true` in every output record. It verifies the complete telemetry →
sensory currents → recurrent dynamics → motor intent path without presenting a
hand-authored reflex as an uploaded fly brain.

## Run the offline trace

The attacking end is required because the available Echo telemetry does not
authoritatively identify which goal the selected player's team is attacking.
Once a display name resolves, its stable player ID and team remain bound for
the whole input; a silent replacement or team switch fails the run.

```powershell
go run ./cmd/fly-agent `
  --replay tests/fixtures/synthetic_session.echoreplay `
  --player BlueOne `
  --attack-goal positive-z `
  --max-ticks 120 `
  --output tmp/fly-agent/blue-one-actions.jsonl
```

Create `tmp/fly-agent` first when writing there. Output files are created with
owner-only permissions where the operating system supports them, and an
existing file is never replaced. Action traces can retain stable player IDs;
keep them private. `tmp/`, `fly-agent-data/`, and `*.jsonl` are ignored by Git.

Omit `--output` to write JSON Lines to standard output. Pass a bounded topology
with `--topology FILE`; the loader rejects unknown fields, duplicate or missing
node references, non-finite values, missing attribution or a non-synthetic
source-manifest SHA-256, more than 250,000 nodes, more than 30 million edges,
and documents over 256 MiB. It rejects duplicate JSON keys. The command also
requires the declared Echo sensor and motor populations plus actionable
input-to-motor reachability. JSON topology is for fixtures and extracted
corridors, not the eventual full graph. Until the importer exists, the loader
syntax-checks a non-synthetic topology's *declared* manifest digest but cannot
independently verify the referenced manifest.

Each action uses body-relative axes `[left, up, forward]`, plus yaw, grip,
release, boost, and brake intent. There is intentionally no action sink. An
inactive phase, stun, invalid input, or unavailable controller layer must result
in neutral output. Missing bound source provenance, raw phase, body orientation,
reported self velocity, team, possession certainty, or a target also resets
recurrent state and fails closed.

## Model boundary

The rate update is:

```text
x[t+1] = (1-a)x[t] + a * clip01(Wnorm*x[t] + B*sensory[t] + bias)
```

where `a = 1-exp(-dt/tau)`. Directed graph weights and signs are frozen,
incoming absolute weight is normalized per destination node, and semantic
sensory currents are clamped to `[0,1]`. This is a deterministic engineering
model, not a biophysical reconstruction. Each record binds the observation,
canonical topology, dynamics, rate-engine and decoder-policy versions, reset
reason, and resulting state with separate SHA-256 digests. The source file is
stream-hashed without recording its path; source kind/time basis/epoch, raw and
applied time deltas, and the explicit attacking goal are retained as well. A
gap clears recurrent state and integrates the reacquired observation for one
nominal 1/15-second tick rather than treating it as a long-held input.
The input is hashed again before a successful finalization, and the run fails
if it changed while being parsed.

Inputs include egocentric directions and distance bands for the current target,
nearest teammate and nearest opponent; relative closing speed; possession,
stun, shield, missingness, and grab/throw/boost/brake opportunities. The target
is the disc while the player does not possess it and the explicitly selected
goal while they do. Nearest-player ordering has a stable ID tie-break.

## MaleCNS data plan

The intended source is **MaleCNS v1.0**, the adult male *Drosophila
melanogaster* brain and ventral nerve cord connectome published by FlyEM at
HHMI Janelia with the University of Cambridge, MRC Laboratory of Molecular
Biology, Google Research, and collaborators. It contains about 166,700 neurons,
roughly 125 million synaptic contacts, and about 25.6 million released
neuron-pair connections after the annotated-neuron policy. The dataset is CC BY
4.0. Cite Berg et al., *Cell* (2026), DOI
[`10.1016/j.cell.2026.08.015`](https://doi.org/10.1016/j.cell.2026.08.015),
and retain the [MaleCNS attribution and download
information](https://male-cns.janelia.org/download/).

Downloaded Feather tables, compiled graphs, replays, checkpoints, training
rows, and action traces must remain outside Git under `fly-agent-data/` or
another ignored data/output directory. The importer milestone should:

1. verify exact source object size and digest before processing;
2. define eligible neurons explicitly from annotations rather than treating all
   EM segments as cells;
3. preserve `body_pre → body_post` direction and join `consensus_nt` separately;
4. record neurotransmitter signs as modelling assumptions;
5. extract a bounded directed visual/proprioceptive-to-descending-neuron
   corridor first, failing instead of silently pruning when limits are exceeded;
6. write a preprocessing manifest with source URLs, hashes, filters, counts,
   normalization, generated-artifact hash, and CC BY attribution;
7. compare the real wiring against shuffled-edge and ordinary-network baselines
   before attributing any performance to the connectome.

Synaptic contacts are not the same as graph edges, and neither supplies neuron
dynamics, sensory tuning, plasticity, a body, or an Echo VR policy. Those parts
remain explicit adaptations.

## Path toward interactive play

The staged deployment boundary is deliberate:

1. **Implemented:** deterministic replay-to-action traces and regression tests.
2. Compile and validate a MaleCNS-derived corridor, then benchmark full-graph
   memory and step latency offline.
3. Add a closed-loop zero-gravity test arena so an action changes the next
   observation; train only sensory and readout adapters initially.
4. Add a sanctioned private-lobby source and controller adapter with a neutral
   watchdog, stale-telemetry timeout, process supervision, checkpoints, session
   duration limits, and an immediate operator stop.
5. Evaluate against held-out private sessions and baseline agents. Synthetic or
   replay success is not evidence of public-match safety or playing ability.
6. Consider public matchmaking only with the community server operator's rules
   and explicit approval, clear bot identification, human-equivalent observation
   limits, population/rate limits, monitoring, and a supported bot slot. Do not
   bypass this repository's anti-cheat or run an unattended bot that degrades
   other players' matches.

`AuthorizeExecution` currently permits replay mode only and fails closed for
both live modes. Public-match input injection is therefore not hidden behind an
undocumented flag; it requires a separate reviewed change after the private
milestones exist.
