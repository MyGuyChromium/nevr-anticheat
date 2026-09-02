package evidence

import (
	"fmt"
	"sort"
	"strings"

	"github.com/nevr-anticheat/nevr-anticheat/internal/model"
)

// FormatReport generates a human-readable moderator report.
func FormatReport(rc model.ReviewCase) string {
	var b strings.Builder

	b.WriteString("================================================================================\n")
	b.WriteString(fmt.Sprintf("                    ANTICHEAT REVIEW CASE: %s\n", rc.CaseID))
	b.WriteString("================================================================================\n\n")

	b.WriteString("PLAYER SUMMARY\n")
	b.WriteString(fmt.Sprintf("  Player ID:      %s\n", rc.PlayerID))
	b.WriteString(fmt.Sprintf("  Match ID:       %s\n", rc.MatchID))
	if !rc.TimestampStart.IsZero() {
		b.WriteString(fmt.Sprintf("  Match start:    %s\n", rc.TimestampStart.UTC().Format("2006-01-02 15:04:05Z")))
	}
	b.WriteString(fmt.Sprintf("  Status:         %s\n", rc.Status))
	if rc.AssignedTo != "" {
		b.WriteString(fmt.Sprintf("  Assigned to:    %s\n", rc.AssignedTo))
	}
	if rc.ThresholdVersion != "" {
		b.WriteString(fmt.Sprintf("  Thresholds:     %s\n", rc.ThresholdVersion))
	}
	b.WriteString("\n")

	b.WriteString(fmt.Sprintf("SEVERITY: %s\n", strings.ToUpper(rc.Severity)))
	b.WriteString(fmt.Sprintf("SUSPICION SCORE: %.1f / 100", rc.SuspicionScore))
	if rc.Level != "" {
		b.WriteString(fmt.Sprintf(" (%s)", rc.Level))
	}
	b.WriteString("\n")
	b.WriteString(fmt.Sprintf("RECOMMENDED ACTION: %s\n", rc.RecommendedAction))
	b.WriteString("\n")

	b.WriteString("================================================================================\n")
	b.WriteString("DETECTION TIMELINE\n")
	b.WriteString("================================================================================\n\n")

	counts := make(map[string]int)
	for _, td := range rc.DetectorsTriggered {
		counts[td.DetectorID]++
		name := td.DetectorName
		if name == "" || name == td.DetectorID {
			name = td.DetectorID
		}
		b.WriteString(fmt.Sprintf("  [%s] %s", td.DetectorID, name))
		if td.DetectorVersion != "" {
			b.WriteString(fmt.Sprintf(" v%s", td.DetectorVersion))
		}
		b.WriteString("\n")
		b.WriteString(fmt.Sprintf("              t=%s  frame %d", formatMatchTime(td.Timestamp), td.FrameIndex))
		if td.FrameRangeEnd > td.FrameRangeStart {
			b.WriteString(fmt.Sprintf(" (frames %d-%d)", td.FrameRangeStart, td.FrameRangeEnd))
		}
		b.WriteString("\n")
		if td.Details != "" {
			b.WriteString(fmt.Sprintf("              Observed: %s\n", td.Details))
		}
		if td.ExpectedRange != "" {
			b.WriteString(fmt.Sprintf("              Expected: %s\n", td.ExpectedRange))
		}
		b.WriteString(fmt.Sprintf("              Confidence: %.2f\n", td.Confidence))
		b.WriteString(fmt.Sprintf("              Severity: %s\n", td.Severity))
		if td.EventID != "" {
			b.WriteString(fmt.Sprintf("              Event: %s\n", td.EventID))
		}
		b.WriteString("\n")
	}

	if len(counts) > 0 {
		ids := make([]string, 0, len(counts))
		for id := range counts {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		b.WriteString("DETECTOR SUMMARY\n")
		for _, id := range ids {
			b.WriteString(fmt.Sprintf("  %-12s x%d\n", id, counts[id]))
		}
		b.WriteString("\n")
	}

	b.WriteString("================================================================================\n")
	b.WriteString(fmt.Sprintf("EXPLANATION: %s\n", rc.Explanation))
	b.WriteString("================================================================================\n")

	return b.String()
}

// formatMatchTime renders match-relative seconds as m:ss.mmm.
func formatMatchTime(sec float64) string {
	if sec < 0 {
		sec = 0
	}
	m := int(sec) / 60
	s := sec - float64(m*60)
	return fmt.Sprintf("%d:%06.3f", m, s)
}
