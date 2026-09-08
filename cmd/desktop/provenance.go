package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/replay"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

// applyActiveProfile applies a previously selected detector profile before the
// runtime starts. Profiles deliberately contain detector settings only: game
// physics and score boundaries stay controlled by the reviewed TOML config.
func applyActiveProfile(engine *replay.Engine) {
	profile, ok, err := engine.Store().GetActiveConfigProfile(context.Background())
	if err != nil {
		engine.Logger().Warn("could not read active detector profile", "error", err)
		return
	}
	if !ok {
		return
	}
	var detectors map[string]config.DetectorConfig
	if err := json.Unmarshal([]byte(profile.DetectorsJSON), &detectors); err != nil {
		engine.Logger().Warn("could not decode active detector profile", "profile", profile.Name, "error", err)
		return
	}
	promotions, promotionErr := engine.Store().ListDetectorPromotions(context.Background())
	approvedID := ""
	approvalValid := promotionErr == nil
	for _, promotion := range promotions {
		if promotion.Status != sqlite.PromotionActive || promotion.ProfileName != profile.Name {
			continue
		}
		if approvedID != "" || promotion.ConfigFingerprint != shortHash([]byte(profile.DetectorsJSON)) {
			approvalValid = false
			break
		}
		approvedID = promotion.DetectorID
	}
	approvalValid = approvalValid && approvedID != ""
	for id, detector := range detectors {
		// Review mode is honored only for the one detector/profile approved by
		// the promotion gate. Imported and hand-saved profiles fail closed.
		approved := approvalValid && id == approvedID
		if detector.Mode != "review" || !approved {
			detector.Mode = "shadow"
			detector.EnforcementWeight = 0
		}
		detector.AutoEnforce = false
		detectors[id] = detector
	}
	candidate := cloneConfig(engine.Config())
	candidate.Detectors = detectors
	if err := config.Validate(candidate); err != nil {
		engine.Logger().Warn("active detector profile is invalid; using configured defaults", "profile", profile.Name, "error", err)
		return
	}
	engine.Config().Detectors = detectors
}

func (s *server) activeProfileName(ctx context.Context) string {
	profile, ok, err := s.engine.Store().GetActiveConfigProfile(ctx)
	if err != nil || !ok {
		return "default"
	}
	return profile.Name
}

func recordAnalysisResults(ctx context.Context, engine *replay.Engine, results []*replay.AnalyzeResult, source string, wall time.Duration) {
	if len(results) == 0 {
		return
	}
	profileName := "default"
	if profile, ok, err := engine.Store().GetActiveConfigProfile(ctx); err == nil && ok {
		profileName = profile.Name
	}
	fingerprint := displayConfigFingerprint(engine.Config())
	calibrationConfigFingerprint := calibrationFingerprint(engine.Config())
	executableHash, hashErr := runningExecutableSHA256()
	if hashErr != nil {
		// Preserve the successful analysis while explicitly recording missing
		// identity. Never stamp a guessed hash; bound review will fail closed.
		executableHash = ""
		engine.Logger().Error("could not identify executable for analysis provenance; bound review unavailable", "error", hashErr)
	}
	perMatchWall := wall.Milliseconds() / int64(len(results))
	for _, result := range results {
		if result == nil || result.Result == nil || result.MatchCtx == nil || result.PersistError() != nil {
			continue
		}
		quality := result.Result.TelemetryQuality
		_, err := engine.Store().StoreAnalysisRun(ctx, sqlite.AnalysisRun{
			MatchID: result.MatchCtx.MatchID, Source: strings.TrimSpace(source), AppVersion: appVersion,
			BuildCommit: analysisBuildRevision(), ExecutableSHA256: executableHash, ConfigFingerprint: fingerprint, CalibrationFingerprint: calibrationConfigFingerprint, ProfileName: profileName,
			TelemetryQuality: quality.Score, QualityGrade: quality.Grade, QualityGated: quality.Gated,
			WallMilliseconds: perMatchWall, PipelineMilliseconds: result.Result.Duration.Milliseconds(),
			FramesProcessed: result.Frames, EventsProduced: len(result.Result.DetectionEvents),
		})
		if err != nil {
			engine.Logger().Warn("could not record analysis provenance", "match_id", result.MatchCtx.MatchID, "error", err)
		}
	}
}

// calibrationFingerprint identifies settings that can change detector output
// while deliberately excluding score/review posture. Moving an unchanged
// threshold from shadow to review therefore does not invalidate its evidence.
func calibrationFingerprint(cfg *config.Config) string {
	type behaviorParam struct {
		Key   string `json:"key"`
		Value any    `json:"value"`
	}
	type behaviorDetector struct {
		ID      string          `json:"id"`
		Enabled bool            `json:"enabled"`
		Params  []behaviorParam `json:"params"`
	}
	detectors := make([]behaviorDetector, 0, len(cfg.Detectors))
	for _, row := range cfg.EffectiveTable() {
		item := behaviorDetector{ID: row.ID, Enabled: row.Enabled, Params: make([]behaviorParam, 0, len(row.Params))}
		for _, param := range row.Params {
			item.Params = append(item.Params, behaviorParam{Key: param.Key, Value: param.Value})
		}
		detectors = append(detectors, item)
	}
	data, _ := json.Marshal(struct {
		Physics   any                   `json:"physics"`
		Pipeline  config.PipelineConfig `json:"pipeline"`
		Detectors []behaviorDetector    `json:"detectors"`
	}{cfg.Physics.Constants(), cfg.Pipeline, detectors})
	return shortHash(data)
}

func shortHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:12])
}
