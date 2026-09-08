package enforce

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// A retry, reordered evidence list, cooldown expiry, or process restart does
// not create a fresh recommendation for the same player/action/evidence set.
// Changed evidence or recommendation type does. This is notification identity,
// not a claim that the underlying observations are independent or validated.
func recommendationID(action model.EnforcementAction) string {
	unique := func(values []string) []string {
		set := make(map[string]bool)
		for _, value := range values {
			if value != "" {
				set[value] = true
			}
		}
		out := make([]string, 0, len(set))
		for value := range set {
			out = append(out, value)
		}
		sort.Strings(out)
		return out
	}
	doc, _ := json.Marshal(struct {
		Version, Player, Action string
		Evidence, Matches       []string
	}{"review-recommendation-v1", action.PlayerID, action.ActionType, unique(action.EvidenceIDs), unique(action.MatchIDs)})
	sum := sha256.Sum256(doc)
	return "recommendation:" + hex.EncodeToString(sum[:])
}
