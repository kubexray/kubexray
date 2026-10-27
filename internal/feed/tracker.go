package feed

import (
	"sync"
	"time"

	"github.com/kubexray/kubexray/internal/graph"
)

// Window is how far apart entries of one set may be.
const Window = 30 * time.Second

// SettleWindow is how long a set may take to settle before it's Stuck, and
// how soon an incident must end for a set to have Fixed it.
const SettleWindow = 10 * time.Minute

// MaxSets is how many sets a Tracker keeps (about a week of a busy cluster).
const MaxSets = 10000

// Tracker builds the feed. It is safe for concurrent use.
type Tracker struct {
	mu      sync.Mutex
	sets    []*Set
	limit   int
	nextID  int
	ongoing map[string]bool // apps with an incident at the last assessment
	// applied is each managed object as its tool last applied it (ADR-0019).
	applied map[string]graph.Node
}

// New returns a tracker that keeps up to limit sets.
func New(limit int) *Tracker {
	return &Tracker{limit: limit, ongoing: map[string]bool{}, applied: map[string]graph.Node{}}
}
