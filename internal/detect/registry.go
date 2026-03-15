package detect

// Registry holds all registered detectors.
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

// Register adds a detector to the registry.
func (r *Registry) Register(d Detector) {
	r.detectors = append(r.detectors, d)
	r.byID[d.ID()] = d
}

// Get returns a detector by ID.
func (r *Registry) Get(id string) (Detector, bool) {
	d, ok := r.byID[id]
	return d, ok
}

// All returns all registered detectors.
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
