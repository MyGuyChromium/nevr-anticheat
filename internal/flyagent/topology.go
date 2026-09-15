// Package flyagent contains the offline, connectome-constrained Echo VR agent
// prototype. It deliberately produces abstract action intents only; it does not
// inject controller input or join live matches.
package flyagent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
)

const (
	TopologySchema       = "nevr.fly.topology/v1"
	maxTopologyJSONBytes = 256 << 20
	maxTopologyNodes     = 250_000
	maxTopologyEdges     = 30_000_000
)

// DatasetMetadata records where a topology came from. A synthetic topology
// must say so; a derived MaleCNS topology should retain the source URL, release
// and license rather than being mistaken for a trained model.
type DatasetMetadata struct {
	Name                 string `json:"name"`
	Version              string `json:"version"`
	Source               string `json:"source"`
	License              string `json:"license"`
	SourceManifestSHA256 string `json:"source_manifest_sha256,omitempty"`
	Synthetic            bool   `json:"synthetic"`
	Derivation           string `json:"derivation,omitempty"`
}

// TopologyNode is one fixed unit in a topology. Bias is part of the frozen
// dynamics, not a learned Echo VR weight.
type TopologyNode struct {
	ID   string  `json:"id"`
	Bias float64 `json:"bias,omitempty"`
}

// TopologyEdge is a directed pre-synaptic -> post-synaptic connection.
// Weight may be negative for an inhibitory assumption profile.
type TopologyEdge struct {
	Pre    string  `json:"pre"`
	Post   string  `json:"post"`
	Weight float64 `json:"weight"`
}

// Topology binds named sensory channels and motor readout populations to a
// frozen directed graph. JSON is intended for bounded research subgraphs; the
// future full MaleCNS importer will use a compact on-disk representation.
type Topology struct {
	Schema  string              `json:"schema"`
	Dataset DatasetMetadata     `json:"dataset"`
	Nodes   []TopologyNode      `json:"nodes"`
	Edges   []TopologyEdge      `json:"edges"`
	Inputs  map[string][]string `json:"inputs"`
	Outputs map[string][]string `json:"outputs"`
}

// LoadTopology decodes one strictly-shaped, bounded topology document.
func LoadTopology(r io.Reader) (*Topology, error) {
	if r == nil {
		return nil, errors.New("topology reader is nil")
	}
	limited := &io.LimitedReader{R: r, N: maxTopologyJSONBytes + 1}
	document, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read topology: %w", err)
	}
	if len(document) > maxTopologyJSONBytes {
		return nil, fmt.Errorf("topology exceeds %d bytes", maxTopologyJSONBytes)
	}
	if err := rejectDuplicateJSONKeys(document); err != nil {
		return nil, fmt.Errorf("decode topology: %w", err)
	}
	var topology Topology
	if err := rejectNonExactJSONFields(document, &topology); err != nil {
		return nil, fmt.Errorf("decode topology: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(document))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&topology); err != nil {
		return nil, fmt.Errorf("decode topology: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("decode topology: multiple JSON values")
		}
		return nil, fmt.Errorf("decode topology trailer: %w", err)
	}
	if err := topology.Validate(); err != nil {
		return nil, err
	}
	return &topology, nil
}

func rejectDuplicateJSONKeys(document []byte) error {
	dec := json.NewDecoder(bytes.NewReader(document))
	var scanValue func() error
	scanValue = func() error {
		token, err := dec.Token()
		if err != nil {
			return err
		}
		delim, composite := token.(json.Delim)
		if !composite {
			return nil
		}
		switch delim {
		case '{':
			seen := make(map[string]struct{})
			for dec.More() {
				keyToken, err := dec.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("object key is not a string")
				}
				if _, duplicate := seen[key]; duplicate {
					return fmt.Errorf("duplicate JSON object key %q", key)
				}
				seen[key] = struct{}{}
				if err := scanValue(); err != nil {
					return err
				}
			}
			_, err = dec.Token()
			return err
		case '[':
			for dec.More() {
				if err := scanValue(); err != nil {
					return err
				}
			}
			_, err = dec.Token()
			return err
		default:
			return fmt.Errorf("unexpected JSON delimiter %q", delim)
		}
	}
	if err := scanValue(); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

// Validate rejects ambiguous, non-finite and unbounded graphs before any
// simulator allocation is attempted.
func (t *Topology) Validate() error {
	if t == nil {
		return errors.New("topology is nil")
	}
	if t.Schema != TopologySchema {
		return fmt.Errorf("topology schema %q is unsupported (want %q)", t.Schema, TopologySchema)
	}
	if strings.TrimSpace(t.Dataset.Name) == "" || strings.TrimSpace(t.Dataset.Version) == "" {
		return errors.New("topology dataset name and version are required")
	}
	if !t.Dataset.Synthetic && (strings.TrimSpace(t.Dataset.Source) == "" || strings.TrimSpace(t.Dataset.License) == "") {
		return errors.New("non-synthetic topology requires source and license attribution")
	}
	if !t.Dataset.Synthetic && !isSHA256(t.Dataset.SourceManifestSHA256) {
		return errors.New("non-synthetic topology requires a 64-hex source_manifest_sha256")
	}
	if len(t.Nodes) == 0 || len(t.Nodes) > maxTopologyNodes {
		return fmt.Errorf("topology node count %d is outside 1..%d", len(t.Nodes), maxTopologyNodes)
	}
	if len(t.Edges) > maxTopologyEdges {
		return fmt.Errorf("topology edge count %d exceeds %d", len(t.Edges), maxTopologyEdges)
	}

	ids := make(map[string]struct{}, len(t.Nodes))
	for i, node := range t.Nodes {
		if node.ID == "" || len(node.ID) > 128 || strings.TrimSpace(node.ID) != node.ID {
			return fmt.Errorf("node %d has an invalid id %q", i, node.ID)
		}
		if !finite(node.Bias) {
			return fmt.Errorf("node %q has a non-finite bias", node.ID)
		}
		if _, duplicate := ids[node.ID]; duplicate {
			return fmt.Errorf("duplicate node id %q", node.ID)
		}
		ids[node.ID] = struct{}{}
	}
	for i, edge := range t.Edges {
		if _, ok := ids[edge.Pre]; !ok {
			return fmt.Errorf("edge %d references unknown pre node %q", i, edge.Pre)
		}
		if _, ok := ids[edge.Post]; !ok {
			return fmt.Errorf("edge %d references unknown post node %q", i, edge.Post)
		}
		if !finite(edge.Weight) || edge.Weight == 0 {
			return fmt.Errorf("edge %d has an invalid weight", i)
		}
	}
	if err := validatePopulations("input", t.Inputs, ids); err != nil {
		return err
	}
	if err := validatePopulations("output", t.Outputs, ids); err != nil {
		return err
	}
	if len(t.Inputs) == 0 || len(t.Outputs) == 0 {
		return errors.New("topology requires at least one input and one output population")
	}
	return nil
}

func validatePopulations(kind string, populations map[string][]string, ids map[string]struct{}) error {
	for name, members := range populations {
		if name == "" || len(name) > 128 || strings.TrimSpace(name) != name {
			return fmt.Errorf("%s population has an invalid name %q", kind, name)
		}
		if len(members) == 0 {
			return fmt.Errorf("%s population %q is empty", kind, name)
		}
		seen := make(map[string]struct{}, len(members))
		for _, id := range members {
			if _, ok := ids[id]; !ok {
				return fmt.Errorf("%s population %q references unknown node %q", kind, name, id)
			}
			if _, duplicate := seen[id]; duplicate {
				return fmt.Errorf("%s population %q repeats node %q", kind, name, id)
			}
			seen[id] = struct{}{}
		}
	}
	return nil
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func isSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}
