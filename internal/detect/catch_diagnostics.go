package detect

import "github.com/nevr-anticheat/nevr-anticheat/internal/model"

// CatchObserver receives one finalized diagnostic per explicit free-to-held
// transition, in first-held frame order for each player. Observers must copy
// the record before retaining it; neither party may mutate the other's state.
// A pending transition is finalized on confirmation, interruption, or EOF.
type CatchObserver func(detectorID, playerID string, record model.CatchReviewRecord)

// CatchObservable is optional diagnostic instrumentation. Attaching it cannot
// change event generation, scoring, or enforcement. Nil detaches the callback;
// detector Reset clears pending transition state without emitting diagnostics.
type CatchObservable interface {
	SetCatchObserver(CatchObserver)
}
