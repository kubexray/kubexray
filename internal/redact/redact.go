// Package redact hides values that look like credentials, for log lines
// sent to a model (ADR-0014) and values shown in diffs (ADR-0017). It errs on
// the side of hiding.
package redact

import (
	"regexp"
	"strings"
)

var patterns = []struct {
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

// Text hides credential-looking parts of a value and reports whether it
// hid anything.
func Text(s string) (string, bool) {
	out := s
	for _, p := range patterns {
		out = p.re.ReplaceAllString(out, p.with)
	}
	return out, out != s
}

// Lines hides credential-looking parts of each line.
func Lines(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i], _ = Text(l)
	}
	return out
}

var sensitive = regexp.MustCompile(`(?i)(password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|credential|auth)`)

// SensitiveName reports whether a name (an environment variable, a
// ConfigMap key) looks like it holds a credential, whatever its value.
func SensitiveName(name string) bool {
	return sensitive.MatchString(strings.ReplaceAll(name, ".", "_"))
}
