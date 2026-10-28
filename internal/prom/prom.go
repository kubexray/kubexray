// Package prom reads request rates, errors and latency from Prometheus
// (ADR-0016): it finds Prometheus, finds which exporters it has, and asks
// each for numbers per Service. It only reads.
package prom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"
)

// Sample is one series of an instant query.
type Sample struct {
	Labels map[string]string
	Value  float64
}

// Querier runs instant PromQL queries.
type Querier interface {
	Query(ctx context.Context, q string) ([]Sample, error)
	// Where describes it, for the logs and the sources page.
	Where() string
}

// maxResponse bounds a query's answer.
const maxResponse = 8 << 20

// HTTP queries Prometheus at a URL, from inside the cluster.
type HTTP struct {
	URL    string
	Client *http.Client
}

// Query implements Querier.
func (h HTTP) Query(ctx context.Context, q string) ([]Sample, error) {
	u := strings.TrimSuffix(h.URL, "/") + "/api/v1/query?" + url.Values{"query": {q}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	res, err := h.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(res.Body, maxResponse))
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("prometheus: %s", res.Status)
	}
	return parse(body)
}

// Where implements Querier.
func (h HTTP) Where() string { return h.URL }

// Proxy queries Prometheus through the Kubernetes API's service proxy, with
// the user's own kubeconfig: kubexray running on a laptop.
type Proxy struct {
	Client          kubernetes.Interface
	Namespace, Name string
	Port            string
}

// Query implements Querier.
func (p Proxy) Query(ctx context.Context, q string) ([]Sample, error) {
	body, err := p.Client.CoreV1().Services(p.Namespace).ProxyGet("http", p.Name, p.Port, "/api/v1/query", map[string]string{"query": q}).DoRaw(ctx)
	if err != nil {
		return nil, err
	}
	if len(body) > maxResponse {
		return nil, errors.New("prometheus: answer too large")
	}
	return parse(body)
}

// Where implements Querier.
func (p Proxy) Where() string { return p.Namespace + "/" + p.Name + ":" + p.Port }

func parse(body []byte) ([]Sample, error) {
	var r struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]string `json:"metric"`
				Value  [2]any            `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("prometheus: reading the answer: %w", err)
	}
	if r.Status != "success" {
		return nil, fmt.Errorf("prometheus: %s", r.Error)
	}
	out := make([]Sample, 0, len(r.Data.Result))
	for _, s := range r.Data.Result {
		str, ok := s.Value[1].(string)
		if !ok {
			continue
		}
		v, err := strconv.ParseFloat(str, 64)
		if err != nil {
			continue // NaN and the like parse; anything else is skipped
		}
		out = append(out, Sample{Labels: s.Metric, Value: v})
	}
	return out, nil
}

// Find looks for Prometheus among the cluster's Services: named prometheus
// or prometheus-server, or labelled app.kubernetes.io/name=prometheus. Its
// port is 9090, or the first one.
func Find(services corelisters.ServiceLister) (ns, name, port string, ok bool) {
	list, err := services.List(labels.Everything())
	if err != nil {
		return "", "", "", false
	}
	var best *corev1.Service
	score := func(s *corev1.Service) int {
		switch {
		case s.Name == "prometheus" || s.Name == "prometheus-server" || s.Name == "prometheus-operated":
			return 3
		case s.Labels["app.kubernetes.io/name"] == "prometheus" && s.Labels["app.kubernetes.io/component"] != "alertmanager":
			return 2
		case strings.HasSuffix(s.Name, "-prometheus") && !strings.Contains(s.Name, "operator"):
			return 1
		}
		return 0
	}
	for _, s := range list {
		if score(s) > 0 && len(s.Spec.Ports) > 0 && (best == nil || score(s) > score(best)) {
			best = s
		}
	}
	if best == nil {
		return "", "", "", false
	}
	port = strconv.Itoa(int(best.Spec.Ports[0].Port))
	for _, p := range best.Spec.Ports {
		if p.Port == 9090 {
			port = "9090"
		}
	}
	return best.Namespace, best.Name, port, true
}

// Window is the rate window of every query.
const Window = 2 * time.Minute
