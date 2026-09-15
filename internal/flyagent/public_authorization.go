package flyagent

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	SignedPublicAuthorizationSchema = "nevr.fly.signed-public-authorization/v1"
	PublicAuthorizationGrantSchema  = "nevr.fly.public-authorization-grant/v1"
	maxPublicAuthorizationBytes     = 64 << 10
	maxPublicAuthorizationLifetime  = 31 * 24 * time.Hour
	maxPublicSessionSeconds         = 2 * 60 * 60
	maxPublicActionRateHz           = 30
	publicAuthorizationDomain       = "nevr.fly.public-authorization/v1\x00"
)

// PublicAuthorizationGrant is a narrow, expiring permission issued by the
// operator of a community server. It does not provide a controller or a
// matchmaking implementation; it is only an admission requirement for a
// future, separately reviewed public runner.
type PublicAuthorizationGrant struct {
	Schema                      string `json:"schema"`
	GrantID                     string `json:"grant_id"`
	Issuer                      string `json:"issuer"`
	CommunityServerID           string `json:"community_server_id"`
	BotAccountID                string `json:"bot_account_id"`
	BotDisplayName              string `json:"bot_display_name"`
	SanctionedBotSlotID         string `json:"sanctioned_bot_slot_id"`
	ObservationAPI              string `json:"observation_api"`
	ControllerAPI               string `json:"controller_api"`
	NotBefore                   string `json:"not_before"`
	NotAfter                    string `json:"not_after"`
	MaxSessionSeconds           int    `json:"max_session_seconds"`
	MaxActionRateHz             int    `json:"max_action_rate_hz"`
	AllowPublicMatchmaking      bool   `json:"allow_public_matchmaking"`
	BotIdentityDisclosed        bool   `json:"bot_identity_disclosed"`
	HumanEquivalentObservations bool   `json:"human_equivalent_observations"`
	OperatorMonitoringRequired  bool   `json:"operator_monitoring_required"`
	OperatorStopRequired        bool   `json:"operator_stop_required"`
}

// SignedPublicAuthorization carries an Ed25519 signature over the canonical
// grant. The trusted public key is deliberately supplied out of band rather
// than embedded in this document.
type SignedPublicAuthorization struct {
	Schema    string                   `json:"schema"`
	KeyID     string                   `json:"key_id"`
	Grant     PublicAuthorizationGrant `json:"grant"`
	Signature string                   `json:"signature"`
}

// TrustedPublicAuthority binds a pinned key to the expected operator name.
type TrustedPublicAuthority struct {
	KeyID     string
	Issuer    string
	PublicKey ed25519.PublicKey
}

// PublicAuthorizationExpectation prevents a valid grant for one deployment
// from being replayed for a different server, account, or controller surface.
type PublicAuthorizationExpectation struct {
	CommunityServerID   string
	BotAccountID        string
	BotDisplayName      string
	SanctionedBotSlotID string
	ObservationAPI      string
	ControllerAPI       string
}

// VerifiedPublicAuthorization is returned only after strict parsing,
// constraint checks, identity binding, expiry checks and signature
// verification all succeed.
type VerifiedPublicAuthorization struct {
	grant       PublicAuthorizationGrant
	keyID       string
	grantDigest string
	valid       bool
}

func (v VerifiedPublicAuthorization) KeyID() string       { return v.keyID }
func (v VerifiedPublicAuthorization) GrantDigest() string { return v.grantDigest }

// Valid rechecks the grant against trusted current time. A verified value does
// not remain usable after expiry.
func (v VerifiedPublicAuthorization) Valid() bool { return v.validAt(time.Now().UTC()) }

// GrantForUse rechecks trusted current time before returning a copy of the
// immutable verified grant. Future public consumers must use this method at
// the point of admission rather than retaining grant fields past expiry.
func (v VerifiedPublicAuthorization) GrantForUse() (PublicAuthorizationGrant, error) {
	return v.grantForUseAt(time.Now().UTC())
}

func (v VerifiedPublicAuthorization) grantForUseAt(now time.Time) (PublicAuthorizationGrant, error) {
	if !v.validAt(now) {
		return PublicAuthorizationGrant{}, errors.New("verified public authorization is inactive or expired")
	}
	return v.grant, nil
}

func (v VerifiedPublicAuthorization) validAt(now time.Time) bool {
	if !v.valid || now.IsZero() {
		return false
	}
	notBefore, beforeErr := parseCanonicalAuthorizationTime("not_before", v.grant.NotBefore)
	notAfter, afterErr := parseCanonicalAuthorizationTime("not_after", v.grant.NotAfter)
	return beforeErr == nil && afterErr == nil && !now.Before(notBefore) && now.Before(notAfter)
}

// VerifyPublicAuthorization validates a signed operator grant against trusted
// current time. Tests use the unexported clock seam below.
func VerifyPublicAuthorization(
	r io.Reader,
	authorities map[string]TrustedPublicAuthority,
	expect PublicAuthorizationExpectation,
) (VerifiedPublicAuthorization, error) {
	return verifyPublicAuthorizationAt(r, authorities, expect, time.Now().UTC())
}

func verifyPublicAuthorizationAt(
	r io.Reader,
	authorities map[string]TrustedPublicAuthority,
	expect PublicAuthorizationExpectation,
	now time.Time,
) (VerifiedPublicAuthorization, error) {
	if r == nil {
		return VerifiedPublicAuthorization{}, errors.New("public authorization reader is nil")
	}
	if now.IsZero() {
		return VerifiedPublicAuthorization{}, errors.New("public authorization verification time is required")
	}
	limited := &io.LimitedReader{R: r, N: maxPublicAuthorizationBytes + 1}
	document, err := io.ReadAll(limited)
	if err != nil {
		return VerifiedPublicAuthorization{}, fmt.Errorf("read public authorization: %w", err)
	}
	if len(document) > maxPublicAuthorizationBytes {
		return VerifiedPublicAuthorization{}, fmt.Errorf("public authorization exceeds %d bytes", maxPublicAuthorizationBytes)
	}
	if err := rejectDuplicateJSONKeys(document); err != nil {
		return VerifiedPublicAuthorization{}, fmt.Errorf("decode public authorization: %w", err)
	}
	var signed SignedPublicAuthorization
	if err := rejectNonExactJSONFields(document, &signed); err != nil {
		return VerifiedPublicAuthorization{}, fmt.Errorf("decode public authorization: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&signed); err != nil {
		return VerifiedPublicAuthorization{}, fmt.Errorf("decode public authorization: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return VerifiedPublicAuthorization{}, errors.New("decode public authorization: multiple JSON values")
		}
		return VerifiedPublicAuthorization{}, fmt.Errorf("decode public authorization trailer: %w", err)
	}
	if signed.Schema != SignedPublicAuthorizationSchema {
		return VerifiedPublicAuthorization{}, fmt.Errorf("signed public authorization schema %q is unsupported", signed.Schema)
	}
	if !boundedIdentifier(signed.KeyID) {
		return VerifiedPublicAuthorization{}, errors.New("public authorization key_id is invalid")
	}
	authority, ok := authorities[signed.KeyID]
	if !ok {
		return VerifiedPublicAuthorization{}, fmt.Errorf("public authorization key %q is not trusted", signed.KeyID)
	}
	if authority.KeyID != signed.KeyID || !boundedIdentifier(authority.Issuer) || len(authority.PublicKey) != ed25519.PublicKeySize {
		return VerifiedPublicAuthorization{}, fmt.Errorf("trusted public authority %q is invalid", signed.KeyID)
	}
	if err := validatePublicGrant(signed.Grant, authority.Issuer, expect, now.UTC()); err != nil {
		return VerifiedPublicAuthorization{}, err
	}
	payload, err := PublicAuthorizationSigningPayload(signed.KeyID, signed.Grant)
	if err != nil {
		return VerifiedPublicAuthorization{}, err
	}
	signature, err := base64.RawURLEncoding.DecodeString(signed.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return VerifiedPublicAuthorization{}, errors.New("public authorization signature is not unpadded base64url Ed25519 data")
	}
	if !ed25519.Verify(authority.PublicKey, payload, signature) {
		return VerifiedPublicAuthorization{}, errors.New("public authorization signature verification failed")
	}
	digest := sha256.Sum256(payload)
	return VerifiedPublicAuthorization{
		grant: signed.Grant, keyID: signed.KeyID,
		grantDigest: fmt.Sprintf("%x", digest[:]), valid: true,
	}, nil
}

func validatePublicGrant(grant PublicAuthorizationGrant, expectedIssuer string, expect PublicAuthorizationExpectation, now time.Time) error {
	if grant.Schema != PublicAuthorizationGrantSchema {
		return fmt.Errorf("public authorization grant schema %q is unsupported", grant.Schema)
	}
	identifiers := map[string]string{
		"grant_id": grant.GrantID, "issuer": grant.Issuer,
		"community_server_id": grant.CommunityServerID, "bot_account_id": grant.BotAccountID,
		"bot_display_name": grant.BotDisplayName, "sanctioned_bot_slot_id": grant.SanctionedBotSlotID,
		"observation_api": grant.ObservationAPI, "controller_api": grant.ControllerAPI,
	}
	for name, value := range identifiers {
		if !boundedIdentifier(value) {
			return fmt.Errorf("public authorization %s is invalid", name)
		}
	}
	if grant.Issuer != expectedIssuer {
		return errors.New("public authorization issuer does not match its trusted key")
	}
	expected := map[string][2]string{
		"community_server_id":    {grant.CommunityServerID, expect.CommunityServerID},
		"bot_account_id":         {grant.BotAccountID, expect.BotAccountID},
		"bot_display_name":       {grant.BotDisplayName, expect.BotDisplayName},
		"sanctioned_bot_slot_id": {grant.SanctionedBotSlotID, expect.SanctionedBotSlotID},
		"observation_api":        {grant.ObservationAPI, expect.ObservationAPI},
		"controller_api":         {grant.ControllerAPI, expect.ControllerAPI},
	}
	for name, pair := range expected {
		if !boundedIdentifier(pair[1]) {
			return fmt.Errorf("expected public authorization %s is invalid", name)
		}
		if pair[0] != pair[1] {
			return fmt.Errorf("public authorization %s does not match this deployment", name)
		}
	}
	notBefore, err := parseCanonicalAuthorizationTime("not_before", grant.NotBefore)
	if err != nil {
		return err
	}
	notAfter, err := parseCanonicalAuthorizationTime("not_after", grant.NotAfter)
	if err != nil {
		return err
	}
	if !notAfter.After(notBefore) || notAfter.Sub(notBefore) > maxPublicAuthorizationLifetime {
		return fmt.Errorf("public authorization validity must be positive and no longer than %s", maxPublicAuthorizationLifetime)
	}
	if now.Before(notBefore) {
		return errors.New("public authorization is not active yet")
	}
	if !now.Before(notAfter) {
		return errors.New("public authorization has expired")
	}
	if grant.MaxSessionSeconds < 1 || grant.MaxSessionSeconds > maxPublicSessionSeconds {
		return fmt.Errorf("public authorization max_session_seconds is outside 1..%d", maxPublicSessionSeconds)
	}
	if grant.MaxActionRateHz < 1 || grant.MaxActionRateHz > maxPublicActionRateHz {
		return fmt.Errorf("public authorization max_action_rate_hz is outside 1..%d", maxPublicActionRateHz)
	}
	if !grant.AllowPublicMatchmaking || !grant.BotIdentityDisclosed || !grant.HumanEquivalentObservations ||
		!grant.OperatorMonitoringRequired || !grant.OperatorStopRequired {
		return errors.New("public authorization must explicitly require matchmaking permission, bot disclosure, human-equivalent observations, monitoring, and operator stop")
	}
	return nil
}

func parseCanonicalAuthorizationTime(name, value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil || parsed.Location() != time.UTC || parsed.Format(time.RFC3339) != value {
		return time.Time{}, fmt.Errorf("public authorization %s must be canonical UTC RFC3339", name)
	}
	return parsed, nil
}

// PublicAuthorizationSigningPayload returns the domain-separated canonical
// bytes an out-of-band community-server authority signs with Ed25519. Signing
// does not make an unsafe grant acceptable; verification still enforces every
// identity, expiry, disclosure, monitoring, and rate/session constraint.
func PublicAuthorizationSigningPayload(keyID string, grant PublicAuthorizationGrant) ([]byte, error) {
	if !boundedIdentifier(keyID) {
		return nil, errors.New("public authorization signing key_id is invalid")
	}
	unsignedEnvelope := struct {
		Schema string                   `json:"schema"`
		KeyID  string                   `json:"key_id"`
		Grant  PublicAuthorizationGrant `json:"grant"`
	}{Schema: SignedPublicAuthorizationSchema, KeyID: keyID, Grant: grant}
	canonical, err := json.Marshal(unsignedEnvelope)
	if err != nil {
		return nil, fmt.Errorf("encode public authorization signing payload: %w", err)
	}
	return append([]byte(publicAuthorizationDomain), canonical...), nil
}

func boundedIdentifier(value string) bool {
	return value != "" && len(value) <= 256 && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\r\n\x00")
}
