// Package regression runs private replay contracts through the production
// replay engine. A captured behavior baseline is never a ground-truth label.
package regression

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/config"
	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
	"github.com/nevr-anticheat/nevr-anticheat/internal/replay"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

const Schema = "nevr-private-replay-regression/v1"

type Manifest struct {
	Schema       string       `json:"schema"`
	CreatedUTC   time.Time    `json:"created_utc"`
	Revision     string       `json:"revision"`
	ConfigSHA256 string       `json:"config_sha256"`
	Cases        []ReplayCase `json:"cases"`
}

type ReplayCase struct {
	ID       string     `json:"id"`
	Path     string     `json:"path"`
	SHA256   string     `json:"sha256"`
	Expected []Snapshot `json:"expected"`
	Windows  []Window   `json:"windows,omitempty"`
}

// Signal deliberately excludes random event IDs and heuristic confidence.
// Its identity records observable detector behavior, not verified cheating.
type Signal struct {
	MatchID     string `json:"match_id"`
	PlayerID    string `json:"player_id"`
	DetectorID  string `json:"detector_id"`
	Frame       int    `json:"frame"`
	Start       int    `json:"start"`
	End         int    `json:"end"`
	Anomaly     string `json:"anomaly"`
	Shadow      bool   `json:"shadow"`
	AutoEnforce bool   `json:"auto_enforce"`
}

type Snapshot struct {
	MatchID      string         `json:"match_id"`
	Frames       int            `json:"frames"`
	MaxFrame     int            `json:"max_frame"`
	PlayerFrames map[string]int `json:"player_frames"`
	Signals      []Signal       `json:"signals"`
}

// Provenance makes independently reviewed evidence distinguishable from
// uncertain observations, self-report, and generated fixture expectations.
// Confirmed is an operator assertion backed by named reviewers and pinned
// artifacts; this tool cannot itself establish that those assertions are true.
type Provenance struct {
	Kind      string     `json:"kind"`  // behavior_only, uncertain, user_reported, confirmed, synthetic
	Truth     string     `json:"truth"` // unknown, positive, negative
	Reviewers []string   `json:"reviewers,omitempty"`
	Artifacts []Artifact `json:"artifacts,omitempty"`
	Note      string     `json:"note,omitempty"`
}

type Artifact struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// Window evaluates incidents whose causal frame range overlaps the inclusive
// window. Expectation controls the regression assertion independently of Truth.
// Use observe for an unresolved example, signal to catch a miss, and quiet to
// catch an unwanted firing. Labels never change detector inputs or settings.
type Window struct {
	ID          string     `json:"id"`
	MatchID     string     `json:"match_id"`
	PlayerID    string     `json:"player_id"`
	DetectorID  string     `json:"detector_id"`
	Start       int        `json:"start"`
	End         int        `json:"end"`
	Expectation string     `json:"expectation"` // signal, quiet, observe
	Provenance  Provenance `json:"provenance"`
}

type WindowResult struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Signals int    `json:"signals"`
	Samples int    `json:"samples"`
	// Samples counts stored player rows, not usable detector opportunities.
	OpportunityCoverage string `json:"opportunity_coverage"`
	Assertion           string `json:"assertion"`
	Passed              bool   `json:"passed"`
	Outcome             string `json:"outcome"`
	Expectation         string `json:"expectation"`
}

type CaseResult struct {
	ID               string            `json:"id"`
	SourceSHA256     string            `json:"source_sha256"`
	Passed           bool              `json:"passed"`
	StructureMatch   bool              `json:"structure_match"`
	Actual           []Snapshot        `json:"actual"`
	Missing          []Signal          `json:"missing_signals"`
	Unexpected       []Signal          `json:"unexpected_signals"`
	Windows          []WindowResult    `json:"windows"`
	DetectorVersions map[string]string `json:"detector_versions"`
	Quality          map[string]string `json:"quality_grade_by_match"`
	WindowSamples    map[string]int    `json:"window_samples,omitempty"`
	Errors           []string          `json:"errors"`
	DurationMS       int64             `json:"duration_ms"`
	SourceBytes      int64             `json:"source_bytes"`
}

type Report struct {
	Schema       string       `json:"schema"`
	GeneratedUTC time.Time    `json:"generated_utc"`
	Revision     string       `json:"revision"`
	Dirty        bool         `json:"dirty_build"`
	GoVersion    string       `json:"go_version"`
	ConfigSHA256 string       `json:"config_sha256"`
	Passed       bool         `json:"passed"`
	Cases        []CaseResult `json:"cases"`
	Notice       string       `json:"notice"`
}

func revision() (string, bool) {
	rev, dirty := "unknown", false
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				rev = setting.Value
			case "vcs.modified":
				dirty = setting.Value == "true"
			}
		}
	}
	return rev, dirty
}

// ConfigFingerprint pins all configuration except destination/logging/server
// settings, which do not affect this isolated offline production pipeline.
func ConfigFingerprint(cfg *config.Config) (string, error) {
	if cfg == nil {
		return "", errors.New("configuration is required")
	}
	doc, err := json.Marshal(struct {
		Physics      config.PhysicsConfig
		ProjectRules model.ProjectRules
		Pipeline     config.PipelineConfig
		Scoring      config.ScoringConfig
		Detectors    map[string]config.DetectorConfig
		Shadow       config.ShadowConfig
	}{cfg.Physics, cfg.ProjectRules, cfg.Pipeline, cfg.Scoring, cfg.Detectors, cfg.Shadow})
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(doc)
	return hex.EncodeToString(hash[:]), nil
}

func FileSHA256(path string) (string, error) {
	return fileSHA256(context.Background(), path)
}

func fileSHA256(ctx context.Context, path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("not a regular file: %s", path)
	}
	hash := sha256.New()
	if _, err := copyWithContext(ctx, hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// Copy in bounded chunks so cancelling a multi-gigabyte preflight/staging read
// does not have to wait for the whole file. Original recordings are read-only.
func copyWithContext(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	buf := make([]byte, 64<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, err := src.Read(buf)
		if n > 0 {
			if stop := ctx.Err(); stop != nil {
				return total, stop
			}
			written, writeErr := dst.Write(buf[:n])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != n {
				return total, io.ErrShortWrite
			}
		}
		if err == io.EOF {
			return total, nil
		}
		if err != nil {
			return total, err
		}
	}
}

func validHash(hash string) bool {
	b, err := hex.DecodeString(hash)
	return err == nil && len(b) == sha256.Size
}

func ResolvePath(base, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(base, path)
}

// LoadManifest rejects misspelled keys and trailing documents rather than
// silently skipping an assertion. Paths are resolved relative to the manifest.
func LoadManifest(path string) (Manifest, error) {
	file, err := os.Open(path)
	if err != nil {
		return Manifest{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Manifest{}, err
	}
	if info.Size() > 32<<20 {
		return Manifest{}, errors.New("manifest exceeds 32 MiB limit")
	}
	decoder := json.NewDecoder(io.LimitReader(file, 32<<20))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return manifest, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return manifest, errors.New("manifest contains trailing data")
	}
	return manifest, manifest.Validate()
}

func (manifest Manifest) Validate() error {
	if manifest.Schema != Schema {
		return fmt.Errorf("unsupported manifest schema %q", manifest.Schema)
	}
	if !validHash(manifest.ConfigSHA256) {
		return errors.New("manifest requires a configuration SHA-256")
	}
	if len(manifest.Cases) == 0 {
		return errors.New("manifest has no replay cases")
	}
	caseIDs := make(map[string]bool)
	for _, item := range manifest.Cases {
		if item.ID == "" || caseIDs[item.ID] {
			return errors.New("case IDs must be nonempty and unique")
		}
		caseIDs[item.ID] = true
		if item.Path == "" || !validHash(item.SHA256) || len(item.Expected) == 0 {
			return fmt.Errorf("case %s needs a replay path, SHA-256 and expected matches", item.ID)
		}
		matches := make(map[string]Snapshot)
		for _, match := range item.Expected {
			if _, exists := matches[match.MatchID]; exists || match.MatchID == "" || match.Frames <= 0 || match.MaxFrame < 0 || len(match.PlayerFrames) == 0 {
				return fmt.Errorf("case %s has an invalid match baseline", item.ID)
			}
			matches[match.MatchID] = match
			for player, frames := range match.PlayerFrames {
				if strings.TrimSpace(player) == "" || frames <= 0 {
					return fmt.Errorf("case %s has an invalid player frame baseline", item.ID)
				}
			}
			for _, signal := range match.Signals {
				if signal.MatchID != match.MatchID || match.PlayerFrames[signal.PlayerID] <= 0 || signal.DetectorID == "" || signal.Start < 0 || signal.End < signal.Start || signal.Frame < 0 || signal.End > match.MaxFrame || signal.Frame > match.MaxFrame {
					return fmt.Errorf("case %s has an invalid signal baseline", item.ID)
				}
				if _, ok := config.DetectorSpecFor(signal.DetectorID); !ok {
					return fmt.Errorf("case %s has an unknown baseline detector", item.ID)
				}
			}
		}
		windowIDs := make(map[string]bool)
		for _, window := range item.Windows {
			match, ok := matches[window.MatchID]
			if !ok || window.ID == "" || windowIDs[window.ID] || window.PlayerID == "" || match.PlayerFrames[window.PlayerID] <= 0 || window.Start < 0 || window.End < window.Start || window.End > match.MaxFrame {
				return fmt.Errorf("case %s has an invalid or unobservable window %q", item.ID, window.ID)
			}
			windowIDs[window.ID] = true
			if _, ok := config.DetectorSpecFor(window.DetectorID); !ok {
				return fmt.Errorf("window %s has unknown detector", window.ID)
			}
			if window.Expectation != "signal" && window.Expectation != "quiet" && window.Expectation != "observe" {
				return fmt.Errorf("window %s has invalid expectation", window.ID)
			}
			p := window.Provenance
			if p.Truth != "unknown" && p.Truth != "positive" && p.Truth != "negative" {
				return fmt.Errorf("window %s needs explicit truth provenance", window.ID)
			}
			switch p.Kind {
			case "behavior_only", "uncertain":
				if p.Truth != "unknown" {
					return fmt.Errorf("window %s cannot infer truth from behavior or uncertainty", window.ID)
				}
			case "synthetic", "user_reported":
			case "confirmed":
				if (p.Truth == "positive" && window.Expectation == "quiet") || (p.Truth == "negative" && window.Expectation == "signal") {
					return fmt.Errorf("confirmed window %s has an expectation contradicting its ground truth; use observe to measure current behavior", window.ID)
				}
				reviewers := make(map[string]bool)
				for _, reviewer := range p.Reviewers {
					id := strings.ToLower(strings.TrimSpace(reviewer))
					if id != "" && id != "local-owner" {
						reviewers[id] = true
					}
				}
				if p.Truth == "unknown" || len(reviewers) < 2 || len(p.Artifacts) == 0 || strings.TrimSpace(p.Note) == "" {
					return fmt.Errorf("confirmed window %s needs decisive truth, two distinct reviewers, pinned evidence and a note", window.ID)
				}
			default:
				return fmt.Errorf("window %s has invalid provenance kind", window.ID)
			}
			for _, artifact := range p.Artifacts {
				if artifact.Path == "" || !validHash(artifact.SHA256) {
					return fmt.Errorf("window %s has unpinned evidence", window.ID)
				}
			}
		}
	}
	return nil
}

func newReport(cfg *config.Config) (Report, error) {
	hash, err := ConfigFingerprint(cfg)
	if err != nil {
		return Report{}, err
	}
	rev, dirty := revision()
	return Report{Schema: Schema, GeneratedUTC: time.Now().UTC(), Revision: rev, Dirty: dirty,
		GoVersion: runtime.Version(), ConfigSHA256: hash, Cases: []CaseResult{},
		Notice: "Private behavior regression through the production replay engine. Stored player samples are not valid detector opportunities; opportunity coverage remains unresolved even for reviewer-confirmed labels. Passed reports assert configured signal/quiet behavior, never true/false positive/negative accuracy. No labels affect detection or authorize enforcement. Correlated windows are not independent accuracy samples."}, nil
}

// Capture freezes current behavior for every source without inventing labels.
// runDir must not exist. Original replays are only read; each production run
// uses a verified staging copy and a new database, never the user's database.
func Capture(ctx context.Context, cfg *config.Config, paths []string, runDir string) (Manifest, Report, error) {
	report, err := newReport(cfg)
	if err != nil {
		return Manifest{}, report, err
	}
	manifest := Manifest{Schema: Schema, CreatedUTC: report.GeneratedUTC, Revision: report.Revision, ConfigSHA256: report.ConfigSHA256, Cases: []ReplayCase{}}
	if len(paths) == 0 {
		return manifest, report, errors.New("at least one replay path is required")
	}
	items := make([]ReplayCase, 0, len(paths))
	for i, path := range paths {
		if err := ctx.Err(); err != nil {
			return manifest, report, err
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return manifest, report, err
		}
		hash, err := fileSHA256(ctx, abs)
		if err != nil {
			return manifest, report, err
		}
		items = append(items, ReplayCase{ID: fmt.Sprintf("replay-%03d", i+1), Path: abs, SHA256: hash})
	}
	if err := os.Mkdir(runDir, 0o700); err != nil {
		return manifest, report, fmt.Errorf("run directory must be new: %w", err)
	}
	report.Passed = true
	for i, item := range items {
		result, err := runCase(ctx, cfg, item, item.Path, filepath.Join(runDir, fmt.Sprintf("%03d", i+1)))
		report.Cases = append(report.Cases, result)
		if err != nil {
			report.Passed = false
			return manifest, report, err
		}
		item.Expected = result.Actual
		manifest.Cases = append(manifest.Cases, item)
	}
	err = manifest.Validate()
	if err != nil {
		report.Passed = false
	}
	return manifest, report, err
}

// Check refuses changed replays/configuration/evidence before creating runDir.
// A hash-verified staging copy prevents concurrent edits of the source file
// from changing the bytes the pipeline analyzes after verification.
func Check(ctx context.Context, cfg *config.Config, manifest Manifest, baseDir, runDir string) (Report, error) {
	report, err := newReport(cfg)
	if err != nil {
		return report, err
	}
	if err := manifest.Validate(); err != nil {
		return report, err
	}
	if report.ConfigSHA256 != manifest.ConfigSHA256 {
		return report, errors.New("configuration fingerprint differs from pinned baseline; capture a separate candidate manifest instead of silently accepting it")
	}
	for _, item := range manifest.Cases {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		if err := verifyFile(ctx, ResolvePath(baseDir, item.Path), item.SHA256); err != nil {
			return report, fmt.Errorf("case %s: %w", item.ID, err)
		}
		for _, window := range item.Windows {
			for _, artifact := range window.Provenance.Artifacts {
				if err := verifyFile(ctx, ResolvePath(baseDir, artifact.Path), artifact.SHA256); err != nil {
					return report, fmt.Errorf("window %s: %w", window.ID, err)
				}
			}
		}
	}
	if err := os.Mkdir(runDir, 0o700); err != nil {
		return report, fmt.Errorf("run directory must be new: %w", err)
	}
	report.Passed = true
	for i, item := range manifest.Cases {
		result, err := runCase(ctx, cfg, item, ResolvePath(baseDir, item.Path), filepath.Join(runDir, fmt.Sprintf("%03d", i+1)))
		if err == nil {
			compareCase(&result, item)
		} else {
			result.Passed = false
		}
		report.Cases = append(report.Cases, result)
		report.Passed = report.Passed && result.Passed
		if err != nil {
			return report, err
		}
	}
	return report, nil
}

func verifyFile(ctx context.Context, path, expected string) error {
	hash, err := fileSHA256(ctx, path)
	if err != nil {
		return err
	}
	if !strings.EqualFold(hash, expected) {
		return fmt.Errorf("SHA-256 mismatch: %s", path)
	}
	return nil
}

func runCase(ctx context.Context, cfg *config.Config, item ReplayCase, source, dir string) (out CaseResult, retErr error) {
	started := time.Now()
	out = CaseResult{ID: item.ID, SourceSHA256: item.SHA256, Passed: true, StructureMatch: true,
		Actual: []Snapshot{}, Missing: []Signal{}, Unexpected: []Signal{}, Windows: []WindowResult{},
		DetectorVersions: map[string]string{}, Quality: map[string]string{}, WindowSamples: map[string]int{}, Errors: []string{}}
	defer func() {
		out.DurationMS = time.Since(started).Milliseconds()
		if retErr != nil {
			out.Passed = false
			out.Errors = append(out.Errors, retErr.Error())
		}
	}()
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return out, err
	}
	input, err := os.Open(source)
	if err != nil {
		return out, err
	}
	defer input.Close()
	stagePath := filepath.Join(dir, "source"+filepath.Ext(source))
	stage, err := os.OpenFile(stagePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return out, err
	}
	// This exact file was created above inside a newly created run directory.
	defer os.Remove(stagePath)
	hash := sha256.New()
	copied, copyErr := copyWithContext(ctx, io.MultiWriter(stage, hash), input)
	out.SourceBytes = copied
	closeErr := stage.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return out, err
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), item.SHA256) {
		return out, errors.New("source changed while staging; refused to analyze unpinned bytes")
	}
	store, err := sqlite.NewStore(filepath.Join(dir, "regression.db"))
	if err != nil {
		return out, err
	}
	defer store.Close()
	engineCfg := *cfg
	engineCfg.General.DBPath = store.Path()
	engineCfg.General.LogLevel = "error"
	engine := replay.NewEngine(&engineCfg, store)
	results, err := engine.AnalyzeFileAll(ctx, stagePath, false)
	if err != nil {
		return out, err
	}
	if len(results) == 0 {
		return out, errors.New("replay produced no matches")
	}
	for _, result := range results {
		if result.AlreadyStored || result.Result == nil || result.MatchCtx == nil {
			return out, errors.New("replay did not produce a complete analysis")
		}
		if err := result.PersistError(); err != nil {
			return out, err
		}
		id := result.MatchCtx.MatchID
		maxFrame, err := store.GetMaxFrameIndex(ctx, id)
		if err != nil {
			return out, err
		}
		snapshot := Snapshot{MatchID: id, Frames: result.Result.FramesProcessed, MaxFrame: maxFrame,
			PlayerFrames: result.Summary.FramesByPlayer, Signals: []Signal{}}
		for _, event := range result.Result.DetectionEvents {
			snapshot.Signals = append(snapshot.Signals, Signal{id, event.PlayerID, event.DetectorID, event.FrameIndex,
				event.FrameRangeStart, event.FrameRangeEnd, event.CausalKey.AnomalyType, event.IsShadow, event.AutoEnforce})
			out.DetectorVersions[event.DetectorID] = event.DetectorVersion
		}
		sortSignals(snapshot.Signals)
		out.Actual = append(out.Actual, snapshot)
		out.Quality[id] = result.Result.TelemetryQuality.Grade
		// Check actual presence in each declared window. Match-level presence
		// alone cannot turn a period when the player was absent into a miss.
		var windows []Window
		for _, window := range item.Windows {
			if window.MatchID == id {
				windows = append(windows, window)
			}
		}
		if len(windows) > 0 {
			frames, err := store.GetMatchFrames(ctx, id)
			if err != nil {
				return out, err
			}
			for _, frame := range frames {
				for _, window := range windows {
					if frame.PlayerID == window.PlayerID && frame.FrameIndex >= window.Start && frame.FrameIndex <= window.End {
						out.WindowSamples[window.ID]++
					}
				}
			}
		}
	}
	return out, nil
}

func sortSignals(signals []Signal) {
	sort.Slice(signals, func(i, j int) bool {
		a, b := signals[i], signals[j]
		if a.MatchID != b.MatchID {
			return a.MatchID < b.MatchID
		}
		if a.Frame != b.Frame {
			return a.Frame < b.Frame
		}
		if a.PlayerID != b.PlayerID {
			return a.PlayerID < b.PlayerID
		}
		if a.DetectorID != b.DetectorID {
			return a.DetectorID < b.DetectorID
		}
		if a.Start != b.Start {
			return a.Start < b.Start
		}
		if a.End != b.End {
			return a.End < b.End
		}
		if a.Anomaly != b.Anomaly {
			return a.Anomaly < b.Anomaly
		}
		if a.Shadow != b.Shadow {
			return !a.Shadow && b.Shadow
		}
		return !a.AutoEnforce && b.AutoEnforce
	})
}

func compareCase(result *CaseResult, expected ReplayCase) {
	actual := make(map[string]Snapshot)
	for _, match := range result.Actual {
		actual[match.MatchID] = match
	}
	if len(actual) != len(expected.Expected) {
		result.StructureMatch = false
	}
	for _, baseline := range expected.Expected {
		match, ok := actual[baseline.MatchID]
		if !ok || match.Frames != baseline.Frames || match.MaxFrame != baseline.MaxFrame || !reflect.DeepEqual(match.PlayerFrames, baseline.PlayerFrames) {
			result.StructureMatch = false
		}
		counts := make(map[Signal]int)
		for _, signal := range baseline.Signals {
			counts[signal]++
		}
		for _, signal := range match.Signals {
			counts[signal]--
		}
		for signal, count := range counts {
			for i := 0; i < count; i++ {
				result.Missing = append(result.Missing, signal)
			}
			for i := 0; i > count; i-- {
				result.Unexpected = append(result.Unexpected, signal)
			}
		}
		delete(actual, baseline.MatchID)
	}
	for _, match := range actual {
		result.Unexpected = append(result.Unexpected, match.Signals...)
	}
	sortSignals(result.Missing)
	sortSignals(result.Unexpected)
	result.Passed = result.StructureMatch && len(result.Missing) == 0 && len(result.Unexpected) == 0
	for _, window := range expected.Windows {
		count := 0
		for _, match := range result.Actual {
			if match.MatchID != window.MatchID {
				continue
			}
			for _, signal := range match.Signals {
				start, end := signal.Start, signal.End
				if start == 0 && end == 0 {
					start, end = signal.Frame, signal.Frame
				}
				if signal.PlayerID == window.PlayerID && signal.DetectorID == window.DetectorID && end >= window.Start && start <= window.End {
					count++
				}
			}
		}
		check := evaluateWindow(window, count)
		check.Samples = result.WindowSamples[window.ID]
		if check.Samples == 0 {
			check.Passed, check.Outcome = false, "unobservable_no_player_samples"
			check.Assertion = "unobservable_no_player_samples"
		}
		result.Windows = append(result.Windows, check)
		result.Passed = result.Passed && check.Passed
	}
}

func evaluateWindow(window Window, count int) WindowResult {
	out := WindowResult{ID: window.ID, Kind: window.Provenance.Kind, Signals: count,
		Expectation: window.Expectation, Passed: true, Outcome: "observation_only",
		OpportunityCoverage: "unresolved_opportunity_coverage", Assertion: "observation_only"}
	if window.Expectation == "signal" {
		out.Assertion = "expected_signal_present"
	}
	if window.Expectation == "quiet" {
		out.Assertion = "expected_quiet"
	}
	if window.Expectation == "signal" && count == 0 {
		out.Passed, out.Outcome = false, "missing_expected_signal"
		out.Assertion = out.Outcome
	}
	if window.Expectation == "quiet" && count > 0 {
		out.Passed, out.Outcome = false, "unexpected_signal"
		out.Assertion = out.Outcome
	}
	p := window.Provenance
	if p.Kind == "confirmed" {
		// A reviewed label is not evidence that the selected detector was
		// enabled, had its required inputs, or assessed a valid opportunity.
		// Keep the behavioral assertion independent of that unresolved gate.
		out.Outcome = "unresolved_opportunity_coverage"
	} else if p.Kind == "user_reported" && ((p.Truth == "positive" && count == 0) || (p.Truth == "negative" && count > 0)) {
		out.Outcome = "user_reported_contradiction"
	} else if p.Kind == "synthetic" {
		out.Outcome = "synthetic_behavior"
	}
	return out
}

// WriteJSON refuses to overwrite an existing private manifest or report.
func WriteJSON(path string, value any) error {
	doc, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(append(doc, '\n'))
	return errors.Join(writeErr, file.Close())
}
