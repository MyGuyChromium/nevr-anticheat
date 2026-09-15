# MaleCNS v1.0 corridor compiler

This optional offline tool verifies three pinned files from the official
[MaleCNS v1.0 download](https://male-cns.janelia.org/download/) and compiles a
bounded visual/proprioceptive-to-descending-neuron graph for the existing Go
fly-agent topology loader. It does not download data, modify the source files,
train a policy, drive a live client, or represent an uploaded fly mind.

The input registry is
[`official_sources_v1.0.json`](official_sources_v1.0.json). It pins the exact
release filenames, byte counts, row counts, GCS generations, MD5 metadata, and
SHA-256 digests. The selected weights file is the 502,169,298-byte
`significant-only` release, not the larger full graph.

## Setup and use

Keep both the environment and all data under the repository's ignored
`fly-agent-data/` directory. Python 3.10 or newer is required. From the
repository root in PowerShell:

```powershell
py -m venv fly-agent-data\malecns\.venv
& fly-agent-data\malecns\.venv\Scripts\python.exe -m pip install `
  -r tools\malecns_compiler\requirements.txt

& fly-agent-data\malecns\.venv\Scripts\python.exe -m tools.malecns_compiler `
  --data-dir fly-agent-data\malecns\v1.0\raw `
  --output-dir fly-agent-data\malecns\v1.0\corridor
```

`--data-dir` must contain these exact manually downloaded filenames:

- `body-annotations-male-cns-v1.0-minconf-0.5.feather`
- `body-neurotransmitters-male-cns-v1.0.feather`
- `connectome-weights-male-cns-v1.0-minconf-0.5-significant-only.feather`

The compiler hashes every source before importing PyArrow or parsing Feather.
It refuses to replace any output. A failed build removes only paths reserved by
that invocation. To deliberately change the bounded profile, use
`--min-weight`, `--visual-max-hops`, `--proprioceptive-max-hops`,
`--max-candidate-edges`, `--max-nodes`, or `--max-edges`. A limit violation is
an error; the compiler never silently samples or prunes.

The measured default build against the pinned release retains 16,550 neurons
and 312,673 directed edges in a roughly 17 MB topology. The extraction makes
three streaming passes over the 25,568,639-row weights file and holds compact
CSR adjacency arrays in memory; allow a few minutes and approximately 1 GB of
working memory on a typical development machine. The full connectome is
intentionally not emitted as JSON because the Go loader's bounded topology
contract is designed for research subgraphs.

## Deterministic extraction

The compiler performs the following fixed transformation:

1. It admits annotated bodies with a non-empty `superclass` and
   `status != "Glia"`. This retains null-status photoreceptors rather than
   incorrectly applying a global `status == "Traced"` filter.
2. Visual roots are `ol_intrinsic` L1/L2 cells with both optical-lobe hex
   coordinates. Proprioceptive roots are `vnc_sensory` cells whose `class` is
   `mechanosensory_proprioceptive` and whose `rootSide` is L or R.
3. Endpoints are descending neurons of types DNa01, DNa02, DNp01, DNp09,
   DNg11, and MDN.
4. It reads every significant-only edge as `body_pre -> body_post`, keeps raw
   synapse counts of at least five, and signs the count from the presynaptic
   body's `consensus_nt`: acetylcholine is positive; GABA, glutamate, and
   histamine are negative. Monoamines, unclear labels, missing labels, and
   unknown labels receive sign zero and their outgoing edges are excluded.
   This is a conservative modeling assumption, not measured receptor biology.
5. It retains the union of nodes on a directed root-to-endpoint path of at most
   three hops for either modality. It then emits every measured, qualifying
   edge induced by those retained nodes. Duplicate pairs, if present, are
   summed; nodes and edges are serialized in stable numeric order.
6. Retained roots and descending endpoints are assigned by ascending body ID,
   round-robin, to the Echo sensor and motor names required by the runtime.
   These sensory and action populations are engineered, uncalibrated adapter
   partitions. They are not claims that a named fly neuron means “boost,”
   “grip,” or any other Echo action.

The Go rate network performs incoming-weight normalization at load time, so the
compiler intentionally preserves signed raw counts rather than normalizing
them a second time.

## Outputs and verification contract

The output directory contains exactly:

- `male-cns-v1.0-corridor.topology.json`, schema
  `nevr.fly.topology/v1`;
- `male-cns-v1.0-corridor.source-manifest.json`, an exact byte-for-byte copy of
  the pinned registry, schema `nevr.fly.malecns-sources/v1`;
- `male-cns-v1.0-corridor.build-manifest.json`, schema
  `nevr.fly.malecns-build/v1`.

The topology's `dataset.source_manifest_sha256` hashes the exact emitted source
manifest. In the build manifest, `outputs.topology` and
`outputs.source_manifest` each contain `path`, `bytes`, `sha256`, and `schema`.
Paths are basenames, and the document contains no timestamp or machine-local
path, so identical inputs and arguments produce byte-identical outputs.

## Tests

With the pinned optional dependency installed:

```powershell
& fly-agent-data\malecns\.venv\Scripts\python.exe -m unittest `
  tools.malecns_compiler.test_compiler -v
```

The tests build tiny Feather fixtures at runtime; no source or generated graph
is committed. They cover deterministic bytes, source and output hashes,
presynaptic sign and edge direction, required population reachability, schema
rejection, input tampering, hard resource limits, and no-overwrite behavior.

## License and citation

MaleCNS v1.0 data is distributed under
[CC BY 4.0](https://creativecommons.org/licenses/by/4.0/). Preserve the emitted
manifests when sharing a derived topology. Cite Berg et al., *Cell* (2026),
[doi:10.1016/j.cell.2026.08.015](https://doi.org/10.1016/j.cell.2026.08.015),
and retain the credits recorded in the official source registry.
