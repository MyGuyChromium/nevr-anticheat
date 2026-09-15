package flyagentlive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/adapter"
)

const MaxSessionResponseBytes = 1 << 20

// LoopbackSessionSource reads the local Echo /session endpoint only. It does
// not follow redirects or honor proxy environment variables, preventing the
// configured endpoint from widening into another network destination.
type LoopbackSessionSource struct {
	endpoint string
	client   *http.Client
}

func NewLoopbackSessionSource(endpoint string, requestTimeout time.Duration) (*LoopbackSessionSource, error) {
	if !validLoopbackSessionURL(endpoint) {
		return nil, errors.New("session URL must be http://<loopback-ip>:<port>/session with no credentials, query, fragment, or redirect target")
	}
	if requestTimeout <= 0 || requestTimeout > MaxStaleTelemetry {
		return nil, fmt.Errorf("request timeout must be within (0, %s]", MaxStaleTelemetry)
	}
	parsed, _ := url.Parse(endpoint)
	canonical := parsed.String()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &LoopbackSessionSource{
		endpoint: canonical,
		client: &http.Client{
			Timeout:   requestTimeout,
			Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func (s *LoopbackSessionSource) Provenance() SourceProvenance {
	if s == nil {
		return SourceProvenance{}
	}
	return SourceProvenance{Kind: LiveSourceKind, ID: s.endpoint, Authority: LiveSourceAuthority, TimeBasis: LiveSourceTimeBasis}
}

func (s *LoopbackSessionSource) Poll(ctx context.Context) (SessionSample, error) {
	if s == nil || s.client == nil {
		return SessionSample{}, errors.New("loopback session source is nil")
	}
	if ctx == nil {
		return SessionSample{}, errors.New("poll context is required")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.endpoint, nil)
	if err != nil {
		return SessionSample{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "nevr-fly-private-dry-run/1")
	response, err := s.client.Do(request)
	if err != nil {
		return SessionSample{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, MaxSessionResponseBytes+1))
	receivedAt := time.Now()
	if err != nil {
		return SessionSample{}, fmt.Errorf("read /session response: %w", err)
	}
	if len(body) > MaxSessionResponseBytes {
		return SessionSample{}, fmt.Errorf("/session response exceeds %d bytes", MaxSessionResponseBytes)
	}
	if response.StatusCode != http.StatusOK {
		return SessionSample{}, fmt.Errorf("/session returned HTTP %d", response.StatusCode)
	}
	session, evidence, err := decodeSessionBody(body)
	if err != nil {
		return SessionSample{}, fmt.Errorf("decode /session response: %w", err)
	}
	digest := sha256.Sum256(body)
	return SessionSample{
		Session: session, ReceivedAt: receivedAt,
		SnapshotSHA256: fmt.Sprintf("%x", digest), Evidence: evidence,
	}, nil
}

func (s *LoopbackSessionSource) CloseIdleConnections() {
	if s != nil && s.client != nil {
		s.client.CloseIdleConnections()
	}
}

func validLoopbackSessionURL(raw string) bool {
	endpoint, err := url.Parse(raw)
	if err != nil || endpoint.Scheme != "http" || endpoint.Opaque != "" || endpoint.User != nil ||
		endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" ||
		endpoint.Path != "/session" || endpoint.RawPath != "" || endpoint.Port() == "" {
		return false
	}
	host := net.ParseIP(endpoint.Hostname())
	if host == nil || !host.IsLoopback() {
		return false
	}
	port, err := strconv.ParseUint(endpoint.Port(), 10, 16)
	return err == nil && port > 0
}

func decodeSessionBody(body []byte) (*adapter.EchoVRSessionResponse, SessionEvidence, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	opening, err := decoder.Token()
	if err != nil {
		return nil, SessionEvidence{}, err
	}
	if delimiter, ok := opening.(json.Delim); !ok || delimiter != '{' {
		return nil, SessionEvidence{}, errors.New("top-level /session value must be an object")
	}

	seen := make(map[string]struct{})
	values := make(map[string]json.RawMessage, 5)
	canonicalEvidenceKeys := []string{"sessionid", "match_type", "private_match", "client_name", "game_status"}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, SessionEvidence{}, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, SessionEvidence{}, errors.New("top-level /session key is not a string")
		}
		if _, duplicate := seen[key]; duplicate {
			return nil, SessionEvidence{}, fmt.Errorf("duplicate top-level /session key %q", key)
		}
		seen[key] = struct{}{}
		for _, canonical := range canonicalEvidenceKeys {
			if key != canonical && strings.EqualFold(key, canonical) {
				return nil, SessionEvidence{}, fmt.Errorf("non-canonical private-evidence key %q", key)
			}
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, SessionEvidence{}, err
		}
		if key == "sessionid" || key == "match_type" || key == "private_match" || key == "client_name" || key == "game_status" {
			values[key] = value
		}
	}
	closing, err := decoder.Token()
	if err != nil {
		return nil, SessionEvidence{}, err
	}
	if delimiter, ok := closing.(json.Delim); !ok || delimiter != '}' {
		return nil, SessionEvidence{}, errors.New("unterminated top-level /session object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, SessionEvidence{}, errors.New("multiple top-level /session values")
		}
		return nil, SessionEvidence{}, err
	}

	evidence := SessionEvidence{
		SessionIDPresent:    values["sessionid"] != nil,
		MatchTypePresent:    values["match_type"] != nil,
		PrivateMatchPresent: values["private_match"] != nil,
		ClientNamePresent:   values["client_name"] != nil,
		GameStatusPresent:   values["game_status"] != nil,
	}
	var exactSessionID, exactMatchType, exactClientName, exactGameStatus string
	var exactPrivate bool
	if evidence.SessionIDPresent {
		if err := json.Unmarshal(values["sessionid"], &exactSessionID); err != nil {
			return nil, SessionEvidence{}, errors.New("sessionid must be a string")
		}
	}
	if evidence.MatchTypePresent {
		if err := json.Unmarshal(values["match_type"], &exactMatchType); err != nil {
			return nil, SessionEvidence{}, errors.New("match_type must be a string")
		}
	}
	if evidence.PrivateMatchPresent {
		if err := json.Unmarshal(values["private_match"], &exactPrivate); err != nil {
			return nil, SessionEvidence{}, errors.New("private_match must be a boolean")
		}
	}
	if evidence.ClientNamePresent {
		if err := json.Unmarshal(values["client_name"], &exactClientName); err != nil {
			return nil, SessionEvidence{}, errors.New("client_name must be a string")
		}
	}
	if evidence.GameStatusPresent {
		if err := json.Unmarshal(values["game_status"], &exactGameStatus); err != nil {
			return nil, SessionEvidence{}, errors.New("game_status must be a string")
		}
	}

	var session adapter.EchoVRSessionResponse
	if err := json.Unmarshal(body, &session); err != nil {
		return nil, SessionEvidence{}, err
	}
	if (evidence.SessionIDPresent && session.SessionID != exactSessionID) ||
		(evidence.MatchTypePresent && session.MatchType != exactMatchType) ||
		(evidence.PrivateMatchPresent && session.PrivateMatch != exactPrivate) ||
		(evidence.ClientNamePresent && session.ClientName != exactClientName) ||
		(evidence.GameStatusPresent && session.GameStatus != exactGameStatus) {
		return nil, SessionEvidence{}, errors.New("case-insensitive JSON aliases conflict with exact private-evidence fields")
	}
	return &session, evidence, nil
}
