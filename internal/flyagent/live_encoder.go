package flyagent

import (
	"errors"
	"net"
	"net/url"
	"strconv"

	"github.com/nevr-anticheat/nevr-anticheat/internal/adapter"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

const (
	liveObservationSource    = "echovr_http"
	liveObservationAuthority = "client_reported"
	liveObservationTimeBasis = "http_response_body_received"
)

// EncodePrivateLive applies the stable Echo observation encoder to a live
// /session tick. Live provenance is deliberately admitted through a separate
// entry point: Encode continues to accept replay/tape provenance only.
//
// The returned observation remains control-blocked unless both normalized and
// raw session metadata say this is a private match and the selected frame is
// bound to an exact, loopback HTTP /session source.
func (e *Encoder) EncodePrivateLive(tick *adapter.ParsedTick) (*Observation, error) {
	if e == nil {
		return nil, errors.New("encoder is nil")
	}
	observation, err := e.Encode(tick)
	if err != nil {
		return nil, err
	}

	// Encode may have recognized replay provenance. Never carry that through a
	// method whose contract is specifically live HTTP provenance.
	observation.SourceKind = ""
	observation.SourceID = ""
	observation.SourceAuthority = ""
	observation.SourceTimeBasis = ""
	observation.SourceEpoch = 0
	observation.SourceEpochKnown = false

	if tick == nil || tick.MatchCtx == nil || tick.Session == nil ||
		!tick.MatchCtx.IsPrivate || !tick.Session.PrivateMatch ||
		tick.Session.SessionID != tick.MatchID {
		return observation, nil
	}
	self, ok := uniqueFrameByPlayerID(tick.Frames, observation.PlayerID)
	if !ok || !privateLiveObservationBound(self, tick) {
		return observation, nil
	}

	observation.SourceKind = self.Observation.Source
	observation.SourceID = self.Observation.SourceID
	observation.SourceAuthority = self.Observation.Authority
	observation.SourceTimeBasis = self.Observation.TimeBasis
	observation.SourceEpoch = self.Observation.SourceEpoch
	observation.SourceEpochKnown = true
	return observation, nil
}

func uniqueFrameByPlayerID(frames []model.PlayerTelemetryFrame, playerID string) (model.PlayerTelemetryFrame, bool) {
	var selected model.PlayerTelemetryFrame
	count := 0
	for _, frame := range frames {
		if frame.PlayerID == playerID {
			selected = frame
			count++
		}
	}
	return selected, count == 1
}

func privateLiveObservationBound(self model.PlayerTelemetryFrame, tick *adapter.ParsedTick) bool {
	if self.Observation == nil || !self.Observation.Valid() || tick == nil || tick.MatchCtx == nil {
		return false
	}
	observation := self.Observation
	if observation.Source != liveObservationSource ||
		observation.Authority != liveObservationAuthority ||
		(observation.TimeBasis != liveObservationTimeBasis && observation.TimeBasis != liveObservationTimeBasis+":clock_rebased") ||
		observation.Freshness != "sampled_snapshot" ||
		observation.SessionID != tick.MatchID || tick.MatchCtx.MatchID != tick.MatchID ||
		observation.SourcePlayerID != self.PlayerID ||
		observation.FrameIndex != self.FrameIndex || self.FrameIndex != tick.FrameIndex ||
		observation.Timestamp != self.Timestamp {
		return false
	}
	return isLoopbackSessionEndpoint(observation.SourceID)
}

func isLoopbackSessionEndpoint(raw string) bool {
	endpoint, err := url.Parse(raw)
	if err != nil || endpoint.Scheme != "http" || endpoint.User != nil ||
		endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Path != "/session" ||
		endpoint.RawPath != "" || endpoint.Port() == "" {
		return false
	}
	host := net.ParseIP(endpoint.Hostname())
	if host == nil || !host.IsLoopback() {
		return false
	}
	port, err := strconv.ParseUint(endpoint.Port(), 10, 16)
	return err == nil && port > 0
}
