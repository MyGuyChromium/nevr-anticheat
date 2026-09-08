package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// GetMatchAnalysisCoverage returns nil for legacy analyses. It deliberately
// does not recompute from today's config: those are not the detectors that
// produced the stored events. Re-analysis refreshes both together.
func (s *Store) GetMatchAnalysisCoverage(ctx context.Context, matchID string) (map[string]*model.PlayerCoverage, error) {
	var doc string
	err := s.db.QueryRowContext(ctx, `SELECT coverage_json FROM match_analysis_coverage WHERE match_id = ?`, matchID).Scan(&doc)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var coverage map[string]*model.PlayerCoverage
	if err := json.Unmarshal([]byte(doc), &coverage); err != nil {
		return nil, fmt.Errorf("decode analysis coverage: %w", err)
	}
	if err := validateMechanicsCoverage(coverage); err != nil {
		return nil, err
	}
	return coverage, nil
}

// A diagnostic belongs to its enclosing player/check; valid JSON alone must
// not allow evidence to be silently reassigned to a different player.
func validateMechanicsCoverage(coverage map[string]*model.PlayerCoverage) error {
	for id, player := range coverage {
		if player == nil {
			continue
		}
		if err := validateDataHealth(player.DataHealth); err != nil {
			return err
		}
		for _, detector := range player.Detectors {
			log := detector.MechanicsReview
			if log == nil {
				continue
			}
			if id == "" {
				return fmt.Errorf("mechanics diagnostics require a player identity")
			}
			if err := log.Validate(); err != nil {
				return fmt.Errorf("invalid mechanics diagnostics: %w", err)
			}
			for _, record := range log.Records {
				if record.PlayerID != "" && record.PlayerID != id {
					return fmt.Errorf("mechanics diagnostic player mismatch")
				}
			}
		}
	}
	return nil
}

const partialMechanicsCoverage = "Additional live mechanics diagnostics were merged without complete input/event coverage; earlier analysis counters do not cover those new actions."

func markPartialMechanicsCoverage(player *model.PlayerCoverage, detector *model.DetectorCoverage) {
	player.Status, detector.Status = model.ReviewStatusInsufficientData, model.ReviewStatusInsufficientData
	for _, limitation := range player.Limitations {
		if limitation == partialMechanicsCoverage {
			return
		}
	}
	player.Limitations = append(player.Limitations, partialMechanicsCoverage)
}

// MergeMatchCatchReviews persists additive live catch and mechanics diagnostics in the
// existing coverage JSON. It never changes events, scores, review cases, or
// unrelated detector coverage. Live callers supply disjoint chronological
// chunks. Without full analysis coverage, the surrounding record explicitly
// remains insufficient/partial rather than claiming all catches were seen.
func (s *Store) MergeMatchCatchReviews(ctx context.Context, matchID string, incoming map[string]*model.PlayerCoverage) error {
	if matchID == "" {
		return fmt.Errorf("merging catch diagnostics: match id required")
	}
	if err := validateMechanicsCoverage(incoming); err != nil {
		return err
	}
	hasCatch := false
	for _, player := range incoming {
		if player == nil {
			continue
		}
		if player.DataHealth != nil {
			hasCatch = true
		}
		for _, detector := range player.Detectors {
			if detector.Capability != nil {
				hasCatch = true
			}
			if detector.MechanicsReview != nil {
				hasCatch = true
				if err := detector.MechanicsReview.Validate(); err != nil {
					return fmt.Errorf("invalid incoming mechanics diagnostics: %w", err)
				}
			}
			if detector.CatchReview != nil {
				hasCatch = true
				if err := detector.CatchReview.Validate(); err != nil {
					return fmt.Errorf("invalid incoming catch diagnostics: %w", err)
				}
			}
		}
	}
	if !hasCatch {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var doc string
	err = tx.QueryRowContext(ctx, `SELECT coverage_json FROM match_analysis_coverage WHERE match_id = ?`, matchID).Scan(&doc)
	coverage := make(map[string]*model.PlayerCoverage)
	if err == nil {
		if err := json.Unmarshal([]byte(doc), &coverage); err != nil {
			return fmt.Errorf("decode existing catch coverage: %w", err)
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if coverage == nil {
		coverage = make(map[string]*model.PlayerCoverage)
	}
	if err := validateMechanicsCoverage(coverage); err != nil {
		return err
	}
	changed := false
	ids := make([]string, 0, len(incoming))
	for id := range incoming {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		player := incoming[id]
		if id == "" || player == nil {
			continue
		}
		target := coverage[id]
		freshSnapshot := target == nil || newerHealthSnapshot(target.DataHealth, player.DataHealth)
		if target == nil && player.DataHealth != nil {
			target = &model.PlayerCoverage{Version: 1, Status: model.ReviewStatusInsufficientData,
				Limitations: []string{"Live health snapshot only; complete analysis input/event denominators are not retained here."}}
			coverage[id], changed = target, true
		}
		if target != nil && mergeLiveDataHealth(target, player.DataHealth) {
			changed = true
		}
		for _, detector := range player.Detectors {
			if detector.CatchReview == nil && detector.MechanicsReview == nil && detector.Capability == nil {
				continue
			}
			target := coverage[id]
			if target == nil {
				changed = true
				target = &model.PlayerCoverage{Version: 1, Status: model.ReviewStatusInsufficientData,
					Limitations: []string{"Only partial live transition/mechanics diagnostics are retained here. These are not all actions, independent calibration opportunities, or validated detector accuracy."}}
				coverage[id] = target
			}
			index := -1
			for i := range target.Detectors {
				if target.Detectors[i].DetectorID == detector.DetectorID {
					index = i
					break
				}
			}
			if index < 0 {
				changed = true
				target.Detectors = append(target.Detectors, model.DetectorCoverage{DetectorID: detector.DetectorID, Enabled: detector.Enabled,
					Status:      model.ReviewStatusInsufficientData,
					Limitations: []string{"Partial live transition/mechanics diagnostics only; no complete input or event denominator is available."}})
				index = len(target.Detectors) - 1
			}
			if detector.Capability != nil && (target.Detectors[index].Capability == nil || freshSnapshot) {
				contract := *detector.Capability
				contract.RequiredInputs = append([]string(nil), detector.Capability.RequiredInputs...)
				contract.EnforcementRequires = append([]string(nil), detector.Capability.EnforcementRequires...)
				if detector.Capability.Rule != nil {
					rule := *detector.Capability.Rule
					rule.Tests = append([]string(nil), detector.Capability.Rule.Tests...)
					rule.ConfiguredParameters = append(json.RawMessage(nil), detector.Capability.Rule.ConfiguredParameters...)
					contract.Rule = &rule
				}
				target.Detectors[index].Capability = &contract
				changed = true
			}
			if detector.MechanicsReview != nil {
				log := target.Detectors[index].MechanicsReview
				if log == nil {
					changed = true
					log = model.NewMechanicsReviewLog()
					target.Detectors[index].MechanicsReview = log
				}
				if err := log.Validate(); err != nil {
					return fmt.Errorf("invalid existing mechanics diagnostics: %w", err)
				}
				total, invalid := log.Total, log.Invalid
				log.Merge(detector.MechanicsReview)
				changed = changed || log.Total != total || log.Invalid != invalid
				if log.Total > total {
					markPartialMechanicsCoverage(target, &target.Detectors[index])
				}
			}
			if detector.CatchReview == nil {
				continue
			}
			log := target.Detectors[index].CatchReview
			if log == nil {
				changed = true
				log = model.NewCatchReviewLog()
				target.Detectors[index].CatchReview = log
			}
			if err := log.Validate(); err != nil {
				return fmt.Errorf("invalid existing catch diagnostics: %w", err)
			}
			total, invalid := log.Total, log.Invalid
			log.Merge(detector.CatchReview)
			changed = changed || log.Total != total || log.Invalid != invalid
		}
	}
	if !changed {
		return nil
	}
	encoded, err := json.Marshal(coverage)
	if err != nil {
		return fmt.Errorf("encode merged catch diagnostics: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO match_analysis_coverage (match_id, coverage_json) VALUES (?, ?)
		ON CONFLICT(match_id) DO UPDATE SET coverage_json=excluded.coverage_json`, matchID, string(encoded)); err != nil {
		return err
	}
	return tx.Commit()
}
