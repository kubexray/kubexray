package cause

import (
	"fmt"
	"time"

	"github.com/kubexray/kubexray/internal/graph"
)

// Signal is what spotted an incident whose pods look healthy (ADR-0016):
// restarts, or memory close to its limit. Pod failures need none.
type Signal struct {
	Kind    string    `json:"kind"`   // restarts or memory
	Source  string    `json:"source"` // Kubernetes or metrics-server
	State   string    `json:"state"`  // e.g. "keeps restarting", as Incident.State says it
	Summary string    `json:"summary"`
	Since   time.Time `json:"since"`
	Pod     string    `json:"pod,omitempty"` // the pod it's most clearly about
}

// Signal kinds.
const (
	RestartsSignal = "restarts"
	MemorySignal   = "memory"
)

const (
	restartWindow = time.Hour
	// restartLimit restarts in restartWindow is an incident: one is a blip,
	// three is a pattern.
	restartLimit = 3
	// memoryHold is how long memory must stay close to the limit.
	memoryHold = 5 * time.Minute
)

// signal looks at an app's otherwise healthy pods.
func signal(pods []graph.Node, now time.Time) *Signal {
	var recent []time.Time
	reason, pod := "", ""
	for _, p := range pods {
		for _, t := range p.Status.Restarted {
			if now.Sub(t) <= restartWindow && !t.After(now) {
				recent = append(recent, t)
				pod = p.ID
				if p.Status.Reason != "" {
					reason = p.Status.Reason // why the last one stopped, e.g. OOMKilled
				}
			}
		}
	}
	if len(recent) >= restartLimit {
		first := recent[0]
		for _, t := range recent {
			if t.Before(first) {
				first = t
			}
		}
		s := &Signal{
			Kind: RestartsSignal, Source: "Kubernetes", State: "keeps restarting", Since: first, Pod: pod,
			Summary: fmt.Sprintf("%d restarts in the last hour; it looks fine between them", len(recent)),
		}
		if reason != "" {
			s.Summary += "; the last one was " + reason
		}
		return s
	}

	var worst graph.Node
	for _, p := range pods {
		if !p.Status.MemoryHigh.IsZero() && now.Sub(p.Status.MemoryHigh) >= memoryHold && p.Status.MemoryPct > worst.Status.MemoryPct {
			worst = p
		}
	}
	if worst.ID == "" {
		return nil
	}
	limit := ""
	if worst.Spec != nil {
		for _, c := range worst.Spec.Containers {
			if !c.Init && c.Limits.Memory != "" {
				limit = " of " + c.Limits.Memory
				break
			}
		}
	}
	return &Signal{
		Kind: MemorySignal, Source: "metrics-server", State: "is close to its memory limit", Since: worst.Status.MemoryHigh, Pod: worst.ID,
		Summary: fmt.Sprintf("a pod uses %d%%%s; Kubernetes kills it (OOMKilled) at the limit", worst.Status.MemoryPct, limit),
	}
}
