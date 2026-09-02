package bio

import "github.com/nevr-anticheat/nevr-anticheat/internal/detect"

// Register the bio detectors in the process-wide catalog (detect.Catalog).
func init() {
	detect.MustRegister(func(p map[string]any) detect.Detector { return NewBio001(p) })
	detect.MustRegister(func(p map[string]any) detect.Detector { return NewBio002(p) })
	detect.MustRegister(func(p map[string]any) detect.Detector { return NewBio003(p) })
	detect.MustRegister(func(p map[string]any) detect.Detector { return NewBio004(p) })
}
