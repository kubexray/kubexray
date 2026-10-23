package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/kubexray/kubexray/internal/cause"
	"github.com/kubexray/kubexray/internal/graph"
	"github.com/kubexray/kubexray/internal/recorder"
	"github.com/kubexray/kubexray/internal/replay"
	"github.com/kubexray/kubexray/internal/web"
)

// fixture is a running server over a small cluster with one broken app.
type fixture struct {
	t    *testing.T
	srv  *Server
	hub  *Hub
	g    *graph.Graph
	rec  *recorder.Recorder
	host string
}

func brokenApp(g *graph.Graph) {
	g.UpsertNode(graph.Node{
		ID: "Deployment/shop/api", Kind: "Deployment", Namespace: "shop", Name: "api",
		Status: graph.Status{Replicas: &graph.Replicas{Desired: 1}},
	})
	g.UpsertNode(graph.Node{
		ID: "Pod/shop/api-x", Kind: "Pod", Namespace: "shop", Name: "api-x",
		Created: time.Now().Add(-10 * time.Minute),
		Status:  graph.Status{Phase: "Running", Reason: "CrashLoopBackOff", Since: time.Now().Add(-5 * time.Minute)},
	})
	g.UpsertEdge(graph.NewEdge(graph.EdgeOwns, "Deployment/shop/api", "Pod/shop/api-x"))
	g.UpsertNode(graph.Node{ID: "Service/shop/api", Kind: "Service", Namespace: "shop", Name: "api"})
}

func start(t *testing.T, maxClients int) *fixture {
	t.Helper()
	g := graph.New()
	brokenApp(g)
	hub := NewHub(maxClients, 4)
	rec := recorder.New(recorder.DefaultOptions)
	_, _, snap := g.FlushAndSnapshot()
	f := &fixture{t: t, hub: hub, g: g, rec: rec}
	f.emit(snap, true)

	srv, err := New(hub, rec, web.Assets(), slog.New(slog.DiscardHandler), Info{Cluster: "test", Version: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := srv.Listen(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
	f.srv, f.host = srv, ln.Addr().String()
	return f
}

// emit sends a message through the hub and the recorder, as the pump does.
func (f *fixture) emit(m graph.Message, keyframe bool) {
	f.t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		f.t.Fatal(err)
	}
	if keyframe {
		err = f.rec.Keyframe(m, raw)
	} else {
		err = f.rec.Record(m, raw)
	}
	if err != nil {
		f.t.Fatal(err)
	}
	if err := f.hub.Emit(m, raw); err != nil {
		f.t.Fatal(err)
	}
}

type req struct {
	path   string
	host   string // overrides the Host header
	cookie bool
	origin string
}

var noRedirects = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// response is a fully read HTTP response.
type response struct {
	code    int
	header  http.Header
	cookies []*http.Cookie
	body    []byte
}

func (f *fixture) do(r req) response {
	f.t.Helper()
	hr, err := http.NewRequestWithContext(f.t.Context(), http.MethodGet, "http://"+f.host+r.path, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	if r.host != "" {
		hr.Host = r.host
	}
	if r.cookie {
		hr.AddCookie(&http.Cookie{Name: f.srv.cookie, Value: f.srv.token}) //nolint:gosec // a client-side test cookie
	}
	if r.origin != "" {
		hr.Header.Set("Origin", r.origin)
	}
	resp, err := noRedirects.Do(hr)
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		f.t.Fatal(err)
	}
	return response{code: resp.StatusCode, header: resp.Header, cookies: resp.Cookies(), body: body}
}

func (f *fixture) status(r req) int { return f.do(r).code }

// dial opens the stream; it returns the HTTP status when the upgrade fails.
func (f *fixture) dial(origin string) (*websocket.Conn, int, error) {
	h := http.Header{}
	h.Set("Cookie", f.srv.cookie+"="+f.srv.token)
	if origin != "" {
		h.Set("Origin", origin)
	}
	ctx, cancel := context.WithTimeout(f.t.Context(), 5*time.Second)
	defer cancel()
	c, resp, err := websocket.Dial(ctx, "ws://"+f.host+"/api/stream", &websocket.DialOptions{HTTPHeader: h})
	code := 0
	if resp != nil {
		code = resp.StatusCode
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
	}
	if c != nil {
		f.t.Cleanup(func() { _ = c.CloseNow() })
	}
	return c, code, err
}

func read(t *testing.T, c *websocket.Conn) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, raw, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("reading from the stream: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// --- security controls ---

func TestListensOnLoopbackOnly(t *testing.T) {
	t.Parallel()
	f := start(t, 4)
	if ip := net.ParseIP(strings.Split(f.host, ":")[0]); !ip.IsLoopback() {
		t.Errorf("listening on %s, want 127.0.0.1", f.host)
	}
	if !strings.HasPrefix(f.srv.URL(), "http://127.0.0.1:") || len(f.srv.token) < 40 {
		t.Errorf("URL = %s, token length %d", f.srv.URL(), len(f.srv.token))
	}
}

func TestTokenExchangeSetsAStrictHttpOnlyCookie(t *testing.T) {
	t.Parallel()
	f := start(t, 4)
	resp := f.do(req{path: "/?token=" + f.srv.token})
	if resp.code != http.StatusSeeOther || resp.header.Get("Location") != "/" {
		t.Fatalf("status %d, Location %q; want a redirect to / that drops the token", resp.code, resp.header.Get("Location"))
	}
	cookies := resp.cookies
	if len(cookies) != 1 {
		t.Fatalf("cookies = %v", cookies)
	}
	c := cookies[0]
	_, port, _ := net.SplitHostPort(f.host)
	if c.Name != "kubexray_session_"+port || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" {
		t.Errorf("cookie = %+v; want HttpOnly, SameSite=Strict, Path=/", c)
	}
}

func TestWrongTokenIsRejected(t *testing.T) {
	t.Parallel()
	f := start(t, 4)
	if got := f.status(req{path: "/?token=guess"}); got != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", got)
	}
}

func TestEverythingNeedsTheSession(t *testing.T) {
	t.Parallel()
	f := start(t, 4)
	for _, path := range []string{"/", "/index.html", "/api/history", "/api/at?t=2026-01-01T00:00:00Z", "/api/why?id=x", "/api/stream"} {
		if got := f.status(req{path: path}); got != http.StatusUnauthorized {
			t.Errorf("GET %s without a session = %d, want 401", path, got)
		}
	}
}

func TestForeignHostIsRejected(t *testing.T) {
	t.Parallel()
	f := start(t, 4)
	// A DNS rebinding page reaches 127.0.0.1 under its own name.
	_, port, _ := net.SplitHostPort(f.host)
	for _, host := range []string{"evil.example:" + port, "evil.example", "127.0.0.1:1"} {
		if got := f.status(req{path: "/api/history", host: host, cookie: true}); got != http.StatusMisdirectedRequest {
			t.Errorf("Host %q = %d, want 421", host, got)
		}
	}
	if got := f.status(req{path: "/api/history", host: "localhost:" + port, cookie: true}); got != http.StatusOK {
		t.Errorf("Host localhost:%s = %d, want 200", port, got)
	}
}

func TestSecurityHeaders(t *testing.T) {
	t.Parallel()
	f := start(t, 4)
	h := f.do(req{path: "/", cookie: true}).header
	csp := h.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'self'", "script-src 'self'", "frame-ancestors 'none'", "object-src 'none'", "connect-src 'self' ws://" + f.host} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q lacks %q", csp, want)
		}
	}
	if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") {
		t.Errorf("CSP allows unsafe code: %q", csp)
	}
	for k, want := range map[string]string{"X-Content-Type-Options": "nosniff", "Referrer-Policy": "no-referrer", "Cache-Control": "no-store"} {
		if got := h.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

func TestCrossOriginWebSocketIsRefused(t *testing.T) {
	t.Parallel()
	f := start(t, 4)
	for _, origin := range []string{"http://evil.example", "null", ""} {
		if _, code, err := f.dial(origin); err == nil || code != http.StatusForbidden {
			t.Errorf("Origin %q: err %v, status %d; want 403", origin, err, code)
		}
	}
}

func TestClientLimit(t *testing.T) {
	t.Parallel()
	f := start(t, 1)
	if _, _, err := f.dial("http://" + f.host); err != nil {
		t.Fatal(err)
	}
	if _, code, err := f.dial("http://" + f.host); err == nil || code != http.StatusServiceUnavailable {
		t.Errorf("second client: err %v, status %d; want 503", err, code)
	}
}

// --- the stream ---

func TestStreamSendsSnapshotIncidentsThenChanges(t *testing.T) {
	t.Parallel()
	f := start(t, 4)
	select {
	case <-f.hub.Ready():
	default:
		t.Fatal("hub not ready after its first snapshot")
	}
	if err := f.hub.UpdateIncidents(time.Now()); err != nil {
		t.Fatal(err)
	}
	c, _, err := f.dial("http://" + f.host)
	if err != nil {
		t.Fatal(err)
	}

	snap := read(t, c)
	if snap["type"] != "snapshot" || len(snap["nodes"].([]any)) != 3 {
		t.Fatalf("first message = %v", snap)
	}
	inc := read(t, c)
	if inc["type"] != cause.MessageIncidents || len(inc["incidents"].([]any)) != 1 {
		t.Fatalf("second message = %v", inc)
	}

	f.g.RemoveNode("Service/shop/api")
	delta, _ := f.g.Flush()
	f.emit(delta, false)
	got := read(t, c)
	if got["type"] != "delta" || got["seq"].(float64) != snap["seq"].(float64)+1 {
		t.Errorf("third message = %v, want the next delta", got)
	}
}

func TestIncidentsAreSentOnlyWhenTheyChange(t *testing.T) {
	t.Parallel()
	f := start(t, 4)
	_ = f.hub.UpdateIncidents(time.Now())
	c, first, err := f.hub.subscribe()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 {
		t.Fatalf("a new client gets %d messages, want snapshot and incidents", len(first))
	}
	// Re-analyzed after the recheck interval, but unchanged: nothing sent.
	_ = f.hub.UpdateIncidents(time.Now().Add(2 * incidentsRecheck))
	select {
	case m := <-c.send:
		t.Errorf("unchanged incidents were sent again: %s", m)
	default:
	}
	// A change shows up even before the recheck interval.
	f.g.RemoveNode("Pod/shop/api-x")
	delta, _ := f.g.Flush()
	f.emit(delta, false)
	<-c.send // the delta
	_ = f.hub.UpdateIncidents(time.Now().Add(2*incidentsRecheck + time.Second))
	select {
	case m := <-c.send:
		if !strings.Contains(string(m), `"incidents":[`) {
			t.Errorf("expected new incidents, got %s", m)
		}
	default:
		t.Error("changed incidents were not sent")
	}
}

func TestSlowClientsAreDropped(t *testing.T) {
	t.Parallel()
	f := start(t, 4) // queue of 4
	c, _, err := f.hub.subscribe()
	if err != nil {
		t.Fatal(err)
	}
	for i := range 6 {
		f.g.UpsertNode(graph.Node{ID: "Pod/shop/n" + string(rune('a'+i)), Kind: "Pod", Name: "n"})
		delta, _ := f.g.Flush()
		f.emit(delta, false)
	}
	n := 0
	for range c.send {
		n++
	}
	if n != 4 {
		t.Errorf("client received %d queued messages before being dropped, want 4", n)
	}
}

func TestHubRejectsOutOfOrderMessages(t *testing.T) {
	t.Parallel()
	h := NewHub(1, 1)
	if err := h.Emit(graph.Message{Type: graph.MessageDelta, Seq: 1}, nil); err == nil {
		t.Error("a delta before any snapshot was accepted")
	}
	if _, _, err := h.subscribe(); err == nil {
		t.Error("subscribe before the first snapshot succeeded")
	}
	select {
	case <-h.Ready():
		t.Error("hub ready before its first snapshot")
	default:
	}
	if _, err := h.explain("x", time.Now()); !errors.Is(err, errNotSynced) {
		t.Errorf("explain before sync = %v, want errNotSynced", err)
	}
}

// --- the API ---

func TestHistoryAndRewind(t *testing.T) {
	t.Parallel()
	f := start(t, 4)
	var hist HistoryResponse
	if err := json.Unmarshal(f.do(req{path: "/api/history", cookie: true}).body, &hist); err != nil || hist.From.IsZero() {
		t.Fatalf("history = %+v, %v", hist, err)
	}

	// Times carry a "+hh:mm" zone; clients must encode them ("+" means space in a query).
	resp := f.do(req{path: "/api/at?t=" + url.QueryEscape(hist.To.Format(time.RFC3339Nano)), cookie: true})
	var at AtResponse
	if err := json.Unmarshal(resp.body, &at); err != nil {
		t.Fatal(err)
	}
	var snap graph.Message
	if err := json.Unmarshal(at.Snapshot, &snap); err != nil || len(snap.Nodes) != 3 || len(at.Incidents) != 1 {
		t.Errorf("at = %d nodes, %d incidents, %v", len(snap.Nodes), len(at.Incidents), err)
	}

	before := url.QueryEscape(hist.From.Add(-time.Hour).Format(time.RFC3339Nano))
	if got := f.status(req{path: "/api/at?t=" + before, cookie: true}); got != http.StatusNotFound {
		t.Errorf("before history = %d, want 404", got)
	}
	if got := f.status(req{path: "/api/at?t=yesterday", cookie: true}); got != http.StatusBadRequest {
		t.Errorf("bad time = %d, want 400", got)
	}
}

func TestWhy(t *testing.T) {
	t.Parallel()
	f := start(t, 4)
	var inc cause.Incident
	if err := json.Unmarshal(f.do(req{path: "/api/why?id=Deployment/shop/api", cookie: true}).body, &inc); err != nil || inc.Healthy || inc.Symptom.Summary != "1 pod CrashLoopBackOff" {
		t.Errorf("why = %+v, %v", inc, err)
	}
	for path, want := range map[string]int{
		"/api/why":                         http.StatusBadRequest,
		"/api/why?id=Service/shop/api":     http.StatusUnprocessableEntity,
		"/api/why?id=Deployment/shop/nope": http.StatusNotFound,
	} {
		if got := f.status(req{path: path, cookie: true}); got != want {
			t.Errorf("GET %s = %d, want %d", path, got, want)
		}
	}
}

func TestChanges(t *testing.T) {
	t.Parallel()
	f := start(t, 4)
	f.g.UpsertNode(graph.Node{
		ID: "ConfigMap/shop/cfg", Kind: "ConfigMap", Namespace: "shop", Name: "cfg",
		Change: &graph.Change{At: time.Now(), By: "kubectl-patch", Tool: "kubectl", Via: "patch"},
	})
	delta, _ := f.g.Flush()
	f.emit(delta, false)
	var list []cause.Change
	if err := json.Unmarshal(f.do(req{path: "/api/changes", cookie: true}).body, &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Object != "ConfigMap/shop/cfg" || list[0].By.Via != "patch" {
		t.Errorf("changes = %+v", list)
	}
}

func TestExport(t *testing.T) {
	t.Parallel()
	f := start(t, 4)
	for _, anon := range []bool{false, true} {
		path := "/api/export"
		if anon {
			path += "?anonymize=1"
		}
		resp := f.do(req{path: path, cookie: true})
		if resp.code != http.StatusOK || resp.header.Get("Content-Type") != "application/gzip" || !strings.Contains(resp.header.Get("Content-Disposition"), ".kxr") {
			t.Fatalf("GET %s = %d %v", path, resp.code, resp.header)
		}
		file, err := replay.Read(bytes.NewReader(resp.body))
		if err != nil {
			t.Fatalf("exported replay doesn't read back: %v", err)
		}
		wantCluster := "test"
		if anon {
			wantCluster = "cluster"
		}
		if file.Header.Cluster != wantCluster || file.Header.Anonymized != anon || len(file.Incidents) == 0 {
			t.Errorf("anonymize=%v: header %+v, %d incident messages", anon, file.Header, len(file.Incidents))
		}
	}
	if got := f.status(req{path: "/api/export?from=bad", cookie: true}); got != http.StatusBadRequest {
		t.Errorf("bad from = %d, want 400", got)
	}
	if got := f.status(req{path: "/api/export?from=2026-01-02T00:00:00Z&to=2026-01-01T00:00:00Z", cookie: true}); got != http.StatusBadRequest {
		t.Errorf("reversed window = %d, want 400", got)
	}
	if got := f.status(req{path: "/api/export"}); got != http.StatusUnauthorized {
		t.Errorf("export without a session = %d, want 401", got)
	}
}

func TestPlayerMode(t *testing.T) {
	t.Parallel()
	file := []byte("a validated replay")
	srv, err := NewPlayer(file, web.Assets(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	ln, err := srv.Listen(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { _ = srv.Serve(ctx, ln) }()
	f := &fixture{t: t, srv: srv, host: ln.Addr().String()}

	if resp := f.do(req{path: "/?token=" + srv.token}); resp.header.Get("Location") != "/?replay=%2Fapi%2Freplay" {
		t.Errorf("landing = %q, want the player with the replay", resp.header.Get("Location"))
	}
	if resp := f.do(req{path: "/api/replay", cookie: true}); resp.code != http.StatusOK || string(resp.body) != string(file) {
		t.Errorf("GET /api/replay = %d %q", resp.code, resp.body)
	}
	for _, path := range []string{"/api/replay"} {
		if got := f.status(req{path: path}); got != http.StatusUnauthorized {
			t.Errorf("%s without a session = %d, want 401", path, got)
		}
	}
	// No cluster routes in player mode.
	for _, path := range []string{"/api/stream", "/api/export", "/api/history"} {
		if got := f.status(req{path: path, cookie: true}); got != http.StatusNotFound {
			t.Errorf("%s in player mode = %d, want 404", path, got)
		}
	}
}

func TestServesTheUI(t *testing.T) {
	t.Parallel()
	f := start(t, 4)
	resp := f.do(req{path: "/", cookie: true})
	if resp.code != http.StatusOK || !strings.Contains(string(resp.body), "kubexray") {
		t.Errorf("GET / = %d: %.80s", resp.code, resp.body)
	}
}

func TestServeStopsWithTheContext(t *testing.T) {
	t.Parallel()
	srv, err := New(NewHub(1, 1), recorder.New(recorder.DefaultOptions), web.Assets(), slog.New(slog.DiscardHandler), Info{})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := srv.Listen(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, ln) }()
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("Serve = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve didn't stop")
	}
}
