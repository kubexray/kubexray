package server

import (
	"encoding/json"
	"time"

	"github.com/kubexray/kubexray/internal/cause"
	"github.com/kubexray/kubexray/internal/feed"
)

// The types in this file are what the browser receives. TypeScript versions
// are generated from them (make gen), so keep them free of server internals.

// HistoryResponse is the recorded time range (GET /api/history).
type HistoryResponse struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
	// Recommend names the model safety recommendations are drafted with,
	// when they're on (ADR-0014).
	Recommend string `json:"recommend,omitempty"`
}

// RecommendPreview is what drafting a safety recommendation would send
// (GET /api/recommend), for the person to agree to first.
type RecommendPreview struct {
	Model   string `json:"model"`
	App     string `json:"app"`
	Object  string `json:"object"`  // what the guardrail would protect, e.g. ConfigMap/shop/checkout-config
	Request string `json:"request"` // the text sent to the model, exactly
}

// FeedResponse is the change feed (GET /api/feed, ADR-0012), newest first.
type FeedResponse struct {
	Sets []feed.Set `json:"sets"`
	Now  time.Time  `json:"now"`
}

// AtResponse is the cluster at a past moment (GET /api/at).
type AtResponse struct {
	Snapshot  json.RawMessage  `json:"snapshot" tstype:"graph.Message"`
	Incidents []cause.Incident `json:"incidents"`
	Groups    []cause.Group    `json:"groups"`
}

// Sources is where kubexray's signals come from, for the sources page.
type Sources struct {
	MetricsServer bool     `json:"metricsServer"`
	Prometheus    string   `json:"prometheus,omitempty"`
	Exporters     []string `json:"exporters,omitempty"`
	Sensitivity   string   `json:"sensitivity"`
	// Alertmanager says whether kubexray receives alerts, and LastAlert
	// when the last notification came.
	Alertmanager bool      `json:"alertmanager"`
	LastAlert    time.Time `json:"lastAlert,omitzero"`
}
