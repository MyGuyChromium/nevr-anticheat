package state

import "github.com/nevr-anticheat/nevr-anticheat/internal/detect"

// Register the state detectors in the process-wide catalog (detect.Catalog).
func init() {
	detect.MustRegister(func(p map[string]any) detect.Detector { return NewState001(p) })
	detect.MustRegister(func(p map[string]any) detect.Detector { return NewState002(p) })
	detect.MustRegister(func(p map[string]any) detect.Detector { return NewState003(p) })
	detect.MustRegister(func(p map[string]any) detect.Detector { return NewState004(p) })
	detect.MustRegister(func(p map[string]any) detect.Detector { return NewState005(p) })
	detect.MustRegister(func(p map[string]any) detect.Detector { return NewState006(p) })
	detect.MustRegister(func(p map[string]any) detect.Detector { return NewState007(p) })
}
