package server

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kubexray/kubexray/internal/graph"
	"github.com/kubexray/kubexray/internal/recorder"
	"github.com/kubexray/kubexray/internal/web"
)

// Plainly fake tokens, long enough to be accepted.
var testToken = "test-token-" + strings.Repeat("a", 32)

// startRemote runs a server in serve mode (ADR-0010) on a loopback port.
func startRemote(t *testing.T, r Remote) *fixture {
	t.Helper()
	g := graph.New()
	brokenApp(g)
	hub := NewHub(MaxClients, 4)
	rec := recorder.New(recorder.DefaultOptions)
	_, _, snap := g.FlushAndSnapshot()
	f := &fixture{t: t, hub: hub, g: g, rec: rec}
	f.emit(snap, true)
	srv, err := New(hub, rec, web.Assets(), slog.New(slog.DiscardHandler), Info{Cluster: "in-cluster", Version: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Addr == "" {
		r.Addr = "127.0.0.1:0"
	}
	ln, err := srv.ListenRemote(t.Context(), r)
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

func TestServeModeRefusesUnsafeSettings(t *testing.T) {
	t.Parallel()
	srv, err := New(nil, nil, web.Assets(), slog.New(slog.DiscardHandler), Info{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []Remote{
		{Addr: "0.0.0.0:0", NoAuth: true}, // reachable without authentication
		{Addr: ":0", NoAuth: true},
		{Addr: "127.0.0.1:0", Token: "short"},
		{Addr: "127.0.0.1:0"},
	} {
		if ln, err := srv.ListenRemote(t.Context(), r); err == nil {
			_ = ln.Close()
			t.Errorf("ListenRemote(%+v) succeeded", r)
		}
	}
}

func TestServeModeAuthentication(t *testing.T) {
	t.Parallel()
	f := startRemote(t, Remote{Token: testToken})
	if got := f.status(req{path: "/healthz", host: "10.244.1.7:8080"}); got != http.StatusOK {
		t.Errorf("/healthz from the kubelet = %d, want 200 without a token", got)
	}
	if got := f.status(req{path: "/api/history"}); got != http.StatusUnauthorized {
		t.Errorf("/api/history without a token = %d, want 401", got)
	}
	if got := f.status(req{path: "/api/history", bearer: "wrong-" + testToken}); got != http.StatusUnauthorized {
		t.Errorf("/api/history with a wrong token = %d, want 401", got)
	}
	if got := f.status(req{path: "/api/history", bearer: testToken}); got != http.StatusOK {
		t.Errorf("/api/history with the token = %d, want 200", got)
	}
	resp := f.do(req{path: "/?token=" + testToken})
	if resp.code != http.StatusSeeOther || len(resp.cookies) != 1 || resp.cookies[0].Secure {
		t.Errorf("token exchange = %d, cookies %+v; want a redirect and a cookie (not Secure without TLS)", resp.code, resp.cookies)
	}
	if got := f.status(req{path: "/api/history", cookie: true}); got != http.StatusOK {
		t.Errorf("/api/history with the cookie = %d", got)
	}
}

func TestServeModeHostsAndOrigins(t *testing.T) {
	t.Parallel()
	f := startRemote(t, Remote{Token: testToken, AllowedHosts: []string{"kubexray.example.com"}, SecureCookie: true})
	for host, want := range map[string]int{
		"localhost:18080":           http.StatusOK, // kubectl port-forward, any local port
		"127.0.0.1:9999":            http.StatusOK,
		"kubexray.example.com":      http.StatusOK, // the Ingress
		"kubexray.example.com:443":  http.StatusOK,
		"evil.example":              http.StatusMisdirectedRequest,
		"kubexray.example.com.evil": http.StatusMisdirectedRequest,
	} {
		if got := f.status(req{path: "/api/history", host: host, bearer: testToken}); got != want {
			t.Errorf("Host %s = %d, want %d", host, got, want)
		}
	}
	resp := f.do(req{path: "/?token=" + testToken, host: "kubexray.example.com"})
	if len(resp.cookies) != 1 || !resp.cookies[0].Secure {
		t.Errorf("cookies behind TLS = %+v, want Secure", resp.cookies)
	}
	if csp := resp.header.Get("Content-Security-Policy"); !strings.Contains(csp, "wss://kubexray.example.com") {
		t.Errorf("CSP = %q, want the Ingress host's WebSocket", csp)
	}
	// The UI over https may open the stream; other origins may not.
	if got := f.status(req{path: "/api/stream", host: "kubexray.example.com", bearer: testToken, origin: "https://kubexray.example.com"}); got == http.StatusForbidden {
		t.Error("same-origin https WebSocket refused")
	}
	if got := f.status(req{path: "/api/stream", host: "kubexray.example.com", bearer: testToken, origin: "https://evil.example"}); got != http.StatusForbidden {
		t.Errorf("cross-origin WebSocket = %d, want 403", got)
	}
}

func TestServeModeBehindAnAuthProxy(t *testing.T) {
	t.Parallel()
	f := startRemote(t, Remote{NoAuth: true})
	if got := f.status(req{path: "/api/history"}); got != http.StatusOK {
		t.Errorf("/api/history behind a proxy = %d, want 200 without a token", got)
	}
}

func TestServeModeFollowsTheTokenFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(testToken), 0o600); err != nil {
		t.Fatal(err)
	}
	f := startRemote(t, Remote{Token: testToken, TokenFile: path})
	clock := time.Now()
	f.srv.tokens.now = func() time.Time { return clock }

	// The Secret is rotated: the kubelet rewrites the file.
	rotated := "rotated-token-" + strings.Repeat("b", 32)
	if err := os.WriteFile(path, []byte(rotated+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, clock, clock.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(tokenRecheck)
	if got := f.status(req{path: "/api/history", bearer: rotated}); got != http.StatusOK {
		t.Errorf("the rotated token = %d, want 200", got)
	}
	if got := f.status(req{path: "/api/history", bearer: testToken}); got != http.StatusUnauthorized {
		t.Errorf("the old token after rotation = %d, want 401", got)
	}
}

func TestServeModeCookiesPerPort(t *testing.T) {
	t.Parallel()
	f := startRemote(t, Remote{Token: testToken})
	a := f.do(req{path: "/?token=" + testToken, host: "localhost:8080"})
	b := f.do(req{path: "/?token=" + testToken, host: "localhost:8081"})
	if len(a.cookies) != 1 || len(b.cookies) != 1 || a.cookies[0].Name == b.cookies[0].Name {
		t.Errorf("cookies for two local ports: %+v and %+v, want different names", a.cookies, b.cookies)
	}
}

func TestServeModeProxyAcceptsAnyHost(t *testing.T) {
	t.Parallel()
	// oauth2-proxy passes the external Host through; it has checked it.
	f := startRemote(t, Remote{NoAuth: true})
	if got := f.status(req{path: "/api/history", host: "kubexray.example.com"}); got != http.StatusOK {
		t.Errorf("Host through the proxy = %d, want 200", got)
	}
}
