package evidence

import (
	"fmt"
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
	b.WriteString("\n")

	b.WriteString(fmt.Sprintf("SEVERITY: %s\n", strings.ToUpper(rc.Severity)))
	b.WriteString(fmt.Sprintf("SUSPICION SCORE: %.1f / 100\n", rc.SuspicionScore))
	b.WriteString(fmt.Sprintf("RECOMMENDED ACTION: %s\n", rc.RecommendedAction))
	b.WriteString("\n")

	b.WriteString("================================================================================\n")
	b.WriteString("DETECTION TIMELINE\n")
	b.WriteString("================================================================================\n\n")

	for _, td := range rc.DetectorsTriggered {
		b.WriteString(fmt.Sprintf("  [%s] %s\n", td.DetectorID, td.DetectorID))
		b.WriteString(fmt.Sprintf("              %s\n", td.Details))
		b.WriteString(fmt.Sprintf("              Confidence: %.2f\n", td.Confidence))
		b.WriteString(fmt.Sprintf("              Severity: %s\n", td.Severity))
		b.WriteString("\n")
	}

	b.WriteString("================================================================================\n")
	b.WriteString(fmt.Sprintf("EXPLANATION: %s\n", rc.Explanation))
	b.WriteString("================================================================================\n")

	return b.String()
}
