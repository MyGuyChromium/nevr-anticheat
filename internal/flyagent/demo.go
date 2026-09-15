package flyagent

import (
	_ "embed"
	"strings"
)

//go:embed testdata/demo_topology.json
var demoTopologyJSON string

// DemoTopology is a tiny synthetic circuit for end-to-end plumbing tests. It
// is intentionally not presented as MaleCNS data or as a useful game policy.
func DemoTopology() (*Topology, error) {
	return LoadTopology(strings.NewReader(demoTopologyJSON))
}
