package replay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/nevr-anticheat/nevr-anticheat/internal/adapter"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

type rawTickNext func() (int, string, error)

func rawMapIterator(raw map[int]string) rawTickNext {
	indices := make([]int, 0, len(raw))
	for idx := range raw {
		indices = append(indices, idx)
	}
	sort.Ints(indices)
	position := 0
	return func() (int, string, error) {
		if position == len(indices) {
			return 0, "", io.EOF
		}
		idx := indices[position]
		position++
		return idx, raw[idx], nil
	}
}

func nativeRecordIdentity(raw string) (*adapter.TapeRawRecord, bool, error) {
	if len(raw) > adapter.DefaultMaxLineBytes {
		return nil, false, errors.New("stored source record exceeds native comparison limit")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return nil, false, fmt.Errorf("invalid stored source JSON: %w", err)
	}
	marker, present := fields["_nevr_tape"]
	if !present {
		return nil, false, nil
	}
	var record *adapter.TapeRawRecord
	if err := json.Unmarshal(marker, &record); err != nil {
		return nil, true, fmt.Errorf("invalid native source identity: %w", err)
	}
	if record == nil || record.Version != 1 || len(record.Header) == 0 || len(record.Frame) == 0 {
		return nil, true, errors.New("native source identity is missing or unsupported")
	}
	return record, true, nil
}

// validateNativeSource protects immutable raw evidence before any new source,
// context or derived outputs are written. A session UUID is not a capture ID:
// different observers and selected clips may share it. Compare original native
// records, not the derived session projection which can change after mapper fixes.
// A matching orphan prefix from an interrupted import can be completed safely.
// Ordinary legacy-to-legacy duplicate handling is intentionally unchanged.
func validateNativeSource(ctx context.Context, store *sqlite.Store, incoming *model.MatchContext, next rawTickNext) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if incoming == nil || incoming.MatchID == "" {
		return errors.New("source preflight: missing match identity")
	}
	stored, err := store.GetMatchContext(ctx, incoming.MatchID)
	if err != nil && !errors.Is(err, sqlite.ErrNotFound) {
		return fmt.Errorf("source preflight: load stored context: %w", err)
	}
	isNative := incoming.Source == "tape" || incoming.NativeCapture != nil
	storedNative := stored != nil && (stored.Source == "tape" || stored.NativeCapture != nil)
	if isNative && (incoming.NativeCapture == nil || incoming.NativeCapture.CaptureID == "") {
		return errors.New("source preflight: incoming native capture metadata is missing")
	}
	if stored != nil && (isNative || storedNative) {
		if !isNative || !storedNative {
			return errors.New("source conflict: this session is already stored from a different native/legacy source; existing evidence was not replaced")
		}
		if stored.NativeCapture == nil || stored.NativeCapture.CaptureID == "" {
			return errors.New("source conflict: stored native capture metadata is missing; existing evidence was not replaced")
		}
		if stored.NativeCapture.CaptureID != incoming.NativeCapture.CaptureID {
			return errors.New("source conflict: this session belongs to a different native capture; existing evidence was not replaced")
		}
		if incoming.Duration < stored.Duration {
			return errors.New("source conflict: incoming native capture has a shorter recorded span than the stored analysis; existing evidence was not replaced")
		}
	}
	if next == nil {
		next = rawMapIterator(nil)
	}
	position := -1
	var candidate string
	n, err := store.ForEachMatchTick(ctx, incoming.MatchID, func(idx int, raw string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Invalid old legacy snapshots retain their existing behavior unless
		// a native source is involved. Detect even null reserved markers.
		if !isNative && !storedNative && !adapter.IsTapeRawJSON(raw) {
			return nil
		}
		old, oldNative, err := nativeRecordIdentity(raw)
		if err != nil {
			return err
		}
		if !isNative || !oldNative {
			return errors.New("source conflict: immutable raw ticks belong to a different native/legacy source")
		}
		for position < idx {
			position, candidate, err = next()
			if errors.Is(err, io.EOF) {
				return fmt.Errorf("source conflict: incoming capture omits stored native frame %d; a shorter viewing clip cannot replace its source", idx)
			}
			if err != nil {
				return err
			}
		}
		if position != idx {
			return fmt.Errorf("source conflict: incoming capture omits stored native frame %d", idx)
		}
		current, currentNative, err := nativeRecordIdentity(candidate)
		if err != nil {
			return err
		}
		if !currentNative || !bytes.Equal(old.Header, current.Header) || !bytes.Equal(old.Frame, current.Frame) {
			return fmt.Errorf("source conflict: native capture or frame range differs at stored frame %d; existing evidence was not replaced", idx)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("source preflight for match %s: %w", incoming.MatchID, err)
	}
	if storedNative && n == 0 {
		return errors.New("source conflict: stored native capture has no original protobuf records; refusing an unverifiable replacement")
	}
	return nil
}
