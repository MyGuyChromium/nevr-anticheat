package adapter

import (
	"strconv"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func heldItemKnown(value string) bool {
	switch value {
	case "none", "disc", "geo":
		return true
	}
	id, err := strconv.ParseUint(value, 10, 32)
	return err == nil && id <= 65535
}

func handAttachments(p *EchoVRPlayer) *model.HandAttachments {
	h := &model.HandAttachments{}
	if p.HoldingLeft != "" {
		v := p.HoldingLeft
		h.Left = &v
	}
	if p.HoldingRight != "" {
		v := p.HoldingRight
		h.Right = &v
	}
	return h
}

func observedDiscAttachment(raw *EchoVRSessionResponse) *model.DiscAttachment {
	a := &model.DiscAttachment{State: "unknown", Reason: "attachment_missing"}
	count, holders := 0, 0
	for teamIndex, team := range raw.Teams {
		if _, ok := mappedTeamName(team.TeamName, teamIndex); !ok {
			continue
		}
		for _, p := range team.Players {
			count++
			if !heldItemKnown(p.HoldingLeft) || !heldItemKnown(p.HoldingRight) {
				return &model.DiscAttachment{State: "unknown", Reason: "attachment_missing"}
			}
			if p.HoldsDisc() {
				holders++
				a.HolderID = playerID(p)
				a.HandCandidates = nil
				if p.HoldingLeft == "disc" {
					a.HandCandidates = append(a.HandCandidates, "left")
				}
				if p.HoldingRight == "disc" {
					a.HandCandidates = append(a.HandCandidates, "right")
				}
			}
		}
	}
	if count == 0 {
		return a
	}
	if holders > 1 {
		return &model.DiscAttachment{State: "unknown", Reason: "attachment_conflict"}
	}
	a.State, a.Reason = "free", ""
	if holders == 1 {
		a.State = "held"
	}
	return a
}

// client_name is only a lookup hint. Ambiguous names, absent persistent IDs,
// and spectator-only/local-unmapped identities must never select a remote.
func uniqueClientPlayer(raw *EchoVRSessionResponse) string {
	if raw.ClientName == "" {
		return ""
	}
	count, id := 0, ""
	for _, team := range raw.Teams {
		for _, p := range team.Players {
			if p.Name == raw.ClientName {
				count++
				if p.UserID != 0 {
					id = playerID(p)
				}
			}
		}
	}
	if count != 1 {
		return ""
	}
	return id
}

// SetObservationSource identifies the caller's actual timestamp mechanism.
// It must never label HTTP completion or recorder-prefix time as engine time.
func (m *Mapper) SetObservationSource(source, timeBasis, sourceID string) {
	if m.observationSource != source || m.observationTimeBasis != timeBasis || m.observationSourceID != sourceID {
		m.haveLastThrow = false
		m.haveFingerprint = false
	}
	m.observationSource, m.observationTimeBasis, m.observationSourceID = source, timeBasis, sourceID
}

func (m *Mapper) observation(raw *EchoVRSessionResponse, frame int, timestamp float64) *model.ObservationContext {
	timeBasis := m.observationTimeBasis
	if m.observationEpoch > 0 {
		timeBasis += ":clock_rebased"
	}
	return &model.ObservationContext{Source: m.observationSource, SourceID: m.observationSourceID, SourceEpoch: m.observationEpoch,
		Authority: "client_reported", TimeBasis: timeBasis, SessionID: raw.SessionID,
		SourcePlayerID: m.throwClientID, FrameIndex: frame, Timestamp: timestamp, Freshness: "sampled_snapshot"}
}
