package recommend

import (
	"regexp"
	"strings"
)

// redactions replace values that look like credentials before log lines
// leave the cluster (ADR-0014, section 5). They err on the side of hiding.
var redactions = []struct {
	re   *regexp.Regexp
	with string
}{
	{regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?(-----END [A-Z ]*PRIVATE KEY-----|$)`), "[redacted private key]"},
	{regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}`), "$1 [redacted]"},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), "[redacted token]"},
	{regexp.MustCompile(`\b(AKIA|ASIA)[A-Z0-9]{16}\b`), "[redacted key]"},
	{regexp.MustCompile(`\b(sk-[A-Za-z0-9_-]{16,}|gh[pousr]_[A-Za-z0-9]{20,}|xox[abpors]-[A-Za-z0-9-]{10,})`), "[redacted key]"},
	{regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^\s/:@]+:[^\s/@]+@`), "${1}[redacted]@"},
	{regexp.MustCompile(`(?i)\b([a-z0-9_.-]*(password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key|credential)s?)(["']?\s*[:=]\s*["']?)[^\s"',;&]+`), "$1$3[redacted]"},
}

// Redact hides credential-looking values in log lines.
func Redact(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		for _, r := range redactions {
			l = r.re.ReplaceAllString(l, r.with)
		}
		out[i] = l
	}
	return out
}

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
