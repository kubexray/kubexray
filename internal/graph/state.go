package graph

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"
)

// ErrGap is returned when a delta doesn't follow the state's sequence number,
// meaning a message was missed and the state must be rebuilt from a snapshot.
var ErrGap = errors.New("graph: sequence gap")

// State is a graph rebuilt from messages: a snapshot followed by deltas,
// applied exactly as a client applies them (ADR-0003). The recorder uses it to
// reconstruct the cluster at a moment in the past.
type State struct {
	Seq   uint64
	TS    time.Time
	Nodes map[string]Node
	Edges map[string]Edge
}

// NewState starts a state from a snapshot message.
func NewState(snapshot Message) (*State, error) {
	if snapshot.Type != MessageSnapshot {
		return nil, fmt.Errorf("graph: state must start from a snapshot, got %q", snapshot.Type)
	}
	s := &State{Seq: snapshot.Seq, TS: snapshot.TS, Nodes: make(map[string]Node, len(snapshot.Nodes)), Edges: make(map[string]Edge, len(snapshot.Edges))}
	for _, n := range snapshot.Nodes {
		s.Nodes[n.ID] = n
	}
	for _, e := range snapshot.Edges {
		s.Edges[e.ID] = e
	}
	return s, nil
}

// Apply applies the next delta. It returns ErrGap if the delta isn't the
// one directly after the state's current sequence number.
func (s *State) Apply(m Message) error {
	if m.Type != MessageDelta {
		return fmt.Errorf("graph: can only apply deltas, got %q", m.Type)
	}
	if m.Seq != s.Seq+1 {
		return fmt.Errorf("%w: at seq %d, got %d", ErrGap, s.Seq, m.Seq)
	}
	// Messages may come from untrusted replay files: check every op before
	// changing anything, so a bad message leaves the state untouched.
	for i, op := range m.Ops {
		if err := validOp(op); err != nil {
			return fmt.Errorf("graph: seq %d op %d: %w", m.Seq, i, err)
		}
	}
	for _, op := range m.Ops {
		switch op.Op {
		case OpUpsertNode:
			s.Nodes[op.Node.ID] = *op.Node
		case OpRemoveNode:
			delete(s.Nodes, op.ID)
		case OpUpsertEdge:
			s.Edges[op.Edge.ID] = *op.Edge
		case OpRemoveEdge:
			delete(s.Edges, op.ID)
		}
	}
	s.Seq, s.TS = m.Seq, m.TS
	return nil
}

func validOp(op Op) error {
	switch op.Op {
	case OpUpsertNode:
		if op.Node == nil || op.Node.ID == "" {
			return errors.New("upsertNode without a node")
		}
	case OpUpsertEdge:
		if op.Edge == nil || op.Edge.ID == "" {
			return errors.New("upsertEdge without an edge")
		}
	case OpRemoveNode, OpRemoveEdge:
		if op.ID == "" {
			return fmt.Errorf("%s without an id", op.Op)
		}
	default:
		return fmt.Errorf("unknown op %q", op.Op)
	}
	return nil
}

// Snapshot returns the state as a snapshot message, sorted by ID.
func (s *State) Snapshot() Message {
	m := Message{
		V: SchemaVersion, Type: MessageSnapshot, Seq: s.Seq, TS: s.TS,
		Nodes: slices.AppendSeq(make([]Node, 0, len(s.Nodes)), maps.Values(s.Nodes)),
		Edges: slices.AppendSeq(make([]Edge, 0, len(s.Edges)), maps.Values(s.Edges)),
	}
	sortSnapshot(&m)
	return m
}
