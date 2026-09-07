package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const CalibrationSplitPolicyVersion = 1

// RecordCalibrationExposure reserves imported evidence as previously inspected
// training data. It cannot erase an existing held-out assignment: such imports
// quarantine it instead. The exposure persists even before its replay exists.
func (s *Store) RecordCalibrationExposure(ctx context.Context, matchID string, knownPlayers ...string) error {
	matchID = strings.TrimSpace(matchID)
	if matchID == "" {
		return fmt.Errorf("calibration exposure needs a match ID")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO calibration_split_assignments
		(match_id, policy_version, group_key, split, players_json) VALUES (?, ?, 'imported-exposure', 'training', '[]')
		ON CONFLICT(match_id) DO UPDATE SET quarantined=CASE WHEN split <> 'training' THEN 1 ELSE quarantined END`,
		matchID, CalibrationSplitPolicyVersion); err != nil {
		return err
	}
	// Imported window/event evidence already identifies a player even when the
	// old replay is absent. Preserve that exposure before any new match is split.
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT players_json FROM calibration_split_assignments WHERE match_id=?`, matchID).Scan(&raw); err != nil {
		return err
	}
	var existingPlayers []string
	if err := json.Unmarshal([]byte(raw), &existingPlayers); err != nil {
		return fmt.Errorf("invalid persisted split roster for %s: %w", matchID, err)
	}
	unique := make(map[string]bool)
	for _, player := range append(existingPlayers, knownPlayers...) {
		if player = strings.TrimSpace(player); player != "" {
			unique[player] = true
		}
	}
	players := make([]string, 0, len(unique))
	for player := range unique {
		players = append(players, player)
	}
	sort.Strings(players)
	merged, err := json.Marshal(players)
	if err != nil {
		return err
	}
	if string(merged) != raw {
		if _, err := tx.ExecContext(ctx, `UPDATE calibration_split_assignments SET players_json=? WHERE match_id=?`, string(merged), matchID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// CalibrationSplitAssignment survives replay deletion. Split and group key are
// immutable; historical player membership only grows and quarantine never clears.
type CalibrationSplitAssignment struct {
	MatchID             string   `json:"match_id"`
	PolicyVersion       int      `json:"policy_version"`
	GroupKey            string   `json:"group_key"`
	Split               string   `json:"split"`
	Players             []string `json:"players"`
	Quarantined         bool     `json:"quarantined"`
	ExposureFingerprint string   `json:"exposure_fingerprint,omitempty"`
}

// RecordCalibrationHoldoutExposure freezes the candidate at first exposure.
// Empty means provenance cannot be verified. Neither updates nor deletion can
// reset the first candidate or clear quarantine; fresh cohorts are needed.
func (s *Store) RecordCalibrationHoldoutExposure(ctx context.Context, fingerprints map[string]string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for id, fingerprint := range fingerprints {
		if _, err := tx.ExecContext(ctx, `UPDATE calibration_split_assignments SET
			quarantined=CASE WHEN ? = '' OR (exposure_fingerprint <> '' AND exposure_fingerprint <> ?) THEN 1 ELSE quarantined END,
			exposure_fingerprint=CASE WHEN exposure_fingerprint='' THEN ? ELSE exposure_fingerprint END
			WHERE match_id=? AND split='holdout' AND quarantined=0
			AND (exposure_fingerprint='' OR exposure_fingerprint<>? OR ?='')`, fingerprint, fingerprint, fingerprint, id, fingerprint, fingerprint); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ReconcileCalibrationSplits allocates unseen components once, inherits existing
// player exposure, and quarantines components whose histories cross split boundaries.
// proposed is only a recommendation for entirely new, disconnected components.
func (s *Store) ReconcileCalibrationSplits(ctx context.Context, matches []StoredMatch, proposed map[string]string) (map[string]CalibrationSplitAssignment, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT match_id, policy_version, group_key, split, players_json, quarantined, exposure_fingerprint FROM calibration_split_assignments`)
	if err != nil {
		return nil, err
	}
	all := make(map[string]*CalibrationSplitAssignment)
	existing := make(map[string]bool)
	originalPlayers := make(map[string]string)
	originalQuarantine := make(map[string]bool)
	for rows.Next() {
		var a CalibrationSplitAssignment
		var players string
		if err := rows.Scan(&a.MatchID, &a.PolicyVersion, &a.GroupKey, &a.Split, &players, &a.Quarantined, &a.ExposureFingerprint); err != nil {
			rows.Close()
			return nil, err
		}
		if err := json.Unmarshal([]byte(players), &a.Players); err != nil {
			rows.Close()
			return nil, fmt.Errorf("invalid persisted split roster for %s: %w", a.MatchID, err)
		}
		if a.PolicyVersion != CalibrationSplitPolicyVersion {
			rows.Close()
			return nil, fmt.Errorf("unsupported calibration split policy %d", a.PolicyVersion)
		}
		all[a.MatchID], existing[a.MatchID] = &a, true
		originalPlayers[a.MatchID], originalQuarantine[a.MatchID] = players, a.Quarantined
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	active := make(map[string]bool)
	for _, match := range matches {
		if match.Context == nil || match.Context.MatchID == "" {
			continue
		}
		id := match.Context.MatchID
		active[id] = true
		a := all[id]
		if a == nil {
			a = &CalibrationSplitAssignment{MatchID: id, PolicyVersion: CalibrationSplitPolicyVersion}
			all[id] = a
		}
		players := make(map[string]bool)
		for _, p := range append(append([]string{}, a.Players...), match.Context.PlayerIDs...) {
			if p = strings.TrimSpace(p); p != "" {
				players[p] = true
			}
		}
		a.Players = a.Players[:0]
		for p := range players {
			a.Players = append(a.Players, p)
		}
		sort.Strings(a.Players)
		// Without stable player identity independence cannot be established.
		if len(a.Players) == 0 {
			a.Quarantined = true
		}
	}
	parent := make(map[string]string, len(all))
	var find func(string) string
	find = func(id string) string {
		if parent[id] != id {
			parent[id] = find(parent[id])
		}
		return parent[id]
	}
	for id := range all {
		parent[id] = id
	}
	firstPlayer := make(map[string]string)
	for id, a := range all {
		for _, player := range a.Players {
			if first, ok := firstPlayer[player]; ok {
				x, y := find(id), find(first)
				if x < y {
					parent[y] = x
				} else {
					parent[x] = y
				}
			} else {
				firstPlayer[player] = id
			}
		}
	}
	components := make(map[string][]string)
	for id := range all {
		root := find(id)
		components[root] = append(components[root], id)
	}
	for _, ids := range components {
		sort.Strings(ids)
		splits := make(map[string]bool)
		exposures := make(map[string]bool)
		quarantined := false
		for _, id := range ids {
			a := all[id]
			if existing[id] {
				splits[a.Split] = true
			}
			quarantined = quarantined || a.Quarantined
			if a.ExposureFingerprint != "" {
				exposures[a.ExposureFingerprint] = true
			}
		}
		quarantined = quarantined || len(splits) > 1 || len(exposures) > 1
		chosen := ""
		for _, split := range []string{"training", "validation", "holdout"} {
			if splits[split] {
				chosen = split
				break
			}
		}
		if chosen == "" {
			chosen = proposed[ids[0]]
			if chosen != "training" && chosen != "validation" && chosen != "holdout" {
				return nil, fmt.Errorf("missing or invalid split proposal for %s", ids[0])
			}
		}
		for _, id := range ids {
			a := all[id]
			if !existing[id] {
				a.Split, a.GroupKey = chosen, "component:"+ids[0]
			}
			a.Quarantined = quarantined
		}
	}
	// Only exposure metadata changes here, never telemetry or labels.
	for id, a := range all {
		players, err := json.Marshal(a.Players)
		if err != nil {
			return nil, err
		}
		if existing[id] && string(players) == originalPlayers[id] && a.Quarantined == originalQuarantine[id] {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO calibration_split_assignments
			(match_id, policy_version, group_key, split, players_json, quarantined) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(match_id) DO UPDATE SET players_json=excluded.players_json,
			quarantined=MAX(calibration_split_assignments.quarantined, excluded.quarantined)`,
			id, a.PolicyVersion, a.GroupKey, a.Split, string(players), a.Quarantined); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	out := make(map[string]CalibrationSplitAssignment, len(active))
	for id := range active {
		out[id] = *all[id]
	}
	return out, nil
}
