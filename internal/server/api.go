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
