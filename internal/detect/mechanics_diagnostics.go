package detect

import "github.com/nevr-anticheat/nevr-anticheat/internal/model"

// MechanicsObserver receives an assessment for an observed event, never a
// scored detection. Collectors must clone retained evidence. Nil detaches.
type MechanicsObserver func(detectorID, playerID string, record model.MechanicsAssessment)

type MechanicsObservable interface {
	SetMechanicsObserver(MechanicsObserver)
}
