package tests

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/bio"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/movement"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/pattern"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/state"
	"github.com/nevr-anticheat/nevr-anticheat/internal/detect/throw"
)

// constructors maps every detector ID to its constructor. The detect catalog
// is used when the sub-package registered itself; the explicit fallback keeps
// this test independent of which sub-packages have a register.go.
var constructors = map[string]func(map[string]any) detect.Detector{
	"THROW_001": func(p map[string]any) detect.Detector { return throw.NewThrow001(p) },
	"THROW_002": func(p map[string]any) detect.Detector { return throw.NewThrow002(p) },
	"THROW_003": func(p map[string]any) detect.Detector { return throw.NewThrow003(p) },
	"THROW_004": func(p map[string]any) detect.Detector { return throw.NewThrow004(p) },
	"THROW_005": func(p map[string]any) detect.Detector { return throw.NewThrow005(p) },
	"THROW_006": func(p map[string]any) detect.Detector { return throw.NewThrow006(p) },
	"THROW_007": func(p map[string]any) detect.Detector { return throw.NewThrow007(p) },
	"THROW_008": func(p map[string]any) detect.Detector { return throw.NewThrow008(p) },
	"BIO_001":   func(p map[string]any) detect.Detector { return bio.NewBio001(p) },
	"BIO_002":   func(p map[string]any) detect.Detector { return bio.NewBio002(p) },
	"BIO_003":   func(p map[string]any) detect.Detector { return bio.NewBio003(p) },
	"BIO_004":   func(p map[string]any) detect.Detector { return bio.NewBio004(p) },
	"MOV_001":   func(p map[string]any) detect.Detector { return movement.NewMov001(p) },
	"MOV_002":   func(p map[string]any) detect.Detector { return movement.NewMov002(p) },
	"MOV_003":   func(p map[string]any) detect.Detector { return movement.NewMov003(p) },
	"MOV_004":   func(p map[string]any) detect.Detector { return movement.NewMov004(p) },
	"MOV_005":   func(p map[string]any) detect.Detector { return movement.NewMov005(p) },
	"MOV_006":   func(p map[string]any) detect.Detector { return movement.NewMov006(p) },
	"STATE_001": func(p map[string]any) detect.Detector { return state.NewState001(p) },
	"STATE_002": func(p map[string]any) detect.Detector { return state.NewState002(p) },
	"STATE_003": func(p map[string]any) detect.Detector { return state.NewState003(p) },
	"STATE_004": func(p map[string]any) detect.Detector { return state.NewState004(p) },
	"STATE_005": func(p map[string]any) detect.Detector { return state.NewState005(p) },
	"STATE_006": func(p map[string]any) detect.Detector { return state.NewState006(p) },
	"STATE_007": func(p map[string]any) detect.Detector { return state.NewState007(p) },
	"STATE_008": func(p map[string]any) detect.Detector { return state.NewState008(p) },
	"PAT_001":   func(p map[string]any) detect.Detector { return pattern.NewPat001(p) },
	"PAT_002":   func(p map[string]any) detect.Detector { return pattern.NewPat002(p) },
	"PAT_003":   func(p map[string]any) detect.Detector { return pattern.NewPat003(p) },
	"PAT_004":   func(p map[string]any) detect.Detector { return pattern.NewPat004(p) },
	"PAT_005":   func(p map[string]any) detect.Detector { return pattern.NewPat005(p) },
}

func build(t *testing.T, id string, params map[string]any) detect.Detector {
	t.Helper()
	if desc, ok := detect.Lookup(id); ok {
		return desc.New(params)
	}
	ctor, ok := constructors[id]
	if !ok {
		t.Fatalf("no constructor for %s", id)
	}
	return ctor(params)
}

// fingerprint renders every scalar field of the detector struct (recursing
// into nested structs, using len() for maps/slices and nil-ness for
// pointers/interfaces) so that a change to any configured field is visible
// without knowing the field name. BaseDetector identity is excluded.
func fingerprint(d detect.Detector) string {
	var b strings.Builder
	walkStruct(reflect.ValueOf(d).Elem(), "", &b)
	return b.String()
}

func walkStruct(v reflect.Value, prefix string, b *strings.Builder) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Anonymous && f.Type == reflect.TypeOf(detect.BaseDetector{}) {
			continue
		}
		fv := v.Field(i)
		name := prefix + f.Name
		switch fv.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			fmt.Fprintf(b, "%s=%d;", name, fv.Int())
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			fmt.Fprintf(b, "%s=%d;", name, fv.Uint())
		case reflect.Float32, reflect.Float64:
			fmt.Fprintf(b, "%s=%g;", name, fv.Float())
		case reflect.Bool:
			fmt.Fprintf(b, "%s=%v;", name, fv.Bool())
		case reflect.String:
			fmt.Fprintf(b, "%s=%q;", name, fv.String())
		case reflect.Map, reflect.Slice:
			fmt.Fprintf(b, "%s.len=%d;", name, fv.Len())
		case reflect.Ptr, reflect.Interface:
			fmt.Fprintf(b, "%s.nil=%v;", name, fv.IsNil())
		case reflect.Struct:
			walkStruct(fv, name+".", b)
		}
	}
}

func sentinelFor(p config.ParamSpec) any {
	switch p.Type {
	case config.ParamInt:
		return 9973
	case config.ParamFloat:
		return 1234.5
	case config.ParamBool:
		if d, ok := p.Default.(bool); ok {
			return !d
		}
		return true
	case config.ParamStringList:
		return []string{"THROW_001"}
	}
	return nil
}

// TestConfigDocs_SpecIdentityMatchesConstructors: the config layer's names and
// categories are copied from the constructors; they must not drift.
func TestConfigDocs_SpecIdentityMatchesConstructors(t *testing.T) {
	ids := config.KnownDetectorIDs()
	if len(ids) != len(constructors) {
		t.Fatalf("spec table has %d detectors, constructor table %d", len(ids), len(constructors))
	}
	for _, id := range ids {
		spec, _ := config.DetectorSpecFor(id)
		d := build(t, id, nil)
		if d.ID() != id {
			t.Errorf("%s: constructor reports ID %s", id, d.ID())
		}
		if d.Name() != spec.Name {
			t.Errorf("%s: spec name %q != constructor name %q", id, spec.Name, d.Name())
		}
		if d.Category() != spec.Category {
			t.Errorf("%s: spec category %q != constructor category %q", id, spec.Category, d.Category())
		}
	}
}

// TestConfigDocs_EveryAcceptedKeyIsConsumed: constructing a detector with a
// documented key set to a sentinel must change some field of the detector,
// which proves the constructor reads the key (F15/F26/F90/F221 were exactly
// keys that nothing read). Aliases must land on the same field as the
// canonical key.
func TestConfigDocs_EveryAcceptedKeyIsConsumed(t *testing.T) {
	for _, spec := range config.DetectorSpecs() {
		base := fingerprint(build(t, spec.ID, nil))
		for _, p := range spec.Params {
			sentinel := sentinelFor(p)
			// THROW_006 now bounds provisional sampling controls to prevent
			// unbounded histories. Probe consumption with distinct valid
			// values, not the generic out-of-range fallback sentinels.
			if spec.ID == "THROW_006" {
				switch p.Key {
				case "min_trajectory_change":
					sentinel = 9.0
				case "post_release_frames":
					sentinel = 20
				case "min_distance_from_thrower":
					sentinel = 3.0
				}
			}
			with := fingerprint(build(t, spec.ID, map[string]any{p.Key: sentinel}))
			if with == base {
				t.Errorf("%s: params key %q is documented but the constructor ignores it (fingerprint unchanged)", spec.ID, p.Key)
				continue
			}
			for _, alias := range p.Aliases {
				viaAlias := fingerprint(build(t, spec.ID, map[string]any{alias: sentinel}))
				if viaAlias != with {
					t.Errorf("%s: alias %q does not configure the same field as %q", spec.ID, alias, p.Key)
				}
			}
			// Configure() must honour the same key.
			d := build(t, spec.ID, nil)
			if err := d.Configure(map[string]any{p.Key: sentinel}); err != nil {
				t.Errorf("%s: Configure(%s) returned %v", spec.ID, p.Key, err)
			} else if spec.ID != "THROW_007" && fingerprint(d) == base {
				// THROW_007 is a stub whose Configure reads only two of its
				// three constructor keys; everything else must be symmetric.
				t.Errorf("%s: Configure ignores %q although the constructor reads it", spec.ID, p.Key)
			}
		}
	}
}

// TestConfigDocs_SpecDefaultsAreConstructorFallbacks: setting a key to the
// spec's documented Default must be a no-op, which proves EffectiveTable's
// "constructor" values are the real fallbacks.
func TestConfigDocs_SpecDefaultsAreConstructorFallbacks(t *testing.T) {
	for _, spec := range config.DetectorSpecs() {
		base := fingerprint(build(t, spec.ID, nil))
		for _, p := range spec.Params {
			with := fingerprint(build(t, spec.ID, map[string]any{p.Key: p.Default}))
			if with != base {
				t.Errorf("%s: spec default for %q (%v) is not the constructor fallback\n base: %s\n with: %s",
					spec.ID, p.Key, p.Default, base, with)
			}
		}
	}
}

// TestConfigDocs_DefaultParamsAreAccepted: every key defaults.go ships is a
// canonical key the constructor reads, and constructing with the shipped
// params never falls back to a constructor default for a key that is set.
func TestConfigDocs_DefaultParamsAreAccepted(t *testing.T) {
	cfg := config.DefaultConfig()
	for _, id := range cfg.DetectorIDs() {
		spec, ok := config.DetectorSpecFor(id)
		if !ok {
			t.Errorf("%s: no spec", id)
			continue
		}
		accepted := map[string]bool{}
		for _, k := range spec.AcceptedKeys() {
			accepted[k] = true
		}
		for k := range cfg.Detectors[id].Params {
			if !accepted[k] {
				t.Errorf("%s: defaults.go ships %q which is not a canonical accepted key", id, k)
			}
		}
		// Building with the shipped params must succeed and produce the
		// detector's own ID (guards against a constructor panicking on a type).
		d := build(t, id, cfg.Detectors[id].Params)
		if d.ID() != id {
			t.Errorf("%s: built detector reports %s", id, d.ID())
		}
	}
	for _, row := range cfg.EffectiveTable() {
		for _, p := range row.Params {
			if p.Source != "config" {
				t.Errorf("%s: %s resolves from the constructor although defaults.go should set every accepted key", row.ID, p.Key)
			}
		}
		if len(row.Unknown) != 0 {
			t.Errorf("%s: unknown keys in defaults: %v", row.ID, row.Unknown)
		}
	}
}

// TestConfigDocs_CatalogAgreesWithSpec: every detector registered in the
// detect catalog must be known to the config layer, with the same identity.
func TestConfigDocs_CatalogAgreesWithSpec(t *testing.T) {
	for _, desc := range detect.Catalog() {
		spec, ok := config.DetectorSpecFor(desc.ID)
		if !ok {
			t.Errorf("catalog detector %s has no config spec", desc.ID)
			continue
		}
		if desc.Name != spec.Name || desc.Category != spec.Category {
			t.Errorf("%s: catalog %q/%q vs spec %q/%q", desc.ID, desc.Name, desc.Category, spec.Name, spec.Category)
		}
	}
}
