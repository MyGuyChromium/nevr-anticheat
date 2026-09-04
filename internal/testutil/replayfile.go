package testutil

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

// replayLineLayout is the reference timestamp prefix of an .echoreplay line.
const replayLineLayout = "2006/01/02 15:04:05.000"

var sessionIDField = regexp.MustCompile(`"sessionid":"[^"]*"`)

// SplitReplaySessions copies the .echoreplay at src to dst with the second
// half of its lines rewritten as a second session: their "sessionid" becomes
// secondID and their timestamps are shifted later by gap, the way a recorder
// that kept running through a rematch in the same lobby writes one file
// holding two matches. Lines must be in the reference layout
// "YYYY/MM/DD HH:MM:SS.mmm\t{session}" with a sessionid field. It returns
// the number of lines in each half.
func SplitReplaySessions(src, dst, secondID string, gap time.Duration) (first, second int, err error) {
	data, err := os.ReadFile(src)
	if err != nil {
		return 0, 0, err
	}
	lines := strings.Split(strings.TrimRight(string(data), "\r\n"), "\n")
	first = len(lines) / 2
	var out strings.Builder
	for i, line := range lines {
		line = strings.TrimRight(line, "\r")
		if i >= first {
			tab := strings.IndexByte(line, '\t')
			if tab < 0 {
				return 0, 0, fmt.Errorf("line %d: no tab between timestamp and payload", i+1)
			}
			ts, err := time.Parse(replayLineLayout, line[:tab])
			if err != nil {
				return 0, 0, fmt.Errorf("line %d: %w", i+1, err)
			}
			payload := line[tab+1:]
			if !sessionIDField.MatchString(payload) {
				return 0, 0, fmt.Errorf("line %d: no sessionid field", i+1)
			}
			line = ts.Add(gap).Format(replayLineLayout) + "\t" +
				sessionIDField.ReplaceAllLiteralString(payload, `"sessionid":"`+secondID+`"`)
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	if err := os.WriteFile(dst, []byte(out.String()), 0o600); err != nil {
		return 0, 0, err
	}
	return first, len(lines) - first, nil
}
