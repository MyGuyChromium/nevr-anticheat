package flyagent

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
)

const (
	// MaleCNSBuildManifestSchema identifies manifests emitted by the offline
	// MaleCNS compiler. The verifier accepts no other build schema.
	MaleCNSBuildManifestSchema = "nevr.fly.malecns-build/v1"
	// MaleCNSSourceManifestSchema identifies the pinned source registry copied
	// beside every compiled topology.
	MaleCNSSourceManifestSchema = "nevr.fly.malecns-sources/v1"
	// MaleCNSArtifactEvidenceSchema identifies the compact result returned after
	// every referenced byte and semantic contract has been checked.
	MaleCNSArtifactEvidenceSchema = "nevr.fly.malecns-evidence/v1"

	maxMaleCNSBuildManifestBytes  = 1 << 20
	maxMaleCNSSourceManifestBytes = 4 << 20

	maleCNSOfficialReleasePage = "https://male-cns.janelia.org/download/"
	maleCNSOfficialLicense     = "CC BY 4.0"
	maleCNSOfficialLicenseURL  = "https://creativecommons.org/licenses/by/4.0/"
	maleCNSOfficialCitation    = "Berg et al., Cell (2026), https://doi.org/10.1016/j.cell.2026.08.015"
	// #nosec G101 -- this is public dataset attribution text, not a credential.
	maleCNSOfficialCredit = "FlyEM/HHMI Janelia, University of Cambridge, MRC Laboratory of Molecular Biology, Google Research, and collaborators"
)

// MaleCNSArtifactEvidence is compact evidence about one successfully verified
// compiler output set. A SHA-256 identifies bytes; it is not a publisher
// signature or independent validation of the biological model.
type MaleCNSArtifactEvidence struct {
	Schema               string `json:"schema"`
	DatasetName          string `json:"dataset_name"`
	DatasetVersion       string `json:"dataset_version"`
	BuildManifestPath    string `json:"build_manifest_path"`
	BuildManifestSHA256  string `json:"build_manifest_sha256"`
	BuildManifestBytes   int64  `json:"build_manifest_bytes"`
	TopologyPath         string `json:"topology_path"`
	TopologySHA256       string `json:"topology_sha256"`
	TopologyBytes        int64  `json:"topology_bytes"`
	TopologyNodes        int    `json:"topology_nodes"`
	TopologyEdges        int    `json:"topology_edges"`
	SourceManifestPath   string `json:"source_manifest_path"`
	SourceManifestSHA256 string `json:"source_manifest_sha256"`
	SourceManifestBytes  int64  `json:"source_manifest_bytes"`
}

type maleCNSArtifactReference struct {
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
	Schema string `json:"schema"`
}

type maleCNSCompilerIdentity struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	PyArrow string `json:"pyarrow"`
}

type maleCNSDescendingFilter struct {
	Superclass string   `json:"superclass"`
	Types      []string `json:"types"`
}

type maleCNSBuildProfile struct {
	Name                     string                  `json:"name"`
	EdgeDirection            string                  `json:"edge_direction"`
	EdgeWeight               string                  `json:"edge_weight"`
	WeightsRelease           string                  `json:"weights_release"`
	MinWeight                int64                   `json:"min_weight"`
	VisualMaxHops            int64                   `json:"visual_max_hops"`
	ProprioceptiveMaxHops    int64                   `json:"proprioceptive_max_hops"`
	MaxCandidateEdges        int64                   `json:"max_candidate_edges"`
	MaxNodes                 int64                   `json:"max_nodes"`
	MaxEdges                 int64                   `json:"max_edges"`
	EligibleFilter           string                  `json:"eligible_filter"`
	VisualRootFilter         string                  `json:"visual_root_filter"`
	ProprioceptiveRootFilter string                  `json:"proprioceptive_root_filter"`
	DescendingTargetFilter   maleCNSDescendingFilter `json:"descending_target_filter"`
	CorridorRule             string                  `json:"corridor_rule"`
	PopulationBinding        string                  `json:"population_binding"`
}

type maleCNSSignProfile struct {
	Basis            string         `json:"basis"`
	Mapping          map[string]int `json:"mapping"`
	UnknownLabelSign int            `json:"unknown_label_sign"`
	ZeroSignEdges    string         `json:"zero_sign_edges"`
}

type maleCNSBuildCounts struct {
	EligibleBodies              int64            `json:"eligible_bodies"`
	SelectedVisualRoots         int64            `json:"selected_visual_roots"`
	SelectedProprioceptiveRoots int64            `json:"selected_proprioceptive_roots"`
	SelectedDescendingTargets   int64            `json:"selected_descending_targets"`
	RetainedVisualRoots         int64            `json:"retained_visual_roots"`
	RetainedProprioceptiveRoots int64            `json:"retained_proprioceptive_roots"`
	RetainedDescendingTargets   int64            `json:"retained_descending_targets"`
	CandidateEdges              int64            `json:"candidate_edges"`
	CorridorNodes               int64            `json:"corridor_nodes"`
	CorridorEdges               int64            `json:"corridor_edges"`
	PositiveEdges               int64            `json:"positive_edges"`
	NegativeEdges               int64            `json:"negative_edges"`
	EligibleConsensusNT         map[string]int64 `json:"eligible_consensus_nt"`
}

type maleCNSBuildOutputs struct {
	Topology       maleCNSArtifactReference `json:"topology"`
	SourceManifest maleCNSArtifactReference `json:"source_manifest"`
}

type maleCNSAttribution struct {
	Citation    string `json:"citation"`
	Credit      string `json:"credit"`
	License     string `json:"license"`
	LicenseURL  string `json:"license_url"`
	ReleasePage string `json:"release_page"`
}

type maleCNSBuildManifest struct {
	Schema         string                   `json:"schema"`
	Compiler       maleCNSCompilerIdentity  `json:"compiler"`
	Dataset        DatasetMetadata          `json:"dataset"`
	SourceManifest maleCNSArtifactReference `json:"source_manifest"`
	Profile        maleCNSBuildProfile      `json:"profile"`
	SignProfile    maleCNSSignProfile       `json:"sign_profile"`
	Counts         maleCNSBuildCounts       `json:"counts"`
	Outputs        maleCNSBuildOutputs      `json:"outputs"`
	Attribution    maleCNSAttribution       `json:"attribution"`
}

type maleCNSSourceDataset struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	ReleasePage string `json:"release_page"`
	License     string `json:"license"`
	LicenseURL  string `json:"license_url"`
	Citation    string `json:"citation"`
	Credit      string `json:"credit"`
}

type maleCNSSourceFile struct {
	Filename        string            `json:"filename"`
	URL             string            `json:"url"`
	Bytes           int64             `json:"bytes"`
	Rows            int64             `json:"rows"`
	SHA256          string            `json:"sha256"`
	MD5Base64       string            `json:"md5_base64"`
	GCSGeneration   string            `json:"gcs_generation"`
	RequiredColumns map[string]string `json:"required_columns"`
}

type maleCNSSourceFiles struct {
	Annotations       maleCNSSourceFile `json:"annotations"`
	Neurotransmitters maleCNSSourceFile `json:"neurotransmitters"`
	Weights           maleCNSSourceFile `json:"weights"`
}

type maleCNSSourceManifest struct {
	Schema  string               `json:"schema"`
	Dataset maleCNSSourceDataset `json:"dataset"`
	Files   maleCNSSourceFiles   `json:"files"`
}

type maleCNSSourceSpec struct {
	Filename        string
	URL             string
	Bytes           int64
	Rows            int64
	SHA256          string
	MD5Base64       string
	GCSGeneration   string
	RequiredColumns map[string]string
}

var maleCNSOfficialSourceSpecs = map[string]maleCNSSourceSpec{
	"annotations": {
		Filename:      "body-annotations-male-cns-v1.0-minconf-0.5.feather",
		URL:           "https://storage.googleapis.com/flyem-male-cns/v1.0/connectome-data/flat-connectome/body-annotations-male-cns-v1.0-minconf-0.5.feather",
		Bytes:         14_483_314,
		Rows:          211_577,
		SHA256:        "2177e246113e4cfbf1e7772ec37c6da1955ff22e8063d0b1f833101f99a9a3b2",
		MD5Base64:     "UKdxh3DFciDxYLpPQxq4ng==",
		GCSGeneration: "1780494878811468",
		RequiredColumns: map[string]string{
			"assignedOlHex1": "double", "assignedOlHex2": "double",
			"bodyId": "int64", "class": "string", "rootSide": "string",
			"status": "string", "superclass": "string", "type": "string",
		},
	},
	"neurotransmitters": {
		Filename:      "body-neurotransmitters-male-cns-v1.0.feather",
		URL:           "https://storage.googleapis.com/flyem-male-cns/v1.0/connectome-data/flat-connectome/body-neurotransmitters-male-cns-v1.0.feather",
		Bytes:         43_282_834,
		Rows:          1_835_518,
		SHA256:        "95c9289220663abeb3409f3ad9e5a7f8a53f8093f5139d15502cd08da8879621",
		MD5Base64:     "PYQrEv5cSe763lKNfdJKHw==",
		GCSGeneration: "1780894899156750",
		RequiredColumns: map[string]string{
			"body": "int64", "consensus_nt": "string",
		},
	},
	"weights": {
		Filename:      "connectome-weights-male-cns-v1.0-minconf-0.5-significant-only.feather",
		URL:           "https://storage.googleapis.com/flyem-male-cns/v1.0/connectome-data/flat-connectome/connectome-weights-male-cns-v1.0-minconf-0.5-significant-only.feather",
		Bytes:         502_169_298,
		Rows:          25_568_639,
		SHA256:        "5c536423a62a688e59e7b441f9c04d6272c9a1f017e35814cf561f8c275d9e9e",
		MD5Base64:     "CfL4M/cWGkatM81vnJBy9g==",
		GCSGeneration: "1780494884449936",
		RequiredColumns: map[string]string{
			"body_post": "int64", "body_pre": "int64", "weight": "int64",
		},
	},
}

var maleCNSExpectedSignProfile = map[string]int{
	"acetylcholine": 1,
	"dopamine":      0,
	"gaba":          -1,
	"glutamate":     -1,
	"histamine":     -1,
	"missing":       0,
	"octopamine":    0,
	"serotonin":     0,
	"unclear":       0,
}

var maleCNSExpectedDescendingTypes = []string{"DNa01", "DNa02", "DNp01", "DNp09", "DNg11", "MDN"}

var maleCNSExpectedVisualInputs = []string{
	"target_left", "target_right", "target_up", "target_down", "target_ahead",
	"target_behind", "target_near", "target_far", "evade_left", "evade_right",
}

var maleCNSExpectedProprioceptiveInputs = []string{
	"grab_opportunity", "throw_opportunity", "boost_opportunity", "brake_opportunity",
}

type maleCNSReadFile struct {
	path   string
	info   os.FileInfo
	bytes  int64
	sha256 string
}

// VerifyMaleCNSArtifact verifies one compiler build manifest and both files it
// references, returning compact evidence while discarding the loaded topology.
func VerifyMaleCNSArtifact(buildManifestPath string) (*MaleCNSArtifactEvidence, error) {
	_, evidence, err := LoadVerifiedMaleCNSArtifact(buildManifestPath)
	return evidence, err
}

// LoadVerifiedMaleCNSArtifact verifies one compiler build manifest and both
// files it references, then returns the topology parsed from the exact bytes
// whose digest appears in the evidence. Referenced paths are portable
// basenames resolved in the build manifest's directory; links and non-regular
// files are rejected before read. The returned topology is newly allocated and
// owned by the caller.
func LoadVerifiedMaleCNSArtifact(buildManifestPath string) (*Topology, *MaleCNSArtifactEvidence, error) {
	manifestPath, manifestDir, err := maleCNSManifestLocation(buildManifestPath)
	if err != nil {
		return nil, nil, err
	}
	manifestDocument, manifestRead, err := readMaleCNSRegularFile(
		manifestPath, "build manifest", maxMaleCNSBuildManifestBytes, nil,
	)
	if err != nil {
		return nil, nil, err
	}
	var manifest maleCNSBuildManifest
	if err := decodeMaleCNSStrictJSON("build manifest", manifestDocument, &manifest); err != nil {
		return nil, nil, err
	}
	if err := validateMaleCNSBuildManifest(&manifest); err != nil {
		return nil, nil, err
	}

	topologyPath, err := maleCNSReferencedPath(manifestDir, "topology", manifest.Outputs.Topology.Path)
	if err != nil {
		return nil, nil, err
	}
	sourcePath, err := maleCNSReferencedPath(manifestDir, "source manifest", manifest.Outputs.SourceManifest.Path)
	if err != nil {
		return nil, nil, err
	}
	if strings.EqualFold(topologyPath, sourcePath) {
		return nil, nil, errors.New("MaleCNS topology and source manifest resolve to the same path")
	}

	sourceDocument, sourceRead, err := readMaleCNSRegularFile(
		sourcePath,
		"source manifest",
		maxMaleCNSSourceManifestBytes,
		&manifest.Outputs.SourceManifest,
	)
	if err != nil {
		return nil, nil, err
	}
	var sourceManifest maleCNSSourceManifest
	if err := decodeMaleCNSStrictJSON("source manifest", sourceDocument, &sourceManifest); err != nil {
		return nil, nil, err
	}
	if err := validateMaleCNSSourceManifest(&sourceManifest); err != nil {
		return nil, nil, err
	}

	topology, topologyRead, err := loadMaleCNSTopology(
		topologyPath, manifest.Outputs.Topology,
	)
	if err != nil {
		return nil, nil, err
	}
	if os.SameFile(sourceRead.info, topologyRead.info) {
		return nil, nil, errors.New("MaleCNS topology and source manifest are the same file")
	}
	if os.SameFile(manifestRead.info, sourceRead.info) || os.SameFile(manifestRead.info, topologyRead.info) {
		return nil, nil, errors.New("MaleCNS build manifest aliases one of its output files")
	}
	if err := validateMaleCNSCrossReferences(&manifest, &sourceManifest, topology, sourceRead); err != nil {
		return nil, nil, err
	}

	evidence := &MaleCNSArtifactEvidence{
		Schema:               MaleCNSArtifactEvidenceSchema,
		DatasetName:          topology.Dataset.Name,
		DatasetVersion:       topology.Dataset.Version,
		BuildManifestPath:    manifestRead.path,
		BuildManifestSHA256:  manifestRead.sha256,
		BuildManifestBytes:   manifestRead.bytes,
		TopologyPath:         topologyRead.path,
		TopologySHA256:       topologyRead.sha256,
		TopologyBytes:        topologyRead.bytes,
		TopologyNodes:        len(topology.Nodes),
		TopologyEdges:        len(topology.Edges),
		SourceManifestPath:   sourceRead.path,
		SourceManifestSHA256: sourceRead.sha256,
		SourceManifestBytes:  sourceRead.bytes,
	}
	return topology, evidence, nil
}

func maleCNSManifestLocation(path string) (string, string, error) {
	if strings.TrimSpace(path) == "" {
		return "", "", errors.New("MaleCNS build manifest path is empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", "", fmt.Errorf("resolve MaleCNS build manifest path: %w", err)
	}
	abs = filepath.Clean(abs)
	dir := filepath.Dir(abs)
	info, err := os.Lstat(dir)
	if err != nil {
		return "", "", fmt.Errorf("inspect MaleCNS artifact directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", "", fmt.Errorf("MaleCNS artifact directory is a link or not a directory: %s", dir)
	}
	return abs, dir, nil
}

func maleCNSReferencedPath(dir, label, name string) (string, error) {
	if !maleCNSSafeBasename(name) {
		return "", fmt.Errorf("MaleCNS %s path %q is not a safe basename", label, name)
	}
	joined := filepath.Join(dir, name)
	if filepath.Dir(joined) != dir {
		return "", fmt.Errorf("MaleCNS %s escapes the build-manifest directory", label)
	}
	return joined, nil
}

func maleCNSSafeBasename(name string) bool {
	if name == "" || len(name) > 255 || strings.TrimSpace(name) != name || name[0] == '.' {
		return false
	}
	if filepath.IsAbs(name) || filepath.VolumeName(name) != "" || strings.ContainsAny(name, `/\`) {
		return false
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '.' || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return filepath.Base(name) == name
}

func readMaleCNSRegularFile(path, label string, maxBytes int64, expected *maleCNSArtifactReference) ([]byte, maleCNSReadFile, error) {
	file, before, err := openMaleCNSRegularFile(path, label, maxBytes, expected)
	if err != nil {
		return nil, maleCNSReadFile{}, err
	}
	defer file.Close()
	document, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, maleCNSReadFile{}, fmt.Errorf("read MaleCNS %s: %w", label, err)
	}
	if int64(len(document)) > maxBytes {
		return nil, maleCNSReadFile{}, fmt.Errorf("MaleCNS %s exceeds %d bytes", label, maxBytes)
	}
	digest := sha256.Sum256(document)
	read := maleCNSReadFile{
		path: path, info: before, bytes: int64(len(document)), sha256: hex.EncodeToString(digest[:]),
	}
	if err := finishMaleCNSRegularFile(file, before, path, label, read.bytes); err != nil {
		return nil, maleCNSReadFile{}, err
	}
	if expected != nil {
		if read.bytes != expected.Bytes {
			return nil, maleCNSReadFile{}, fmt.Errorf("MaleCNS %s byte count %d does not match declared %d", label, read.bytes, expected.Bytes)
		}
		if read.sha256 != expected.SHA256 {
			return nil, maleCNSReadFile{}, fmt.Errorf("MaleCNS %s SHA-256 %s does not match declared %s", label, read.sha256, expected.SHA256)
		}
	}
	return document, read, nil
}

func openMaleCNSRegularFile(path, label string, maxBytes int64, expected *maleCNSArtifactReference) (*os.File, os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, nil, fmt.Errorf("inspect MaleCNS %s: %w", label, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("MaleCNS %s is a link or not a regular file: %s", label, path)
	}
	if info.Size() < 1 || info.Size() > maxBytes {
		return nil, nil, fmt.Errorf("MaleCNS %s size %d is outside 1..%d", label, info.Size(), maxBytes)
	}
	if expected != nil && info.Size() != expected.Bytes {
		return nil, nil, fmt.Errorf("MaleCNS %s size %d does not match declared %d", label, info.Size(), expected.Bytes)
	}
	file, err := os.Open(path) // #nosec G304 -- path is operator supplied or a validated same-directory basename.
	if err != nil {
		return nil, nil, fmt.Errorf("open MaleCNS %s: %w", label, err)
	}
	opened, err := file.Stat()
	if err != nil {
		closeErr := file.Close()
		return nil, nil, errors.Join(fmt.Errorf("stat opened MaleCNS %s: %w", label, err), closeErr)
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		closeErr := file.Close()
		return nil, nil, errors.Join(
			fmt.Errorf("MaleCNS %s changed or resolved through a link before open", label),
			closeErr,
		)
	}
	return file, info, nil
}

func finishMaleCNSRegularFile(file *os.File, before os.FileInfo, path, label string, bytesRead int64) error {
	after, err := file.Stat()
	if err != nil {
		return fmt.Errorf("restat opened MaleCNS %s: %w", label, err)
	}
	if !os.SameFile(before, after) || after.Size() != before.Size() || bytesRead != before.Size() {
		return fmt.Errorf("MaleCNS %s changed while it was read", label)
	}
	current, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("reinspect MaleCNS %s: %w", label, err)
	}
	if current.Mode()&os.ModeSymlink != 0 || !current.Mode().IsRegular() || !os.SameFile(before, current) {
		return fmt.Errorf("MaleCNS %s path changed while it was read", label)
	}
	return nil
}

type maleCNSHashCounter struct {
	hash  io.Writer
	bytes int64
}

func (counter *maleCNSHashCounter) Write(p []byte) (int, error) {
	n, err := counter.hash.Write(p)
	counter.bytes += int64(n)
	return n, err
}

func loadMaleCNSTopology(path string, expected maleCNSArtifactReference) (*Topology, maleCNSReadFile, error) {
	file, before, err := openMaleCNSRegularFile(path, "topology", maxTopologyJSONBytes, &expected)
	if err != nil {
		return nil, maleCNSReadFile{}, err
	}
	defer file.Close()
	hash := sha256.New()
	counter := &maleCNSHashCounter{hash: hash}
	topology, err := LoadTopology(io.TeeReader(file, counter))
	if err != nil {
		return nil, maleCNSReadFile{}, fmt.Errorf("load MaleCNS topology: %w", err)
	}
	read := maleCNSReadFile{
		path: path, info: before, bytes: counter.bytes, sha256: hex.EncodeToString(hash.Sum(nil)),
	}
	if err := finishMaleCNSRegularFile(file, before, path, "topology", read.bytes); err != nil {
		return nil, maleCNSReadFile{}, err
	}
	if read.bytes != expected.Bytes {
		return nil, maleCNSReadFile{}, fmt.Errorf("MaleCNS topology byte count %d does not match declared %d", read.bytes, expected.Bytes)
	}
	if read.sha256 != expected.SHA256 {
		return nil, maleCNSReadFile{}, fmt.Errorf("MaleCNS topology SHA-256 %s does not match declared %s", read.sha256, expected.SHA256)
	}
	if err := ValidateAgentContract(topology); err != nil {
		return nil, maleCNSReadFile{}, fmt.Errorf("validate MaleCNS agent contract: %w", err)
	}
	if err := validateMaleCNSCompiledTopologyShape(topology); err != nil {
		return nil, maleCNSReadFile{}, err
	}
	return topology, read, nil
}

func validateMaleCNSCompiledTopologyShape(topology *Topology) error {
	bodyByNode := make(map[string]int64, len(topology.Nodes))
	var priorBody int64
	for index, node := range topology.Nodes {
		body, err := maleCNSBodyID(node.ID)
		if err != nil {
			return fmt.Errorf("MaleCNS topology node %d: %w", index, err)
		}
		if node.Bias != 0 {
			return fmt.Errorf("MaleCNS compiler topology node %q has a non-zero bias", node.ID)
		}
		if index > 0 && body <= priorBody {
			return errors.New("MaleCNS compiler topology nodes are not in ascending bodyId order")
		}
		bodyByNode[node.ID] = body
		priorBody = body
	}

	var priorPost, priorPre int64
	for index, edge := range topology.Edges {
		pre, preOK := bodyByNode[edge.Pre]
		post, postOK := bodyByNode[edge.Post]
		if !preOK || !postOK {
			return fmt.Errorf("MaleCNS topology edge %d does not reference compiler body IDs", index)
		}
		if math.Trunc(edge.Weight) != edge.Weight || math.Abs(edge.Weight) > 1<<53 {
			return fmt.Errorf("MaleCNS topology edge %d is not an exact signed raw synapse count", index)
		}
		if index > 0 && (post < priorPost || (post == priorPost && pre <= priorPre)) {
			return errors.New("MaleCNS compiler topology edges are not unique and sorted by post/pre bodyId")
		}
		priorPost, priorPre = post, pre
	}
	if err := validateMaleCNSRoundRobinPopulation(topology.Inputs, maleCNSExpectedVisualInputs, bodyByNode, "visual input"); err != nil {
		return err
	}
	if err := validateMaleCNSRoundRobinPopulation(topology.Inputs, maleCNSExpectedProprioceptiveInputs, bodyByNode, "proprioceptive input"); err != nil {
		return err
	}
	if err := validateMaleCNSRoundRobinPopulation(topology.Outputs, requiredMotorPopulations, bodyByNode, "output"); err != nil {
		return err
	}
	return nil
}

type maleCNSPopulationMember struct {
	body int64
	name string
}

func validateMaleCNSRoundRobinPopulation(populations map[string][]string, names []string, bodyByNode map[string]int64, label string) error {
	var all []maleCNSPopulationMember
	for _, name := range names {
		members := populations[name]
		var priorBody int64
		for index, member := range members {
			body, ok := bodyByNode[member]
			if !ok {
				return fmt.Errorf("MaleCNS compiler %s %q references non-body node %q", label, name, member)
			}
			if index > 0 && body <= priorBody {
				return fmt.Errorf("MaleCNS compiler %s %q is not in ascending bodyId order", label, name)
			}
			priorBody = body
			all = append(all, maleCNSPopulationMember{body: body, name: name})
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].body < all[j].body })
	for index, member := range all {
		want := names[index%len(names)]
		if member.name != want {
			return fmt.Errorf("MaleCNS compiler %s populations do not match ascending-bodyId round-robin binding", label)
		}
	}
	return nil
}

func maleCNSBodyID(nodeID string) (int64, error) {
	const prefix = "mcns:"
	if !strings.HasPrefix(nodeID, prefix) {
		return 0, fmt.Errorf("node ID %q does not have the %q prefix", nodeID, prefix)
	}
	raw := strings.TrimPrefix(nodeID, prefix)
	body, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || body <= 0 || strconv.FormatInt(body, 10) != raw {
		return 0, fmt.Errorf("node ID %q does not contain a canonical positive bodyId", nodeID)
	}
	return body, nil
}

func decodeMaleCNSStrictJSON(label string, document []byte, destination any) error {
	if err := rejectDuplicateJSONKeys(document); err != nil {
		return fmt.Errorf("decode MaleCNS %s: %w", label, err)
	}
	if err := rejectNonExactJSONFields(document, destination); err != nil {
		return fmt.Errorf("decode MaleCNS %s: %w", label, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode MaleCNS %s: %w", label, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("decode MaleCNS %s: multiple JSON values", label)
		}
		return fmt.Errorf("decode MaleCNS %s trailer: %w", label, err)
	}
	return nil
}

func validateMaleCNSBuildManifest(manifest *maleCNSBuildManifest) error {
	if manifest.Schema != MaleCNSBuildManifestSchema {
		return fmt.Errorf("MaleCNS build manifest schema %q is unsupported (want %q)", manifest.Schema, MaleCNSBuildManifestSchema)
	}
	if manifest.Compiler.Name != "nevr MaleCNS corridor compiler" || manifest.Compiler.Version != "1" || manifest.Compiler.PyArrow != "25.0.1" {
		return fmt.Errorf("MaleCNS build manifest has unsupported compiler identity %+v", manifest.Compiler)
	}
	if manifest.SourceManifest != manifest.Outputs.SourceManifest {
		return errors.New("MaleCNS build manifest has inconsistent source-manifest descriptors")
	}
	if err := validateMaleCNSReference("topology", manifest.Outputs.Topology, TopologySchema, maxTopologyJSONBytes); err != nil {
		return err
	}
	if err := validateMaleCNSReference("source manifest", manifest.Outputs.SourceManifest, MaleCNSSourceManifestSchema, maxMaleCNSSourceManifestBytes); err != nil {
		return err
	}
	profile := manifest.Profile
	if profile.Name != "visual-proprioceptive-descending/v1" || profile.EdgeDirection != "body_pre -> body_post" ||
		profile.EdgeWeight != "signed raw synapse count (not normalized)" || profile.WeightsRelease != "significant-only" {
		return errors.New("MaleCNS build manifest has an unsupported extraction profile")
	}
	if profile.MinWeight < 1 || profile.VisualMaxHops < 1 || profile.VisualMaxHops > 32 ||
		profile.ProprioceptiveMaxHops < 1 || profile.ProprioceptiveMaxHops > 32 || profile.MaxCandidateEdges < 1 ||
		profile.MaxNodes < 1 || profile.MaxNodes > maxTopologyNodes || profile.MaxEdges < 1 || profile.MaxEdges > maxTopologyEdges {
		return errors.New("MaleCNS build manifest has invalid extraction bounds")
	}
	if profile.EligibleFilter != "non-empty superclass and status != Glia" ||
		profile.VisualRootFilter != "superclass == ol_intrinsic and type in {L1,L2} and assignedOlHex1/assignedOlHex2 are non-null" ||
		profile.ProprioceptiveRootFilter != "superclass == vnc_sensory and class == mechanosensory_proprioceptive and rootSide in {L,R}" ||
		profile.CorridorRule != "union of nodes on a directed root-to-target path within the per-modality hop bound" ||
		profile.PopulationBinding != "ascending bodyId round-robin; engineered and uncalibrated" ||
		profile.DescendingTargetFilter.Superclass != "descending_neuron" ||
		!slices.Equal(profile.DescendingTargetFilter.Types, maleCNSExpectedDescendingTypes) {
		return errors.New("MaleCNS build manifest has an unsupported extraction filter or population binding")
	}
	seenTypes := make(map[string]struct{}, len(profile.DescendingTargetFilter.Types))
	for _, name := range profile.DescendingTargetFilter.Types {
		if strings.TrimSpace(name) == "" {
			return errors.New("MaleCNS build manifest has an empty descending-neuron type")
		}
		if _, duplicate := seenTypes[name]; duplicate {
			return fmt.Errorf("MaleCNS build manifest repeats descending-neuron type %q", name)
		}
		seenTypes[name] = struct{}{}
	}
	if manifest.SignProfile.Basis != "consensus_nt of body_pre" || manifest.SignProfile.UnknownLabelSign != 0 ||
		manifest.SignProfile.ZeroSignEdges != "excluded" || !maps.Equal(manifest.SignProfile.Mapping, maleCNSExpectedSignProfile) {
		return errors.New("MaleCNS build manifest has an unsupported neurotransmitter sign profile")
	}
	if err := validateMaleCNSCounts(manifest); err != nil {
		return err
	}
	if strings.TrimSpace(manifest.Attribution.Citation) == "" || strings.TrimSpace(manifest.Attribution.Credit) == "" ||
		strings.TrimSpace(manifest.Attribution.License) == "" || strings.TrimSpace(manifest.Attribution.LicenseURL) == "" ||
		strings.TrimSpace(manifest.Attribution.ReleasePage) == "" {
		return errors.New("MaleCNS build manifest attribution is incomplete")
	}
	return nil
}

func validateMaleCNSReference(label string, reference maleCNSArtifactReference, schema string, maxBytes int64) error {
	if !maleCNSSafeBasename(reference.Path) {
		return fmt.Errorf("MaleCNS %s path %q is not a safe basename", label, reference.Path)
	}
	if reference.Bytes < 1 || reference.Bytes > maxBytes {
		return fmt.Errorf("MaleCNS %s declared bytes %d is outside 1..%d", label, reference.Bytes, maxBytes)
	}
	if !maleCNSLowerSHA256(reference.SHA256) {
		return fmt.Errorf("MaleCNS %s requires a lowercase SHA-256", label)
	}
	if reference.Schema != schema {
		return fmt.Errorf("MaleCNS %s schema %q is unsupported (want %q)", label, reference.Schema, schema)
	}
	return nil
}

func validateMaleCNSCounts(manifest *maleCNSBuildManifest) error {
	counts, profile := manifest.Counts, manifest.Profile
	values := []int64{
		counts.EligibleBodies, counts.SelectedVisualRoots, counts.SelectedProprioceptiveRoots,
		counts.SelectedDescendingTargets, counts.RetainedVisualRoots, counts.RetainedProprioceptiveRoots,
		counts.RetainedDescendingTargets, counts.CandidateEdges, counts.CorridorNodes,
		counts.CorridorEdges, counts.PositiveEdges, counts.NegativeEdges,
	}
	for _, value := range values {
		if value < 0 {
			return errors.New("MaleCNS build manifest contains a negative count")
		}
	}
	if counts.EligibleBodies < counts.CorridorNodes || counts.SelectedVisualRoots < counts.RetainedVisualRoots ||
		counts.SelectedProprioceptiveRoots < counts.RetainedProprioceptiveRoots ||
		counts.SelectedDescendingTargets < counts.RetainedDescendingTargets {
		return errors.New("MaleCNS build manifest retained counts exceed selected/eligible counts")
	}
	if counts.CandidateEdges < counts.CorridorEdges || counts.CandidateEdges > profile.MaxCandidateEdges ||
		counts.CorridorNodes > profile.MaxNodes || counts.CorridorEdges > profile.MaxEdges ||
		counts.PositiveEdges > counts.CorridorEdges || counts.NegativeEdges > counts.CorridorEdges ||
		counts.PositiveEdges+counts.NegativeEdges != counts.CorridorEdges {
		return errors.New("MaleCNS build manifest graph counts violate declared bounds")
	}
	if len(manifest.Counts.EligibleConsensusNT) == 0 {
		return errors.New("MaleCNS build manifest has no neurotransmitter counts")
	}
	var transmitterBodies int64
	for name, count := range manifest.Counts.EligibleConsensusNT {
		if strings.TrimSpace(name) == "" || count < 0 {
			return errors.New("MaleCNS build manifest has an invalid neurotransmitter count")
		}
		if transmitterBodies > counts.EligibleBodies || count > counts.EligibleBodies-transmitterBodies {
			return errors.New("MaleCNS neurotransmitter counts exceed the eligible body count")
		}
		transmitterBodies += count
	}
	if transmitterBodies != counts.EligibleBodies {
		return fmt.Errorf("MaleCNS neurotransmitter counts total %d, want eligible body count %d", transmitterBodies, counts.EligibleBodies)
	}
	return nil
}

func validateMaleCNSSourceManifest(manifest *maleCNSSourceManifest) error {
	if manifest.Schema != MaleCNSSourceManifestSchema {
		return fmt.Errorf("MaleCNS source manifest schema %q is unsupported (want %q)", manifest.Schema, MaleCNSSourceManifestSchema)
	}
	dataset := manifest.Dataset
	if dataset.Name != "MaleCNS" || dataset.Version != "v1.0" ||
		dataset.ReleasePage != maleCNSOfficialReleasePage || dataset.License != maleCNSOfficialLicense ||
		dataset.LicenseURL != maleCNSOfficialLicenseURL || dataset.Citation != maleCNSOfficialCitation ||
		dataset.Credit != maleCNSOfficialCredit {
		return errors.New("MaleCNS source manifest does not identify the official attributed v1.0 dataset")
	}
	files := map[string]maleCNSSourceFile{
		"annotations": manifest.Files.Annotations, "neurotransmitters": manifest.Files.Neurotransmitters,
		"weights": manifest.Files.Weights,
	}
	for role, expected := range maleCNSOfficialSourceSpecs {
		actual := files[role]
		if actual.Filename != expected.Filename || actual.URL != expected.URL || actual.Bytes != expected.Bytes ||
			actual.Rows != expected.Rows || actual.SHA256 != expected.SHA256 || actual.MD5Base64 != expected.MD5Base64 ||
			actual.GCSGeneration != expected.GCSGeneration || !maps.Equal(actual.RequiredColumns, expected.RequiredColumns) {
			return fmt.Errorf("MaleCNS source manifest %s metadata does not match the pinned v1.0 release", role)
		}
		if !maleCNSLowerSHA256(actual.SHA256) {
			return fmt.Errorf("MaleCNS source manifest %s SHA-256 is invalid", role)
		}
		decodedMD5, err := base64.StdEncoding.DecodeString(actual.MD5Base64)
		if err != nil || len(decodedMD5) != 16 {
			return fmt.Errorf("MaleCNS source manifest %s MD5 metadata is invalid", role)
		}
		if _, err := strconv.ParseUint(actual.GCSGeneration, 10, 64); err != nil {
			return fmt.Errorf("MaleCNS source manifest %s GCS generation is invalid", role)
		}
	}
	return nil
}

func validateMaleCNSCrossReferences(manifest *maleCNSBuildManifest, source *maleCNSSourceManifest, topology *Topology, sourceRead maleCNSReadFile) error {
	if topology.Dataset.Synthetic || topology.Dataset.Name != "MaleCNS" || topology.Dataset.Version != "v1.0" {
		return errors.New("MaleCNS topology must be a non-synthetic MaleCNS v1.0 dataset")
	}
	if manifest.Dataset != topology.Dataset {
		return errors.New("MaleCNS build-manifest dataset does not match topology dataset metadata")
	}
	if topology.Dataset.Derivation != "deterministic significant-only directed corridor; raw synapse counts signed by conservative presynaptic consensus-neurotransmitter assumptions; engineered, uncalibrated sensory and descending-neuron Echo readout partitions" {
		return errors.New("MaleCNS topology has an unsupported compiler derivation")
	}
	if topology.Dataset.SourceManifestSHA256 != sourceRead.sha256 {
		return fmt.Errorf("MaleCNS topology source_manifest_sha256 %q does not match actual source manifest %q", topology.Dataset.SourceManifestSHA256, sourceRead.sha256)
	}
	if topology.Dataset.Source != source.Dataset.ReleasePage || topology.Dataset.License != source.Dataset.License {
		return errors.New("MaleCNS topology attribution does not match the source manifest")
	}
	if manifest.Attribution.ReleasePage != source.Dataset.ReleasePage || manifest.Attribution.License != source.Dataset.License ||
		manifest.Attribution.LicenseURL != source.Dataset.LicenseURL || manifest.Attribution.Citation != source.Dataset.Citation ||
		manifest.Attribution.Credit != source.Dataset.Credit {
		return errors.New("MaleCNS build-manifest attribution does not match the source manifest")
	}
	counts := manifest.Counts
	if counts.CorridorNodes != int64(len(topology.Nodes)) || counts.CorridorEdges != int64(len(topology.Edges)) {
		return errors.New("MaleCNS build-manifest graph counts do not match the topology")
	}
	var positive, negative int64
	for _, edge := range topology.Edges {
		if edge.Weight > 0 {
			positive++
		} else {
			negative++
		}
	}
	if counts.PositiveEdges != positive || counts.NegativeEdges != negative {
		return errors.New("MaleCNS build-manifest edge-sign counts do not match the topology")
	}
	if err := validateMaleCNSPopulationPartitions(topology, counts); err != nil {
		return err
	}
	return nil
}

func validateMaleCNSPopulationPartitions(topology *Topology, counts maleCNSBuildCounts) error {
	if len(topology.Inputs) != len(maleCNSExpectedVisualInputs)+len(maleCNSExpectedProprioceptiveInputs) {
		return errors.New("MaleCNS topology has unexpected input populations")
	}
	seenInputs := make(map[string]struct{})
	var visualMembers, proprioceptiveMembers int64
	for _, name := range maleCNSExpectedVisualInputs {
		members, ok := topology.Inputs[name]
		if !ok {
			return fmt.Errorf("MaleCNS topology is missing compiler visual input %q", name)
		}
		visualMembers += int64(len(members))
		for _, member := range members {
			if _, duplicate := seenInputs[member]; duplicate {
				return fmt.Errorf("MaleCNS compiler input partitions repeat node %q", member)
			}
			seenInputs[member] = struct{}{}
		}
	}
	for _, name := range maleCNSExpectedProprioceptiveInputs {
		members, ok := topology.Inputs[name]
		if !ok {
			return fmt.Errorf("MaleCNS topology is missing compiler proprioceptive input %q", name)
		}
		proprioceptiveMembers += int64(len(members))
		for _, member := range members {
			if _, duplicate := seenInputs[member]; duplicate {
				return fmt.Errorf("MaleCNS compiler input partitions repeat node %q", member)
			}
			seenInputs[member] = struct{}{}
		}
	}
	if visualMembers != counts.RetainedVisualRoots || proprioceptiveMembers != counts.RetainedProprioceptiveRoots {
		return errors.New("MaleCNS input partition sizes do not match retained-root counts")
	}

	if len(topology.Outputs) != len(requiredMotorPopulations) {
		return errors.New("MaleCNS topology has unexpected output populations")
	}
	seenOutputs := make(map[string]struct{})
	var outputMembers int64
	for _, name := range requiredMotorPopulations {
		members, ok := topology.Outputs[name]
		if !ok {
			return fmt.Errorf("MaleCNS topology is missing compiler output %q", name)
		}
		outputMembers += int64(len(members))
		for _, member := range members {
			if _, duplicate := seenOutputs[member]; duplicate {
				return fmt.Errorf("MaleCNS compiler output partitions repeat node %q", member)
			}
			seenOutputs[member] = struct{}{}
		}
	}
	if outputMembers != counts.RetainedDescendingTargets {
		return errors.New("MaleCNS output partition sizes do not match retained-target count")
	}
	return nil
}

func maleCNSLowerSHA256(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}
