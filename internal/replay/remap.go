package replay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/adapter"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

// StoredTelemetryRemap is a fresh normalized view of a match's immutable raw
// Echo snapshots. MappingErrors counts individual player poses rejected by
// the mapper; the remaining players/ticks are retained exactly as they are on
// initial replay ingestion.
type StoredTelemetryRemap struct {
	Context         *model.MatchContext
	Frames          []model.PlayerTelemetryFrame
	RawTicks        int
	MappingWarnings int
	MappingErrors   int
}

// RemapStoredTelemetry re-runs the current Echo adapter over match_ticks.
// This is the canonical source for reprocessing when it exists: replaying an
// older normalized frame cache would preserve mapper bugs (for example, the
// legacy possession flag created releases many metres from a player's hand).
//
// The raw tick indices must be contiguous from zero. Without that invariant
// the mapper's stateful release and kinematics histories would be labelled
// with the wrong frame indices, so the function fails instead of guessing.
func RemapStoredTelemetry(ctx context.Context, store *sqlite.Store, stored *model.MatchContext, physics model.PhysicsConstants) (*StoredTelemetryRemap, error) {
	if store == nil || stored == nil || stored.MatchID == "" {
		return nil, errors.New("remap stored telemetry: missing store or match context")
	}
	timestamps, err := store.GetMatchTickTimestamps(ctx, stored.MatchID)
	if err != nil {
		return nil, fmt.Errorf("loading stored tick timestamps: %w", err)
	}
	remap := &StoredTelemetryRemap{Context: cloneMatchContext(stored)}
	remap.Context.Physics = physics
	mapper := adapter.NewMapper()
	mapper.SetPhysics(physics)

	base := stored.StartTime
	if base.IsZero() {
		base = time.Unix(0, 0).UTC()
	}
	nominal := 1.0 / 30.0
	if stored.TickRate > 0 {
		nominal = 1 / stored.TickRate
	}
	lastTimestamp := 0.0
	haveTimestamp := false
	expectedFrame := 0
	n, err := store.ForEachMatchTick(ctx, stored.MatchID, func(frameIndex int, raw string) error {
		if frameIndex != expectedFrame {
			return fmt.Errorf("raw tick sequence is not contiguous: got frame %d, want %d", frameIndex, expectedFrame)
		}
		expectedFrame++
		var session adapter.EchoVRSessionResponse
		if err := json.Unmarshal([]byte(raw), &session); err != nil {
			return fmt.Errorf("decode raw tick %d: %w", frameIndex, err)
		}
		if session.SessionID != "" && session.SessionID != stored.MatchID {
			return fmt.Errorf("raw tick %d belongs to match %q, want %q", frameIndex, session.SessionID, stored.MatchID)
		}
		timestamp, ok := timestamps[frameIndex]
		if !ok {
			if haveTimestamp {
				timestamp = lastTimestamp + nominal
			}
		}
		if haveTimestamp && timestamp < lastTimestamp {
			timestamp = lastTimestamp
		}
		lastTimestamp, haveTimestamp = timestamp, true
		mapped := mapper.MapSessionAt(&session, base.Add(time.Duration(timestamp*float64(time.Second))))
		remap.MappingWarnings += len(mapped.Warnings)
		remap.MappingErrors += len(mapped.Errors)
		for i := range mapped.Frames {
			if mapped.Frames[i].FrameIndex != frameIndex {
				return fmt.Errorf("mapper labelled raw tick %d as frame %d", frameIndex, mapped.Frames[i].FrameIndex)
			}
		}
		remap.Frames = append(remap.Frames, mapped.Frames...)
		adapter.MergeMatchContext(remap.Context, mapped.MatchCtx)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("remapping match %s: %w", stored.MatchID, err)
	}
	if n == 0 {
		return nil, fmt.Errorf("match %s: %w", stored.MatchID, ErrNoRawTicks)
	}
	if len(remap.Frames) == 0 {
		return nil, fmt.Errorf("match %s: raw ticks produced no player telemetry", stored.MatchID)
	}
	remap.RawTicks = n
	return remap, nil
}

func cloneMatchContext(src *model.MatchContext) *model.MatchContext {
	dst := *src
	dst.PlayerIDs = append([]string(nil), src.PlayerIDs...)
	dst.TeamAssignments = make(map[string]string, len(src.TeamAssignments))
	for id, team := range src.TeamAssignments {
		dst.TeamAssignments[id] = team
	}
	if src.PlayerNames != nil {
		dst.PlayerNames = make(map[string]string, len(src.PlayerNames))
		for id, name := range src.PlayerNames {
			dst.PlayerNames[id] = name
		}
	}
	return &dst
}
