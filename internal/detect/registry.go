package detect

import (
	"fmt"
	"sort"
	"sync"
)

// Constructor builds a detector from its params map (nil is allowed).
type Constructor func(params map[string]any) Detector

// Descriptor is one catalog entry: the identity a detector reports about
// itself plus the constructor that produces it. Identity fields are read
// from a prototype built with nil params, so the catalog cannot drift from
// what the detector actually declares.
type Descriptor struct {
	ID          string
	Version     string
	Name        string
	Category    string
	Weight      float64 // constructor default; config enforcement_weight overrides via SetWeight
	AutoEnforce bool
	New         Constructor
}

var (
	catalogMu sync.RWMutex
	catalog   = map[string]Descriptor{}
)

// RegisterConstructor adds a detector constructor to the process-wide
// catalog. It is called from the init() of each detector sub-package, so
// importing a sub-package (which every binary and the test harness already
// does) is what makes its detectors visible to Catalog/BuildAll.
// Duplicate IDs are rejected: the same detector must never run twice.
func RegisterConstructor(newFn Constructor) error {
	if newFn == nil {
		return fmt.Errorf("detect: nil constructor")
	}
	proto := newFn(nil)
	if proto == nil {
		return fmt.Errorf("detect: constructor returned nil detector")
	}
	id := proto.ID()
	if id == "" {
		return fmt.Errorf("detect: constructor produced a detector with an empty ID")
	}
	desc := Descriptor{
		ID:          id,
		Version:     proto.Version(),
		Name:        proto.Name(),
		Category:    proto.Category(),
		Weight:      proto.DefaultEnforcementWeight(),
		AutoEnforce: proto.AutoEnforce(),
		New:         newFn,
	}
	catalogMu.Lock()
	defer catalogMu.Unlock()
	if _, dup := catalog[id]; dup {
		return fmt.Errorf("detect: detector %s registered twice", id)
	}
	catalog[id] = desc
	return nil
}

// MustRegister is RegisterConstructor for package init(): a duplicate or
// malformed registration is a programming error and panics.
func MustRegister(newFn Constructor) {
	if err := RegisterConstructor(newFn); err != nil {
		panic(err)
	}
}

// Catalog returns every registered descriptor sorted by ID.
func Catalog() []Descriptor {
	catalogMu.RLock()
	defer catalogMu.RUnlock()
	out := make([]Descriptor, 0, len(catalog))
	for _, d := range catalog {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Lookup returns the descriptor for a detector ID.
func Lookup(id string) (Descriptor, bool) {
	catalogMu.RLock()
	defer catalogMu.RUnlock()
	d, ok := catalog[id]
	return d, ok
}

// Build constructs one detector by ID with the given params.
func Build(id string, params map[string]any) (Detector, error) {
	desc, ok := Lookup(id)
	if !ok {
		return nil, fmt.Errorf("detect: unknown detector %s", id)
	}
	if params == nil {
		params = map[string]any{}
	}
	d := desc.New(params)
	if d == nil {
		return nil, fmt.Errorf("detect: constructor for %s returned nil", id)
	}
	return d, nil
}

// BuildOptions carries the per-detector configuration BuildAll needs.
type BuildOptions struct {
	Enabled     bool
	Weight      float64
	HasWeight   bool // apply Weight (config supplied enforcement_weight)
	AutoEnforce bool
	HasAuto     bool // apply AutoEnforce (config supplied auto_enforce)
	Params      map[string]any
}

// WeightSetter is implemented by every detector embedding BaseDetector.
type WeightSetter interface {
	SetWeight(float64)
}

// AutoEnforceSetter is implemented by every detector embedding BaseDetector.
type AutoEnforceSetter interface {
	SetAutoEnforce(bool)
}

// BuildAll constructs every catalogued detector whose options report
// Enabled, in ID order, applying configured weight / auto-enforce through
// the BaseDetector setters. optionsFor is called once per catalog entry; a
// nil function enables everything with constructor defaults. This is the
// single place cmd/anticheat, cmd/server and the test harness should build
// their detector sets from, so the catalogs cannot drift.
func BuildAll(optionsFor func(id string) BuildOptions) []Detector {
	var out []Detector
	for _, desc := range Catalog() {
		opts := BuildOptions{Enabled: true}
		if optionsFor != nil {
			opts = optionsFor(desc.ID)
		}
		if !opts.Enabled {
			continue
		}
		params := opts.Params
		if params == nil {
			params = map[string]any{}
		}
		d := desc.New(params)
		if d == nil {
			continue
		}
		if setter, ok := d.(WeightSetter); ok && opts.HasWeight {
			setter.SetWeight(opts.Weight)
		}
		if setter, ok := d.(AutoEnforceSetter); ok && opts.HasAuto {
			setter.SetAutoEnforce(opts.AutoEnforce)
		}
		out = append(out, d)
	}
	return out
}

// Registry holds an explicit, ordered set of detector instances (a
// per-match working set). Register rejects duplicate IDs.
type Registry struct {
	detectors []Detector
	byID      map[string]Detector
}

// NewRegistry creates an empty detector registry.
func NewRegistry() *Registry {
	return &Registry{
		byID: make(map[string]Detector),
	}
}

// Register adds a detector to the registry. A detector whose ID is already
// present is rejected so it can never run twice per frame.
func (r *Registry) Register(d Detector) error {
	if d == nil {
		return fmt.Errorf("detect: nil detector")
	}
	id := d.ID()
	if _, dup := r.byID[id]; dup {
		return fmt.Errorf("detect: detector %s already registered", id)
	}
	r.detectors = append(r.detectors, d)
	r.byID[id] = d
	return nil
}

// Get returns a detector by ID.
func (r *Registry) Get(id string) (Detector, bool) {
	d, ok := r.byID[id]
	return d, ok
}

// All returns all registered detectors in registration order.
func (r *Registry) All() []Detector {
	return r.detectors
}

// Enabled returns detectors whose IDs are in the enabled set.
func (r *Registry) Enabled(enabledIDs map[string]bool) []Detector {
	var out []Detector
	for _, d := range r.detectors {
		if enabledIDs[d.ID()] {
			out = append(out, d)
		}
	}
	return out
}

// Categories returns detectors grouped by category.
func (r *Registry) Categories() map[string][]Detector {
	cats := make(map[string][]Detector)
	for _, d := range r.detectors {
		cats[d.Category()] = append(cats[d.Category()], d)
	}
	return cats
}

// Count returns the number of registered detectors.
func (r *Registry) Count() int {
	return len(r.detectors)
}
