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
	var order []key
	for _, inc := range rankBySeverity(incidents) {
		if len(inc.Causes) == 0 || inc.Causes[0].Confidence.rank() < Likely.rank() {
			continue
		}
		c := inc.Causes[0]
		k := key{kind: c.Kind, object: c.Object}
		if c.Kind != KindNode && c.Kind != KindMissing {
			// A change is one moment; a failed node or a missing object is a
			// state, whose time can differ per app (it falls back to when each
			// app broke).
			k.at = c.At.UTC()
		}
		g := groups[k]
		if g == nil {
			g = &Group{Cause: c}
			g.Cause.Evidence = nil // per-app evidence stays on each incident
			groups[k] = g
			order = append(order, k)
		}
		g.Apps = append(g.Apps, inc.App)
	}
	out := make([]Group, 0, len(groups))
	for _, k := range order {
		if g := groups[k]; len(g.Apps) > 1 {
			out = append(out, *g)
		}
	}
	slices.SortStableFunc(out, func(a, b Group) int { return cmp.Compare(len(b.Apps), len(a.Apps)) })
	return out
}

// Entry is what to show as one incident: a group, or a single incident.
type Entry struct {
	Group     *Group     `json:"group,omitempty"`
	Incidents []Incident `json:"incidents"`
}

// Entries arranges incidents for display, most severe first: entries with an
// app that's down, then larger groups, then the oldest. Groups naming no
// known incident are dropped.
func Entries(incidents []Incident, groups []Group) []Entry {
	byApp := make(map[string]Incident, len(incidents))
	for _, inc := range incidents {
		byApp[inc.App] = inc
	}
	grouped := map[string]bool{}
	var out []Entry
	for i := range groups {
		e := Entry{Group: &groups[i]}
		for _, app := range groups[i].Apps {
			if inc, ok := byApp[app]; ok {
				e.Incidents = append(e.Incidents, inc)
				grouped[app] = true
			}
		}
		if len(e.Incidents) > 0 {
			out = append(out, e)
		}
	}
	for _, inc := range rankBySeverity(incidents) {
		if !grouped[inc.App] {
			out = append(out, Entry{Incidents: []Incident{inc}})
		}
	}
	slices.SortStableFunc(out, func(a, b Entry) int {
		return cmp.Or(cmp.Compare(b.down(), a.down()), cmp.Compare(len(b.Incidents), len(a.Incidents)), a.since().Compare(b.since()))
	})
	return out
}

func (e Entry) down() int {
	for _, inc := range e.Incidents {
		if inc.Down() {
			return 1
		}
	}
	return 0
}

func (e Entry) since() time.Time {
	var t time.Time
	for _, inc := range e.Incidents {
		if !inc.Since.IsZero() && (t.IsZero() || inc.Since.Before(t)) {
			t = inc.Since
		}
	}
	return t
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
