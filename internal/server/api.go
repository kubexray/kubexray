package server

import (
	"encoding/json"
	"time"

	"github.com/kubexray/kubexray/internal/cause"
)

// The types in this file are what the browser receives. TypeScript versions
// are generated from them (make gen), so keep them free of server internals.

// MessageIncidents carries the cause engine's current incidents (ADR-0007).
const MessageIncidents = "incidents"

// IncidentsMessage is sent on the stream whenever the set of incidents changes.
type IncidentsMessage struct {
	V         int              `json:"v"`
	Type      string           `json:"type" tstype:"\"incidents\""`
	TS        time.Time        `json:"ts"`
	Incidents []cause.Incident `json:"incidents"`
}

// HistoryResponse is the recorded time range (GET /api/history).
type HistoryResponse struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

// AtResponse is the cluster at a past moment (GET /api/at).
type AtResponse struct {
	Snapshot  json.RawMessage  `json:"snapshot" tstype:"graph.Message"`
	Incidents []cause.Incident `json:"incidents"`
}
