package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/kubexray/kubexray/internal/cause"
	"github.com/kubexray/kubexray/internal/feed"
	"github.com/kubexray/kubexray/internal/graph"
)

var (
	errTooManyClients = errors.New("too many clients")
	errNotSynced      = errors.New("not synced yet; try again in a moment")
)

// incidentsRecheck is how often incidents are re-analyzed when nothing has
// changed, so problems that outlast the grace period still get reported.
const incidentsRecheck = 5 * time.Second

// Hub fans the pump's messages out to WebSocket clients. It keeps the graph
// state as of the last message it broadcast, so a joining client gets a
// snapshot that matches exactly the deltas that follow. It also serves the
// cause engine from that state.
type Hub struct {
	maxClients int
	queue      int // per-client send queue length

	ready chan struct{} // closed on the first snapshot

	mu        sync.Mutex
	state     *graph.State
	clients   map[*client]struct{}
	incidents []byte // encoded IncidentsMessage, or nil before the first
	payload   []byte // incidents without the timestamp, to detect changes
	analyzed  uint64 // state seq at the last analysis
	lastRun   time.Time

	feed *feed.Tracker // the change feed, from every delta (ADR-0012)
}

// client is one subscriber. send is closed when the hub drops it.
type client struct {
	send chan []byte
}

// NewHub returns a hub allowing maxClients subscribers, each with a send
// queue of queue messages.
func NewHub(maxClients, queue int) *Hub {
	return &Hub{maxClients: maxClients, queue: queue, clients: map[*client]struct{}{}, ready: make(chan struct{}), feed: feed.New(feed.MaxSets)}
}

// Feed is the change feed the hub keeps. Rebuild it from recorded history
// (feed.Rebuild) before the pump starts.
func (h *Hub) Feed() *feed.Tracker { return h.feed }

// Ready is closed once the hub has its first snapshot and can serve clients.
func (h *Hub) Ready() <-chan struct{} { return h.ready }

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
		if h.state == nil {
			close(h.ready)
		}
		h.state = st
	case graph.MessageDelta:
		if h.state == nil {
			return errors.New("hub: delta before the first snapshot")
		}
		h.feed.Observe(h.state, msg) // before and after
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
		return nil, nil, errNotSynced
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

// UpdateIncidents runs the cause engine and broadcasts the result if it
// changed. It analyzes a copy of the state outside the lock, so a slow
// analysis on a big cluster never holds up the live stream, and skips the
// work when nothing changed recently.
func (h *Hub) UpdateIncidents(now time.Time) error {
	h.mu.Lock()
	if h.state == nil || (h.state.Seq == h.analyzed && h.incidents != nil && now.Sub(h.lastRun) < incidentsRecheck) {
		h.mu.Unlock()
		return nil
	}
	st := h.state.Clone()
	h.mu.Unlock()

	msg := cause.NewIncidentsMessage(st, now)
	h.feed.Assess(st, msg.Incidents, now)
	payload, err := json.Marshal([]any{msg.Incidents, msg.Groups})
	if err != nil {
		return err
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	h.analyzed, h.lastRun = st.Seq, now
	if h.incidents != nil && bytes.Equal(payload, h.payload) {
		return nil
	}
	h.incidents, h.payload = raw, payload
	h.broadcast(raw)
	return nil
}

// changes lists recent changes on the current state.
func (h *Hub) changes(since time.Time) ([]cause.Change, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.state == nil {
		return nil, errNotSynced
	}
	list := cause.Changes(h.state, since, false)
	if list == nil {
		list = []cause.Change{}
	}
	return list, nil
}

// explain runs the cause engine for one app on the current state.
func (h *Hub) explain(id string, now time.Time) (cause.Incident, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.state == nil {
		return cause.Incident{}, errNotSynced
	}
	return cause.Explain(h.state, id, cause.DefaultOptions(now))
}

// explainAll explains several apps of the same moment, indexing it once.
func (h *Hub) explainAll(ids []string, now time.Time) (map[string]cause.Incident, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.state == nil {
		return nil, errNotSynced
	}
	explain := cause.NewExplainer(h.state, cause.DefaultOptions(now))
	out := map[string]cause.Incident{}
	for _, id := range ids {
		if inc, err := explain(id); err == nil {
			out[id] = inc
		}
	}
	return out, nil
}
