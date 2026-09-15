package flyagent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type maleCNSArtifactFixture struct {
	dir          string
	buildPath    string
	topologyPath string
	sourcePath   string
	source       maleCNSSourceManifest
	topology     *Topology
	build        maleCNSBuildManifest
}

func newMaleCNSArtifactFixture(t *testing.T) *maleCNSArtifactFixture {
	t.Helper()
	dir := t.TempDir()
	topology := &Topology{
		Schema: TopologySchema,
		Inputs: make(map[string][]string), Outputs: make(map[string][]string),
	}
	var inputIDs []string
	for index, name := range maleCNSExpectedVisualInputs {
		id := "mcns:" + strconv.Itoa(1000+index)
		topology.Nodes = append(topology.Nodes, TopologyNode{ID: id})
		topology.Inputs[name] = []string{id}
		inputIDs = append(inputIDs, id)
	}
	for index, name := range maleCNSExpectedProprioceptiveInputs {
		id := "mcns:" + strconv.Itoa(1100+index)
		topology.Nodes = append(topology.Nodes, TopologyNode{ID: id})
		topology.Inputs[name] = []string{id}
		inputIDs = append(inputIDs, id)
	}
	topology.Nodes = append(topology.Nodes, TopologyNode{ID: "mcns:2000"})
	for index, id := range inputIDs {
		weight := float64(5)
		if index == 0 {
			weight = -5
		}
		topology.Edges = append(topology.Edges, TopologyEdge{Pre: id, Post: "mcns:2000", Weight: weight})
	}
	for index, name := range requiredMotorPopulations {
		id := "mcns:" + strconv.Itoa(3000+index)
		topology.Nodes = append(topology.Nodes, TopologyNode{ID: id})
		topology.Edges = append(topology.Edges, TopologyEdge{Pre: "mcns:2000", Post: id, Weight: 7})
		topology.Outputs[name] = []string{id}
	}
	sourceDataset := maleCNSSourceDataset{
		Name:        "MaleCNS",
		Version:     "v1.0",
		ReleasePage: "https://male-cns.janelia.org/download/",
		License:     "CC BY 4.0",
		LicenseURL:  "https://creativecommons.org/licenses/by/4.0/",
		Citation:    "Berg et al., Cell (2026), https://doi.org/10.1016/j.cell.2026.08.015",
		Credit:      "FlyEM/HHMI Janelia, University of Cambridge, MRC Laboratory of Molecular Biology, Google Research, and collaborators",
	}
	source := maleCNSSourceManifest{
		Schema:  MaleCNSSourceManifestSchema,
		Dataset: sourceDataset,
		Files: maleCNSSourceFiles{
			Annotations:       maleCNSSourceFileFromSpec(maleCNSOfficialSourceSpecs["annotations"]),
			Neurotransmitters: maleCNSSourceFileFromSpec(maleCNSOfficialSourceSpecs["neurotransmitters"]),
			Weights:           maleCNSSourceFileFromSpec(maleCNSOfficialSourceSpecs["weights"]),
		},
	}
	topology.Dataset = DatasetMetadata{
		Name:       "MaleCNS",
		Version:    "v1.0",
		Source:     sourceDataset.ReleasePage,
		License:    sourceDataset.License,
		Synthetic:  false,
		Derivation: "deterministic significant-only directed corridor; raw synapse counts signed by conservative presynaptic consensus-neurotransmitter assumptions; engineered, uncalibrated sensory and descending-neuron Echo readout partitions",
	}
	positive, negative := maleCNSEdgeSignCounts(topology)
	build := maleCNSBuildManifest{
		Schema: MaleCNSBuildManifestSchema,
		Compiler: maleCNSCompilerIdentity{
			Name: "nevr MaleCNS corridor compiler", Version: "1", PyArrow: "25.0.1",
		},
		Profile: maleCNSBuildProfile{
			Name:                     "visual-proprioceptive-descending/v1",
			EdgeDirection:            "body_pre -> body_post",
			EdgeWeight:               "signed raw synapse count (not normalized)",
			WeightsRelease:           "significant-only",
			MinWeight:                5,
			VisualMaxHops:            3,
			ProprioceptiveMaxHops:    3,
			MaxCandidateEdges:        int64(len(topology.Edges)),
			MaxNodes:                 int64(len(topology.Nodes)),
			MaxEdges:                 int64(len(topology.Edges)),
			EligibleFilter:           "non-empty superclass and status != Glia",
			VisualRootFilter:         "superclass == ol_intrinsic and type in {L1,L2} and assignedOlHex1/assignedOlHex2 are non-null",
			ProprioceptiveRootFilter: "superclass == vnc_sensory and class == mechanosensory_proprioceptive and rootSide in {L,R}",
			DescendingTargetFilter: maleCNSDescendingFilter{
				Superclass: "descending_neuron",
				Types:      []string{"DNa01", "DNa02", "DNp01", "DNp09", "DNg11", "MDN"},
			},
			CorridorRule:      "union of nodes on a directed root-to-target path within the per-modality hop bound",
			PopulationBinding: "ascending bodyId round-robin; engineered and uncalibrated",
		},
		SignProfile: maleCNSSignProfile{
			Basis: "consensus_nt of body_pre",
			Mapping: map[string]int{
				"acetylcholine": 1, "dopamine": 0, "gaba": -1,
				"glutamate": -1, "histamine": -1, "missing": 0,
				"octopamine": 0, "serotonin": 0, "unclear": 0,
			},
			UnknownLabelSign: 0,
			ZeroSignEdges:    "excluded",
		},
		Counts: maleCNSBuildCounts{
			EligibleBodies:              int64(len(topology.Nodes)),
			SelectedVisualRoots:         int64(len(maleCNSExpectedVisualInputs)),
			SelectedProprioceptiveRoots: int64(len(maleCNSExpectedProprioceptiveInputs)),
			SelectedDescendingTargets:   int64(len(requiredMotorPopulations)),
			RetainedVisualRoots:         int64(len(maleCNSExpectedVisualInputs)),
			RetainedProprioceptiveRoots: int64(len(maleCNSExpectedProprioceptiveInputs)),
			RetainedDescendingTargets:   int64(len(requiredMotorPopulations)),
			CandidateEdges:              int64(len(topology.Edges)),
			CorridorNodes:               int64(len(topology.Nodes)),
			CorridorEdges:               int64(len(topology.Edges)),
			PositiveEdges:               positive,
			NegativeEdges:               negative,
			EligibleConsensusNT:         map[string]int64{"acetylcholine": int64(len(topology.Nodes))},
		},
		Attribution: maleCNSAttribution{
			Citation: sourceDataset.Citation, Credit: sourceDataset.Credit,
			License: sourceDataset.License, LicenseURL: sourceDataset.LicenseURL,
			ReleasePage: sourceDataset.ReleasePage,
		},
	}
	fixture := &maleCNSArtifactFixture{
		dir:          dir,
		buildPath:    filepath.Join(dir, "build-manifest.json"),
		topologyPath: filepath.Join(dir, "topology.json"),
		sourcePath:   filepath.Join(dir, "source-manifest.json"),
		source:       source,
		topology:     topology,
		build:        build,
	}
	fixture.sync(t)
	return fixture
}

func maleCNSSourceFileFromSpec(spec maleCNSSourceSpec) maleCNSSourceFile {
	columns := make(map[string]string, len(spec.RequiredColumns))
	for name, kind := range spec.RequiredColumns {
		columns[name] = kind
	}
	return maleCNSSourceFile{
		Filename: spec.Filename, URL: spec.URL, Bytes: spec.Bytes, Rows: spec.Rows,
		SHA256: spec.SHA256, MD5Base64: spec.MD5Base64,
		GCSGeneration: spec.GCSGeneration, RequiredColumns: columns,
	}
}

func maleCNSEdgeSignCounts(topology *Topology) (int64, int64) {
	var positive, negative int64
	for _, edge := range topology.Edges {
		if edge.Weight > 0 {
			positive++
		} else {
			negative++
		}
	}
	return positive, negative
}

func marshalMaleCNSFixture(t *testing.T, value any) []byte {
	t.Helper()
	document, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(document, '\n')
}

func writeMaleCNSFixture(t *testing.T, path string, document []byte) maleCNSArtifactReference {
	t.Helper()
	if err := os.WriteFile(path, document, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(document)
	return maleCNSArtifactReference{
		Path: filepath.Base(path), Bytes: int64(len(document)),
		SHA256: hex.EncodeToString(digest[:]),
	}
}

func (fixture *maleCNSArtifactFixture) sync(t *testing.T) {
	t.Helper()
	fixture.syncWithSourceDocument(t, marshalMaleCNSFixture(t, fixture.source))
}

func (fixture *maleCNSArtifactFixture) syncWithSourceDocument(t *testing.T, sourceDocument []byte) {
	t.Helper()
	sourceRef := writeMaleCNSFixture(t, fixture.sourcePath, sourceDocument)
	sourceRef.Schema = MaleCNSSourceManifestSchema
	fixture.topology.Dataset.SourceManifestSHA256 = sourceRef.SHA256
	topologyRef := writeMaleCNSFixture(t, fixture.topologyPath, marshalMaleCNSFixture(t, fixture.topology))
	topologyRef.Schema = TopologySchema
	positive, negative := maleCNSEdgeSignCounts(fixture.topology)
	fixture.build.Dataset = fixture.topology.Dataset
	fixture.build.SourceManifest = sourceRef
	fixture.build.Outputs = maleCNSBuildOutputs{Topology: topologyRef, SourceManifest: sourceRef}
	fixture.build.Counts.CorridorNodes = int64(len(fixture.topology.Nodes))
	fixture.build.Counts.CorridorEdges = int64(len(fixture.topology.Edges))
	fixture.build.Counts.PositiveEdges = positive
	fixture.build.Counts.NegativeEdges = negative
	fixture.writeBuild(t)
}

func (fixture *maleCNSArtifactFixture) syncWithTopologyDocument(t *testing.T, topologyDocument []byte) {
	t.Helper()
	topologyRef := writeMaleCNSFixture(t, fixture.topologyPath, topologyDocument)
	topologyRef.Schema = TopologySchema
	fixture.build.Outputs.Topology = topologyRef
	fixture.writeBuild(t)
}

func (fixture *maleCNSArtifactFixture) writeBuild(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(fixture.buildPath, marshalMaleCNSFixture(t, fixture.build), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyMaleCNSArtifactReturnsBoundEvidence(t *testing.T) {
	fixture := newMaleCNSArtifactFixture(t)
	evidence, err := VerifyMaleCNSArtifact(fixture.buildPath)
	if err != nil {
		t.Fatal(err)
	}
	wantBuild, err := filepath.Abs(fixture.buildPath)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Schema != MaleCNSArtifactEvidenceSchema || evidence.DatasetName != "MaleCNS" || evidence.DatasetVersion != "v1.0" {
		t.Fatalf("evidence identity = %+v", evidence)
	}
	if evidence.BuildManifestPath != wantBuild || evidence.TopologyPath != fixture.topologyPath || evidence.SourceManifestPath != fixture.sourcePath {
		t.Fatalf("evidence paths = %+v", evidence)
	}
	if evidence.TopologyNodes != len(fixture.topology.Nodes) || evidence.TopologyEdges != len(fixture.topology.Edges) {
		t.Fatalf("evidence graph counts = %+v", evidence)
	}
	for label, digest := range map[string]string{
		"build": evidence.BuildManifestSHA256, "topology": evidence.TopologySHA256,
		"source": evidence.SourceManifestSHA256,
	} {
		if !maleCNSLowerSHA256(digest) {
			t.Errorf("%s evidence digest = %q", label, digest)
		}
	}
	if evidence.TopologySHA256 != fixture.build.Outputs.Topology.SHA256 || evidence.SourceManifestSHA256 != fixture.build.Outputs.SourceManifest.SHA256 {
		t.Fatalf("evidence hashes do not match manifest: %+v", evidence)
	}
}

func TestLoadVerifiedMaleCNSArtifactMatchesVerifierAndReturnsOwnedTopology(t *testing.T) {
	fixture := newMaleCNSArtifactFixture(t)
	topology, loadedEvidence, err := LoadVerifiedMaleCNSArtifact(fixture.buildPath)
	if err != nil {
		t.Fatal(err)
	}
	verifiedEvidence, err := VerifyMaleCNSArtifact(fixture.buildPath)
	if err != nil {
		t.Fatal(err)
	}
	if *loadedEvidence != *verifiedEvidence {
		t.Fatalf("load/verify evidence differs:\nload:   %+v\nverify: %+v", loadedEvidence, verifiedEvidence)
	}
	if topology == fixture.topology || topology.Dataset != fixture.topology.Dataset || len(topology.Nodes) != len(fixture.topology.Nodes) {
		t.Fatalf("loaded topology identity/content is wrong: %+v", topology.Dataset)
	}
	originalFirstNode := topology.Nodes[0].ID
	topology.Nodes[0].ID = "caller-owned-mutation"
	topology.Inputs["target_left"][0] = "caller-owned-mutation"

	reloaded, reloadedEvidence, err := LoadVerifiedMaleCNSArtifact(fixture.buildPath)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded == topology || reloaded.Nodes[0].ID != originalFirstNode || reloaded.Inputs["target_left"][0] != originalFirstNode {
		t.Fatal("caller mutation leaked into a separately loaded topology")
	}
	if *reloadedEvidence != *loadedEvidence {
		t.Fatal("evidence changed after mutating the caller-owned topology")
	}
}

func TestVerifyMaleCNSArtifactRejectsBuildManifestJSONAmbiguity(t *testing.T) {
	tests := map[string]func(*testing.T, *maleCNSArtifactFixture){
		"duplicate key": func(t *testing.T, fixture *maleCNSArtifactFixture) {
			doc := `{"schema":"nevr.fly.malecns-build/v1","schema":"nevr.fly.malecns-build/v1"}`
			if err := os.WriteFile(fixture.buildPath, []byte(doc), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"unknown top-level field": func(t *testing.T, fixture *maleCNSArtifactFixture) {
			var object map[string]any
			if err := json.Unmarshal(marshalMaleCNSFixture(t, fixture.build), &object); err != nil {
				t.Fatal(err)
			}
			object["unexpected"] = true
			if err := os.WriteFile(fixture.buildPath, marshalMaleCNSFixture(t, object), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"unknown nested field": func(t *testing.T, fixture *maleCNSArtifactFixture) {
			var object map[string]any
			if err := json.Unmarshal(marshalMaleCNSFixture(t, fixture.build), &object); err != nil {
				t.Fatal(err)
			}
			object["profile"].(map[string]any)["unexpected"] = true
			if err := os.WriteFile(fixture.buildPath, marshalMaleCNSFixture(t, object), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"trailing value": func(t *testing.T, fixture *maleCNSArtifactFixture) {
			file, err := os.OpenFile(fixture.buildPath, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := file.WriteString("true\n"); err != nil {
				t.Fatal(errors.Join(err, file.Close()))
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
		},
		"wrong schema": func(t *testing.T, fixture *maleCNSArtifactFixture) {
			fixture.build.Schema = "nevr.fly.malecns-build/v2"
			fixture.writeBuild(t)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newMaleCNSArtifactFixture(t)
			mutate(t, fixture)
			if _, err := VerifyMaleCNSArtifact(fixture.buildPath); err == nil {
				t.Fatal("ambiguous/unsupported build manifest was accepted")
			}
		})
	}
}

func TestVerifyMaleCNSArtifactRejectsCaseAliasedJSONFields(t *testing.T) {
	t.Run("build manifest case-only alias", func(t *testing.T) {
		fixture := newMaleCNSArtifactFixture(t)
		document := bytes.Replace(
			marshalMaleCNSFixture(t, fixture.build),
			[]byte(`"schema"`),
			[]byte(`"SCHEMA"`),
			1,
		)
		if err := os.WriteFile(fixture.buildPath, document, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyMaleCNSArtifact(fixture.buildPath); err == nil || !strings.Contains(err.Error(), "non-canonical JSON field") {
			t.Fatalf("case-only build alias error = %v", err)
		}
	})

	t.Run("build manifest case-colliding alias", func(t *testing.T) {
		fixture := newMaleCNSArtifactFixture(t)
		document := bytes.Replace(
			marshalMaleCNSFixture(t, fixture.build),
			[]byte("{\n"),
			[]byte("{\n  \"SCHEMA\": \"nevr.fly.malecns-build/v1\",\n"),
			1,
		)
		if err := os.WriteFile(fixture.buildPath, document, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyMaleCNSArtifact(fixture.buildPath); err == nil || !strings.Contains(err.Error(), "non-canonical JSON field") {
			t.Fatalf("case-colliding build alias error = %v", err)
		}
	})

	t.Run("source manifest case-only alias", func(t *testing.T) {
		fixture := newMaleCNSArtifactFixture(t)
		document := bytes.Replace(
			marshalMaleCNSFixture(t, fixture.source),
			[]byte(`"schema"`),
			[]byte(`"SCHEMA"`),
			1,
		)
		fixture.syncWithSourceDocument(t, document)
		if _, err := VerifyMaleCNSArtifact(fixture.buildPath); err == nil || !strings.Contains(err.Error(), "non-canonical JSON field") {
			t.Fatalf("case-only source alias error = %v", err)
		}
	})

	t.Run("topology case-only alias", func(t *testing.T) {
		fixture := newMaleCNSArtifactFixture(t)
		document := bytes.Replace(
			marshalMaleCNSFixture(t, fixture.topology),
			[]byte(`"schema"`),
			[]byte(`"SCHEMA"`),
			1,
		)
		fixture.syncWithTopologyDocument(t, document)
		if _, err := VerifyMaleCNSArtifact(fixture.buildPath); err == nil || !strings.Contains(err.Error(), "non-canonical JSON field") {
			t.Fatalf("case-only topology alias error = %v", err)
		}
	})
}

func TestVerifyMaleCNSArtifactRejectsReferencedJSONAmbiguity(t *testing.T) {
	t.Run("unknown source field", func(t *testing.T) {
		fixture := newMaleCNSArtifactFixture(t)
		var object map[string]any
		if err := json.Unmarshal(marshalMaleCNSFixture(t, fixture.source), &object); err != nil {
			t.Fatal(err)
		}
		object["unexpected"] = true
		fixture.syncWithSourceDocument(t, marshalMaleCNSFixture(t, object))
		if _, err := VerifyMaleCNSArtifact(fixture.buildPath); err == nil || !strings.Contains(err.Error(), "unknown field") {
			t.Fatalf("unknown source field error = %v", err)
		}
	})
	t.Run("duplicate source key", func(t *testing.T) {
		fixture := newMaleCNSArtifactFixture(t)
		fixture.syncWithSourceDocument(t, []byte(`{"schema":"nevr.fly.malecns-sources/v1","schema":"nevr.fly.malecns-sources/v1"}`))
		if _, err := VerifyMaleCNSArtifact(fixture.buildPath); err == nil || !strings.Contains(err.Error(), "duplicate JSON object key") {
			t.Fatalf("duplicate source key error = %v", err)
		}
	})
	t.Run("trailing source value", func(t *testing.T) {
		fixture := newMaleCNSArtifactFixture(t)
		document := append(marshalMaleCNSFixture(t, fixture.source), []byte("true\n")...)
		fixture.syncWithSourceDocument(t, document)
		if _, err := VerifyMaleCNSArtifact(fixture.buildPath); err == nil || !strings.Contains(err.Error(), "multiple JSON values") {
			t.Fatalf("trailing source value error = %v", err)
		}
	})
	t.Run("unknown topology field", func(t *testing.T) {
		fixture := newMaleCNSArtifactFixture(t)
		var object map[string]any
		if err := json.Unmarshal(marshalMaleCNSFixture(t, fixture.topology), &object); err != nil {
			t.Fatal(err)
		}
		object["unexpected"] = true
		fixture.syncWithTopologyDocument(t, marshalMaleCNSFixture(t, object))
		if _, err := VerifyMaleCNSArtifact(fixture.buildPath); err == nil || !strings.Contains(err.Error(), "unknown field") {
			t.Fatalf("unknown topology field error = %v", err)
		}
	})
}

func TestVerifyMaleCNSArtifactRejectsUnsafeReferences(t *testing.T) {
	tests := map[string]string{
		"parent traversal": "../topology.json",
		"child path":       "sub/topology.json",
		"windows path":     `sub\topology.json`,
		"hidden path":      ".topology.json",
		"space":            " topology.json",
	}
	for name, path := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newMaleCNSArtifactFixture(t)
			fixture.build.Outputs.Topology.Path = path
			fixture.writeBuild(t)
			if _, err := VerifyMaleCNSArtifact(fixture.buildPath); err == nil || !strings.Contains(err.Error(), "safe basename") {
				t.Fatalf("unsafe path error = %v", err)
			}
		})
	}
	t.Run("absolute", func(t *testing.T) {
		fixture := newMaleCNSArtifactFixture(t)
		fixture.build.Outputs.Topology.Path = fixture.topologyPath
		fixture.writeBuild(t)
		if _, err := VerifyMaleCNSArtifact(fixture.buildPath); err == nil || !strings.Contains(err.Error(), "safe basename") {
			t.Fatalf("absolute path error = %v", err)
		}
	})
	if _, err := VerifyMaleCNSArtifact(""); err == nil || !strings.Contains(err.Error(), "path is empty") {
		t.Fatalf("empty path error = %v", err)
	}
}

func TestVerifyMaleCNSArtifactRejectsArtifactTampering(t *testing.T) {
	t.Run("topology bytes", func(t *testing.T) {
		fixture := newMaleCNSArtifactFixture(t)
		file, err := os.OpenFile(fixture.topologyPath, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.WriteString(" "); err != nil {
			t.Fatal(errors.Join(err, file.Close()))
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyMaleCNSArtifact(fixture.buildPath); err == nil || !strings.Contains(err.Error(), "size") {
			t.Fatalf("topology tamper error = %v", err)
		}
	})
	t.Run("source bytes same size", func(t *testing.T) {
		fixture := newMaleCNSArtifactFixture(t)
		document, err := os.ReadFile(fixture.sourcePath)
		if err != nil {
			t.Fatal(err)
		}
		document[len(document)/2] ^= 1
		if err := os.WriteFile(fixture.sourcePath, document, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyMaleCNSArtifact(fixture.buildPath); err == nil || !strings.Contains(err.Error(), "SHA-256") {
			t.Fatalf("source tamper error = %v", err)
		}
	})
	t.Run("declared hash", func(t *testing.T) {
		fixture := newMaleCNSArtifactFixture(t)
		fixture.build.Outputs.Topology.SHA256 = strings.Repeat("a", 64)
		fixture.writeBuild(t)
		if _, err := VerifyMaleCNSArtifact(fixture.buildPath); err == nil || !strings.Contains(err.Error(), "SHA-256") {
			t.Fatalf("declared hash error = %v", err)
		}
	})
	t.Run("declared size", func(t *testing.T) {
		fixture := newMaleCNSArtifactFixture(t)
		fixture.build.Outputs.Topology.Bytes++
		fixture.writeBuild(t)
		if _, err := VerifyMaleCNSArtifact(fixture.buildPath); err == nil || !strings.Contains(err.Error(), "size") {
			t.Fatalf("declared size error = %v", err)
		}
	})
	t.Run("declared schema", func(t *testing.T) {
		fixture := newMaleCNSArtifactFixture(t)
		fixture.build.Outputs.Topology.Schema = "nevr.fly.topology/v2"
		fixture.writeBuild(t)
		if _, err := VerifyMaleCNSArtifact(fixture.buildPath); err == nil || !strings.Contains(err.Error(), "topology schema") {
			t.Fatalf("declared schema error = %v", err)
		}
	})
}

func TestVerifyMaleCNSArtifactRejectsSemanticMismatch(t *testing.T) {
	t.Run("source schema", func(t *testing.T) {
		fixture := newMaleCNSArtifactFixture(t)
		fixture.source.Schema = "nevr.fly.malecns-sources/v2"
		fixture.sync(t)
		if _, err := VerifyMaleCNSArtifact(fixture.buildPath); err == nil || !strings.Contains(err.Error(), "source manifest schema") {
			t.Fatalf("source schema error = %v", err)
		}
	})
	t.Run("unofficial source metadata", func(t *testing.T) {
		fixture := newMaleCNSArtifactFixture(t)
		fixture.source.Files.Weights.SHA256 = strings.Repeat("a", 64)
		fixture.sync(t)
		if _, err := VerifyMaleCNSArtifact(fixture.buildPath); err == nil || !strings.Contains(err.Error(), "pinned v1.0") {
			t.Fatalf("source metadata error = %v", err)
		}
	})
	t.Run("synthetic topology", func(t *testing.T) {
		fixture := newMaleCNSArtifactFixture(t)
		fixture.topology.Dataset.Synthetic = true
		fixture.sync(t)
		if _, err := VerifyMaleCNSArtifact(fixture.buildPath); err == nil || !strings.Contains(err.Error(), "non-synthetic") {
			t.Fatalf("synthetic topology error = %v", err)
		}
	})
	t.Run("agent contract", func(t *testing.T) {
		fixture := newMaleCNSArtifactFixture(t)
		delete(fixture.topology.Outputs, outputBrake)
		fixture.sync(t)
		if _, err := VerifyMaleCNSArtifact(fixture.buildPath); err == nil || !strings.Contains(err.Error(), "missing required motor") {
			t.Fatalf("contract error = %v", err)
		}
	})
	t.Run("source digest cross-reference", func(t *testing.T) {
		fixture := newMaleCNSArtifactFixture(t)
		fixture.topology.Dataset.SourceManifestSHA256 = strings.Repeat("a", 64)
		topologyRef := writeMaleCNSFixture(t, fixture.topologyPath, marshalMaleCNSFixture(t, fixture.topology))
		topologyRef.Schema = TopologySchema
		fixture.build.Dataset = fixture.topology.Dataset
		fixture.build.Outputs.Topology = topologyRef
		fixture.writeBuild(t)
		if _, err := VerifyMaleCNSArtifact(fixture.buildPath); err == nil || !strings.Contains(err.Error(), "does not match actual source") {
			t.Fatalf("source cross-reference error = %v", err)
		}
	})
	t.Run("manifest graph count", func(t *testing.T) {
		fixture := newMaleCNSArtifactFixture(t)
		fixture.build.Counts.CorridorNodes--
		fixture.writeBuild(t)
		if _, err := VerifyMaleCNSArtifact(fixture.buildPath); err == nil || !strings.Contains(err.Error(), "graph counts") {
			t.Fatalf("count mismatch error = %v", err)
		}
	})
	t.Run("sign profile", func(t *testing.T) {
		fixture := newMaleCNSArtifactFixture(t)
		fixture.build.SignProfile.Mapping["gaba"] = 1
		fixture.writeBuild(t)
		if _, err := VerifyMaleCNSArtifact(fixture.buildPath); err == nil || !strings.Contains(err.Error(), "sign profile") {
			t.Fatalf("sign-profile error = %v", err)
		}
	})
	t.Run("compiler input partition", func(t *testing.T) {
		fixture := newMaleCNSArtifactFixture(t)
		delete(fixture.topology.Inputs, "target_near")
		fixture.sync(t)
		if _, err := VerifyMaleCNSArtifact(fixture.buildPath); err == nil || !strings.Contains(err.Error(), "input") {
			t.Fatalf("compiler input error = %v", err)
		}
	})
	t.Run("neurotransmitter count overflow", func(t *testing.T) {
		fixture := newMaleCNSArtifactFixture(t)
		fixture.build.Counts.EligibleConsensusNT = map[string]int64{
			"acetylcholine": fixture.build.Counts.EligibleBodies,
			"gaba":          1,
		}
		fixture.writeBuild(t)
		if _, err := VerifyMaleCNSArtifact(fixture.buildPath); err == nil || !strings.Contains(err.Error(), "exceed") {
			t.Fatalf("neurotransmitter-count error = %v", err)
		}
	})
}

func TestVerifyMaleCNSArtifactRejectsLinksAndNonRegularFiles(t *testing.T) {
	t.Run("source symlink", func(t *testing.T) {
		fixture := newMaleCNSArtifactFixture(t)
		realPath := filepath.Join(fixture.dir, "real-source.json")
		if err := os.Rename(fixture.sourcePath, realPath); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Base(realPath), fixture.sourcePath); err != nil {
			if errors.Is(err, os.ErrPermission) {
				t.Skipf("creating symlinks is unavailable: %v", err)
			}
			t.Skipf("creating symlinks is unavailable: %v", err)
		}
		if _, err := VerifyMaleCNSArtifact(fixture.buildPath); err == nil || !strings.Contains(err.Error(), "link or not a regular file") {
			t.Fatalf("source symlink error = %v", err)
		}
	})
	t.Run("topology directory", func(t *testing.T) {
		fixture := newMaleCNSArtifactFixture(t)
		if err := os.Remove(fixture.topologyPath); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(fixture.topologyPath, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyMaleCNSArtifact(fixture.buildPath); err == nil || !strings.Contains(err.Error(), "link or not a regular file") {
			t.Fatalf("directory error = %v", err)
		}
	})
}

func TestVerifyMaleCNSArtifactBoundsBuildManifest(t *testing.T) {
	fixture := newMaleCNSArtifactFixture(t)
	document := []byte(`{"padding":"` + strings.Repeat("x", maxMaleCNSBuildManifestBytes) + `"}`)
	if err := os.WriteFile(fixture.buildPath, document, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyMaleCNSArtifact(fixture.buildPath); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("oversize build-manifest error = %v", err)
	}
}
