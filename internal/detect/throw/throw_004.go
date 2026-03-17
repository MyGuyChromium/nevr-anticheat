package throw

import (
	"fmt"
	"math"

	"github.com/nevr-anticheat/nevr-anticheat/internal/detect"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

type signatureVec struct {
	values     []float64
	frameIndex int
}

type Throw004 struct {
	detect.BaseDetector
	minThrows          int
	minGenVariance     float64
	signatures         map[string][]signatureVec
}

func NewThrow004(params map[string]any) *Throw004 {
	return &Throw004{
		BaseDetector: detect.BaseDetector{
			DetectorID: "THROW_004", DetectorVersion: "1.0.0",
			DetectorName: "Repeated Release Signatures", DetectorCategory: "throw",
			Inputs: []string{"throw_event"}, Warmup: 5, Weight: 0.6,
		},
		minThrows:      detect.GetInt(params, "min_throws", 12),
		minGenVariance: detect.GetFloat(params, "min_generalized_variance", 1e-8),
		signatures:     make(map[string][]signatureVec),
	}
}

func (d *Throw004) Reset() { d.signatures = make(map[string][]signatureVec) }
func (d *Throw004) Configure(params map[string]any) error {
	d.minThrows = detect.GetInt(params, "min_throws", d.minThrows)
	d.minGenVariance = detect.GetFloat(params, "min_generalized_variance", d.minGenVariance)
	return nil
}

func (d *Throw004) buildSignature(t *model.ThrowEvent) signatureVec {
	maxSpeed := 20.0
	return signatureVec{
		values: []float64{
			t.ReleaseSpeed / maxSpeed,
			t.ReleaseAngle / 180.0,
			t.HandSpeed / 15.0,
			t.WristAngularVelocity / 30.0,
			model.Clamp(t.PossessionDuration/5.0, 0, 1),
			t.HandToDiscDistance / 1.0,
		},
		frameIndex: t.FrameIndex,
	}
}

func (d *Throw004) Evaluate(matchCtx *model.MatchContext, players map[string]*model.PlayerState, frameIdx int) []model.DetectionEvent {
	var events []model.DetectionEvent
	for _, ps := range players {
		if ps.LastThrow == nil || ps.LastThrow.FrameIndex != frameIdx {
			continue
		}
		sig := d.buildSignature(ps.LastThrow)
		d.signatures[ps.PlayerID] = append(d.signatures[ps.PlayerID], sig)
		sigs := d.signatures[ps.PlayerID]
		if len(sigs) < d.minThrows {
			continue
		}
		// Compute per-dimension variance
		dims := len(sigs[0].values)
		dimVars := make([]float64, dims)
		genVar := 1.0
		for dim := 0; dim < dims; dim++ {
			vals := make([]float64, len(sigs))
			for i, s := range sigs {
				vals[i] = s.values[dim]
			}
			v := model.Variance(vals)
			dimVars[dim] = v
			if v < 1e-15 {
				v = 1e-15
			}
			genVar *= v
		}
		if genVar >= d.minGenVariance {
			continue
		}
		severity := 1.0 - model.SigmoidConfidence(genVar, d.minGenVariance, 100.0)
		throwCountFactor := math.Min(1.0, float64(len(sigs))/float64(d.minThrows*2))
		confidence := severity * throwCountFactor

		ev := d.MakeEvent(matchCtx, ps.PlayerID, frameIdx, ps.LastThrow.Timestamp, severity, confidence,
			model.SignatureRepeatEvidence{
				ThrowCount: len(sigs), GeneralizedVariance: genVar,
				DimensionVariances: dimVars,
				DimensionNames: []string{"speed", "angle", "hand_speed", "wrist_angvel", "poss_dur", "hand_disc_dist"},
			},
			fmt.Sprintf("gen_variance: %.6f over %d throws", genVar, len(sigs)),
			fmt.Sprintf("gen_variance: > %.6f", d.minGenVariance),
			model.CausalKey{PlayerID: ps.PlayerID, FrameStart: sigs[0].frameIndex, FrameEnd: frameIdx, AnomalyType: "throw_signature"},
		)
		events = append(events, ev)
		// Reset signatures after detection to prevent unbounded growth
		d.signatures[ps.PlayerID] = nil
	}
	return events
}
