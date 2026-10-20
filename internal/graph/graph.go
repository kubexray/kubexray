package graph

import (
	"cmp"
	"maps"
	"slices"
	"sync"
	"time"
)

// Graph holds the current cluster graph and turns changes into deltas.
//
// Writers call the Upsert/Remove methods as Kubernetes events arrive. A
// broadcaster periodically calls Flush to get one coalesced delta, and calls
// Snapshot for newly connected clients. Snapshot reflects exactly what has been
// flushed, so a client that applies a snapshot followed by every later delta
// always ends up in sync.
//
// It is safe for concurrent use.
type Graph struct {
	mu  sync.Mutex
	now func() time.Time

	// Current state, including edges whose endpoints don't exist yet.
	nodes  map[string]Node
	edges  map[string]Edge
	byNode map[string]map[string]struct{} // node ID -> IDs of edges touching it

	// Changed since the last Flush.
	dirtyNodes map[string]struct{}
	dirtyEdges map[string]struct{}

	// State as of the last Flush, i.e. what clients know.
	sentNodes map[string]Node
	sentEdges map[string]Edge
	seq       uint64
}

// New returns an empty graph.
func New() *Graph {
	return &Graph{
		now:        time.Now,
		nodes:      map[string]Node{},
		edges:      map[string]Edge{},
		byNode:     map[string]map[string]struct{}{},
		dirtyNodes: map[string]struct{}{},
		dirtyEdges: map[string]struct{}{},
		sentNodes:  map[string]Node{},
		sentEdges:  map[string]Edge{},
	}
}

// UpsertNode adds or replaces a node. Upserting an identical node is a no-op.
func (g *Graph) UpsertNode(n Node) {
	n = n.clone()

	g.mu.Lock()
	defer g.mu.Unlock()

	old, exists := g.nodes[n.ID]
	if exists && old.Equal(n) {
		return
	}
	g.nodes[n.ID] = n
	g.dirtyNodes[n.ID] = struct{}{}
	if !exists {
		// Edges waiting for this endpoint may now be visible.
		for id := range g.byNode[n.ID] {
			g.dirtyEdges[id] = struct{}{}
		}
	}
}

// RemoveNode removes a node and every edge touching it.
func (g *Graph) RemoveNode(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if _, ok := g.nodes[id]; !ok {
		return
	}
	delete(g.nodes, id)
	g.dirtyNodes[id] = struct{}{}
	for edgeID := range g.byNode[id] {
		g.removeEdgeLocked(edgeID)
	}
}

// UpsertEdge adds an edge. It is held back from clients until both endpoints
// exist.
func (g *Graph) UpsertEdge(e Edge) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if _, ok := g.edges[e.ID]; ok {
		return // edge IDs are derived from their content
	}
	g.edges[e.ID] = e
	g.dirtyEdges[e.ID] = struct{}{}
	g.index(e.From, e.ID)
	g.index(e.To, e.ID)
}

// RemoveEdge removes an edge.
func (g *Graph) RemoveEdge(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.removeEdgeLocked(id)
}

func (g *Graph) removeEdgeLocked(id string) {
	e, ok := g.edges[id]
	if !ok {
		return
	}
	delete(g.edges, id)
	g.dirtyEdges[id] = struct{}{}
	g.unindex(e.From, id)
	g.unindex(e.To, id)
}

func (g *Graph) index(nodeID, edgeID string) {
	if g.byNode[nodeID] == nil {
		g.byNode[nodeID] = map[string]struct{}{}
	}
	g.byNode[nodeID][edgeID] = struct{}{}
}

func (g *Graph) unindex(nodeID, edgeID string) {
	delete(g.byNode[nodeID], edgeID)
	if len(g.byNode[nodeID]) == 0 {
		delete(g.byNode, nodeID)
	}
}

// Flush returns a delta with every change since the previous Flush, and false
// if nothing changed. Changes that cancel out (added then removed) produce no
// operations.
//
// Operations are ordered so a client never sees an edge without both
// endpoints: node upserts, edge upserts, edge removes, node removes.
func (g *Graph) Flush() (Message, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	var nodeUpserts, edgeUpserts, edgeRemoves, nodeRemoves []Op

	for id := range g.dirtyNodes {
		n, exists := g.nodes[id]
		prev, sent := g.sentNodes[id]
		switch {
		case exists && (!sent || !prev.Equal(n)):
			nodeUpserts = append(nodeUpserts, Op{Op: OpUpsertNode, Node: &n})
			g.sentNodes[id] = n
		case !exists && sent:
			nodeRemoves = append(nodeRemoves, Op{Op: OpRemoveNode, ID: id})
			delete(g.sentNodes, id)
		}
	}

	for id := range g.dirtyEdges {
		e, exists := g.edges[id]
		_, sent := g.sentEdges[id]
		visible := exists && g.hasNode(e.From) && g.hasNode(e.To)
		switch {
		case visible && !sent:
			edgeUpserts = append(edgeUpserts, Op{Op: OpUpsertEdge, Edge: &e})
			g.sentEdges[id] = e
		case !visible && sent:
			edgeRemoves = append(edgeRemoves, Op{Op: OpRemoveEdge, ID: id})
			delete(g.sentEdges, id)
		}
	}

	clear(g.dirtyNodes)
	clear(g.dirtyEdges)

	ops := slices.Concat(sortOps(nodeUpserts), sortOps(edgeUpserts), sortOps(edgeRemoves), sortOps(nodeRemoves))
	if len(ops) == 0 {
		return Message{}, false
	}
	g.seq++
	return Message{V: SchemaVersion, Type: MessageDelta, Seq: g.seq, TS: g.now(), Ops: ops}, true
}

func (g *Graph) hasNode(id string) bool {
	_, ok := g.nodes[id]
	return ok
}

// Snapshot returns the graph as of the last Flush, sorted by ID. Its Seq is
// that of the last delta, so the next delta a client should apply is Seq+1.
// Returned nodes share label maps with the graph and must not be modified.
func (g *Graph) Snapshot() Message {
	g.mu.Lock()
	defer g.mu.Unlock()

	nodes := slices.SortedFunc(maps.Values(g.sentNodes), func(a, b Node) int { return cmp.Compare(a.ID, b.ID) })
	edges := slices.SortedFunc(maps.Values(g.sentEdges), func(a, b Edge) int { return cmp.Compare(a.ID, b.ID) })
	return Message{V: SchemaVersion, Type: MessageSnapshot, Seq: g.seq, TS: g.now(), Nodes: nodes, Edges: edges}
}

func sortOps(ops []Op) []Op {
	slices.SortFunc(ops, func(a, b Op) int { return cmp.Compare(opID(a), opID(b)) })
	return ops
}

func opID(o Op) string {
	switch {
	case o.Node != nil:
		return o.Node.ID
	case o.Edge != nil:
		return o.Edge.ID
	default:
		return o.ID
	}
}
