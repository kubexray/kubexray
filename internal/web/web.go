// Package web holds the UI, built by Vite into dist and embedded into the
// binary (ADR-0007). Without a build (Go-only development), a placeholder
// page is served instead.
package web

import (
	"embed"
	"io/fs"
)

var (
	//go:embed all:dist
	dist embed.FS
	//go:embed placeholder
	placeholder embed.FS
)

// Assets returns the UI files: the embedded build, or the placeholder.
func Assets() fs.FS {
	if sub, err := fs.Sub(dist, "dist"); err == nil {
		if _, err := fs.Stat(sub, "index.html"); err == nil {
			return sub
		}
	}
	sub, _ := fs.Sub(placeholder, "placeholder") // embedded at build time, can't fail
	return sub
}
