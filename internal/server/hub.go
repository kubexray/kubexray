package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/kubexray/kubexray/internal/cause"
	"github.com/kubexray/kubexray/internal/graph"
)

// MessageIncidents carries the cause engine's current incidents (ADR-0007).
const MessageIncidents = "incidents"

// IncidentsMessage is sent whenever the set of incidents changes.
type IncidentsMessage struct {
	V         int              `json:"v"`
	Type      string           `json:"type"`
	TS        time.Time        `json:"ts"`
	Incidents []cause.Incident `json:"incidents"`
}

// errTooManyClients is returned when the client limit is reached.
var errTooManyClients = errors.New("too many clients")

// Hub fans the pump's messages out to WebSocket clients. It keeps the graph
// state as of the last message it broadcast, so a joining client gets a
// snapshot that matches exactly the deltas that follow. It also serves the
// cause engine from that state.
type Hub struct {
	maxClients int
	queue      int // per-client send queue length

	mu        sync.Mutex
	state     *graph.State
	clients   map[*client]struct{}
	incidents []byte // encoded IncidentsMessage, or nil before the first
	payload   []byte // incidents without the timestamp, to detect changes
}

// client is one subscriber. send is closed when the hub drops it.
type client struct {
	send chan []byte
}

// NewHub returns a hub allowing maxClients subscribers, each with a send
// queue of queue messages.
func NewHub(maxClients, queue int) *Hub {
	return &Hub{maxClients: maxClients, queue: queue, clients: map[*client]struct{}{}}
}

// Emit receives each message from the pump (it satisfies recorder.Emit). It
// never fails: a client that can't keep up is dropped, not waited for.
func (h *Hub) Emit(msg graph.Message, raw []byte) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch msg.Type {
	case graph.MessageSnapshot:
		st, err := graph.NewState(msg)
		if err != nil {
			return err
		}
		h.state = st
	case graph.MessageDelta:
		if h.state == nil {
			return errors.New("hub: delta before the first snapshot")
		}
		if err := h.state.Apply(msg); err != nil {
			return err
		}
	case graph.MessageHeartbeat, graph.MessageError:
		return fmt.Errorf("hub: unexpected %q message from the pump", msg.Type)
	default:
		return fmt.Errorf("hub: unknown %q message from the pump", msg.Type)
	}
	h.broadcast(raw)
	return nil
}

// broadcast queues raw for every client; the caller holds mu.
func (h *Hub) broadcast(raw []byte) {
	for c := range h.clients {
		select {
		case c.send <- raw:
		default:
			// Too far behind: drop it. Its browser reconnects and resyncs
			// from a fresh snapshot.
			h.drop(c)
		}
	}
}

func (h *Hub) drop(c *client) {
	if _, ok := h.clients[c]; ok {
		delete(h.clients, c)
		close(c.send)
	}
}

// subscribe registers a client and returns it with the messages it needs
// first: a snapshot of the current state and the current incidents.
func (h *Hub) subscribe() (*client, [][]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.state == nil {
		return nil, nil, errors.New("hub: not synced yet")
	}
	if len(h.clients) >= h.maxClients {
		return nil, nil, errTooManyClients
	}
	snap, err := json.Marshal(h.state.Snapshot())
	if err != nil {
		return nil, nil, err
	}
	first := [][]byte{snap}
	if h.incidents != nil {
		first = append(first, h.incidents)
	}
	c := &client{send: make(chan []byte, h.queue)}
	h.clients[c] = struct{}{}
	return c, first, nil
}

func (h *Hub) unsubscribe(c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.drop(c)
}

// UpdateIncidents runs the cause engine on the current state and broadcasts
// the result if it changed.
func (h *Hub) UpdateIncidents(now time.Time) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.state == nil {
		return nil
	}
	incidents := cause.Analyze(h.state, cause.DefaultOptions(now))
	if incidents == nil {
		incidents = []cause.Incident{}
	}
	payload, err := json.Marshal(incidents)
	if err != nil {
		return err
	}
	if h.incidents != nil && bytes.Equal(payload, h.payload) {
		return nil
	}
	raw, err := json.Marshal(IncidentsMessage{V: graph.SchemaVersion, Type: MessageIncidents, TS: now, Incidents: incidents})
	if err != nil {
		return err
	}
	h.incidents, h.payload = raw, payload
	h.broadcast(raw)
	return nil
}

// explain runs the cause engine for one app on the current state.
func (h *Hub) explain(id string, now time.Time) (cause.Incident, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.state == nil {
		return cause.Incident{}, errors.New("hub: not synced yet")
	}
	return cause.Explain(h.state, id, cause.DefaultOptions(now))
}
