package flyagent

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func signedPublicAuthorizationFixture(t *testing.T) (string, map[string]TrustedPublicAuthority, PublicAuthorizationExpectation, time.Time) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	grant := PublicAuthorizationGrant{
		Schema: PublicAuthorizationGrantSchema, GrantID: "grant-123", Issuer: "Example Community Operator",
		CommunityServerID: "community.example", BotAccountID: "fly-bot-1", BotDisplayName: "[BOT] Fruit Fly",
		SanctionedBotSlotID: "arena-bot-slot-4", ObservationAPI: "echo-session-v1", ControllerAPI: "operator-bot-controller-v1",
		NotBefore: now.Add(-time.Minute).Format(time.RFC3339), NotAfter: now.Add(24 * time.Hour).Format(time.RFC3339),
		MaxSessionSeconds: 1800, MaxActionRateHz: 15, AllowPublicMatchmaking: true,
		BotIdentityDisclosed: true, HumanEquivalentObservations: true,
		OperatorMonitoringRequired: true, OperatorStopRequired: true,
	}
	payload, err := PublicAuthorizationSigningPayload("operator-key-2026", grant)
	if err != nil {
		t.Fatal(err)
	}
	signed := SignedPublicAuthorization{
		Schema: SignedPublicAuthorizationSchema, KeyID: "operator-key-2026", Grant: grant,
		Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, payload)),
	}
	document, err := json.Marshal(signed)
	if err != nil {
		t.Fatal(err)
	}
	authorities := map[string]TrustedPublicAuthority{
		signed.KeyID: {KeyID: signed.KeyID, Issuer: grant.Issuer, PublicKey: publicKey},
	}
	expect := PublicAuthorizationExpectation{
		CommunityServerID: grant.CommunityServerID, BotAccountID: grant.BotAccountID,
		BotDisplayName: grant.BotDisplayName, SanctionedBotSlotID: grant.SanctionedBotSlotID,
		ObservationAPI: grant.ObservationAPI, ControllerAPI: grant.ControllerAPI,
	}
	return string(document), authorities, expect, now
}

func TestVerifiedPublicAuthorizationIsImmutableAndExpiresAtUse(t *testing.T) {
	document, authorities, expect, now := signedPublicAuthorizationFixture(t)
	verified, err := verifyPublicAuthorizationAt(strings.NewReader(document), authorities, expect, now)
	if err != nil {
		t.Fatal(err)
	}
	copy, err := verified.grantForUseAt(now)
	if err != nil {
		t.Fatal(err)
	}
	copy.BotAccountID = "mutated"
	copy.MaxActionRateHz = maxPublicActionRateHz
	original, err := verified.grantForUseAt(now)
	if err != nil {
		t.Fatal(err)
	}
	if original.BotAccountID != expect.BotAccountID || original.MaxActionRateHz != 15 {
		t.Fatal("mutating a returned grant changed the verified capability")
	}
	if _, err := verified.grantForUseAt(now.Add(48 * time.Hour)); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired grant use error = %v", err)
	}
}

func TestPublicAuthorizationSignatureBindsKeyID(t *testing.T) {
	document, authorities, expect, now := signedPublicAuthorizationFixture(t)
	var signed SignedPublicAuthorization
	if err := json.Unmarshal([]byte(document), &signed); err != nil {
		t.Fatal(err)
	}
	original := authorities[signed.KeyID]
	signed.KeyID = "relabelled-key"
	authorities[signed.KeyID] = TrustedPublicAuthority{KeyID: signed.KeyID, Issuer: original.Issuer, PublicKey: original.PublicKey}
	tampered, err := json.Marshal(signed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifyPublicAuthorizationAt(strings.NewReader(string(tampered)), authorities, expect, now); err == nil || !strings.Contains(err.Error(), "signature verification failed") {
		t.Fatalf("key relabel error = %v", err)
	}
}

func TestVerifyPublicAuthorizationAcceptsBoundSignedGrant(t *testing.T) {
	document, authorities, expect, now := signedPublicAuthorizationFixture(t)
	verified, err := verifyPublicAuthorizationAt(strings.NewReader(document), authorities, expect, now)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := verified.grantForUseAt(now)
	if err != nil {
		t.Fatal(err)
	}
	if !verified.validAt(now) || grant.BotAccountID != expect.BotAccountID || len(verified.GrantDigest()) != 64 {
		t.Fatalf("verified authorization = %+v", verified)
	}
}

func TestVerifyPublicAuthorizationRejectsTamperingAndWrongDeployment(t *testing.T) {
	document, authorities, expect, now := signedPublicAuthorizationFixture(t)
	tampered := strings.Replace(document, `"max_action_rate_hz":15`, `"max_action_rate_hz":16`, 1)
	if _, err := verifyPublicAuthorizationAt(strings.NewReader(tampered), authorities, expect, now); err == nil || !strings.Contains(err.Error(), "signature verification failed") {
		t.Fatalf("tamper error = %v", err)
	}

	wrong := expect
	wrong.BotAccountID = "different-bot"
	if _, err := verifyPublicAuthorizationAt(strings.NewReader(document), authorities, wrong, now); err == nil || !strings.Contains(err.Error(), "does not match this deployment") {
		t.Fatalf("wrong deployment error = %v", err)
	}
	delete(authorities, "operator-key-2026")
	if _, err := verifyPublicAuthorizationAt(strings.NewReader(document), authorities, expect, now); err == nil || !strings.Contains(err.Error(), "not trusted") {
		t.Fatalf("untrusted key error = %v", err)
	}
}

func TestVerifyPublicAuthorizationRejectsInactiveAndUnsafeGrant(t *testing.T) {
	document, authorities, expect, now := signedPublicAuthorizationFixture(t)
	if _, err := verifyPublicAuthorizationAt(strings.NewReader(document), authorities, expect, now.Add(48*time.Hour)); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expiry error = %v", err)
	}

	var signed SignedPublicAuthorization
	if err := json.Unmarshal([]byte(document), &signed); err != nil {
		t.Fatal(err)
	}
	signed.Grant.OperatorStopRequired = false
	// The constraint check intentionally happens before signature verification,
	// so an unsafe grant is rejected even if its signature is also stale.
	unsafe, err := json.Marshal(signed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifyPublicAuthorizationAt(strings.NewReader(string(unsafe)), authorities, expect, now); err == nil || !strings.Contains(err.Error(), "must explicitly require") {
		t.Fatalf("unsafe grant error = %v", err)
	}
}

func TestVerifyPublicAuthorizationUsesStrictBoundedJSON(t *testing.T) {
	document, authorities, expect, now := signedPublicAuthorizationFixture(t)
	unknown := strings.Replace(document, `"key_id":`, `"unknown":true,"key_id":`, 1)
	if _, err := verifyPublicAuthorizationAt(strings.NewReader(unknown), authorities, expect, now); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown field error = %v", err)
	}
	duplicate := strings.Replace(document, `"key_id":`, `"key_id":"duplicate","key_id":`, 1)
	if _, err := verifyPublicAuthorizationAt(strings.NewReader(duplicate), authorities, expect, now); err == nil || !strings.Contains(err.Error(), "duplicate JSON object key") {
		t.Fatalf("duplicate key error = %v", err)
	}
	caseVariant := strings.Replace(document, `"key_id":`, `"KEY_ID":`, 1)
	if _, err := verifyPublicAuthorizationAt(strings.NewReader(caseVariant), authorities, expect, now); err == nil || !strings.Contains(err.Error(), "non-canonical JSON field") {
		t.Fatalf("case-variant field error = %v", err)
	}
	caseAlias := strings.Replace(document, `"key_id":`, `"KEY_ID":"duplicate","key_id":`, 1)
	if _, err := verifyPublicAuthorizationAt(strings.NewReader(caseAlias), authorities, expect, now); err == nil || !strings.Contains(err.Error(), "non-canonical JSON field") {
		t.Fatalf("case-alias field error = %v", err)
	}
	if _, err := verifyPublicAuthorizationAt(strings.NewReader(document+` {}`), authorities, expect, now); err == nil || !strings.Contains(err.Error(), "multiple JSON values") {
		t.Fatalf("trailing value error = %v", err)
	}
	oversized := strings.Repeat(" ", maxPublicAuthorizationBytes+1)
	if _, err := verifyPublicAuthorizationAt(strings.NewReader(oversized), authorities, expect, now); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversize error = %v", err)
	}
}
