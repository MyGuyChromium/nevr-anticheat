package flyagent

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
)

const (
	RateModelSchema          = "nevr.fly.rate-model/v1"
	DecoderPolicySchema      = "nevr.fly.decoder-policy/v1"
	NominalReplayStepSeconds = 1.0 / 15.0
)

// Dynamics names the deliberately simple, rate-based approximation used by
// the first prototype. These parameters are engineering assumptions; they are
// not measurements from the source fly.
type Dynamics struct {
	TauSeconds    float64 `json:"tau_seconds"`
	InputGain     float64 `json:"input_gain"`
	RecurrentGain float64 `json:"recurrent_gain"`
	MaxStep       float64 `json:"max_step"`
}

// DefaultDynamics returns stable, deterministic values for replay inference.
func DefaultDynamics() Dynamics {
	return Dynamics{TauSeconds: 0.12, InputGain: 1.6, RecurrentGain: 1.0, MaxStep: 0.25}
}

type compiledEdge struct {
	pre, post int
	weight    float64
}

// Network is a deterministic leaky-rate network over a frozen topology.
// Incoming absolute connection strength is normalized per post-synaptic node,
// which keeps large fan-in populations bounded without altering edge signs.
type Network struct {
	dataset         DatasetMetadata
	dynamics        Dynamics
	topologyDigest  string
	modelDigest     string
	agentContractOK bool
	state           []float64
	next            []float64
	drive           []float64
	bias            []float64
	edges           []compiledEdge
	inputs          map[string][]int
	outputs         map[string][]int
}

// NewNetwork validates and compiles a topology into deterministic index order.
func NewNetwork(topology *Topology, dynamics Dynamics) (*Network, error) {
	if err := topology.Validate(); err != nil {
		return nil, err
	}
	if !finite(dynamics.TauSeconds) || dynamics.TauSeconds <= 0 ||
		!finite(dynamics.InputGain) || dynamics.InputGain < 0 ||
		!finite(dynamics.RecurrentGain) || dynamics.RecurrentGain < 0 ||
		!finite(dynamics.MaxStep) || dynamics.MaxStep <= 0 {
		return nil, errors.New("network dynamics must be finite and non-negative, with positive tau and max step")
	}
	topologyDigest, err := canonicalTopologyDigest(topology)
	if err != nil {
		return nil, fmt.Errorf("digest topology: %w", err)
	}
	modelDigest, err := canonicalModelDigest(topologyDigest, dynamics)
	if err != nil {
		return nil, fmt.Errorf("digest model: %w", err)
	}

	nodes := append([]TopologyNode(nil), topology.Nodes...)
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	index := make(map[string]int, len(topology.Nodes))
	bias := make([]float64, len(topology.Nodes))
	for i, node := range nodes {
		index[node.ID] = i
		bias[i] = node.Bias
	}
	edges := make([]compiledEdge, len(topology.Edges))
	for i, edge := range topology.Edges {
		pre, post := index[edge.Pre], index[edge.Post]
		edges[i] = compiledEdge{pre: pre, post: post, weight: edge.Weight}
	}
	// Sort before summing incoming weights. Floating-point addition is not
	// associative, so normalizing before this sort would make a topology's
	// behavior depend on its source table row order.
	sort.SliceStable(edges, func(i, j int) bool {
		if edges[i].post != edges[j].post {
			return edges[i].post < edges[j].post
		}
		if edges[i].pre != edges[j].pre {
			return edges[i].pre < edges[j].pre
		}
		return edges[i].weight < edges[j].weight
	})
	incoming := make([]float64, len(topology.Nodes))
	for _, edge := range edges {
		incoming[edge.post] += math.Abs(edge.weight)
		if !finite(incoming[edge.post]) {
			return nil, fmt.Errorf("incoming weight overflows at node %q", nodes[edge.post].ID)
		}
	}
	for i := range edges {
		denominator := math.Max(1, incoming[edges[i].post])
		edges[i].weight /= denominator
	}

	compilePopulations := func(source map[string][]string) map[string][]int {
		result := make(map[string][]int, len(source))
		for name, members := range source {
			compiled := make([]int, len(members))
			for i, id := range members {
				compiled[i] = index[id]
			}
			sort.Ints(compiled)
			result[name] = compiled
		}
		return result
	}
	n := &Network{
		dataset: topology.Dataset, dynamics: dynamics, topologyDigest: topologyDigest,
		modelDigest: modelDigest, agentContractOK: ValidateAgentContract(topology) == nil,
		bias: bias, edges: edges,
		state: make([]float64, len(topology.Nodes)), next: make([]float64, len(topology.Nodes)),
		drive: make([]float64, len(topology.Nodes)), inputs: compilePopulations(topology.Inputs),
		outputs: compilePopulations(topology.Outputs),
	}
	return n, nil
}

// Reset clears all activity while retaining the frozen topology.
func (n *Network) Reset() {
	clear(n.state)
	clear(n.next)
	clear(n.drive)
}

// Step advances the network by dt seconds. Unknown sensory channels are
// ignored so a newer encoder can remain compatible with an older topology.
// A telemetry gap larger than MaxStep clears recurrent state before applying
// one bounded step, preventing an old action state from surviving a dropout.
func (n *Network) Step(dt float64, currents map[string]float64) error {
	if n == nil {
		return errors.New("network is nil")
	}
	appliedDeltaTime, gapReset, err := n.AppliedDeltaTime(dt)
	if err != nil {
		return err
	}
	channels := make([]string, 0, len(currents))
	for channel := range currents {
		channels = append(channels, channel)
	}
	sort.Strings(channels)
	for _, channel := range channels {
		current := currents[channel]
		if !finite(current) {
			return fmt.Errorf("sensory current %q is non-finite", channel)
		}
	}
	if gapReset {
		n.Reset()
	}
	copy(n.drive, n.bias)
	for _, edge := range n.edges {
		n.drive[edge.post] += n.dynamics.RecurrentGain * edge.weight * n.state[edge.pre]
	}
	for _, channel := range channels {
		current := currents[channel]
		members := n.inputs[channel]
		if len(members) == 0 {
			continue
		}
		current = clamp(current, 0, 1) * n.dynamics.InputGain
		if !finite(current) {
			return fmt.Errorf("sensory drive %q overflows", channel)
		}
		for _, node := range members {
			n.drive[node] += current
			if !finite(n.drive[node]) {
				return fmt.Errorf("network drive overflows at input %q", channel)
			}
		}
	}
	alpha := 1 - math.Exp(-appliedDeltaTime/n.dynamics.TauSeconds)
	for i, drive := range n.drive {
		if !finite(drive) {
			return fmt.Errorf("network drive is non-finite at node %d", i)
		}
		target := clamp(drive, 0, 1)
		n.next[i] = clamp(n.state[i]+alpha*(target-n.state[i]), 0, 1)
		if !finite(n.next[i]) {
			return fmt.Errorf("network state is non-finite at node %d", i)
		}
	}
	n.state, n.next = n.next, n.state
	return nil
}

// AppliedDeltaTime reports the bounded integration interval for a raw sample.
// A zero/first sample and a post-gap reacquisition both receive one nominal
// replay tick; the latter also requests a recurrent-state reset.
func (n *Network) AppliedDeltaTime(dt float64) (applied float64, gapReset bool, err error) {
	if n == nil {
		return 0, false, errors.New("network is nil")
	}
	if !finite(dt) || dt < 0 {
		return 0, false, fmt.Errorf("network step is invalid: %v", dt)
	}
	if dt == 0 {
		return NominalReplayStepSeconds, false, nil
	}
	if dt > n.dynamics.MaxStep {
		return NominalReplayStepSeconds, true, nil
	}
	return dt, false, nil
}

// Output returns mean activity for a named motor population.
func (n *Network) Output(name string) float64 {
	if n == nil {
		return 0
	}
	members := n.outputs[name]
	if len(members) == 0 {
		return 0
	}
	var total float64
	for _, node := range members {
		total += n.state[node]
	}
	return total / float64(len(members))
}

// StateDigest is a reproducible fingerprint for regression checks and replay
// checkpoint comparisons; it is not a scientific validation metric.
func (n *Network) StateDigest() string {
	if n == nil {
		return ""
	}
	h := sha256.New()
	_, _ = h.Write([]byte(n.modelDigest))
	var buf [8]byte
	for _, value := range n.state {
		binary.LittleEndian.PutUint64(buf[:], math.Float64bits(value))
		_, _ = h.Write(buf[:])
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// Dataset returns the provenance attached to the frozen topology.
func (n *Network) Dataset() DatasetMetadata {
	if n == nil {
		return DatasetMetadata{}
	}
	return n.dataset
}

// Dynamics returns the immutable integration parameters for this network.
func (n *Network) Dynamics() Dynamics {
	if n == nil {
		return Dynamics{}
	}
	return n.dynamics
}

// TopologyDigest identifies the canonical graph, populations, and dataset
// metadata. Semantically irrelevant node, edge, map, and member ordering is
// removed before hashing.
func (n *Network) TopologyDigest() string {
	if n == nil {
		return ""
	}
	return n.topologyDigest
}

// ModelDigest also commits to the dynamics and observation/action interfaces.
func (n *Network) ModelDigest() string {
	if n == nil {
		return ""
	}
	return n.modelDigest
}

func (n *Network) agentCompatible() bool { return n != nil && n.agentContractOK }

func canonicalTopologyDigest(topology *Topology) (string, error) {
	canonical := Topology{
		Schema: topology.Schema, Dataset: topology.Dataset,
		Nodes:  append([]TopologyNode(nil), topology.Nodes...),
		Edges:  append([]TopologyEdge(nil), topology.Edges...),
		Inputs: canonicalPopulations(topology.Inputs), Outputs: canonicalPopulations(topology.Outputs),
	}
	sort.Slice(canonical.Nodes, func(i, j int) bool { return canonical.Nodes[i].ID < canonical.Nodes[j].ID })
	sort.Slice(canonical.Edges, func(i, j int) bool {
		left, right := canonical.Edges[i], canonical.Edges[j]
		if left.Post != right.Post {
			return left.Post < right.Post
		}
		if left.Pre != right.Pre {
			return left.Pre < right.Pre
		}
		return left.Weight < right.Weight
	})
	blob, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(blob)
	return fmt.Sprintf("%x", digest), nil
}

func canonicalPopulations(source map[string][]string) map[string][]string {
	result := make(map[string][]string, len(source))
	for name, members := range source {
		copyMembers := append([]string(nil), members...)
		sort.Strings(copyMembers)
		result[name] = copyMembers
	}
	return result
}

func canonicalModelDigest(topologyDigest string, dynamics Dynamics) (string, error) {
	payload := struct {
		TopologyDigest      string   `json:"topology_digest"`
		Dynamics            Dynamics `json:"dynamics"`
		ObservationSchema   string   `json:"observation_schema"`
		ActionSchema        string   `json:"action_schema"`
		RateModelSchema     string   `json:"rate_model_schema"`
		DecoderPolicySchema string   `json:"decoder_policy_schema"`
	}{topologyDigest, dynamics, ObservationSchema, ActionSchema, RateModelSchema, DecoderPolicySchema}
	blob, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(blob)
	return fmt.Sprintf("%x", digest), nil
}

func clamp(value, minimum, maximum float64) float64 {
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}
