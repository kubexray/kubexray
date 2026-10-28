package cluster

import (
	"context"
	"math"
	"net/http"
	"slices"
	"time"

	"github.com/kubexray/kubexray/internal/graph"
	"github.com/kubexray/kubexray/internal/prom"
)

// Traffic from Prometheus (ADR-0016): each app's requests, errors and p99
// latency, from the first exporter that has them.

const (
	trafficEvery = time.Minute
	// detectEvery is how often kubexray looks again for Prometheus and its
	// exporters: they get installed after kubexray starts.
	detectEvery = 10 * time.Minute

	// Floors: below them, nothing is a break. A handful of failed requests
	// to an app with almost no traffic isn't an incident.
	minRPS       = 0.2
	errorPctHigh = 5.0
	p99High      = 1.0 // seconds; until baselines (ADR-0016)
)

// WithPrometheus reads traffic from Prometheus at url. Without it, kubexray
// looks for Prometheus among the cluster's Services. In the cluster
// (inCluster), it calls that Service directly; elsewhere it goes through
// the Kubernetes API's service proxy with the user's own permissions.
// kubexray in the cluster never gets services/proxy, which would let it
// reach any Service.
func (w *Watcher) WithPrometheus(url string, inCluster bool) *Watcher {
	w.promURL, w.inCluster = url, inCluster
	return w
}

// watchTraffic reads traffic every minute until ctx ends.
func (w *Watcher) watchTraffic(ctx context.Context) {
	go func() {
		var (
			q        prom.Querier
			adapters []prom.Adapter
			detected time.Time
		)
		t := time.NewTicker(trafficEvery)
		defer t.Stop()
		for {
			if w.now().Sub(detected) >= detectEvery {
				detected = w.now()
				q, adapters = w.detect(ctx)
			}
			if q != nil && len(adapters) > 0 {
				w.readTraffic(ctx, q, adapters)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

// detect finds Prometheus and the exporters it has, logging changes.
func (w *Watcher) detect(ctx context.Context) (prom.Querier, []prom.Adapter) {
	var q prom.Querier
	switch {
	case w.promURL != "":
		q = prom.HTTP{URL: w.promURL, Client: &http.Client{Timeout: 20 * time.Second}}
	case w.client != nil:
		ns, name, port, ok := prom.Find(w.services)
		if !ok {
			w.noteSources("", nil)
			return nil, nil
		}
		q = prom.Proxy{Client: w.client, Namespace: ns, Name: name, Port: port}
		if w.inCluster {
			q = prom.HTTP{URL: "http://" + name + "." + ns + ".svc:" + port, Client: &http.Client{Timeout: 20 * time.Second}}
		}
	default:
		return nil, nil
	}
	adapters, err := prom.Detect(ctx, q)
	if err != nil {
		w.log.Info("can't read Prometheus: request signals are off", "prometheus", q.Where(), "err", err)
		w.noteSources("", nil)
		return nil, nil
	}
	w.noteSources(q.Where(), adapters)
	return q, adapters
}

// noteSources logs what kubexray reads traffic from, when it changes.
func (w *Watcher) noteSources(where string, adapters []prom.Adapter) {
	names := make([]string, len(adapters))
	for i, a := range adapters {
		names[i] = a.Name
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if where == w.sources.Prometheus && slices.Equal(names, w.sources.Exporters) {
		return
	}
	w.sources = Sources{Prometheus: where, Exporters: names}
	if where == "" {
		w.log.Info("no Prometheus found: request signals are off")
	} else {
		w.log.Info("reading traffic from Prometheus", "prometheus", where, "exporters", names)
	}
}

// Sources says where signals come from, for the sources page.
type Sources struct {
	Prometheus string   `json:"prometheus,omitempty"`
	Exporters  []string `json:"exporters,omitempty"`
}

// Sources reports what the watcher reads signals from.
func (w *Watcher) Sources() Sources {
	w.mu.Lock()
	defer w.mu.Unlock()
	return Sources{Prometheus: w.sources.Prometheus, Exporters: slices.Clone(w.sources.Exporters)}
}

// readTraffic collects every adapter's numbers and files them under apps.
func (w *Watcher) readTraffic(ctx context.Context, q prom.Querier, adapters []prom.Adapter) {
	known := func(ns, name string) bool {
		_, err := w.services.Services(ns).Get(name)
		return err == nil
	}
	type reading struct {
		source string
		n      prom.Numbers
	}
	byService := map[prom.Key]reading{}
	for _, a := range adapters {
		numbers, err := prom.Collect(ctx, q, a, known)
		if err != nil {
			w.log.Debug("reading traffic", "exporter", a.Name, "err", err)
			continue
		}
		for k, n := range numbers {
			if _, ok := byService[k]; !ok && n.RPS > 0 {
				byService[k] = reading{a.Name, n} // the first exporter wins
			}
		}
	}

	snap := w.graph.Snapshot()
	st, err := graph.NewState(snap)
	if err != nil {
		return
	}
	apps := appsBehind(st)
	now := w.now()
	seen := map[string]bool{}

	w.mu.Lock()
	defer w.mu.Unlock()
	for k, r := range byService {
		app, ok := apps[graph.ID(KindService, k.Namespace, k.Name)]
		if !ok {
			continue
		}
		id := graph.TrafficID(app)
		if seen[id] {
			continue // two Services in front of one app: the first one read
		}
		seen[id] = true
		prev := w.traffic[id]
		t := graph.Traffic{
			Source:   r.source,
			RPS:      round(r.n.RPS, 10),
			ErrorPct: round(100*r.n.Errors/max(r.n.RPS, 1e-9), 10),
			P99:      round(r.n.P99, 100),
		}
		if t.RPS >= minRPS && t.ErrorPct >= errorPctHigh {
			t.ErrorsSince = cmpTime(prev.ErrorsSince, now)
		}
		if t.RPS >= minRPS && t.P99 >= p99High {
			t.SlowSince = cmpTime(prev.SlowSince, now)
		}
		w.traffic[id] = t
		w.graph.UpsertNode(graph.Node{
			ID: id, Kind: graph.KindTraffic, Namespace: app.Namespace, Name: app.Name, Parent: app.ID,
			Status: graph.Status{Ready: t.ErrorsSince.IsZero() && t.SlowSince.IsZero()}, Traffic: &t,
		})
	}
	for id := range w.traffic {
		if !seen[id] {
			delete(w.traffic, id)
			w.graph.RemoveNode(id)
		}
	}
}

// appsBehind maps each Service to the app whose pods it selects.
func appsBehind(st *graph.State) map[string]graph.Node {
	owner := map[string]string{}
	for _, e := range st.Edges {
		if e.Type == graph.EdgeOwns {
			owner[e.To] = e.From
		}
	}
	out := map[string]graph.Node{}
	for _, e := range st.Edges {
		if e.Type != graph.EdgeSelects {
			continue
		}
		if _, done := out[e.From]; done {
			continue
		}
		for id := owner[e.To]; id != ""; id = owner[id] {
			if n, ok := st.Nodes[id]; ok && (n.Kind == KindDeployment || n.Kind == KindStatefulSet || n.Kind == KindDaemonSet) {
				out[e.From] = n
				break
			}
		}
	}
	return out
}

func round(v, per float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return math.Round(v*per) / per
}

// cmpTime keeps when a break began, or starts it now.
func cmpTime(since, now time.Time) time.Time {
	if since.IsZero() {
		return now
	}
	return since
}
