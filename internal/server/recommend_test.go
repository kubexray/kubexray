package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/kubexray/kubexray/internal/cause"
	"github.com/kubexray/kubexray/internal/feed"
	"github.com/kubexray/kubexray/internal/recommend"
)

type noModel struct{}

func (noModel) Name() string { return "test/none" }
func (noModel) Complete(context.Context, string, string, map[string]any) (string, recommend.Usage, error) {
	return "", recommend.Usage{}, context.Canceled
}

type noCluster struct{}

func (noCluster) Object(context.Context, string, string, string) (*unstructured.Unstructured, error) {
	return nil, context.Canceled
}

func (noCluster) List(context.Context, string, string) ([]*unstructured.Unstructured, error) {
	return nil, context.Canceled
}

func (noCluster) PreviousLogs(context.Context, string, string, string) (string, error) {
	return "", context.Canceled
}

func TestRecommendEndpoints(t *testing.T) {
	t.Parallel()
	f := start(t, MaxClients)
	// Off: no endpoint, and the UI isn't told about a model.
	if got := f.status(req{path: "/api/recommend?set=x", cookie: true}); got != http.StatusNotFound {
		t.Errorf("off: %d", got)
	}
	var h HistoryResponse
	if err := json.Unmarshal(f.do(req{path: "/api/history", cookie: true}).body, &h); err != nil || h.Recommend != "" {
		t.Errorf("history = %+v, %v", h, err)
	}

	f = start(t, MaxClients, func(s *Server) {
		s.SetRecommender(recommend.NewService(noModel{}, noCluster{}, slog.New(slog.DiscardHandler)))
	})
	if err := json.Unmarshal(f.do(req{path: "/api/history", cookie: true}).body, &h); err != nil || h.Recommend != "test/none" {
		t.Errorf("history = %+v, %v", h, err)
	}
	if got := f.status(req{path: "/api/recommend?set=no-such-change", cookie: true}); got != http.StatusNotFound {
		t.Errorf("unknown change = %d, want 404", got)
	}
}

func TestMadeBy(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 26, 18, 10, 16, 0, time.UTC)
	set := feed.Set{At: at, Last: at, Entries: []feed.Entry{{Object: "ConfigMap/shop/checkout-config"}}}
	for _, tc := range []struct {
		c    cause.Cause
		want bool
	}{
		{cause.Cause{Object: "ConfigMap/shop/checkout-config", At: at}, true},
		{cause.Cause{Object: "ConfigMap/shop/checkout-config", At: at.Add(2 * time.Minute)}, false}, // the fix, a later change
		{cause.Cause{Object: "ConfigMap/shop/api-config", At: at}, false},
	} {
		if got := madeBy(set, tc.c); got != tc.want {
			t.Errorf("madeBy(%+v) = %v", tc.c, got)
		}
	}
}
