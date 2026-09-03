package throw

import "github.com/nevr-anticheat/nevr-anticheat/internal/detect"

// Register the throw detectors in the process-wide catalog (detect.Catalog).
// Constructors tolerate nil params, which MustRegister relies on to read the
// prototype's identity.
func init() {
	detect.MustRegister(func(p map[string]any) detect.Detector { return NewThrow001(p) })
	detect.MustRegister(func(p map[string]any) detect.Detector { return NewThrow002(p) })
	detect.MustRegister(func(p map[string]any) detect.Detector { return NewThrow003(p) })
	detect.MustRegister(func(p map[string]any) detect.Detector { return NewThrow004(p) })
	detect.MustRegister(func(p map[string]any) detect.Detector { return NewThrow005(p) })
	detect.MustRegister(func(p map[string]any) detect.Detector { return NewThrow006(p) })
	detect.MustRegister(func(p map[string]any) detect.Detector { return NewThrow007(p) })
	detect.MustRegister(func(p map[string]any) detect.Detector { return NewThrow008(p) })
}
