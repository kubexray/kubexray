// Package version holds build metadata injected at link time via -ldflags.
package version

import "fmt"

// Set via -ldflags "-X github.com/kubexray/kubexray/internal/version.Version=...".
var (
	Version = "dev"
	Commit  = "none"
)

// String returns a human-readable version line.
func String() string {
	return fmt.Sprintf("kubexray %s (%s)", Version, Commit)
}
