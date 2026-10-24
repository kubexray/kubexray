// Package replay writes and reads replay files (ADR-0008): a recorded time
// window as gzip-compressed NDJSON, with the cause engine's incidents computed
// at export time, so a static player can show exactly what kubexray saw.
package replay

import (
	"time"

	"github.com/kubexray/kubexray/internal/cause"
	"github.com/kubexray/kubexray/internal/feed"
)

// Ext is the replay file extension.
const Ext = ".kxr"

// Message types that only appear in replay files.
const (
	MessageHeader  = "header"
	MessageChanges = "changes"
	MessageFeed    = "feed"
)

// Limits for reading untrusted files (ADR-0008, section 4).
const (
	MaxUncompressed = 50 << 20
	MaxLine         = 16 << 20
)

// Header is the first line of a replay.
type Header struct {
	V          int       `json:"v"`
	Type       string    `json:"type" tstype:"\"header\""`
	From       time.Time `json:"from"`
	To         time.Time `json:"to"`
	Cluster    string    `json:"cluster"`
	Anonymized bool      `json:"anonymized"`
	Kubexray   string    `json:"kubexray"` // version that wrote it
}

// FeedMessage is the change feed for the window (ADR-0012), before the
// changes line.
type FeedMessage struct {
	V    int        `json:"v"`
	Type string     `json:"type" tstype:"\"feed\""`
	Sets []feed.Set `json:"sets"`
}

// ChangesMessage is the last line: who changed what in the window.
type ChangesMessage struct {
	V       int            `json:"v"`
	Type    string         `json:"type" tstype:"\"changes\""`
	Changes []cause.Change `json:"changes"`
}
