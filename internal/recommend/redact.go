package recommend

import (
	"strings"

	"github.com/kubexray/kubexray/internal/redact"
)

// Redact hides credential-looking values in log lines.
func Redact(lines []string) []string { return redact.Lines(lines) }

// maxLogLines and maxLogBytes bound the log tail sent with a draft.
const (
	maxLogLines = 50
	maxLogBytes = 8 << 10
)

// Tail keeps the last lines of a log, within the limits, redacted.
func Tail(log string) []string {
	lines := strings.Split(strings.TrimRight(log, "\n"), "\n")
	if len(lines) > maxLogLines {
		lines = lines[len(lines)-maxLogLines:]
	}
	size := 0
	for i := len(lines) - 1; i >= 0; i-- {
		size += len(lines[i]) + 1
		if size > maxLogBytes {
			lines = lines[i+1:]
			break
		}
	}
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	return Redact(lines)
}
