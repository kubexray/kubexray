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
	"strings"
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
	Spec      *Spec             `json:"spec,omitempty"`     // workloads and pods (ADR-0005)
	Change    *Change           `json:"change,omitempty"`   // latest non-status write
	Origin    *Origin           `json:"origin,omitempty"`   // Helm, Argo CD or Flux
	Selector  map[string]string `json:"selector,omitempty"` // a Service's pod selector
	// Data is a ConfigMap's contents, only when values are on (ADR-0017).
	// Secrets are never read.
	Data map[string]Value `json:"data,omitempty"`
	// Manifest is the object's desired state for kinds kubexray keeps a
	// history of (ADR-0018): each field by its path, such as
	// "spec.maxReplicas" or "spec.rules[0].host", as a Value. Status and
	// metadata other than labels are left out.
	Manifest map[string]Value `json:"manifest,omitempty"`
}

// Value is a value kubexray may show in a diff (ADR-0017): its text when
// values are on and it isn't masked, and always a fingerprint, so a change
// shows even when the text is hidden.
type Value struct {
	Text   string `json:"text,omitempty"`
	Hash   string `json:"hash,omitempty"`   // "fp:…", keyed per run
	Masked string `json:"masked,omitempty"` // why the text is hidden or cut: name, value or size
}

// Spec is the allow-listed desired state of a workload or pod. Values of
// commands, args and environment variables are kept only when values are on
// (ADR-0017), masked where they look like credentials.
type Spec struct {
	Generation int64       `json:"generation,omitempty"`
	Revision   string      `json:"revision,omitempty"` // deployment.kubernetes.io/revision
	Containers []Container `json:"containers,omitempty"`
	// ServiceAccount the pods run as, and the objects their volumes come
	// from ("ConfigMap/api-config", "Secret/tls", "PVC/data"), for diffs
	// (ADR-0015).
	ServiceAccount string   `json:"serviceAccount,omitempty"`
	Volumes        []string `json:"volumes,omitempty"`
	// Labels of the pods a workload creates, to match Services against it
	// when it has no pods (scaled to zero).
	Labels map[string]string `json:"labels,omitempty"`
}

// Container is one container of a pod or pod template.
type Container struct {
	Name      string    `json:"name"`
	Image     string    `json:"image"`
	Init      bool      `json:"init,omitempty"`
	Ephemeral bool      `json:"ephemeral,omitempty"` // added by kubectl debug
	Requests  Resources `json:"requests,omitzero"`
	Limits    Resources `json:"limits,omitzero"`
	Probes    Probes    `json:"probes,omitzero"` // which are defined, and what they check (ADR-0009, ADR-0015)
	// Environment variables by name and source; a literal value as a
	// fingerprint (ADR-0015), and its text when values are on (ADR-0017).
	Env     []EnvVar `json:"env,omitempty"`
	EnvFrom []string `json:"envFrom,omitempty"` // "ConfigMap/name", "Secret/name"
	Command string   `json:"command,omitempty"` // a fingerprint of the command and args
	Run     Value    `json:"run,omitzero"`      // the command and args, when values are on
	Ports   []string `json:"ports,omitempty"`   // "80/TCP"
}

// EnvVar is an environment variable's name and where its value comes from.
type EnvVar struct {
	Name string `json:"name"`
	// From is the value's source: "ConfigMap/checkout-config:LOG_LEVEL",
	// "Secret/db-credentials:password", "field:metadata.name",
	// "resource:limits.memory", or empty for a literal value.
	From string `json:"from,omitempty"`
	// Value is a fingerprint of a literal value ("fp:…"): it shows that a
	// value changed without saying what it was.
	Value string `json:"value,omitempty"`
	// Text is the literal value when values are on, unless it's masked.
	Text   string `json:"text,omitempty"`
	Masked string `json:"masked,omitempty"` // name, value or size: why Text is hidden or cut
}

// WithoutValues returns a node with every value removed: what leaves the
// cluster unless someone chooses to include values (ADR-0017). Names,
// sources and fingerprints stay.
func WithoutValues(n Node) Node {
	n.Data, n.Manifest = hashes(n.Data), hashes(n.Manifest)
	if n.Spec != nil && slices.ContainsFunc(n.Spec.Containers, hasValues) {
		spec := *n.Spec
		spec.Containers = slices.Clone(spec.Containers)
		for i, c := range spec.Containers {
			c.Run = Value{Hash: c.Run.Hash}
			if len(c.Env) > 0 {
				env := slices.Clone(c.Env)
				for j := range env {
					env[j].Text, env[j].Masked = "", ""
				}
				c.Env = env
			}
			spec.Containers[i] = c
		}
		n.Spec = &spec
	}
	return n
}

func hashes(m map[string]Value) map[string]Value {
	if len(m) == 0 {
		return m
	}
	out := make(map[string]Value, len(m))
	for k, v := range m {
		out[k] = Value{Hash: v.Hash}
	}
	return out
}

// StripValues applies WithoutValues to every node in a message.
func StripValues(m *Message) {
	StripValuesWhere(m, func(Node) bool { return true })
}

// StripValuesWhere applies WithoutValues to the nodes in a message that
// strip picks.
func StripValuesWhere(m *Message, strip func(Node) bool) {
	for i, n := range m.Nodes {
		if strip(n) {
			m.Nodes[i] = WithoutValues(n)
		}
	}
	for i, op := range m.Ops {
		if op.Node != nil && strip(*op.Node) {
			n := WithoutValues(*op.Node)
			m.Ops[i].Node = &n
		}
	}
}

func hasValues(c Container) bool {
	return c.Run.Text != "" || slices.ContainsFunc(c.Env, func(e EnvVar) bool { return e.Text != "" || e.Masked != "" })
}

// Probes records which health probes a container defines.
type Probes struct {
	Liveness  bool `json:"liveness,omitempty"`
	Readiness bool `json:"readiness,omitempty"`
	Startup   bool `json:"startup,omitempty"`
	// What each one checks: "http GET :80/healthz", "tcp :5432", "grpc
	// :9000" or "a command" (whose contents aren't recorded).
	LivenessCheck  string `json:"livenessCheck,omitempty"`
	ReadinessCheck string `json:"readinessCheck,omitempty"`
	StartupCheck   string `json:"startupCheck,omitempty"`
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
	// Kind and Namespace of what applies it, where known, for the command
	// that puts a drifted object back: a Flux "Kustomization" or
	// "HelmRelease", a Helm release's namespace.
	Kind      string `json:"kind,omitempty"`
	Namespace string `json:"namespace,omitempty"`
}

// ID returns the node ID for an object (ADR-0004). Cluster-scoped objects
// have an empty namespace.
func ID(kind, namespace, name string) string {
	if namespace == "" {
		return kind + "/" + name
	}
	return kind + "/" + namespace + "/" + name
}

// ParseID splits a node ID into its kind, namespace and name: the inverse of
// ID. Names and namespaces can't contain a slash.
func ParseID(id string) (kind, namespace, name string) {
	parts := strings.SplitN(id, "/", 3)
	switch len(parts) {
	case 3:
		return parts[0], parts[1], parts[2]
	case 2:
		return parts[0], "", parts[1]
	default:
		return id, "", ""
	}
}

// Status is a kind-specific summary; fields that don't apply stay empty.
type Status struct {
	Phase    string    `json:"phase,omitempty"`
	Ready    bool      `json:"ready"`
	Reason   string    `json:"reason,omitempty"`
	Restarts int32     `json:"restarts,omitempty"`
	Replicas *Replicas `json:"replicas,omitempty"`
	Since    time.Time `json:"since,omitzero"` // when readiness last changed (pods, nodes)
	// Why, in the words of a Kubernetes component: the scheduler, the kubelet
	// or a controller. Never an application's own message (ADR-0009).
	Message   string `json:"message,omitempty"`
	Exit      int32  `json:"exit,omitempty"`      // exit code behind the reason (pods)
	Container string `json:"container,omitempty"` // the container the reason comes from (pods)
	// Signals (ADR-0016), pods only: when the latest restarts happened (at
	// most MaxRestartTimes), memory use as a percentage of the limit in steps
	// of 5, and since when it has been at MemoryHighPct or more.
	Restarted  []time.Time `json:"restarted,omitempty"`
	MemoryPct  int32       `json:"memoryPct,omitempty"`
	MemoryHigh time.Time   `json:"memoryHigh,omitzero"`
}

// MaxRestartTimes is how many restart times a pod keeps.
const MaxRestartTimes = 10

// MemoryHighPct is where memory use counts as close to the limit.
const MemoryHighPct = 90

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
		equalPtr(n.Origin, o.Origin, func(a, b Origin) bool { return a == b }) &&
		maps.Equal(n.Selector, o.Selector) &&
		maps.Equal(n.Data, o.Data) &&
		maps.Equal(n.Manifest, o.Manifest)
}

// Equal reports whether two specs are identical.
func (s Spec) Equal(o Spec) bool {
	return s.Generation == o.Generation && s.Revision == o.Revision && slices.EqualFunc(s.Containers, o.Containers, Container.Equal) &&
		maps.Equal(s.Labels, o.Labels) && s.ServiceAccount == o.ServiceAccount && slices.Equal(s.Volumes, o.Volumes)
}

// Equal reports whether two containers are identical.
func (c Container) Equal(o Container) bool {
	return c.Name == o.Name && c.Image == o.Image && c.Init == o.Init && c.Ephemeral == o.Ephemeral &&
		c.Requests == o.Requests && c.Limits == o.Limits && c.Probes == o.Probes &&
		slices.Equal(c.Env, o.Env) && slices.Equal(c.EnvFrom, o.EnvFrom) && c.Command == o.Command && c.Run == o.Run && slices.Equal(c.Ports, o.Ports)
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
	return s.Phase == o.Phase && s.Ready == o.Ready && s.Reason == o.Reason && s.Restarts == o.Restarts && s.Since.Equal(o.Since) &&
		s.Message == o.Message && s.Exit == o.Exit && s.Container == o.Container &&
		slices.EqualFunc(s.Restarted, o.Restarted, time.Time.Equal) && s.MemoryPct == o.MemoryPct && s.MemoryHigh.Equal(o.MemoryHigh)
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
