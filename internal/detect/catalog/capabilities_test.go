package catalog

import (
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"testing"
)

func TestEveryProductionDetectorHasFailClosedContract(t *testing.T) {
	entries := detect.Catalog()
	if len(entries) == 0 {
		t.Fatal("empty production catalog")
	}
	for _, d := range entries {
		c := detect.CapabilityFor(d.ID)
		if c == nil {
			t.Fatalf("missing capability for %s", d.ID)
		}
		if c.Version == "" || c.RuleID != d.ID || len(c.RequiredInputs) == 0 || c.SourceTrust == "" || c.Timing == "" || c.Validity == "" || c.ApplicableBuild == "" || c.MissingBehavior == "" || len(c.EnforcementRequires) == 0 {
			t.Fatalf("incomplete contract %s: %+v", d.ID, c)
		}
		if c.EnforcementEligible {
			t.Fatalf("unverified production detector promoted: %s", d.ID)
		}
		if c.Rule == nil || c.Rule.Version == "" || c.Rule.Meaning == "" || c.Rule.Units == "" || c.Rule.Provenance == "" || c.Rule.Applicability == "" || c.Rule.Exceptions == "" || len(c.Rule.Tests) == 0 {
			t.Fatalf("missing versioned rule definition for %s: %+v", d.ID, c.Rule)
		}
		c.RequiredInputs[0] = "mutated"
		if detect.CapabilityFor(d.ID).RequiredInputs[0] == "mutated" {
			t.Fatal("mutable contract leaked across calls")
		}
	}
}
