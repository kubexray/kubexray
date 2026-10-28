package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sync"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/kubexray/kubexray/internal/auth"
	"github.com/kubexray/kubexray/internal/feed"
	"github.com/kubexray/kubexray/internal/graph"
)

// ValuesAccess decides whose values a signed-in person may see (ADR-0020).
type ValuesAccess interface {
	// Allowed reports whether u may see values in namespace ns ("" for
	// cluster-scoped objects).
	Allowed(ctx context.Context, u auth.User, ns string) bool
}

// KubeAccess asks Kubernetes: may this person get ConfigMaps in the
// namespace? The same question kubectl would answer for them, with the
// identity the cluster's API server gives them through the same OIDC
// provider.
type KubeAccess struct {
	client                  kubernetes.Interface
	userPrefix, groupPrefix string
	now                     func() time.Time
	mu                      sync.Mutex
	cache                   map[accessKey]accessAnswer
}

type accessKey struct{ user, groups, ns string }

type accessAnswer struct {
	allowed bool
	at      time.Time
}

// accessTTL is how long an answer is reused: a changed RoleBinding takes
// effect within it.
const accessTTL = time.Minute

// NewKubeAccess checks with the cluster through client. The prefixes match
// the API server's --oidc-username-prefix and --oidc-groups-prefix.
func NewKubeAccess(client kubernetes.Interface, userPrefix, groupPrefix string) *KubeAccess {
	return &KubeAccess{client: client, userPrefix: userPrefix, groupPrefix: groupPrefix, now: time.Now, cache: map[accessKey]accessAnswer{}}
}

// Allowed implements ValuesAccess. Anything it can't confirm is a no.
func (a *KubeAccess) Allowed(ctx context.Context, u auth.User, ns string) bool {
	if u.KubeUser == "" {
		return false
	}
	groups := make([]string, len(u.Groups))
	for i, g := range u.Groups {
		groups[i] = a.groupPrefix + g
	}
	key := accessKey{user: a.userPrefix + u.KubeUser, groups: joinGroups(groups), ns: ns}
	a.mu.Lock()
	if ans, ok := a.cache[key]; ok && a.now().Sub(ans.at) < accessTTL {
		a.mu.Unlock()
		return ans.allowed
	}
	a.mu.Unlock()

	sar := &authorizationv1.SubjectAccessReview{Spec: authorizationv1.SubjectAccessReviewSpec{
		User:   key.user,
		Groups: groups,
		ResourceAttributes: &authorizationv1.ResourceAttributes{
			Namespace: ns, Verb: "get", Resource: "configmaps",
		},
	}}
	res, err := a.client.AuthorizationV1().SubjectAccessReviews().Create(ctx, sar, metav1.CreateOptions{})
	allowed := err == nil && res.Status.Allowed && !res.Status.Denied

	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.cache) > maxAccessCache {
		clear(a.cache)
	}
	a.cache[key] = accessAnswer{allowed: allowed, at: a.now()}
	return allowed
}

// maxAccessCache bounds the answers kept: people times namespaces.
const maxAccessCache = 10000

func joinGroups(groups []string) string {
	sorted := slices.Sorted(slices.Values(groups))
	out := ""
	for _, g := range sorted {
		out += g + "\x00"
	}
	return out
}

// valuesFor is the request's policy: nil when everyone sees every value
// (values access isn't on, or nobody signs in by name).
func (s *Server) valuesFor(r *http.Request) func(ns string) bool {
	if s.access == nil {
		return nil
	}
	u, ok := user(r)
	if !ok {
		return func(string) bool { return false }
	}
	ctx := r.Context()
	seen := map[string]bool{}
	return func(ns string) bool {
		if allowed, ok := seen[ns]; ok {
			return allowed
		}
		allowed := s.access.Allowed(ctx, u, ns)
		seen[ns] = allowed
		return allowed
	}
}

// stripMessage drops values from nodes in namespaces allow refuses.
func stripMessage(m *graph.Message, allow func(string) bool) {
	for i, n := range m.Nodes {
		if !allow(n.Namespace) {
			m.Nodes[i] = graph.WithoutValues(n)
		}
	}
	for i, op := range m.Ops {
		if op.Node != nil && !allow(op.Node.Namespace) {
			n := graph.WithoutValues(*op.Node)
			m.Ops[i].Node = &n
		}
	}
}

// stripSets drops values from the feed's entries in namespaces allow
// refuses: their diffs, and the summaries and titles that quote them.
func stripSets(sets []feed.Set, allow func(string) bool) []feed.Set {
	out := make([]feed.Set, len(sets))
	for i, s := range sets {
		s.Entries = slices.Clone(s.Entries)
		for j, e := range s.Entries {
			if allow(e.Namespace) {
				continue
			}
			var plain func(string) string
			e.Diff, plain = feed.WithoutValues(e.Diff)
			e.Summary, s.Title = plain(e.Summary), plain(s.Title)
			var plainDrift func(string) string
			e.DriftDiff, plainDrift = feed.WithoutValues(e.DriftDiff)
			e.Summary, s.Title = plainDrift(e.Summary), plainDrift(s.Title)
			s.Entries[j] = e
		}
		out[i] = s
	}
	return out
}

// errUnfiltered ends a stream whose message couldn't be filtered, rather
// than pass values through or skip a delta; the UI reconnects and resyncs.
var errUnfiltered = errors.New("couldn't filter a message")

// stripRaw drops values from a graph message on its way to one person.
// Messages that carry no nodes go as they are.
func stripRaw(raw []byte, allow func(string) bool) ([]byte, error) {
	var head struct {
		Type graph.MessageType `json:"type"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return nil, errUnfiltered
	}
	if head.Type != graph.MessageSnapshot && head.Type != graph.MessageDelta {
		return raw, nil
	}
	var m graph.Message
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, errUnfiltered
	}
	stripMessage(&m, allow)
	out, err := json.Marshal(m)
	if err != nil {
		return nil, errUnfiltered
	}
	return out, nil
}
