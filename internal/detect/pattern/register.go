package pattern

import "github.com/nevr-anticheat/nevr-anticheat/internal/detect"

// Register the pattern detectors in the process-wide catalog (detect.Catalog).
func init() {
	detect.MustRegister(func(p map[string]any) detect.Detector { return NewPat001(p) })
	detect.MustRegister(func(p map[string]any) detect.Detector { return NewPat002(p) })
	detect.MustRegister(func(p map[string]any) detect.Detector { return NewPat003(p) })
	detect.MustRegister(func(p map[string]any) detect.Detector { return NewPat004(p) })
	detect.MustRegister(func(p map[string]any) detect.Detector { return NewPat005(p) })
}
