// Package graph is kubexray's model of a cluster: a set of nodes and edges
// streamed to the UI as snapshots and deltas (see docs/adr/0003).
//
// The model is an allow-list: only the fields declared here ever leave the
// backend, so Secret data, env values and annotations are excluded by
// construction. Labels are the one free-form field and are passed through
// as-is; replay export anonymises them separately.
package graph

import (
	"maps"
	"slices"
	"time"
)

// SchemaVersion is the wire format version carried in every message.
const SchemaVersion = 1

// EdgeType is the kind of relationship an edge represents.
type EdgeType string

// Edge types.
const (
	EdgeOwns    EdgeType = "owns"    // ownerReference, e.g. ReplicaSet -> Pod
	EdgeSelects EdgeType = "selects" // label selector, e.g. Service -> Pod
	EdgeMounts  EdgeType = "mounts"  // volume, e.g. Pod -> ConfigMap
	EdgeRoutes  EdgeType = "routes"  // traffic routing, e.g. Ingress -> Service
)

// Node is one Kubernetes object as shown on the map.
type Node struct {
	ID        string            `json:"id"` // Kind/namespace/name, see ID
	Kind      string            `json:"kind"`
	Namespace string            `json:"ns,omitempty"`
	Name      string            `json:"name"`
	Parent    string            `json:"parent,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
	Status    Status            `json:"status"`
	Created   time.Time         `json:"created"`
	Spec      *Spec             `json:"spec,omitempty"`   // workloads and pods (ADR-0005)
	Change    *Change           `json:"change,omitempty"` // latest non-status write
	Origin    *Origin           `json:"origin,omitempty"` // Helm, Argo CD or Flux
}

// Spec is the allow-listed desired state of a workload or pod. Commands,
// args and environment variables are deliberately absent.
type Spec struct {
	Generation int64       `json:"generation,omitempty"`
	Revision   string      `json:"revision,omitempty"` // deployment.kubernetes.io/revision
	Containers []Container `json:"containers,omitempty"`
}

// Container is one container of a pod or pod template.
type Container struct {
	Name      string    `json:"name"`
	Image     string    `json:"image"`
	Init      bool      `json:"init,omitempty"`
	Ephemeral bool      `json:"ephemeral,omitempty"` // added by kubectl debug
	Requests  Resources `json:"requests,omitzero"`
	Limits    Resources `json:"limits,omitzero"`
}

// Resources holds CPU and memory quantities in Kubernetes notation.
type Resources struct {
	CPU    string `json:"cpu,omitempty"`
	Memory string `json:"memory,omitempty"`
}

// Change attributes the most recent write to an object.
type Change struct {
	At   time.Time `json:"at"`
	By   string    `json:"by"`   // field manager, e.g. "kubectl-patch"
	Tool string    `json:"tool"` // kubectl, helm, argocd, flux, controller or other
	Via  string    `json:"via,omitempty"`
}

// Origin is where an object is managed from.
type Origin struct {
	Tool string `json:"tool"` // helm, argocd or flux
	Name string `json:"name,omitempty"`
}

// ID returns the node ID for an object (ADR-0004). Cluster-scoped objects
// have an empty namespace.
func ID(kind, namespace, name string) string {
	if namespace == "" {
		return kind + "/" + name
	}
	return kind + "/" + namespace + "/" + name
}

// Status is a kind-specific summary; fields that don't apply stay empty.
type Status struct {
	Phase    string    `json:"phase,omitempty"`
	Ready    bool      `json:"ready"`
	Reason   string    `json:"reason,omitempty"`
	Restarts int32     `json:"restarts,omitempty"`
	Replicas *Replicas `json:"replicas,omitempty"`
}

// Replicas summarises a workload's scale.
type Replicas struct {
	Desired int32 `json:"desired"`
	Ready   int32 `json:"ready"`
	Updated int32 `json:"updated"`
}

// Equal reports whether two nodes would render identically.
func (n Node) Equal(o Node) bool {
	return n.ID == o.ID &&
		n.Kind == o.Kind &&
		n.Namespace == o.Namespace &&
		n.Name == o.Name &&
		n.Parent == o.Parent &&
		maps.Equal(n.Labels, o.Labels) &&
		n.Status.Equal(o.Status) &&
		n.Created.Equal(o.Created) &&
		equalPtr(n.Spec, o.Spec, Spec.Equal) &&
		equalPtr(n.Change, o.Change, Change.Equal) &&
		equalPtr(n.Origin, o.Origin, func(a, b Origin) bool { return a == b })
}

// Equal reports whether two specs are identical.
func (s Spec) Equal(o Spec) bool {
	return s.Generation == o.Generation && s.Revision == o.Revision && slices.Equal(s.Containers, o.Containers)
}

// Equal reports whether two changes are identical.
func (c Change) Equal(o Change) bool {
	return c.At.Equal(o.At) && c.By == o.By && c.Tool == o.Tool && c.Via == o.Via
}

func equalPtr[T any](a, b *T, eq func(T, T) bool) bool {
	if a == nil || b == nil {
		return a == b
	}
	return eq(*a, *b)
}

// clone returns a deep copy, so callers can't mutate stored state.
func (n Node) clone() Node {
	n.Labels = maps.Clone(n.Labels)
	if n.Status.Replicas != nil {
		r := *n.Status.Replicas
		n.Status.Replicas = &r
	}
	if n.Spec != nil {
		s := *n.Spec
		s.Containers = slices.Clone(s.Containers)
		n.Spec = &s
	}
	if n.Change != nil {
		c := *n.Change
		n.Change = &c
	}
	if n.Origin != nil {
		o := *n.Origin
		n.Origin = &o
	}
	return n
}

// Equal reports whether two statuses are identical.
func (s Status) Equal(o Status) bool {
	if (s.Replicas == nil) != (o.Replicas == nil) {
		return false
	}
	if s.Replicas != nil && *s.Replicas != *o.Replicas {
		return false
	}
	return s.Phase == o.Phase && s.Ready == o.Ready && s.Reason == o.Reason && s.Restarts == o.Restarts
}

// Edge is a directed relationship between two nodes.
type Edge struct {
	ID   string   `json:"id"`
	Type EdgeType `json:"type"`
	From string   `json:"from"`
	To   string   `json:"to"`
}

// NewEdge returns an edge with its deterministic ID.
func NewEdge(t EdgeType, from, to string) Edge {
	return Edge{ID: string(t) + ":" + from + ":" + to, Type: t, From: from, To: to}
}

// MessageType identifies a message on the stream or in a replay file.
type MessageType string

// Message types.
const (
	MessageSnapshot  MessageType = "snapshot"
	MessageDelta     MessageType = "delta"
	MessageHeartbeat MessageType = "heartbeat"
	MessageError     MessageType = "error"
)

// Message is the envelope for everything sent to the UI.
type Message struct {
	V     int         `json:"v"`
	Type  MessageType `json:"type"`
	Seq   uint64      `json:"seq"`
	TS    time.Time   `json:"ts"`
	Nodes []Node      `json:"nodes,omitzero"`  // snapshot; always present, possibly []
	Edges []Edge      `json:"edges,omitzero"`  // snapshot; always present, possibly []
	Ops   []Op        `json:"ops,omitzero"`    // delta
	Error string      `json:"error,omitempty"` // error
}

// OpKind is the operation applied by a delta.
type OpKind string

// Delta operations.
const (
	OpUpsertNode OpKind = "upsertNode"
	OpRemoveNode OpKind = "removeNode"
	OpUpsertEdge OpKind = "upsertEdge"
	OpRemoveEdge OpKind = "removeEdge"
)

// Op is one change inside a delta. Exactly one of Node, Edge or ID is set,
// depending on Op.
type Op struct {
	Op   OpKind `json:"op"`
	Node *Node  `json:"node,omitempty"`
	Edge *Edge  `json:"edge,omitempty"`
	ID   string `json:"id,omitempty"`
}
