package cause

import (
	"cmp"
	"fmt"
	"slices"
	"time"

	"github.com/kubexray/kubexray/internal/graph"
)

// Change is one entry in the list of recent changes.
type Change struct {
	At      time.Time     `json:"at"`
	Object  string        `json:"object"`
	Kind    string        `json:"kind"`
	Name    string        `json:"name"`
	NS      string        `json:"namespace,omitempty"`
	By      *graph.Change `json:"by,omitempty"`
	Origin  *graph.Origin `json:"origin,omitempty"`
	Summary string        `json:"summary"`
}

// Changes lists changes made since a given time, newest first, from what the
// cluster itself records: each object's latest write (ADR-0005) and
// Deployment rollouts (from their ReplicaSets). Without the recorder, earlier
// writes to the same object aren't visible.
//
// Writes by controllers are follow-ups (a ReplicaSet creating pods) and are
// left out unless includeControllers is set.
func Changes(st *graph.State, since time.Time, includeControllers bool) []Change {
	x := newIndex(st)
	var out []Change
	rolledOut := map[string]time.Time{} // Deployment ID → rollout time listed
	for _, n := range st.Nodes {
		if n.Kind != "Deployment" || n.Spec == nil {
			continue
		}
		cur, prev := x.revisions(n)
		if cur == nil || prev == nil || cur.Created.Before(since) {
			continue
		}
		summary := fmt.Sprintf("rolled out revision %s", cur.Spec.Revision)
		if from, to, ok := imageDiff(prev.Spec, cur.Spec); ok {
			summary += fmt.Sprintf(": image %s → %s", from, to)
		} else if from, to, _, ok := resourceDiff(prev.Spec, cur.Spec); ok {
			summary += fmt.Sprintf(": resources %s → %s", from, to)
		}
		by := n.Change
		if by != nil && by.At.Sub(cur.Created).Abs() > 10*time.Second {
			by = nil // a later write (a scale, say) isn't who rolled this out
		}
		out = append(out, Change{At: cur.Created, Object: n.ID, Kind: n.Kind, Name: n.Name, NS: n.Namespace, By: by, Origin: n.Origin, Summary: summary})
		rolledOut[n.ID] = cur.Created
	}
	for _, n := range st.Nodes {
		c := n.Change
		if c == nil || c.At.Before(since) || n.Kind == "Pod" || n.Kind == "ReplicaSet" {
			continue
		}
		if c.Tool == "controller" && !includeControllers {
			continue
		}
		if t, ok := rolledOut[n.ID]; ok && c.At.Sub(t).Abs() <= 10*time.Second {
			continue // already listed as the rollout
		}
		summary := "changed"
		if n.Kind == "ConfigMap" || n.Kind == "Secret" {
			summary = "edited (contents not read)"
		}
		out = append(out, Change{At: c.At, Object: n.ID, Kind: n.Kind, Name: n.Name, NS: n.Namespace, By: c, Origin: n.Origin, Summary: summary})
	}
	slices.SortFunc(out, func(a, b Change) int { return cmp.Or(b.At.Compare(a.At), cmp.Compare(a.Object, b.Object)) })
	return out
}
