package server

import (
	"encoding/json"
	"time"

	"github.com/kubexray/kubexray/internal/cause"
)

// The types in this file are what the browser receives. TypeScript versions
// are generated from them (make gen), so keep them free of server internals.

// HistoryResponse is the recorded time range (GET /api/history).
type HistoryResponse struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

// AtResponse is the cluster at a past moment (GET /api/at).
type AtResponse struct {
	Snapshot  json.RawMessage  `json:"snapshot" tstype:"graph.Message"`
	Incidents []cause.Incident `json:"incidents"`
	Groups    []cause.Group    `json:"groups"`
}
