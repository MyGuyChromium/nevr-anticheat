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

// Signature dimensions, in order.
var signatureDimensionNames = []string{"speed", "angle", "hand_speed", "wrist_angvel", "poss_dur", "hand_disc_dist"}

const (
	// degenerateVarianceFloor: a dimension whose variance is below this is
	// constant for the player (e.g. wrist angular velocity when the source
	// reports identity hand rotation) and carries no signature information.
	// It is excluded from the product instead of collapsing it.
	degenerateVarianceFloor = 1e-9
	// minInformativeDimensions: fewer informative dimensions than this and
	// the signature is not evaluated at all.
	minInformativeDimensions = 3
	// severityDecades: orders of magnitude below the effective threshold at
	// which severity saturates to 1.
	severityDecades = 4.0
)

// Throw004 detects repeated release signatures (THROW_004).
//
// STATUS: UNSAFE — regrab playstyle produces low generalized variance
// naturally, causing false positives on skilled players. Disabled by default.
//
// The generalized variance is the product of per-dimension variances over
// the informative dimensions only; min_generalized_variance is defined for
// all six dimensions and is rescaled to the number of informative ones
// (threshold^(k/6)) so the per-dimension scale is preserved.
type Throw004 struct {
	detect.BaseDetector
	minThrows      int
	minGenVariance float64
	signatures     map[string][]signatureVec
}

func NewThrow004(params map[string]any) *Throw004 {
	return &Throw004{
		BaseDetector: detect.BaseDetector{
			DetectorID: "THROW_004", DetectorVersion: "1.1.0",
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
	for _, pid := range sortedPlayerIDs(players) {
		ps := players[pid]
		t := throwAt(ps, frameIdx)
		if t == nil {
			continue
		}
		sig := d.buildSignature(t)
		d.signatures[pid] = append(d.signatures[pid], sig)
		if len(d.signatures[pid]) > 50 {
			d.signatures[pid] = d.signatures[pid][len(d.signatures[pid])-50:]
		}
		sigs := d.signatures[pid]
		if len(sigs) < d.minThrows || d.minGenVariance <= 0 {
			continue
		}
		// Per-dimension variance; degenerate (constant) dimensions are
		// reported but excluded from the product.
		dims := len(sigs[0].values)
		dimVars := make([]float64, dims)
		var degenerate []string
		informative := 0
		genVar := 1.0
		for dim := 0; dim < dims; dim++ {
			vals := make([]float64, len(sigs))
			for i, s := range sigs {
				vals[i] = s.values[dim]
			}
			v := model.Variance(vals)
			dimVars[dim] = v
			if v < degenerateVarianceFloor || math.IsNaN(v) {
				degenerate = append(degenerate, signatureDimensionNames[dim])
				continue
			}
			informative++
			genVar *= v
		}
		if informative < minInformativeDimensions {
			continue
		}
		effThreshold := math.Pow(d.minGenVariance, float64(informative)/float64(dims))
		if genVar >= effThreshold {
			continue
		}
		// Severity grows with how many decades below the threshold the
		// signature variance sits (just below -> ~0, 4 decades -> 1).
		severity := model.Clamp01(math.Log10(effThreshold/genVar) / severityDecades)
		throwCountFactor := math.Min(1.0, float64(len(sigs))/float64(d.minThrows*2))
		confidence := severity * throwCountFactor

		ev := d.MakeEvent(matchCtx, pid, frameIdx, t.Timestamp, severity, confidence,
			model.SignatureRepeatEvidence{
				ThrowCount: len(sigs), GeneralizedVariance: genVar,
				DimensionVariances: dimVars, DimensionNames: signatureDimensionNames,
				InformativeDimensions: informative, DegenerateDimensions: degenerate,
				EffectiveThreshold: effThreshold,
			},
			fmt.Sprintf("gen_variance: %.3g over %d throws (%d informative dims)", genVar, len(sigs), informative),
			fmt.Sprintf("gen_variance: > %.3g", effThreshold),
			model.CausalKey{PlayerID: pid, FrameStart: sigs[0].frameIndex, FrameEnd: frameIdx, AnomalyType: "throw_signature"},
		)
		ev.Attribution = &t.Attribution
		events = append(events, ev)
		// Reset signatures after detection to prevent unbounded growth
		d.signatures[pid] = nil
	}
	return events
}
