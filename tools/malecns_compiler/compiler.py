"""Compile a bounded, deterministic MaleCNS v1.0 corridor topology.

This module is deliberately an offline build tool.  The generated topology is
consumed by the Go runtime, while the large source Feather files and generated
JSON documents stay under the repository's ignored ``fly-agent-data`` tree.
"""

from __future__ import annotations

import argparse
from array import array
from collections import Counter, deque
from dataclasses import dataclass
import gc
import hashlib
import json
from pathlib import Path
import sys
from typing import Any, Iterable, Iterator, Mapping, Sequence


SOURCE_SCHEMA = "nevr.fly.malecns-sources/v1"
BUILD_SCHEMA = "nevr.fly.malecns-build/v1"
TOPOLOGY_SCHEMA = "nevr.fly.topology/v1"
PROFILE_NAME = "visual-proprioceptive-descending/v1"
SOURCE_REGISTRY = Path(__file__).with_name("official_sources_v1.0.json")

SOURCE_ROLES = ("annotations", "neurotransmitters", "weights")
TOPOLOGY_BASENAME = "male-cns-v1.0-corridor.topology.json"
SOURCE_MANIFEST_BASENAME = "male-cns-v1.0-corridor.source-manifest.json"
BUILD_MANIFEST_BASENAME = "male-cns-v1.0-corridor.build-manifest.json"

RUNTIME_MAX_NODES = 250_000
RUNTIME_MAX_EDGES = 30_000_000
RUNTIME_MAX_JSON_BYTES = 256 << 20

VISUAL_CHANNELS = (
    "target_left",
    "target_right",
    "target_up",
    "target_down",
    "target_ahead",
    "target_behind",
    "target_near",
    "target_far",
    "evade_left",
    "evade_right",
)
PROPRIOCEPTIVE_CHANNELS = (
    "grab_opportunity",
    "throw_opportunity",
    "boost_opportunity",
    "brake_opportunity",
)
OUTPUT_CHANNELS = (
    "move_left",
    "move_right",
    "move_up",
    "move_down",
    "move_forward",
    "move_backward",
    "yaw_left",
    "yaw_right",
    "grip_left",
    "grip_right",
    "release",
    "boost",
    "brake",
)

# These named descending-neuron types provide a small, reproducible endpoint
# set.  Their assignment to Echo actions is explicitly engineered, not a claim
# about the biological role of an individual neuron.
DEFAULT_DESCENDING_TYPES = (
    "DNa01",
    "DNa02",
    "DNp01",
    "DNp09",
    "DNg11",
    "MDN",
)

# Conservative polarity assumptions applied to the presynaptic body.  The
# monoamines and uncertain/missing labels are intentionally excluded rather
# than assigned an unsupported fast-synapse sign.
NT_SIGN = {
    "acetylcholine": 1,
    "gaba": -1,
    "glutamate": -1,
    "histamine": -1,
    "dopamine": 0,
    "octopamine": 0,
    "serotonin": 0,
    "unclear": 0,
    "missing": 0,
}


class CompilationError(RuntimeError):
    """A safe, user-actionable compiler failure."""


@dataclass(frozen=True)
class CompilerConfig:
    """Deterministic extraction profile and hard resource ceilings."""

    min_weight: int = 5
    visual_max_hops: int = 3
    proprioceptive_max_hops: int = 3
    max_candidate_edges: int = 12_000_000
    max_nodes: int = 50_000
    max_edges: int = 2_000_000
    descending_types: tuple[str, ...] = DEFAULT_DESCENDING_TYPES

    def validate(self) -> None:
        if self.min_weight < 1:
            raise CompilationError("min_weight must be at least 1")
        if not 1 <= self.visual_max_hops <= 32:
            raise CompilationError("visual_max_hops must be in 1..32")
        if not 1 <= self.proprioceptive_max_hops <= 32:
            raise CompilationError("proprioceptive_max_hops must be in 1..32")
        if self.max_candidate_edges < 1:
            raise CompilationError("max_candidate_edges must be positive")
        if not 1 <= self.max_nodes <= RUNTIME_MAX_NODES:
            raise CompilationError(
                f"max_nodes must be in 1..{RUNTIME_MAX_NODES}"
            )
        if not 1 <= self.max_edges <= RUNTIME_MAX_EDGES:
            raise CompilationError(
                f"max_edges must be in 1..{RUNTIME_MAX_EDGES}"
            )
        if len(set(self.descending_types)) != len(self.descending_types):
            raise CompilationError("descending_types contains a duplicate")
        if not self.descending_types or any(not value for value in self.descending_types):
            raise CompilationError("descending_types must contain non-empty names")


@dataclass(frozen=True)
class CompilationResult:
    topology_path: Path
    source_manifest_path: Path
    build_manifest_path: Path
    topology_sha256: str
    topology_bytes: int
    node_count: int
    edge_count: int


@dataclass(frozen=True)
class _Annotations:
    body_ids: tuple[int, ...]
    visual_roots: tuple[int, ...]
    proprioceptive_roots: tuple[int, ...]
    descending_targets: tuple[int, ...]


def _reject_duplicate_keys(pairs: Sequence[tuple[str, Any]]) -> dict[str, Any]:
    result: dict[str, Any] = {}
    for key, value in pairs:
        if key in result:
            raise CompilationError(f"source registry repeats JSON key {key!r}")
        result[key] = value
    return result


def _load_registry(path: Path) -> tuple[bytes, dict[str, Any]]:
    try:
        raw = path.read_bytes()
    except OSError as exc:
        raise CompilationError(f"read source registry {path}: {exc}") from exc
    try:
        registry = json.loads(raw, object_pairs_hook=_reject_duplicate_keys)
    except (json.JSONDecodeError, UnicodeDecodeError) as exc:
        raise CompilationError(f"decode source registry {path}: {exc}") from exc
    if not isinstance(registry, dict) or registry.get("schema") != SOURCE_SCHEMA:
        raise CompilationError(
            f"source registry schema must be {SOURCE_SCHEMA!r}"
        )
    dataset = registry.get("dataset")
    files = registry.get("files")
    if not isinstance(dataset, dict) or not isinstance(files, dict):
        raise CompilationError("source registry requires dataset and files objects")
    for key in ("name", "version", "release_page", "license", "citation", "credit"):
        if not isinstance(dataset.get(key), str) or not dataset[key].strip():
            raise CompilationError(f"source registry dataset.{key} is required")
    for role in SOURCE_ROLES:
        entry = files.get(role)
        if not isinstance(entry, dict):
            raise CompilationError(f"source registry files.{role} is required")
        filename = entry.get("filename")
        if (
            not isinstance(filename, str)
            or not filename
            or Path(filename).name != filename
        ):
            raise CompilationError(f"source registry files.{role}.filename is unsafe")
        if not isinstance(entry.get("bytes"), int) or entry["bytes"] < 1:
            raise CompilationError(f"source registry files.{role}.bytes is invalid")
        if not isinstance(entry.get("rows"), int) or entry["rows"] < 1:
            raise CompilationError(f"source registry files.{role}.rows is invalid")
        digest = entry.get("sha256")
        if (
            not isinstance(digest, str)
            or len(digest) != 64
            or any(ch not in "0123456789abcdef" for ch in digest)
        ):
            raise CompilationError(f"source registry files.{role}.sha256 is invalid")
        if not isinstance(entry.get("url"), str) or not entry["url"].startswith(
            "https://storage.googleapis.com/flyem-male-cns/v1.0/"
        ):
            raise CompilationError(f"source registry files.{role}.url is not official")
        columns = entry.get("required_columns")
        if not isinstance(columns, dict) or not columns:
            raise CompilationError(
                f"source registry files.{role}.required_columns is invalid"
            )
        if any(not isinstance(name, str) or not isinstance(kind, str) for name, kind in columns.items()):
            raise CompilationError(
                f"source registry files.{role}.required_columns is invalid"
            )
    return raw, registry


def _sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(8 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _verify_inputs(data_dir: Path, registry: Mapping[str, Any]) -> dict[str, Path]:
    paths: dict[str, Path] = {}
    for role in SOURCE_ROLES:
        entry = registry["files"][role]
        path = data_dir / entry["filename"]
        try:
            stat = path.stat()
        except OSError as exc:
            raise CompilationError(f"required {role} file {path} is unavailable: {exc}") from exc
        if not path.is_file():
            raise CompilationError(f"required {role} path is not a file: {path}")
        if stat.st_size != entry["bytes"]:
            raise CompilationError(
                f"{role} size mismatch for {path.name}: got {stat.st_size}, "
                f"want {entry['bytes']}"
            )
        actual = _sha256_file(path)
        if actual != entry["sha256"]:
            raise CompilationError(
                f"{role} SHA-256 mismatch for {path.name}: got {actual}, "
                f"want {entry['sha256']}"
            )
        paths[role] = path
    return paths


def _require_pyarrow() -> Any:
    try:
        import pyarrow as pa  # type: ignore[import-not-found]
        import pyarrow.ipc as ipc  # type: ignore[import-not-found]
    except ImportError as exc:
        raise CompilationError(
            "the optional compiler dependency is missing; install "
            "tools/malecns_compiler/requirements.txt"
        ) from exc
    if pa.__version__ != "25.0.1":
        raise CompilationError(
            f"unsupported pyarrow version {pa.__version__}; install the pinned "
            "tools/malecns_compiler/requirements.txt"
        )
    return pa, ipc


def _iter_feather_batches(
    pa: Any,
    ipc: Any,
    path: Path,
    entry: Mapping[str, Any],
    selected_columns: Sequence[str],
) -> Iterator[Any]:
    try:
        with pa.memory_map(str(path), "r") as source:
            reader = ipc.open_file(source)
            schema = reader.schema
            for name, expected_type in entry["required_columns"].items():
                index = schema.get_field_index(name)
                if index < 0:
                    raise CompilationError(
                        f"{path.name} is missing required column {name!r}"
                    )
                actual_type = str(schema.field(index).type)
                if actual_type != expected_type:
                    raise CompilationError(
                        f"{path.name} column {name!r} has type {actual_type!r}; "
                        f"want {expected_type!r}"
                    )
            selected_indexes: list[int] = []
            for name in selected_columns:
                index = schema.get_field_index(name)
                if index < 0:
                    raise CompilationError(
                        f"{path.name} is missing selected column {name!r}"
                    )
                selected_indexes.append(index)
            row_count = 0
            for batch_index in range(reader.num_record_batches):
                batch = reader.get_batch(batch_index).select(selected_indexes)
                row_count += batch.num_rows
                yield batch
            if row_count != entry["rows"]:
                raise CompilationError(
                    f"{path.name} row count mismatch: got {row_count}, "
                    f"want {entry['rows']}"
                )
    except CompilationError:
        raise
    except Exception as exc:
        raise CompilationError(f"read Feather file {path}: {exc}") from exc


def _load_annotations(
    pa: Any,
    ipc: Any,
    path: Path,
    entry: Mapping[str, Any],
    config: CompilerConfig,
) -> _Annotations:
    columns = (
        "bodyId",
        "superclass",
        "status",
        "type",
        "assignedOlHex1",
        "assignedOlHex2",
        "class",
        "rootSide",
    )
    seen: set[int] = set()
    eligible: list[int] = []
    visual: list[int] = []
    proprioceptive: list[int] = []
    descending: list[int] = []
    target_types = set(config.descending_types)

    for batch in _iter_feather_batches(pa, ipc, path, entry, columns):
        values = batch.to_pydict()
        for row in zip(*(values[name] for name in columns), strict=True):
            body, superclass, status, cell_type, hex1, hex2, cell_class, side = row
            if body is None:
                raise CompilationError(f"{path.name} contains a null bodyId")
            body = int(body)
            if body in seen:
                raise CompilationError(f"{path.name} repeats bodyId {body}")
            seen.add(body)
            is_eligible = (
                isinstance(superclass, str)
                and bool(superclass.strip())
                and status != "Glia"
            )
            if not is_eligible:
                continue
            eligible.append(body)
            if (
                superclass == "ol_intrinsic"
                and cell_type in ("L1", "L2")
                and hex1 is not None
                and hex2 is not None
            ):
                visual.append(body)
            if (
                superclass == "vnc_sensory"
                and cell_class == "mechanosensory_proprioceptive"
                and side in ("L", "R")
            ):
                proprioceptive.append(body)
            if superclass == "descending_neuron" and cell_type in target_types:
                descending.append(body)

    eligible.sort()
    visual.sort()
    proprioceptive.sort()
    descending.sort()
    if len(visual) < len(VISUAL_CHANNELS):
        raise CompilationError(
            f"visual root filter produced {len(visual)} bodies; "
            f"need at least {len(VISUAL_CHANNELS)}"
        )
    if len(proprioceptive) < len(PROPRIOCEPTIVE_CHANNELS):
        raise CompilationError(
            f"proprioceptive root filter produced {len(proprioceptive)} bodies; "
            f"need at least {len(PROPRIOCEPTIVE_CHANNELS)}"
        )
    if len(descending) < len(OUTPUT_CHANNELS):
        raise CompilationError(
            f"descending target filter produced {len(descending)} bodies; "
            f"need at least {len(OUTPUT_CHANNELS)}"
        )
    return _Annotations(
        body_ids=tuple(eligible),
        visual_roots=tuple(visual),
        proprioceptive_roots=tuple(proprioceptive),
        descending_targets=tuple(descending),
    )


def _load_signs(
    pa: Any,
    ipc: Any,
    path: Path,
    entry: Mapping[str, Any],
    body_to_index: Mapping[int, int],
) -> tuple[array, dict[str, int]]:
    signs = array("b", [0]) * len(body_to_index)
    seen: set[int] = set()
    label_counts: Counter[str] = Counter()
    for batch in _iter_feather_batches(
        pa, ipc, path, entry, ("body", "consensus_nt")
    ):
        values = batch.to_pydict()
        for body, label in zip(values["body"], values["consensus_nt"], strict=True):
            if body is None:
                raise CompilationError(f"{path.name} contains a null body")
            index = body_to_index.get(int(body))
            if index is None:
                continue
            if index in seen:
                raise CompilationError(f"{path.name} repeats body {body}")
            seen.add(index)
            normalized = (
                label.strip().lower() if isinstance(label, str) and label.strip() else "missing"
            )
            label_counts[normalized] += 1
            signs[index] = NT_SIGN.get(normalized, 0)
    missing = len(body_to_index) - len(seen)
    if missing:
        label_counts["missing"] += missing
    return signs, dict(sorted(label_counts.items()))


def _candidate_edges(
    pa: Any,
    ipc: Any,
    path: Path,
    entry: Mapping[str, Any],
    body_to_index: Mapping[int, int],
    signs: array,
    min_weight: int,
) -> Iterator[tuple[int, int, int]]:
    columns = ("body_pre", "body_post", "weight")
    for batch in _iter_feather_batches(pa, ipc, path, entry, columns):
        values = batch.to_pydict()
        for pre, post, weight in zip(
            values["body_pre"], values["body_post"], values["weight"], strict=True
        ):
            if pre is None or post is None or weight is None:
                raise CompilationError(f"{path.name} contains a null edge field")
            weight = int(weight)
            if weight < min_weight:
                continue
            pre_index = body_to_index.get(int(pre))
            if pre_index is None:
                continue
            post_index = body_to_index.get(int(post))
            if post_index is None:
                continue
            sign = signs[pre_index]
            if sign == 0:
                continue
            yield pre_index, post_index, sign * weight


def _prefix_offsets(degrees: array) -> array:
    offsets = array("Q", [0])
    running = 0
    for degree in degrees:
        running += degree
        offsets.append(running)
    return offsets


def _build_adjacency(
    pa: Any,
    ipc: Any,
    weights_path: Path,
    weights_entry: Mapping[str, Any],
    body_to_index: Mapping[int, int],
    signs: array,
    config: CompilerConfig,
) -> tuple[array, array, array, array, int]:
    node_count = len(body_to_index)
    forward_degrees = array("Q", [0]) * node_count
    reverse_degrees = array("Q", [0]) * node_count
    candidate_count = 0
    for pre, post, _ in _candidate_edges(
        pa,
        ipc,
        weights_path,
        weights_entry,
        body_to_index,
        signs,
        config.min_weight,
    ):
        candidate_count += 1
        if candidate_count > config.max_candidate_edges:
            raise CompilationError(
                "candidate edge count exceeds max_candidate_edges="
                f"{config.max_candidate_edges}; raise the explicit limit or use a "
                "larger min_weight"
            )
        forward_degrees[pre] += 1
        reverse_degrees[post] += 1

    forward_offsets = _prefix_offsets(forward_degrees)
    reverse_offsets = _prefix_offsets(reverse_degrees)
    forward_neighbors = array("I", [0]) * candidate_count
    reverse_neighbors = array("I", [0]) * candidate_count
    forward_cursor = array("Q", forward_offsets[:-1])
    reverse_cursor = array("Q", reverse_offsets[:-1])

    filled = 0
    for pre, post, _ in _candidate_edges(
        pa,
        ipc,
        weights_path,
        weights_entry,
        body_to_index,
        signs,
        config.min_weight,
    ):
        forward_neighbors[forward_cursor[pre]] = post
        forward_cursor[pre] += 1
        reverse_neighbors[reverse_cursor[post]] = pre
        reverse_cursor[post] += 1
        filled += 1
    if filled != candidate_count:
        raise CompilationError(
            f"weights changed while compiling: first pass found {candidate_count} "
            f"candidate edges, second pass found {filled}"
        )
    for index in range(node_count):
        if (
            forward_cursor[index] != forward_offsets[index + 1]
            or reverse_cursor[index] != reverse_offsets[index + 1]
        ):
            raise CompilationError("internal adjacency fill mismatch")
    del forward_degrees, reverse_degrees, forward_cursor, reverse_cursor
    gc.collect()
    return (
        forward_offsets,
        forward_neighbors,
        reverse_offsets,
        reverse_neighbors,
        candidate_count,
    )


def _bounded_distances(
    offsets: array, neighbors: array, roots: Iterable[int], max_hops: int
) -> array:
    distances = array("i", [-1]) * (len(offsets) - 1)
    queue: deque[int] = deque()
    for root in sorted(set(roots)):
        if distances[root] == -1:
            distances[root] = 0
            queue.append(root)
    while queue:
        node = queue.popleft()
        distance = distances[node]
        if distance >= max_hops:
            continue
        for position in range(offsets[node], offsets[node + 1]):
            neighbor = neighbors[position]
            if distances[neighbor] == -1:
                distances[neighbor] = distance + 1
                queue.append(neighbor)
    return distances


def _select_corridor(
    annotations: _Annotations,
    body_to_index: Mapping[int, int],
    forward_offsets: array,
    forward_neighbors: array,
    reverse_offsets: array,
    reverse_neighbors: array,
    config: CompilerConfig,
) -> tuple[array, list[int], list[int], list[int]]:
    visual_indexes = [body_to_index[body] for body in annotations.visual_roots]
    proprioceptive_indexes = [
        body_to_index[body] for body in annotations.proprioceptive_roots
    ]
    target_indexes = [body_to_index[body] for body in annotations.descending_targets]
    visual_distance = _bounded_distances(
        forward_offsets, forward_neighbors, visual_indexes, config.visual_max_hops
    )
    proprioceptive_distance = _bounded_distances(
        forward_offsets,
        forward_neighbors,
        proprioceptive_indexes,
        config.proprioceptive_max_hops,
    )
    reverse_distance = _bounded_distances(
        reverse_offsets,
        reverse_neighbors,
        target_indexes,
        max(config.visual_max_hops, config.proprioceptive_max_hops),
    )

    retained = array("b", [0]) * len(annotations.body_ids)
    retained_count = 0
    for index in range(len(retained)):
        to_target = reverse_distance[index]
        keep = to_target >= 0 and (
            (
                visual_distance[index] >= 0
                and visual_distance[index] + to_target <= config.visual_max_hops
            )
            or (
                proprioceptive_distance[index] >= 0
                and proprioceptive_distance[index] + to_target
                <= config.proprioceptive_max_hops
            )
        )
        if keep:
            retained[index] = 1
            retained_count += 1
            if retained_count > config.max_nodes:
                raise CompilationError(
                    f"corridor node count exceeds max_nodes={config.max_nodes}; "
                    "raise the explicit bound or tighten the profile"
                )

    retained_visual = [index for index in visual_indexes if retained[index]]
    retained_proprioceptive = [
        index for index in proprioceptive_indexes if retained[index]
    ]
    retained_targets = [
        index
        for index in target_indexes
        if retained[index]
        and (
            visual_distance[index] >= 0 or proprioceptive_distance[index] >= 0
        )
    ]
    if len(retained_visual) < len(VISUAL_CHANNELS):
        raise CompilationError(
            f"only {len(retained_visual)} visual roots reach the selected descending "
            f"targets within {config.visual_max_hops} hops; need "
            f"{len(VISUAL_CHANNELS)}"
        )
    if len(retained_proprioceptive) < len(PROPRIOCEPTIVE_CHANNELS):
        raise CompilationError(
            f"only {len(retained_proprioceptive)} proprioceptive roots reach the "
            f"selected descending targets within {config.proprioceptive_max_hops} "
            f"hops; need {len(PROPRIOCEPTIVE_CHANNELS)}"
        )
    if len(retained_targets) < len(OUTPUT_CHANNELS):
        raise CompilationError(
            f"only {len(retained_targets)} selected descending targets are reachable; "
            f"need {len(OUTPUT_CHANNELS)} for the engineered readout"
        )
    return retained, retained_visual, retained_proprioceptive, retained_targets


def _collect_corridor_edges(
    pa: Any,
    ipc: Any,
    weights_path: Path,
    weights_entry: Mapping[str, Any],
    body_to_index: Mapping[int, int],
    signs: array,
    retained: array,
    config: CompilerConfig,
) -> list[tuple[int, int, int]]:
    edges: list[tuple[int, int, int]] = []
    for pre, post, signed_weight in _candidate_edges(
        pa,
        ipc,
        weights_path,
        weights_entry,
        body_to_index,
        signs,
        config.min_weight,
    ):
        if retained[pre] and retained[post]:
            edges.append((post, pre, signed_weight))
            if len(edges) > config.max_edges:
                raise CompilationError(
                    f"corridor edge count exceeds max_edges={config.max_edges}; "
                    "raise the explicit bound or tighten the profile"
                )
    edges.sort()
    if not edges:
        raise CompilationError("corridor contains no edges")

    merged: list[tuple[int, int, int]] = []
    for post, pre, signed_weight in edges:
        if merged and merged[-1][0] == post and merged[-1][1] == pre:
            old_post, old_pre, old_weight = merged[-1]
            merged[-1] = (old_post, old_pre, old_weight + signed_weight)
        else:
            merged.append((post, pre, signed_weight))
    if any(weight == 0 for _, _, weight in merged):
        raise CompilationError("merged corridor unexpectedly contains a zero edge")
    return merged


def _partition(indexes: Sequence[int], names: Sequence[str]) -> dict[str, list[int]]:
    if len(indexes) < len(names):
        raise CompilationError(
            f"cannot populate {len(names)} channels with only {len(indexes)} nodes"
        )
    populations = {name: [] for name in names}
    for position, index in enumerate(sorted(indexes)):
        populations[names[position % len(names)]].append(index)
    return populations


def _validate_contract(
    retained: array,
    edges: Sequence[tuple[int, int, int]],
    inputs: Mapping[str, Sequence[int]],
    outputs: Mapping[str, Sequence[int]],
    forward_offsets: array,
    forward_neighbors: array,
) -> None:
    node_count = sum(retained)
    if not 1 <= node_count <= RUNTIME_MAX_NODES:
        raise CompilationError(f"invalid compiled node count {node_count}")
    if len(edges) > RUNTIME_MAX_EDGES:
        raise CompilationError(f"invalid compiled edge count {len(edges)}")
    if set(inputs) != set(VISUAL_CHANNELS + PROPRIOCEPTIVE_CHANNELS):
        raise CompilationError("internal input population mismatch")
    if set(outputs) != set(OUTPUT_CHANNELS):
        raise CompilationError("internal output population mismatch")
    for kind, populations in (("input", inputs), ("output", outputs)):
        for name, members in populations.items():
            if not members:
                raise CompilationError(f"{kind} population {name!r} is empty")
            if len(set(members)) != len(members):
                raise CompilationError(f"{kind} population {name!r} repeats a node")
            if any(not retained[index] for index in members):
                raise CompilationError(
                    f"{kind} population {name!r} references an unretained node"
                )

    actionable_roots = [index for members in inputs.values() for index in members]
    reachable = array("b", [0]) * len(retained)
    queue: deque[int] = deque()
    for index in actionable_roots:
        if not reachable[index]:
            reachable[index] = 1
            queue.append(index)
    while queue:
        pre = queue.popleft()
        for position in range(forward_offsets[pre], forward_offsets[pre + 1]):
            post = forward_neighbors[position]
            if retained[post] and not reachable[post]:
                reachable[post] = 1
                queue.append(post)
    unreachable = [
        name
        for name, members in outputs.items()
        if not any(reachable[index] for index in members)
    ]
    if unreachable:
        raise CompilationError(
            "compiled outputs lack a directed path from an actionable input: "
            + ", ".join(unreachable)
        )


def _node_id(body: int) -> str:
    return f"mcns:{body}"


def _json_bytes(value: Any) -> bytes:
    return json.dumps(
        value, ensure_ascii=False, allow_nan=False, sort_keys=True, separators=(",", ":")
    ).encode("utf-8")


def _write_topology(
    path: Path,
    dataset_metadata: Mapping[str, Any],
    body_ids: Sequence[int],
    retained: array,
    edges: Sequence[tuple[int, int, int]],
    inputs: Mapping[str, Sequence[int]],
    outputs: Mapping[str, Sequence[int]],
) -> None:
    retained_indexes = [index for index, keep in enumerate(retained) if keep]
    with path.open("wb") as stream:
        stream.write(b'{"schema":')
        stream.write(_json_bytes(TOPOLOGY_SCHEMA))
        stream.write(b',"dataset":')
        stream.write(_json_bytes(dataset_metadata))
        stream.write(b',"nodes":[')
        for position, index in enumerate(retained_indexes):
            if position:
                stream.write(b",")
            stream.write(_json_bytes({"id": _node_id(body_ids[index])}))
        stream.write(b'],"edges":[')
        for position, (post, pre, signed_weight) in enumerate(edges):
            if position:
                stream.write(b",")
            stream.write(
                _json_bytes(
                    {
                        "pre": _node_id(body_ids[pre]),
                        "post": _node_id(body_ids[post]),
                        "weight": signed_weight,
                    }
                )
            )
        encoded_inputs = {
            name: [_node_id(body_ids[index]) for index in members]
            for name, members in inputs.items()
        }
        encoded_outputs = {
            name: [_node_id(body_ids[index]) for index in members]
            for name, members in outputs.items()
        }
        stream.write(b'],"inputs":')
        stream.write(_json_bytes(encoded_inputs))
        stream.write(b',"outputs":')
        stream.write(_json_bytes(encoded_outputs))
        stream.write(b"}\n")


def _write_json_reserved(path: Path, value: Any) -> None:
    document = json.dumps(
        value,
        ensure_ascii=False,
        allow_nan=False,
        sort_keys=True,
        indent=2,
    ).encode("utf-8") + b"\n"
    with path.open("wb") as stream:
        stream.write(document)


def _reserve_outputs(paths: Sequence[Path]) -> list[Path]:
    """Atomically claim each final pathname without replacing another writer."""

    reserved: list[Path] = []
    try:
        for path in paths:
            with path.open("xb"):
                pass
            reserved.append(path)
    except FileExistsError as exc:
        for path in reversed(reserved):
            try:
                path.unlink()
            except OSError:
                pass
        raise CompilationError(f"refusing to overwrite existing compiler output: {exc.filename}") from exc
    except BaseException:
        for path in reversed(reserved):
            try:
                path.unlink()
            except OSError:
                pass
        raise
    return reserved


def compile_corridor(
    data_dir: Path | str,
    output_dir: Path | str,
    config: CompilerConfig | None = None,
    *,
    source_registry_path: Path | str | None = None,
) -> CompilationResult:
    """Verify official inputs and compile one bounded directed corridor.

    ``source_registry_path`` exists for hermetic tests.  The CLI intentionally
    always uses the committed, pinned official registry.
    """

    config = config or CompilerConfig()
    config.validate()
    data_dir = Path(data_dir)
    output_dir = Path(output_dir)
    registry_path = (
        Path(source_registry_path) if source_registry_path else SOURCE_REGISTRY
    )
    registry_raw, registry = _load_registry(registry_path)

    # Authenticity/integrity checks intentionally precede importing Arrow or
    # parsing a single byte from any dataset file.
    source_paths = _verify_inputs(data_dir, registry)
    pa, ipc = _require_pyarrow()

    output_dir.mkdir(parents=True, exist_ok=True)
    topology_path = output_dir / TOPOLOGY_BASENAME
    source_manifest_path = output_dir / SOURCE_MANIFEST_BASENAME
    build_manifest_path = output_dir / BUILD_MANIFEST_BASENAME
    output_paths = (topology_path, source_manifest_path, build_manifest_path)
    existing = [str(path) for path in output_paths if path.exists()]
    if existing:
        raise CompilationError(
            "refusing to overwrite existing compiler output(s): " + ", ".join(existing)
        )

    annotations = _load_annotations(
        pa,
        ipc,
        source_paths["annotations"],
        registry["files"]["annotations"],
        config,
    )
    body_to_index = {body: index for index, body in enumerate(annotations.body_ids)}
    signs, nt_counts = _load_signs(
        pa,
        ipc,
        source_paths["neurotransmitters"],
        registry["files"]["neurotransmitters"],
        body_to_index,
    )
    (
        forward_offsets,
        forward_neighbors,
        reverse_offsets,
        reverse_neighbors,
        candidate_count,
    ) = _build_adjacency(
        pa,
        ipc,
        source_paths["weights"],
        registry["files"]["weights"],
        body_to_index,
        signs,
        config,
    )
    retained, visual_roots, proprioceptive_roots, targets = _select_corridor(
        annotations,
        body_to_index,
        forward_offsets,
        forward_neighbors,
        reverse_offsets,
        reverse_neighbors,
        config,
    )
    edges = _collect_corridor_edges(
        pa,
        ipc,
        source_paths["weights"],
        registry["files"]["weights"],
        body_to_index,
        signs,
        retained,
        config,
    )
    inputs = _partition(visual_roots, VISUAL_CHANNELS)
    proprioceptive_inputs = _partition(
        proprioceptive_roots, PROPRIOCEPTIVE_CHANNELS
    )
    inputs.update(proprioceptive_inputs)
    outputs = _partition(targets, OUTPUT_CHANNELS)
    _validate_contract(
        retained,
        edges,
        inputs,
        outputs,
        forward_offsets,
        forward_neighbors,
    )

    # Close the time-of-check/time-of-use window across the three graph passes.
    # Re-hashing is cheap compared with compilation and proves the bytes used by
    # the build still match the emitted source manifest.
    _verify_inputs(data_dir, registry)

    source_manifest_sha256 = hashlib.sha256(registry_raw).hexdigest()
    dataset = registry["dataset"]
    dataset_metadata = {
        "name": dataset["name"],
        "version": dataset["version"],
        "source": dataset["release_page"],
        "license": dataset["license"],
        "source_manifest_sha256": source_manifest_sha256,
        "synthetic": False,
        "derivation": (
            "deterministic significant-only directed corridor; raw synapse counts "
            "signed by conservative presynaptic consensus-neurotransmitter "
            "assumptions; engineered, uncalibrated sensory and descending-neuron "
            "Echo readout partitions"
        ),
    }

    created = _reserve_outputs(output_paths)
    try:
        with source_manifest_path.open("wb") as stream:
            stream.write(registry_raw)
        _write_topology(
            topology_path,
            dataset_metadata,
            annotations.body_ids,
            retained,
            edges,
            inputs,
            outputs,
        )
        topology_bytes = topology_path.stat().st_size
        if topology_bytes > RUNTIME_MAX_JSON_BYTES:
            raise CompilationError(
                f"topology is {topology_bytes} bytes, exceeding the Go runtime limit "
                f"of {RUNTIME_MAX_JSON_BYTES}; tighten the profile"
            )
        topology_sha256 = _sha256_file(topology_path)
        source_manifest_bytes = source_manifest_path.stat().st_size
        if source_manifest_bytes != len(registry_raw):
            raise CompilationError("written source manifest size changed unexpectedly")
        if _sha256_file(source_manifest_path) != source_manifest_sha256:
            raise CompilationError("written source manifest digest changed unexpectedly")

        positive_edges = sum(weight > 0 for _, _, weight in edges)
        negative_edges = len(edges) - positive_edges
        build_manifest = {
            "schema": BUILD_SCHEMA,
            "compiler": {
                "name": "nevr MaleCNS corridor compiler",
                "version": "1",
                "pyarrow": "25.0.1",
            },
            "dataset": dataset_metadata,
            "source_manifest": {
                "path": SOURCE_MANIFEST_BASENAME,
                "bytes": source_manifest_bytes,
                "sha256": source_manifest_sha256,
                "schema": SOURCE_SCHEMA,
            },
            "profile": {
                "name": PROFILE_NAME,
                "edge_direction": "body_pre -> body_post",
                "edge_weight": "signed raw synapse count (not normalized)",
                "weights_release": "significant-only",
                "min_weight": config.min_weight,
                "visual_max_hops": config.visual_max_hops,
                "proprioceptive_max_hops": config.proprioceptive_max_hops,
                "max_candidate_edges": config.max_candidate_edges,
                "max_nodes": config.max_nodes,
                "max_edges": config.max_edges,
                "eligible_filter": "non-empty superclass and status != Glia",
                "visual_root_filter": (
                    "superclass == ol_intrinsic and type in {L1,L2} and "
                    "assignedOlHex1/assignedOlHex2 are non-null"
                ),
                "proprioceptive_root_filter": (
                    "superclass == vnc_sensory and class == "
                    "mechanosensory_proprioceptive and rootSide in {L,R}"
                ),
                "descending_target_filter": {
                    "superclass": "descending_neuron",
                    "types": list(config.descending_types),
                },
                "corridor_rule": (
                    "union of nodes on a directed root-to-target path within the "
                    "per-modality hop bound"
                ),
                "population_binding": (
                    "ascending bodyId round-robin; engineered and uncalibrated"
                ),
            },
            "sign_profile": {
                "basis": "consensus_nt of body_pre",
                "mapping": dict(sorted(NT_SIGN.items())),
                "unknown_label_sign": 0,
                "zero_sign_edges": "excluded",
            },
            "counts": {
                "eligible_bodies": len(annotations.body_ids),
                "selected_visual_roots": len(annotations.visual_roots),
                "selected_proprioceptive_roots": len(
                    annotations.proprioceptive_roots
                ),
                "selected_descending_targets": len(
                    annotations.descending_targets
                ),
                "retained_visual_roots": len(visual_roots),
                "retained_proprioceptive_roots": len(proprioceptive_roots),
                "retained_descending_targets": len(targets),
                "candidate_edges": candidate_count,
                "corridor_nodes": int(sum(retained)),
                "corridor_edges": len(edges),
                "positive_edges": positive_edges,
                "negative_edges": negative_edges,
                "eligible_consensus_nt": nt_counts,
            },
            "outputs": {
                "topology": {
                    "path": TOPOLOGY_BASENAME,
                    "bytes": topology_bytes,
                    "sha256": topology_sha256,
                    "schema": TOPOLOGY_SCHEMA,
                },
                "source_manifest": {
                    "path": SOURCE_MANIFEST_BASENAME,
                    "bytes": source_manifest_bytes,
                    "sha256": source_manifest_sha256,
                    "schema": SOURCE_SCHEMA,
                },
            },
            "attribution": {
                "citation": dataset["citation"],
                "credit": dataset["credit"],
                "license": dataset["license"],
                "license_url": dataset["license_url"],
                "release_page": dataset["release_page"],
            },
        }
        _write_json_reserved(build_manifest_path, build_manifest)
    except BaseException:
        for path in reversed(created):
            try:
                path.unlink()
            except OSError:
                pass
        raise

    return CompilationResult(
        topology_path=topology_path,
        source_manifest_path=source_manifest_path,
        build_manifest_path=build_manifest_path,
        topology_sha256=topology_sha256,
        topology_bytes=topology_bytes,
        node_count=int(sum(retained)),
        edge_count=len(edges),
    )


def _positive_int(value: str) -> int:
    try:
        parsed = int(value)
    except ValueError as exc:
        raise argparse.ArgumentTypeError("must be an integer") from exc
    if parsed < 1:
        raise argparse.ArgumentTypeError("must be positive")
    return parsed


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="python -m tools.malecns_compiler",
        description=(
            "Verify the pinned official MaleCNS v1.0 Feather inputs and compile "
            "a deterministic, bounded fly-agent corridor."
        ),
    )
    parser.add_argument(
        "--data-dir",
        required=True,
        type=Path,
        help="directory containing the three fixed official Feather filenames",
    )
    parser.add_argument(
        "--output-dir",
        required=True,
        type=Path,
        help="new/empty ignored directory for topology and manifests",
    )
    parser.add_argument("--min-weight", type=_positive_int, default=5)
    parser.add_argument("--visual-max-hops", type=_positive_int, default=3)
    parser.add_argument("--proprioceptive-max-hops", type=_positive_int, default=3)
    parser.add_argument(
        "--max-candidate-edges", type=_positive_int, default=12_000_000
    )
    parser.add_argument("--max-nodes", type=_positive_int, default=50_000)
    parser.add_argument("--max-edges", type=_positive_int, default=2_000_000)
    return parser


def main(argv: Sequence[str] | None = None) -> int:
    args = _parser().parse_args(argv)
    config = CompilerConfig(
        min_weight=args.min_weight,
        visual_max_hops=args.visual_max_hops,
        proprioceptive_max_hops=args.proprioceptive_max_hops,
        max_candidate_edges=args.max_candidate_edges,
        max_nodes=args.max_nodes,
        max_edges=args.max_edges,
    )
    try:
        result = compile_corridor(args.data_dir, args.output_dir, config)
    except CompilationError as exc:
        print(f"malecns-compiler: {exc}", file=sys.stderr)
        return 1
    except OSError as exc:
        print(f"malecns-compiler: filesystem error: {exc}", file=sys.stderr)
        return 1
    print(f"topology: {result.topology_path}")
    print(f"sha256:   {result.topology_sha256}")
    print(f"bytes:    {result.topology_bytes}")
    print(f"nodes:    {result.node_count}")
    print(f"edges:    {result.edge_count}")
    print(f"manifest: {result.build_manifest_path}")
    return 0
