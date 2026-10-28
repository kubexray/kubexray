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
	AlertSignal    = "alert"
	ErrorsSignal   = "errors"
	LatencySignal  = "latency"
	RestartsSignal = "restarts"
	MemorySignal   = "memory"
)

// alertSignal is the user's own alert about the app, firing in
// Alertmanager: the earliest, with how many more.
func alertSignal(alerts []graph.Node) *Signal {
	if len(alerts) == 0 {
		return nil
	}
	first := alerts[0]
	for _, a := range alerts {
		if a.Status.Since.Before(first.Status.Since) {
			first = a
		}
	}
	summary := first.Name + " is firing"
	if sev := first.Labels["severity"]; sev != "" {
		summary = fmt.Sprintf("%s (%s) is firing", first.Name, sev)
	}
	if len(alerts) > 1 {
		summary += fmt.Sprintf(", and %s", plural(len(alerts)-1, "more alert"))
	}
	return &Signal{Kind: AlertSignal, Source: "Alertmanager", State: "has a firing alert", Summary: summary, Since: first.Status.Since}
}

// trafficHold is how long errors or latency must stay too high, unless the
// reading says (its sensitivity): Normal (ADR-0016).
const trafficHold = 2 * time.Minute

func holdOf(t *graph.Traffic) time.Duration {
	if t.Hold > 0 {
		return time.Duration(t.Hold) * time.Second
	}
	return trafficHold
}

// trafficSignal reads an app's traffic, if it's breaking. It applies
// whether or not the pods look healthy: requests failing is the incident
// people feel.
func trafficSignal(t *graph.Traffic, now time.Time) *Signal {
	switch {
	case t == nil:
		return nil
	case !t.ErrorsSince.IsZero() && now.Sub(t.ErrorsSince) >= holdOf(t):
		normal := ""
		if r := t.ErrorsNormal; r != nil {
			normal = fmt.Sprintf(", normal %s–%s", pct(r.Low), pct(r.High))
		}
		return &Signal{
			Kind: ErrorsSignal, Source: t.Source, State: "is failing requests", Since: t.ErrorsSince,
			Summary: fmt.Sprintf("%s of requests fail (%s a second%s)", pct(t.ErrorPct), rate(t.RPS), normal),
		}
	case !t.SlowSince.IsZero() && now.Sub(t.SlowSince) >= holdOf(t):
		normal := ""
		if r := t.P99Normal; r != nil {
			normal = fmt.Sprintf(", normally %s–%s", seconds(r.Low), seconds(r.High))
		}
		return &Signal{
			Kind: LatencySignal, Source: t.Source, State: "is slow", Since: t.SlowSince,
			Summary: fmt.Sprintf("99%% of requests answer within %s (%s a second%s)", seconds(t.P99), rate(t.RPS), normal),
		}
	}
	return nil
}

func pct(v float64) string {
	if v >= 10 {
		return fmt.Sprintf("%.0f%%", v)
	}
	return fmt.Sprintf("%.1f%%", v)
}

func rate(v float64) string {
	if v >= 10 {
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprintf("%.1f", v)
}

func seconds(v float64) string {
	if v < 1 {
		return fmt.Sprintf("%.0fms", v*1000)
	}
	return fmt.Sprintf("%.1fs", v)
}

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
