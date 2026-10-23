package cause

import (
	"cmp"
	"slices"
	"time"
)

// Group is several incidents with the same likely cause (ADR-0008): a
// NotReady node, or a ConfigMap shared by several apps.
type Group struct {
	Cause Cause `json:"cause"`
	// Apps are the affected apps' IDs, most severe first.
	Apps []string `json:"apps"`
}

// GroupIncidents groups incidents whose top cause is the same change at
// confidence likely or better. Possible causes never group: a coincidence
// must not merge unrelated incidents. Only groups of two or more are
// returned, largest first.
func GroupIncidents(incidents []Incident) []Group {
	type key struct {
		kind   Kind
		object string
		at     time.Time
	}
	groups := map[key]*Group{}
	for _, inc := range rankBySeverity(incidents) {
		if len(inc.Causes) == 0 || inc.Causes[0].Confidence.rank() < Likely.rank() {
			continue
		}
		c := inc.Causes[0]
		k := key{c.Kind, c.Object, c.At.UTC()}
		g := groups[k]
		if g == nil {
			g = &Group{Cause: c}
			g.Cause.Evidence = nil // per-app evidence stays on each incident
			groups[k] = g
		}
		g.Apps = append(g.Apps, inc.App)
	}
	out := make([]Group, 0, len(groups))
	for _, g := range groups {
		if len(g.Apps) > 1 {
			out = append(out, *g)
		}
	}
	slices.SortFunc(out, func(a, b Group) int {
		return cmp.Or(cmp.Compare(len(b.Apps), len(a.Apps)), cmp.Compare(a.Cause.Object, b.Cause.Object))
	})
	return out
}

// rankBySeverity orders incidents: apps that are down first, then the oldest.
func rankBySeverity(incidents []Incident) []Incident {
	out := slices.Clone(incidents)
	slices.SortStableFunc(out, func(a, b Incident) int {
		down := func(i Incident) int {
			if i.Down() {
				return 0
			}
			return 1
		}
		return cmp.Or(cmp.Compare(down(a), down(b)), a.Since.Compare(b.Since), cmp.Compare(a.App, b.App))
	})
	return out
}
