package sqlite

import (
	"bytes"
	"strings"
	"sync"
	"testing"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

func blindFixture(t *testing.T) (*Store, BlindReviewSession, string) {
	t.Helper()
	s := newTestStore(t)
	ctx := t.Context()
	if err := s.StoreMatchContext(ctx, &model.MatchContext{MatchID: "M", PlayerIDs: []string{"P"}}, 4); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StoreTelemetryFrames(ctx, "M", mkFrames("P", 10, 14)); err != nil {
		t.Fatal(err)
	}
	a, err := s.StoreBlindArtifact(ctx, "trial.mp4", []byte("independent synchronized evidence fixture"))
	if err != nil {
		t.Fatal(err)
	}
	candidate := strings.Repeat("a", 64)
	x, err := s.CreateBlindReviewSession(ctx, BlindReviewBinding{MatchID: "M", PlayerID: "P", DetectorID: "THROW_001", Kind: OpportunityThrow, FrameStart: 10, FrameEnd: 12, EvidenceMethod: "synchronized_video", ArtifactSHA256: a.SHA256, LegalContext: "slap"}, candidate)
	if err != nil {
		t.Fatal(err)
	}
	return s, x, candidate
}
func ballotPair(t *testing.T, s *Store, x BlindReviewSession, candidate, first, second string) {
	t.Helper()
	for i, truth := range []string{first, second} {
		reviewer := []string{"Alice", "Bob"}[i]
		out, err := s.SubmitBlindReviewBallot(t.Context(), x.SessionID, candidate, BlindReviewBallot{ReviewerID: reviewer, GroundTruth: truth, Comment: "independent vote", PreRevealAttestation: true})
		if err != nil {
			t.Fatal(err)
		}
		if out.Revealed || len(out.Ballots) != 0 || out.Consensus != "" || out.BallotCount != i+1 {
			t.Fatalf("premature reveal: %+v", out)
		}
	}
}
func TestBlindReviewImmutableBallotsBindActualArtifactWindowAndCandidate(t *testing.T) {
	s, x, candidate := blindFixture(t)
	ctx := t.Context()
	if _, err := s.RevealBlindReview(ctx, x.SessionID, candidate); err == nil {
		t.Fatal("revealed without ballots")
	}
	if _, err := s.SubmitBlindReviewBallot(ctx, x.SessionID, candidate, BlindReviewBallot{ReviewerID: "Alice", GroundTruth: GroundTruthPositive}); err == nil {
		t.Fatal("missing pre-reveal attestation accepted")
	}
	if _, err := s.SubmitBlindReviewBallot(ctx, x.SessionID, "different", BlindReviewBallot{ReviewerID: "Alice", GroundTruth: GroundTruthPositive, PreRevealAttestation: true}); err == nil {
		t.Fatal("changed candidate accepted")
	}
	if _, err := s.SubmitBlindReviewBallot(ctx, x.SessionID, candidate, BlindReviewBallot{ReviewerID: "Alice", GroundTruth: GroundTruthPositive, PreRevealAttestation: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitBlindReviewBallot(ctx, x.SessionID, candidate, BlindReviewBallot{ReviewerID: " ALICE ", GroundTruth: GroundTruthNegative, PreRevealAttestation: true}); err == nil {
		t.Fatal("same reviewer replaced ballot")
	}
	if _, err := s.RevealBlindReview(ctx, x.SessionID, candidate); err == nil {
		t.Fatal("revealed with one ballot")
	}
	if _, err := s.SubmitBlindReviewBallot(ctx, x.SessionID, candidate, BlindReviewBallot{ReviewerID: "Bob", GroundTruth: GroundTruthPositive, PreRevealAttestation: true}); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListBlindReviewSessions(ctx)
	if err != nil || len(list) != 1 || len(list[0].Ballots) != 0 || list[0].Consensus != "" {
		t.Fatalf("list leaked votes: %+v %v", list, err)
	}
	if _, err := s.SubmitBlindReviewBallot(ctx, x.SessionID, candidate, BlindReviewBallot{ReviewerID: "Third", GroundTruth: GroundTruthPositive, PreRevealAttestation: true}); err == nil {
		t.Fatal("third ballot accepted")
	}
	revealed, err := s.RevealBlindReview(ctx, x.SessionID, candidate)
	if err != nil || !revealed.Revealed || revealed.Consensus != GroundTruthPositive || len(revealed.Ballots) != 2 {
		t.Fatalf("reveal: %+v %v", revealed, err)
	}
	opportunities, err := s.ListCalibrationOpportunities(ctx, "M", "")
	if err != nil || len(opportunities) != 1 {
		t.Fatalf("opportunities: %+v %v", opportunities, err)
	}
	o := opportunities[0]
	if ok, legal, err := s.VerifiedBlindOpportunity(ctx, o, candidate); err != nil || !ok || legal != "slap" {
		t.Fatalf("proof: %t %s %v", ok, legal, err)
	}
	if ok, _, _ := s.VerifiedBlindOpportunity(ctx, o, strings.Repeat("b", 64)); ok {
		t.Fatal("changed candidate proof accepted")
	}
	if _, err := s.StoreCalibrationOpportunity(ctx, o); err == nil {
		t.Fatal("bound annotation replaced")
	}
	if err := s.ImportCalibrationOpportunity(ctx, o); err == nil {
		t.Fatal("portable text replaced bound annotation")
	}
	if _, err := s.db.Exec(`UPDATE blind_review_ballots SET ballot_json='{}'`); err == nil {
		t.Fatal("immutable ballot updated")
	}
	if _, err := s.db.Exec(`DELETE FROM blind_review_ballots`); err == nil {
		t.Fatal("immutable ballot deleted")
	}
	if _, err := s.db.Exec(`UPDATE blind_review_sessions SET window_sha256='fake'`); err == nil {
		t.Fatal("bound window replaced")
	}
	frames := mkFrames("P", 10, 14)
	frames[0].Timestamp = 999
	if _, err := s.ReplaceMatchTelemetryFrames(ctx, "M", frames); err != nil {
		t.Fatal(err)
	}
	if ok, _, _ := s.VerifiedBlindOpportunity(ctx, o, candidate); ok {
		t.Fatal("replaced source window retained proof")
	}
	list, err = s.ListBlindReviewSessions(ctx)
	if err != nil || len(list[0].Ballots) != 2 {
		t.Fatal("source change discarded ballot history")
	}
}
func TestBlindReviewDisagreementAndUncertainNeverVerify(t *testing.T) {
	for _, second := range []string{GroundTruthNegative, GroundTruthUncertain} {
		t.Run(second, func(t *testing.T) {
			s, x, c := blindFixture(t)
			ballotPair(t, s, x, c, GroundTruthPositive, second)
			out, err := s.RevealBlindReview(t.Context(), x.SessionID, c)
			if err != nil || out.Consensus != GroundTruthUncertain {
				t.Fatalf("consensus: %+v %v", out, err)
			}
			items, _ := s.ListCalibrationOpportunities(t.Context(), "M", "")
			if len(items) != 1 || items[0].GroundTruth != GroundTruthUncertain {
				t.Fatal("disagreement became decisive")
			}
			if ok, _, _ := s.VerifiedBlindOpportunity(t.Context(), items[0], c); ok {
				t.Fatal("disagreement verified")
			}
		})
	}
}
func TestBlindReviewArtifactBoundsHashAndMissingWindow(t *testing.T) {
	s, x, c := blindFixture(t)
	ctx := t.Context()
	a, payload, err := s.GetBlindArtifact(ctx, x.Binding.ArtifactSHA256)
	if err != nil || a.SizeBytes != len(payload) || digestBytes(payload) != a.SHA256 {
		t.Fatal("artifact bytes did not round trip")
	}
	dup, err := s.StoreBlindArtifact(ctx, "other.mp4", payload)
	if err != nil || dup.SHA256 != a.SHA256 || dup.Filename != a.Filename {
		t.Fatal("dedup failed")
	}
	for _, name := range []string{"../trial.mp4", "C:\\private\\trial.mp4", "https://example.test/clip", "bad\r\nname"} {
		if _, err := s.StoreBlindArtifact(ctx, name, []byte("x")); err == nil {
			t.Fatalf("path accepted: %q", name)
		}
	}
	if _, err := s.StoreBlindArtifact(ctx, "too-large.bin", make([]byte, BlindArtifactMaxBytes+1)); err == nil {
		t.Fatal("oversize artifact accepted")
	}
	for _, interval := range [][2]int{{20, 25}, {0, 10000}, {13, 12}} {
		b := x.Binding
		b.FrameStart, b.FrameEnd = interval[0], interval[1]
		if _, err := s.CreateBlindReviewSession(ctx, b, c); err == nil {
			t.Fatalf("invalid window accepted: %+v", interval)
		}
	}
	b := x.Binding
	b.ArtifactSHA256 = strings.Repeat("b", 64)
	if _, err := s.CreateBlindReviewSession(ctx, b, c); err == nil {
		t.Fatal("free-text hash accepted without artifact")
	}
	ballotPair(t, s, x, c, GroundTruthPositive, GroundTruthPositive)
	if _, err := s.db.Exec(`DROP TRIGGER blind_artifacts_no_update; UPDATE blind_evidence_artifacts SET payload=?`, []byte("tampered")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.GetBlindArtifact(ctx, a.SHA256); err == nil {
		t.Fatal("tampered attachment downloaded")
	}
	if _, err := s.RevealBlindReview(ctx, x.SessionID, c); err == nil {
		t.Fatal("tampered attachment revealed")
	}
}
func TestBlindReviewConcurrentBallotsNeverExceedTwo(t *testing.T) {
	s, x, c := blindFixture(t)
	var wg sync.WaitGroup
	success := make(chan bool, 3)
	for _, reviewer := range []string{"Alice", "Bob", "Carol"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.SubmitBlindReviewBallot(t.Context(), x.SessionID, c, BlindReviewBallot{ReviewerID: reviewer, GroundTruth: GroundTruthPositive, PreRevealAttestation: true})
			success <- err == nil
		}()
	}
	wg.Wait()
	close(success)
	n := 0
	for ok := range success {
		if ok {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("accepted %d ballots", n)
	}
}
func TestBlindReviewLegacyTextAndPortableBindingCannotVerify(t *testing.T) {
	s, x, c := blindFixture(t)
	o := CalibrationOpportunity{MatchID: "M", PlayerID: "P", DetectorID: "THROW_001", Kind: OpportunityThrow, FrameStart: 10, FrameEnd: 12, GroundTruth: GroundTruthPositive, ReviewerID: "Alice", VerifierID: "Bob", VerifiedGroundTruth: GroundTruthPositive, BlindReview: true, EvidenceMethod: "synchronized_video", EvidenceReference: "sha256:" + x.Binding.ArtifactSHA256, ReviewSessionID: x.SessionID}
	if err := s.ImportCalibrationOpportunity(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	items, _ := s.ListCalibrationOpportunities(t.Context(), "M", "")
	if len(items) != 1 || items[0].ReviewSessionID != "" {
		t.Fatal("portable binding was trusted")
	}
	if ok, _, _ := s.VerifiedBlindOpportunity(t.Context(), items[0], c); ok {
		t.Fatal("text attestation verified")
	}
}

func TestBlindReviewOverlapFailureDoesNotRevealOrDiscardBallots(t *testing.T) {
	s, x, c := blindFixture(t)
	if _, err := s.StoreCalibrationOpportunity(t.Context(), CalibrationOpportunity{MatchID: "M", PlayerID: "P", DetectorID: "THROW_001", Kind: OpportunityThrow, FrameStart: 10, FrameEnd: 12, GroundTruth: GroundTruthUncertain}); err != nil {
		t.Fatal(err)
	}
	ballotPair(t, s, x, c, GroundTruthPositive, GroundTruthPositive)
	out, err := s.RevealBlindReview(t.Context(), x.SessionID, c)
	if err == nil || out.Revealed || len(out.Ballots) > 0 || out.Consensus != "" {
		t.Fatalf("overlap revealed a failed transaction: %+v %v", out, err)
	}
	sessions, err := s.ListBlindReviewSessions(t.Context())
	if err != nil || len(sessions) != 1 || sessions[0].BallotCount != 2 || sessions[0].Revealed {
		t.Fatal("failed reveal changed durable state")
	}
}
func TestBlindArtifactCapacityIsBounded(t *testing.T) {
	s := newTestStore(t)
	for i := 0; i < BlindArtifactMaxCount; i++ {
		if _, err := s.StoreBlindArtifact(t.Context(), "small.bin", []byte{byte(i)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.StoreBlindArtifact(t.Context(), "overflow.bin", []byte{255}); err == nil {
		t.Fatal("artifact count limit bypassed")
	}
	inv, err := s.ListBlindArtifacts(t.Context())
	if err != nil || len(inv.Artifacts) != BlindArtifactMaxCount || inv.UsedBytes != BlindArtifactMaxCount {
		t.Fatalf("inventory: %+v %v", inv, err)
	}
	// Payload hashes, not filenames, govern deduplication.
	if _, err := s.StoreBlindArtifact(t.Context(), "same.bin", bytes.Repeat([]byte{0}, 1)); err != nil {
		t.Fatal("duplicate at capacity should succeed")
	}
}
