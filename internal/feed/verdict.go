package feed

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/kubexray/kubexray/internal/cause"
	"github.com/kubexray/kubexray/internal/graph"
)

// Assess updates verdicts from the cluster as it is now and the cause
// engine's incidents (ADR-0012, section 3). A verdict stays provisional for
// the cause engine's lookback: a ConfigMap edit leaves its apps healthy
// until a pod restarts minutes later, and can still turn out to have broken
// one.
func (t *Tracker) Assess(st *graph.State, incidents []cause.Incident, now time.Time) {
	byApp := map[string]cause.Incident{}
	for _, inc := range incidents {
		byApp[inc.App] = inc
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := len(t.sets) - 1; i >= 0; i-- {
		s := t.sets[i]
		if now.Sub(s.Last) > ReassessFor {
			break // older verdicts are final
		}
		if s.Last.After(st.TS) {
			continue // made after the state analyzed: judged next time
		}
		assess(s, st, byApp, now)
	}
	t.ongoing = map[string]bool{}
	for app := range byApp {
		t.ongoing[app] = true
	}
}

// ReassessFor is how long verdicts are revisited: the cause engine's
// lookback (ADR-0006), plus time to note when an incident ended.
const ReassessFor = 2 * time.Hour

func assess(s *Set, st *graph.State, incidents map[string]cause.Incident, now time.Time) {
	if s.Verdict == Broke {
		if _, still := incidents[s.Incident]; !still && s.Ended.IsZero() {
			s.Ended = now
		}
		return
	}
	if len(s.Apps) == 0 {
		s.Verdict = NoEffect
		return
	}
	if now.Sub(s.Last) <= cause.DefaultOptions(now).Lookback && broke(s, incidents) {
		return
	}
	switch s.Verdict { //nolint:exhaustive // the rest are worked out below
	case Fixed, Stuck:
		return // provisional, but only an incident it caused changes it
	case Healthy:
		// A rollout that unsettles again soon after (the controller hadn't
		// started it yet, or it hung) is still settling.
		if !rollsOut(s) || now.Sub(s.Last) > SettleWindow || allSettled(s, st, incidents) {
			return
		}
		s.healthAt, s.disturbed = time.Time{}, true
	}

	// Fixed: made during an incident on its apps, which has ended.
	if len(s.during) > 0 {
		over := true
		for app := range s.during {
			if _, still := incidents[app]; still {
				over = false
			}
		}
		if over && now.Sub(s.At) <= SettleWindow {
			s.Verdict, s.Fixes = Fixed, slices.Sorted(maps.Keys(s.during))
			s.Note = fmt.Sprintf("%s healthy %s later", names(slices.Sorted(maps.Keys(s.during))), short(now.Sub(s.Last)))
			return
		}
	}

	// Healthy: every app it touches is ready and rolled out, with no incident.
	ready, total := 0, 0
	for _, app := range s.Apps {
		n, ok := st.Nodes[app]
		if !ok {
			continue // deleted since: nothing to settle
		}
		total++
		if _, sick := incidents[app]; !sick && settled(n) {
			ready++
		}
	}
	if total == 0 {
		s.Verdict, s.Note = NoEffect, "its apps are gone" // e.g. it deleted them
		return
	}
	if ready < total {
		s.disturbed = true
	}
	// A rollout counts once it was seen rolling out, or has looked settled
	// for a while: right after the change, status may not have caught up.
	early := rollsOut(s) && !s.disturbed && now.Sub(s.Last) < 2*Window
	switch {
	case ready == total && early:
		s.Verdict, s.Note = Settling, "starting"
	case ready == total:
		if s.healthAt.IsZero() {
			s.healthAt = now
		}
		s.Verdict, s.Note = Healthy, "settled in "+short(s.healthAt.Sub(s.Last))
		if !s.disturbed && !rollsOut(s) {
			s.Note = "its apps stayed healthy" // e.g. a ConfigMap edit: running pods keep their config
		}
	case now.Sub(s.Last) > SettleWindow:
		s.Verdict, s.Note = Stuck, fmt.Sprintf("not settled after %s", short(SettleWindow))
	default:
		s.Verdict, s.Note = Settling, progress(st, s.Apps)
	}
}

// broke marks a set the cause engine names as an incident's cause.
func broke(s *Set, incidents map[string]cause.Incident) bool {
	for _, app := range s.Apps {
		inc, ok := incidents[app]
		if !ok || len(inc.Causes) == 0 {
			continue
		}
		top := inc.Causes[0]
		for _, e := range s.Entries {
			if top.Object == e.Object && top.At.Sub(e.At).Abs() <= Window {
				s.Verdict, s.Incident = Broke, app
				if s.Broke.IsZero() || inc.Since.Before(s.Broke) {
					s.Broke = inc.Since
				}
				later := ""
				if !inc.Since.IsZero() && inc.Since.After(s.At) {
					later = " " + short(inc.Since.Sub(s.At)) + " later"
				}
				s.Note = fmt.Sprintf("%s %s%s", inc.Name, inc.State(), later)
				return true
			}
		}
	}
	return false
}

// allSettled reports whether every app a set touches is settled and well.
func allSettled(s *Set, st *graph.State, incidents map[string]cause.Incident) bool {
	for _, app := range s.Apps {
		n, ok := st.Nodes[app]
		if _, sick := incidents[app]; ok && (sick || !settled(n)) {
			return false
		}
	}
	return true
}

// rollsOut reports whether a set replaces or adds pods: a template change,
// a scale, a new app.
func rollsOut(s *Set) bool {
	for _, e := range s.Entries {
		switch e.What {
		case "image", "resources", "probes", "template", "replicas", "created":
			if appKinds[e.Kind] {
				return true
			}
		}
	}
	return false
}

// settled reports whether an app has finished rolling out: the mapper's
// Ready for workloads means every desired replica is updated and ready.
func settled(n graph.Node) bool {
	if n.Kind == "Job" {
		return n.Status.Phase != "Failed"
	}
	return n.Status.Ready
}

// progress describes a rollout in flight, e.g. "2 of 3 ready".
func progress(st *graph.State, apps []string) string {
	for _, app := range apps {
		if n, ok := st.Nodes[app]; ok && n.Status.Replicas != nil && !n.Status.Ready {
			r := n.Status.Replicas
			return fmt.Sprintf("%s: %d of %d ready", n.Name, r.Ready, r.Desired)
		}
	}
	return "rolling out"
}

func names(ids []string) string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id[strings.LastIndex(id, "/")+1:]
	}
	return strings.Join(out, ", ")
}

// short formats a duration the way the UI does: 48s, 3m 10s, 2h 5m.
func short(d time.Duration) string {
	s := int(d.Round(time.Second) / time.Second)
	switch {
	case s < 0:
		s = 0
		fallthrough
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm %ds", s/60, s%60)
	default:
		return fmt.Sprintf("%dh %dm", s/3600, s%3600/60)
	}
}
