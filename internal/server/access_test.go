package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/kubexray/kubexray/internal/auth"
	"github.com/kubexray/kubexray/internal/feed"
	"github.com/kubexray/kubexray/internal/graph"
)

// A cluster where ada (or the shop-devs group) may read ConfigMaps in shop
// only. It counts the reviews it answers.
func reviewer(t *testing.T) (*KubeAccess, *int) {
	t.Helper()
	client := kubefake.NewClientset()
	asked := 0
	client.PrependReactor("create", "subjectaccessreviews", func(a k8stesting.Action) (bool, runtime.Object, error) {
		asked++
		sar := a.(k8stesting.CreateAction).GetObject().(*authorizationv1.SubjectAccessReview)
		ra := sar.Spec.ResourceAttributes
		member := sar.Spec.User == "oidc:ada@example.com"
		for _, g := range sar.Spec.Groups {
			member = member || g == "oidc:shop-devs"
		}
		sar.Status.Allowed = member && ra.Namespace == "shop" && ra.Verb == "get" && ra.Resource == "configmaps"
		return true, sar, nil
	})
	return NewKubeAccess(client, "oidc:", "oidc:"), &asked
}

func TestKubeAccess(t *testing.T) {
	t.Parallel()
	a, asked := reviewer(t)
	ctx := context.Background()
	ada := auth.User{Email: "ada@example.com", KubeUser: "ada@example.com"}
	bob := auth.User{Email: "bob@example.com", KubeUser: "bob@example.com", Groups: []string{"shop-devs"}}
	eve := auth.User{Email: "eve@example.com", KubeUser: "eve@example.com"}
	for _, c := range []struct {
		u    auth.User
		ns   string
		want bool
	}{
		{ada, "shop", true},
		{ada, "payments", false},
		{ada, "", false},
		{bob, "shop", true},
		{eve, "shop", false},
		{auth.User{Email: "x@example.com"}, "shop", false}, // no Kubernetes username: never asked
	} {
		if got := a.Allowed(ctx, c.u, c.ns); got != c.want {
			t.Errorf("%s in %q: %v, want %v", c.u.Email, c.ns, got, c.want)
		}
	}
	before := *asked
	a.Allowed(ctx, ada, "shop")
	if *asked != before {
		t.Error("asked again within the cache's lifetime")
	}
	a.now = func() time.Time { return time.Now().Add(2 * accessTTL) }
	a.Allowed(ctx, ada, "shop")
	if *asked != before+1 {
		t.Error("didn't ask again once the answer was old")
	}
}

func TestKubeAccessDeniesOnErrors(t *testing.T) {
	t.Parallel()
	client := kubefake.NewClientset()
	client.PrependReactor("create", "subjectaccessreviews", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, context.DeadlineExceeded
	})
	a := NewKubeAccess(client, "", "")
	if a.Allowed(context.Background(), auth.User{KubeUser: "ada"}, "shop") {
		t.Error("allowed when the review failed")
	}
}

func onlyShop(ns string) bool { return ns == "shop" }

func TestStripSets(t *testing.T) {
	t.Parallel()
	edit := func(ns string) feed.Set {
		return feed.Set{
			Title: "ConfigMap checkout-config edited (LOG_LEVEL info → verbose)",
			Entries: []feed.Entry{{
				Namespace: ns, Summary: "edited (LOG_LEVEL info → verbose)",
				Diff: []feed.DiffLine{
					{Path: "LOG_LEVEL", Op: feed.Changed, From: "info", To: "verbose", Value: true},
					{Path: "image", Op: feed.Changed, From: "nginx:1.27", To: "nginx:1.29"}, // not a value
				},
			}},
		}
	}
	sets := []feed.Set{edit("shop"), edit("payments")}
	got := stripSets(sets, onlyShop)
	if got[0].Entries[0].Diff[0].To != "verbose" {
		t.Errorf("hid an allowed value: %+v", got[0])
	}
	p := got[1]
	if p.Title != "ConfigMap checkout-config edited (LOG_LEVEL changed)" || p.Entries[0].Summary != "edited (LOG_LEVEL changed)" {
		t.Errorf("title %q, summary %q", p.Title, p.Entries[0].Summary)
	}
	if l := p.Entries[0].Diff[0]; l.From != "" || l.To != "" || !strings.Contains(l.Note, "can't read ConfigMaps") {
		t.Errorf("value line = %+v", l)
	}
	if l := p.Entries[0].Diff[1]; l.To != "nginx:1.29" {
		t.Errorf("hid an image: %+v", l)
	}
	if sets[1].Entries[0].Diff[0].To != "verbose" {
		t.Error("changed the tracker's own sets")
	}
}

func TestStripRaw(t *testing.T) {
	t.Parallel()
	node := func(ns string) graph.Node {
		return graph.Node{ID: "ConfigMap/" + ns + "/c", Kind: "ConfigMap", Namespace: ns, Name: "c", Data: map[string]graph.Value{"K": {Text: "secret-ish", Hash: "fp:1"}}}
	}
	raw, _ := json.Marshal(graph.Message{V: 1, Type: graph.MessageSnapshot, Nodes: []graph.Node{node("shop"), node("payments")}})
	out, err := stripRaw(raw, onlyShop)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(out), "secret-ish") != 1 {
		t.Errorf("out = %s", out)
	}
	incidents := []byte(`{"v":1,"type":"incidents","incidents":[]}`)
	if out, _ := stripRaw(incidents, onlyShop); string(out) != string(incidents) {
		t.Errorf("changed a message without nodes: %s", out)
	}
	if _, err := stripRaw([]byte("{"), onlyShop); err == nil {
		t.Error("passed an unreadable message through")
	}
}
