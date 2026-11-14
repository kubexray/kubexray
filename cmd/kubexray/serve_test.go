package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kubexray/kubexray/internal/graph"
	"github.com/kubexray/kubexray/internal/recorder"
	"github.com/kubexray/kubexray/internal/server"
	"github.com/kubexray/kubexray/internal/web"
)

// Serve mode answers its probes from the moment it listens, before the
// cluster has synced, and stops cleanly when startup fails.
func TestServingStartsBeforeTheClusterSyncs(t *testing.T) {
	t.Parallel()
	hub := server.NewHub(server.MaxClients, server.ClientQueue)
	srv, err := server.New(hub, recorder.New(recorder.DefaultOptions), web.Assets(), slog.New(slog.DiscardHandler), server.Info{})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := srv.ListenRemote(t.Context(), server.Remote{Addr: "127.0.0.1:0", Token: "test-token-" + strings.Repeat("a", 32)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	running := startServing(ctx, cancel, srv, ln)
	probe := func(path string) int {
		t.Helper()
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+ln.Addr().String()+path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if live, ready := probe("/healthz"), probe("/readyz"); live != http.StatusOK || ready != http.StatusServiceUnavailable {
		t.Errorf("while syncing: /healthz %d, /readyz %d; want 200 and 503", live, ready)
	}
	failed := errors.New("can't list pods")
	if err := running.abort(failed); !errors.Is(err, failed) {
		t.Errorf("abort = %v, want the startup error", err)
	}
}

func TestLearnFromHistory(t *testing.T) {
	t.Parallel()
	rec := recorder.New(recorder.DefaultOptions)
	ts := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	n := graph.Node{ID: "Traffic/shop/deployment.api", Kind: graph.KindTraffic, Name: "api", Traffic: &graph.Traffic{RPS: 5}}
	snap := graph.Message{V: graph.SchemaVersion, Type: graph.MessageSnapshot, TS: ts, Nodes: []graph.Node{n}}
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	if err := rec.Keyframe(snap, raw); err != nil {
		t.Fatal(err)
	}
	hub := server.NewHub(server.MaxClients, server.ClientQueue)
	select {
	case h := <-learnFromHistory(rec.Walk, hub, slog.New(slog.DiscardHandler)):
		if h == nil {
			t.Error("no traffic history learned")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("learning from history never finished")
	}
}
