from __future__ import annotations

from dataclasses import replace
import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock

try:
    import pyarrow as pa
    import pyarrow.feather as feather
except ImportError:  # The compiler is an explicitly optional tool.
    pa = None
    feather = None

from tools.malecns_compiler.compiler import (
    BUILD_MANIFEST_BASENAME,
    BUILD_SCHEMA,
    CompilationError,
    CompilerConfig,
    OUTPUT_CHANNELS,
    SOURCE_MANIFEST_BASENAME,
    SOURCE_REGISTRY,
    SOURCE_SCHEMA,
    TOPOLOGY_BASENAME,
    TOPOLOGY_SCHEMA,
    compile_corridor,
)


def _digest(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


@unittest.skipIf(pa is None, "optional pyarrow compiler dependency is not installed")
class CompilerTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.data_dir = self.root / "raw"
        self.data_dir.mkdir()
        self.registry_path = self.root / "sources.json"
        self._write_fixture()

    def tearDown(self) -> None:
        self.temp.cleanup()

    def _write_fixture(self, *, weight_type: object | None = None) -> None:
        visual = list(range(1000, 1010))
        proprioceptive = list(range(2000, 2004))
        intermediates = [3000, 3001, 3002]
        targets = list(range(4000, 4013))
        bodies = visual + proprioceptive + intermediates + targets

        superclass: list[str] = []
        status: list[str] = []
        cell_type: list[str] = []
        hex1: list[float | None] = []
        hex2: list[float | None] = []
        cell_class: list[str | None] = []
        side: list[str | None] = []
        descending_types = ("DNa01", "DNa02", "DNp01", "DNp09", "DNg11", "MDN")
        for index, body in enumerate(bodies):
            status.append("Traced")
            if body in visual:
                superclass.append("ol_intrinsic")
                cell_type.append("L1" if body % 2 else "L2")
                hex1.append(float(index + 1))
                hex2.append(float(index + 2))
                cell_class.append(None)
                side.append(None)
            elif body in proprioceptive:
                superclass.append("vnc_sensory")
                cell_type.append("fixture-proprioceptor")
                hex1.append(None)
                hex2.append(None)
                cell_class.append("mechanosensory_proprioceptive")
                side.append("L" if body % 2 else "R")
            elif body in targets:
                superclass.append("descending_neuron")
                cell_type.append(descending_types[(body - 4000) % len(descending_types)])
                hex1.append(None)
                hex2.append(None)
                cell_class.append(None)
                side.append(None)
            else:
                superclass.append("cb_intrinsic")
                cell_type.append("fixture-interneuron")
                hex1.append(None)
                hex2.append(None)
                cell_class.append(None)
                side.append(None)
        annotation_table = pa.table(
            {
                "bodyId": pa.array(bodies, type=pa.int64()),
                "superclass": pa.array(superclass, type=pa.string()),
                "status": pa.array(status, type=pa.string()),
                "type": pa.array(cell_type, type=pa.string()),
                "assignedOlHex1": pa.array(hex1, type=pa.float64()),
                "assignedOlHex2": pa.array(hex2, type=pa.float64()),
                "class": pa.array(cell_class, type=pa.string()),
                "rootSide": pa.array(side, type=pa.string()),
            }
        )

        labels = []
        for body in bodies:
            if body in proprioceptive or body == 3001:
                labels.append("gaba")
            elif body == 3002:
                labels.append("unclear")
            else:
                labels.append("acetylcholine")
        nt_table = pa.table(
            {
                "body": pa.array(bodies, type=pa.int64()),
                "consensus_nt": pa.array(labels, type=pa.string()),
            }
        )

        pres: list[int] = []
        posts: list[int] = []
        weights: list[int] = []
        for body in visual:
            pres.append(body)
            posts.append(3000)
            weights.append(6)
        for body in proprioceptive:
            pres.append(body)
            posts.append(3001)
            weights.append(7)
        for target in targets:
            pres.extend((3000, 3001))
            posts.extend((target, target))
            weights.extend((8, 9))
        # An uncertain presynaptic label and a sub-threshold edge are both omitted.
        pres.extend((3002, 1000))
        posts.extend((4000, 4000))
        weights.extend((100, 4))
        weight_table = pa.table(
            {
                "body_pre": pa.array(pres, type=pa.int64()),
                "body_post": pa.array(posts, type=pa.int64()),
                "weight": pa.array(weights, type=weight_type or pa.int64()),
            }
        )

        filenames = {
            "annotations": "fixture-annotations.feather",
            "neurotransmitters": "fixture-neurotransmitters.feather",
            "weights": "fixture-significant-only-weights.feather",
        }
        tables = {
            "annotations": annotation_table,
            "neurotransmitters": nt_table,
            "weights": weight_table,
        }
        for role, table in tables.items():
            feather.write_feather(table, self.data_dir / filenames[role])

        required_columns = {
            "annotations": {
                "assignedOlHex1": "double",
                "assignedOlHex2": "double",
                "bodyId": "int64",
                "class": "string",
                "rootSide": "string",
                "status": "string",
                "superclass": "string",
                "type": "string",
            },
            "neurotransmitters": {"body": "int64", "consensus_nt": "string"},
            "weights": {
                "body_post": "int64",
                "body_pre": "int64",
                "weight": "int64",
            },
        }
        files = {}
        for role, table in tables.items():
            path = self.data_dir / filenames[role]
            files[role] = {
                "filename": filenames[role],
                "url": (
                    "https://storage.googleapis.com/flyem-male-cns/v1.0/fixture/"
                    + filenames[role]
                ),
                "bytes": path.stat().st_size,
                "rows": table.num_rows,
                "sha256": _digest(path),
                "required_columns": required_columns[role],
            }
        registry = {
            "schema": SOURCE_SCHEMA,
            "dataset": {
                "name": "MaleCNS fixture",
                "version": "v1.0-test",
                "release_page": "https://male-cns.janelia.org/download/",
                "license": "CC BY 4.0",
                "license_url": "https://creativecommons.org/licenses/by/4.0/",
                "citation": "fixture citation",
                "credit": "fixture credit",
            },
            "files": files,
        }
        self.registry_path.write_text(
            json.dumps(registry, sort_keys=True, indent=2) + "\n", encoding="utf-8"
        )

    def _compile(self, output_name: str, config: CompilerConfig | None = None):
        return compile_corridor(
            self.data_dir,
            self.root / output_name,
            config or CompilerConfig(),
            source_registry_path=self.registry_path,
        )

    def test_compiles_deterministically_with_direction_sign_and_manifests(self) -> None:
        first = self._compile("out-a")
        second = self._compile("out-b")

        self.assertEqual(
            first.topology_path.read_bytes(), second.topology_path.read_bytes()
        )
        self.assertEqual(
            first.source_manifest_path.read_bytes(),
            second.source_manifest_path.read_bytes(),
        )
        self.assertEqual(
            first.build_manifest_path.read_bytes(),
            second.build_manifest_path.read_bytes(),
        )

        topology = json.loads(first.topology_path.read_bytes())
        build = json.loads(first.build_manifest_path.read_bytes())
        self.assertEqual(topology["schema"], TOPOLOGY_SCHEMA)
        self.assertFalse(topology["dataset"]["synthetic"])
        self.assertEqual(build["schema"], BUILD_SCHEMA)
        self.assertEqual(set(topology["outputs"]), set(OUTPUT_CHANNELS))
        self.assertTrue(all(topology["outputs"].values()))
        self.assertEqual(first.node_count, 29)
        self.assertEqual(first.edge_count, 40)

        edge_by_pair = {
            (edge["pre"], edge["post"]): edge["weight"]
            for edge in topology["edges"]
        }
        self.assertEqual(edge_by_pair[("mcns:1000", "mcns:3000")], 6)
        self.assertEqual(edge_by_pair[("mcns:2000", "mcns:3001")], -7)
        self.assertEqual(edge_by_pair[("mcns:3001", "mcns:4000")], -9)
        self.assertNotIn(("mcns:3002", "mcns:4000"), edge_by_pair)
        self.assertNotIn(("mcns:1000", "mcns:4000"), edge_by_pair)
        self.assertNotIn("mcns:3002", {node["id"] for node in topology["nodes"]})

        source_bytes = first.source_manifest_path.read_bytes()
        self.assertEqual(
            topology["dataset"]["source_manifest_sha256"],
            hashlib.sha256(source_bytes).hexdigest(),
        )
        self.assertEqual(
            build["outputs"]["topology"],
            {
                "path": TOPOLOGY_BASENAME,
                "bytes": first.topology_path.stat().st_size,
                "sha256": _digest(first.topology_path),
                "schema": TOPOLOGY_SCHEMA,
            },
        )
        self.assertEqual(
            build["outputs"]["source_manifest"],
            {
                "path": SOURCE_MANIFEST_BASENAME,
                "bytes": len(source_bytes),
                "sha256": hashlib.sha256(source_bytes).hexdigest(),
                "schema": SOURCE_SCHEMA,
            },
        )

    def test_hash_mismatch_rejected_before_output_creation(self) -> None:
        weights = self.data_dir / "fixture-significant-only-weights.feather"
        with weights.open("r+b") as stream:
            stream.seek(32)
            original = stream.read(1)
            self.assertEqual(len(original), 1)
            stream.seek(32)
            stream.write(bytes((original[0] ^ 0x01,)))
        output = self.root / "tampered-output"
        with self.assertRaisesRegex(CompilationError, "SHA-256 mismatch"):
            self._compile(output.name)
        self.assertFalse(output.exists())

    def test_schema_mismatch_is_rejected(self) -> None:
        self._write_fixture(weight_type=pa.int32())
        with self.assertRaisesRegex(CompilationError, "column 'weight'.*want 'int64'"):
            self._compile("bad-schema")

    def test_resource_bounds_fail_instead_of_pruning(self) -> None:
        with self.assertRaisesRegex(CompilationError, "max_candidate_edges=1"):
            self._compile(
                "candidate-bound",
                replace(CompilerConfig(), max_candidate_edges=1),
            )
        with self.assertRaisesRegex(CompilationError, "max_nodes=5"):
            self._compile("node-bound", replace(CompilerConfig(), max_nodes=5))
        with self.assertRaisesRegex(CompilationError, "max_edges=2"):
            self._compile("edge-bound", replace(CompilerConfig(), max_edges=2))

    def test_existing_output_is_never_overwritten(self) -> None:
        result = self._compile("existing")
        before = result.topology_path.read_bytes()
        with self.assertRaisesRegex(CompilationError, "refusing to overwrite"):
            self._compile("existing")
        self.assertEqual(result.topology_path.read_bytes(), before)

    def test_reserved_outputs_are_cleaned_after_write_failure(self) -> None:
        output = self.root / "write-failure"
        with mock.patch(
            "tools.malecns_compiler.compiler._write_topology",
            side_effect=OSError("fixture write failure"),
        ):
            with self.assertRaisesRegex(OSError, "fixture write failure"):
                self._compile(output.name)
        self.assertTrue(output.is_dir())
        self.assertEqual(list(output.iterdir()), [])


class OfficialRegistryTests(unittest.TestCase):
    def test_registry_pins_released_significant_only_inputs(self) -> None:
        registry_bytes = SOURCE_REGISTRY.read_bytes()
        self.assertEqual(
            hashlib.sha256(registry_bytes).hexdigest(),
            "3653f582dc66fe276d879be52bb65d0159aec9de0e212204d2e54bf9c68594e4",
        )
        registry = json.loads(registry_bytes)
        self.assertEqual(registry["schema"], SOURCE_SCHEMA)
        expected = {
            "annotations": (
                "body-annotations-male-cns-v1.0-minconf-0.5.feather",
                14_483_314,
                "2177e246113e4cfbf1e7772ec37c6da1955ff22e8063d0b1f833101f99a9a3b2",
            ),
            "neurotransmitters": (
                "body-neurotransmitters-male-cns-v1.0.feather",
                43_282_834,
                "95c9289220663abeb3409f3ad9e5a7f8a53f8093f5139d15502cd08da8879621",
            ),
            "weights": (
                "connectome-weights-male-cns-v1.0-minconf-0.5-significant-only.feather",
                502_169_298,
                "5c536423a62a688e59e7b441f9c04d6272c9a1f017e35814cf561f8c275d9e9e",
            ),
        }
        for role, (filename, size, digest) in expected.items():
            entry = registry["files"][role]
            self.assertEqual(entry["filename"], filename)
            self.assertEqual(entry["bytes"], size)
            self.assertEqual(entry["sha256"], digest)
            self.assertTrue(entry["url"].endswith("/" + filename))
        self.assertEqual(registry["files"]["weights"]["rows"], 25_568_639)


if __name__ == "__main__":
    unittest.main()
