package detect

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Enumerate the actual literal reasons, including the shared reader's dynamic
// trajectory_ -> release_speed_ conversion, so a newly added branch cannot
// silently regress to the generic UI/export label.
func TestReleaseSpeedBranchDescriptionsCoverEmitters(t *testing.T) {
	reasons := map[string]bool{}
	collect := func(node ast.Node, prefix string) {
		ast.Inspect(node, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(literal.Value)
			if err == nil && strings.HasPrefix(value, prefix) && len(value) > len(prefix) {
				reasons["release_speed_"+strings.TrimPrefix(value, prefix)] = true
			}
			return true
		})
	}
	for _, name := range []string{"throw_001.go", "throw_001_samples.go"} {
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join("throw", name), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		collect(file, "release_speed_")
	}
	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join("throw", "throw_006.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	foundReader := false
	for _, declaration := range file.Decls {
		if function, ok := declaration.(*ast.FuncDecl); ok && function.Name.Name == "readFlightReview" {
			foundReader = true
			collect(function.Body, "trajectory_")
		}
	}
	if !foundReader || len(reasons) < 23 {
		t.Fatalf("reader/reason enumeration incomplete: reader=%v reasons=%v", foundReader, reasons)
	}
	for reason := range reasons {
		if description, found := decisionReasonDescriptions[reason]; !found || description == "" || description == "Additional detector diagnostic" {
			t.Errorf("actual branch %q has no specific description", reason)
		}
	}
	corroborated := DecisionReasonDescription("release_speed_three_samples_above_cap")
	if !strings.Contains(corroborated, "does not prove") || !strings.Contains(corroborated, "sampled speeds") {
		t.Fatalf("sample corroboration was presented as launch/violation proof: %q", corroborated)
	}
	if got := DecisionReasonDescription("unknown-future-diagnostic"); got != "Additional detector diagnostic" {
		t.Fatalf("unknown diagnostic behavior changed: %q", got)
	}
}
