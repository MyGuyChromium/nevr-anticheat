package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/nevr-anticheat/nevr-anticheat/internal/adapter"
	"github.com/nevr-anticheat/nevr-anticheat/internal/replay"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

// storedTelemetryHealth returns the persisted telemetry health of a match. A
// match analyzed before the document existed (or whose document is unreadable)
// is rebuilt once from its raw ticks and stored, so later views never touch
// raw ticks again.
func (s *server) storedTelemetryHealth(ctx context.Context, matchID string) (*replay.TelemetryHealthDoc, error) {
	store := s.engine.Store()
	raw, _, err := store.GetMatchTelemetryHealth(ctx, matchID)
	if err == nil {
		doc, decodeErr := replay.DecodeTelemetryHealth(raw)
		if decodeErr == nil {
			return doc, nil
		}
		// Unknown schema or damaged JSON: derived data, rebuild it below.
		if deleteErr := store.DeleteMatchTelemetryHealth(ctx, matchID); deleteErr != nil {
			return nil, errors.Join(decodeErr, deleteErr)
		}
	} else if !errors.Is(err, sqlite.ErrNotFound) {
		return nil, err
	}
	return s.backfillTelemetryHealth(ctx, matchID)
}

func (s *server) backfillTelemetryHealth(ctx context.Context, matchID string) (*replay.TelemetryHealthDoc, error) {
	value, err := s.runDetached(ctx, "telemetry-health\x00"+matchID, func(workCtx context.Context) (any, error) {
		doc, err := rebuildTelemetryHealth(workCtx, s.engine.Store(), matchID)
		if err != nil {
			s.engine.Logger().Warn("telemetry health backfill failed", "match_id", matchID, "error", err)
		}
		return doc, err
	})
	if err != nil {
		return nil, err
	}
	doc, _ := value.(*replay.TelemetryHealthDoc)
	return doc, nil
}

// rebuildTelemetryHealth re-derives field presence from the stored raw ticks
// of one match, streaming them so a full match is never held in memory, and
// stores the result unless an analysis wrote its own document meanwhile.
func rebuildTelemetryHealth(ctx context.Context, store *sqlite.Store, matchID string) (*replay.TelemetryHealthDoc, error) {
	diag := adapter.NewDiagnosticReport()
	ticks, err := store.ForEachMatchTick(ctx, matchID, func(_ int, raw string) error {
		if _, err := diag.RecordSessionJSON([]byte(raw)); err != nil {
			diag.FramesRejected++
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reading raw ticks: %w", err)
	}
	if ticks == 0 {
		diag = nil // nothing to inspect: legacy JSON replay or archived raw ticks
	}
	encoded, err := replay.EncodeTelemetryHealth(diag, sqlite.TelemetryHealthSourceBackfill, 1)
	if err != nil {
		return nil, err
	}
	written, err := store.StoreMatchTelemetryHealthIfAbsent(ctx, matchID, sqlite.TelemetryHealthSourceBackfill, encoded)
	if err != nil {
		return nil, fmt.Errorf("storing telemetry health: %w", err)
	}
	if !written {
		if current, _, getErr := store.GetMatchTelemetryHealth(ctx, matchID); getErr == nil {
			if doc, decodeErr := replay.DecodeTelemetryHealth(current); decodeErr == nil {
				return doc, nil
			}
		}
	}
	return replay.DecodeTelemetryHealth(encoded)
}

// applyStoredTelemetryHealth fills the telemetry sections of a stored match
// view from the persisted document. Mapping diagnostics are only shown for a
// document the analysis wrote: a backfilled report never saw the mapper, and
// its zero counters would read as "nothing was rejected".
func applyStoredTelemetryHealth(mv *matchView, doc *replay.TelemetryHealthDoc) {
	if doc == nil || doc.Report == nil {
		return
	}
	mv.TelemetryHealth = telemetryHealth(doc.Report)
	if mv.TelemetryHealth != nil {
		mv.TelemetryHealth.Source, mv.TelemetryHealth.Scope = doc.Source, doc.Scope
	}
	if doc.Source == sqlite.TelemetryHealthSourceAnalysis {
		mv.Diagnostics = diagnosticsView(doc.Report)
	}
}
