package movement

import "github.com/nevr-anticheat/nevr-anticheat/internal/detect"

// Register the movement detectors in the process-wide catalog (detect.Catalog).
func init() {
	detect.MustRegister(func(p map[string]any) detect.Detector { return NewMov001(p) })
	detect.MustRegister(func(p map[string]any) detect.Detector { return NewMov002(p) })
	detect.MustRegister(func(p map[string]any) detect.Detector { return NewMov003(p) })
	detect.MustRegister(func(p map[string]any) detect.Detector { return NewMov004(p) })
	detect.MustRegister(func(p map[string]any) detect.Detector { return NewMov005(p) })
	detect.MustRegister(func(p map[string]any) detect.Detector { return NewMov006(p) })
}
