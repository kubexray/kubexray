package prom

import (
	"context"
	"fmt"
	"strings"
)

// Adapter knows one exporter's metrics: how to find them, the queries for
// request rate, errors and p99 latency, and which Service a series is about.
type Adapter struct {
	Name   string // as people know it: Traefik
	Metric string // a counter it always has, to tell whether it's there
	by     string // the labels the queries group by
	total  string // requests per second
	errors string // 5xx responses per second
	p99    string // seconds; empty when the exporter has no histogram
	// target names the Service a series is about. known says whether a
	// Service exists, for exporters whose labels are ambiguous.
	target func(l map[string]string, known func(ns, name string) bool) (ns, name string, ok bool)
}

// Adapters are the exporters kubexray reads, in order of preference: an
// app behind several is read from the first.
var Adapters = []Adapter{
	{
		Name: "Traefik", Metric: "traefik_service_requests_total", by: "service",
		total:  `traefik_service_requests_total`,
		errors: `traefik_service_requests_total{code=~"5.."}`,
		p99:    `traefik_service_request_duration_seconds_bucket`,
		target: traefikTarget,
	},
	{
		Name: "ingress-nginx", Metric: "nginx_ingress_controller_requests", by: "exported_namespace, namespace, service",
		total:  `nginx_ingress_controller_requests`,
		errors: `nginx_ingress_controller_requests{status=~"5.."}`,
		p99:    `nginx_ingress_controller_request_duration_seconds_bucket`,
		target: func(l map[string]string, _ func(string, string) bool) (string, string, bool) {
			ns := l["exported_namespace"] // when Prometheus kept its own namespace label
			if ns == "" {
				ns = l["namespace"]
			}
			return ns, l["service"], ns != "" && l["service"] != ""
		},
	},
	{
		Name: "Istio", Metric: "istio_requests_total", by: "destination_service_namespace, destination_service_name",
		total:  `istio_requests_total{reporter="destination"}`,
		errors: `istio_requests_total{reporter="destination", response_code=~"5.."}`,
		p99:    `istio_request_duration_milliseconds_bucket{reporter="destination"}`, // milliseconds: see Collect
		target: func(l map[string]string, _ func(string, string) bool) (string, string, bool) {
			ns, name := l["destination_service_namespace"], l["destination_service_name"]
			return ns, name, ns != "" && name != ""
		},
	},
	{
		// OpenTelemetry's HTTP server metrics, when the collector promotes the
		// Kubernetes namespace to a label. service.name is matched against
		// Services.
		Name: "OpenTelemetry", Metric: "http_server_request_duration_seconds_count", by: "k8s_namespace_name, service_name",
		total:  `http_server_request_duration_seconds_count`,
		errors: `http_server_request_duration_seconds_count{http_response_status_code=~"5.."}`,
		p99:    `http_server_request_duration_seconds_bucket`,
		target: func(l map[string]string, known func(string, string) bool) (string, string, bool) {
			ns, name := l["k8s_namespace_name"], l["service_name"]
			return ns, name, ns != "" && name != "" && known(ns, name)
		},
	},
}

// traefikTarget reads Traefik's "namespace-name-port@kubernetes". Names and
// namespaces can both hold dashes, so the split that names a known Service
// wins.
func traefikTarget(l map[string]string, known func(ns, name string) bool) (string, string, bool) {
	s, provider, ok := strings.Cut(l["service"], "@")
	if !ok || !strings.HasPrefix(provider, "kubernetes") {
		return "", "", false
	}
	cut := strings.LastIndex(s, "-")
	if cut < 0 {
		return "", "", false
	}
	s = s[:cut] // the port
	for i := strings.Index(s, "-"); i > 0; i = nextDash(s, i) {
		if ns, name := s[:i], s[i+1:]; known(ns, name) {
			return ns, name, true
		}
	}
	return "", "", false
}

func nextDash(s string, i int) int {
	j := strings.Index(s[i+1:], "-")
	if j < 0 {
		return -1
	}
	return i + 1 + j
}

// Detect lists the adapters whose metrics Prometheus has.
func Detect(ctx context.Context, q Querier) ([]Adapter, error) {
	names := make([]string, len(Adapters))
	for i, a := range Adapters {
		names[i] = a.Metric
	}
	samples, err := q.Query(ctx, fmt.Sprintf(`group by (__name__) ({__name__=~%q})`, strings.Join(names, "|")))
	if err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for _, s := range samples {
		have[s.Labels["__name__"]] = true
	}
	var out []Adapter
	for _, a := range Adapters {
		if have[a.Metric] {
			out = append(out, a)
		}
	}
	return out, nil
}

// Numbers are a Service's traffic over the last Window.
type Numbers struct {
	RPS    float64 // requests per second
	Errors float64 // 5xx responses per second
	P99    float64 // seconds; 0 when unknown
}

// Key names a Service.
type Key struct{ Namespace, Name string }

// Collect asks Prometheus for each Service's numbers through one adapter.
func Collect(ctx context.Context, q Querier, a Adapter, known func(ns, name string) bool) (map[Key]Numbers, error) {
	w := fmt.Sprintf("[%dm]", int(Window.Minutes()))
	rate := func(sel string) string { return fmt.Sprintf("sum by (%s) (rate(%s%s))", a.by, sel, w) }
	out := map[Key]Numbers{}
	add := func(samples []Sample, set func(*Numbers, float64)) {
		for _, s := range samples {
			ns, name, ok := a.target(s.Labels, known)
			if !ok {
				continue
			}
			k := Key{ns, name}
			n := out[k]
			set(&n, s.Value)
			out[k] = n
		}
	}
	total, err := q.Query(ctx, rate(a.total))
	if err != nil {
		return nil, err
	}
	add(total, func(n *Numbers, v float64) { n.RPS += v })
	errs, err := q.Query(ctx, rate(a.errors))
	if err != nil {
		return nil, err
	}
	add(errs, func(n *Numbers, v float64) { n.Errors += v })
	if a.p99 != "" {
		p99, err := q.Query(ctx, fmt.Sprintf("histogram_quantile(0.99, sum by (%s, le) (rate(%s%s)))", a.by, a.p99, w))
		if err != nil {
			return nil, err
		}
		scale := 1.0
		if strings.Contains(a.p99, "milliseconds") {
			scale = 0.001
		}
		add(p99, func(n *Numbers, v float64) {
			if v == v && v*scale > n.P99 { // NaN when there was no traffic
				n.P99 = v * scale
			}
		})
	}
	return out, nil
}
