package server

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/kubexray/kubexray/internal/cause"
	"github.com/kubexray/kubexray/internal/feed"
	"github.com/kubexray/kubexray/internal/graph"
	"github.com/kubexray/kubexray/internal/recommend"
)

// SetRecommender turns on safety recommendations (ADR-0014).
func (s *Server) SetRecommender(r *recommend.Service) { s.recommender = r }

// captureTimeout bounds one pass of keeping crash logs.
const captureTimeout = 20 * time.Second

// captureCrashLogs keeps the crash logs of current incidents, in the
// background so incident updates never wait on the cluster.
func (s *Server) captureCrashLogs(ctx context.Context) {
	if s.recommender == nil || !s.capturing.CompareAndSwap(false, true) {
		return
	}
	incidents := s.hub.Incidents()
	go func() {
		defer s.capturing.Store(false)
		ctx, cancel := context.WithTimeout(ctx, captureTimeout)
		defer cancel()
		s.recommender.Capture(ctx, incidents)
	}()
}

// draftTimeout bounds drafting: a local model can take a while.
const draftTimeout = 5 * time.Minute

// recommendPreview shows what drafting would send, without sending it.
func (s *Server) recommendPreview(w http.ResponseWriter, r *http.Request) {
	in, ok := s.recommendInput(w, r)
	if !ok {
		return
	}
	writeJSON(w, RecommendPreview{
		Model:   s.recommender.ModelName(),
		App:     in.Incident.App,
		Object:  in.Cause.Object,
		Request: recommend.Request(in),
	})
}

// recommend drafts and proves a guardrail for a change that broke an app.
func (s *Server) recommend(w http.ResponseWriter, r *http.Request) {
	in, ok := s.recommendInput(w, r)
	if !ok {
		return
	}
	who := "token"
	if u, ok := user(r); ok {
		who = u.Email
	}
	ctx, cancel := context.WithTimeout(r.Context(), draftTimeout)
	defer cancel()
	rec, err := s.recommender.Recommend(ctx, who, in)
	var notPossible *recommend.NotPossibleError
	switch {
	case errors.Is(err, recommend.ErrBusy):
		http.Error(w, err.Error(), http.StatusTooManyRequests)
	case errors.As(err, &notPossible):
		http.Error(w, "The model says no admission rule could have prevented this: "+notPossible.Reason, http.StatusUnprocessableEntity)
	case err != nil:
		http.Error(w, "Drafting failed: "+err.Error(), http.StatusBadGateway)
	default:
		y, err := rec.Guardrail.YAML()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, struct {
			*recommend.Recommendation
			YAML string `json:"yaml"` // the policy and binding, for kubectl apply
		}{rec, string(y)})
	}
}

// recommendInput finds the change set, the incident it caused (as it was
// while it lasted) and what a draft needs.
func (s *Server) recommendInput(w http.ResponseWriter, r *http.Request) (recommend.Input, bool) {
	id := r.URL.Query().Get("set")
	i := slices.IndexFunc(s.hub.Feed().Sets(time.Time{}), func(set feed.Set) bool { return set.ID == id })
	if i < 0 {
		http.Error(w, "kubexray doesn't have that change", http.StatusNotFound)
		return recommend.Input{}, false
	}
	set := s.hub.Feed().Sets(time.Time{})[i]
	switch {
	case set.Verdict != feed.Broke || set.Incident == "":
		http.Error(w, "Safety recommendations are for changes that broke an app.", http.StatusConflict)
		return recommend.Input{}, false
	case set.Ended.IsZero():
		http.Error(w, "This change is still breaking the app. Draft a recommendation once it's fixed, so the guardrail can be proven to allow the fix.", http.StatusConflict)
		return recommend.Input{}, false
	}
	inc, err := s.incidentDuring(set)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return recommend.Input{}, false
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	in, err := s.recommender.Prepare(ctx, inc)
	switch {
	case errors.Is(err, recommend.ErrUnsupported):
		http.Error(w, "kubexray can recommend guardrails for ConfigMap changes so far; this kind of cause is coming.", http.StatusUnprocessableEntity)
		return recommend.Input{}, false
	case err != nil:
		http.Error(w, err.Error(), http.StatusConflict)
		return recommend.Input{}, false
	}
	return in, true
}

// lookBack are the moments before an incident ended at which its cause is
// looked for: the fix is often a later change to the same object, and once
// it's made the incident's latest change is the fix, not what broke it.
var lookBack = []time.Duration{time.Second, 5 * time.Second, 15 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute, 30 * time.Minute}

// incidentDuring explains the broken app from the recording, at the latest
// moment before the incident ended when its likely cause was this change.
func (s *Server) incidentDuring(set feed.Set) (cause.Incident, error) {
	sawIncident := false
	for _, back := range lookBack {
		t := set.Ended.Add(-back)
		if t.Before(set.At) {
			break
		}
		snap, err := s.rec.At(t)
		if err != nil {
			return cause.Incident{}, errors.New("kubexray no longer has the history of this incident")
		}
		st, err := graph.NewState(snap)
		if err != nil {
			return cause.Incident{}, err
		}
		inc, err := cause.Explain(st, set.Incident, cause.DefaultOptions(t))
		if err != nil || inc.Healthy {
			continue
		}
		sawIncident = true
		if slices.ContainsFunc(inc.Causes, func(c cause.Cause) bool { return madeBy(set, c) }) {
			return inc, nil
		}
	}
	if !sawIncident {
		return cause.Incident{}, errors.New("kubexray can't find the incident in its history")
	}
	return cause.Incident{}, errors.New("kubexray can't tie this change to the incident's likely cause")
}

// madeBy reports whether a cause is this change: one of its objects,
// changed while the change was being made.
func madeBy(set feed.Set, c cause.Cause) bool {
	return !c.At.Before(set.At.Add(-time.Second)) && !c.At.After(set.Last.Add(time.Second)) &&
		slices.ContainsFunc(set.Entries, func(e feed.Entry) bool { return e.Object == c.Object })
}
