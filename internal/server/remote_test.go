package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/kubexray/kubexray/internal/alert"
	"github.com/kubexray/kubexray/internal/auth"
	"github.com/kubexray/kubexray/internal/auth/authtest"
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
		if got := f.status(req{path: "/api/history", host: host, cookie: true}); got != want {
			t.Errorf("Host %s = %d, want %d", host, got, want)
		}
	}
	// Bearer clients send no cookies: the in-cluster Service name is fine.
	if got := f.status(req{path: "/api/history", host: "kx-kubexray.kubexray.svc", bearer: testToken}); got != http.StatusOK {
		t.Errorf("bearer client on the Service name = %d, want 200", got)
	}
	if got := f.status(req{path: "/api/history", host: "kx-kubexray.kubexray.svc", bearer: "wrong-" + testToken}); got != http.StatusUnauthorized {
		t.Errorf("wrong bearer token on the Service name = %d, want 401", got)
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

func TestAlertmanagerReceiver(t *testing.T) {
	t.Parallel()
	got := make(chan string, 4)
	slack := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- string(b)
	}))
	defer slack.Close()

	g := graph.New()
	brokenApp(g)
	hub := NewHub(MaxClients, 4)
	rec := recorder.New(recorder.DefaultOptions)
	_, _, snap := g.FlushAndSnapshot()
	f := &fixture{t: t, hub: hub, g: g, rec: rec}
	f.emit(snap, true)
	srv, err := New(hub, rec, web.Assets(), slog.New(slog.DiscardHandler), Info{})
	if err != nil {
		t.Fatal(err)
	}
	notifier := alert.NewNotifier(slack.URL, "", slog.New(slog.DiscardHandler))
	srv.SetAlerts(notifier, "https://kubexray.example.com")
	ln, err := srv.ListenRemote(t.Context(), Remote{Addr: "127.0.0.1:0", Token: testToken})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { _ = srv.Serve(ctx, ln) }()
	go notifier.Run(ctx)

	post := func(token, body string) int {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+ln.Addr().String()+"/api/alertmanager", strings.NewReader(body))
		req.Host = "kx-kubexray.kubexray.svc" // Alertmanager uses the Service name
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	const firing = `{"version":"4","status":"firing","alerts":[
		{"status":"firing","fingerprint":"a1","labels":{"alertname":"KubePodCrashLooping","namespace":"shop","pod":"api-x"},"startsAt":"2026-10-24T14:00:00Z"},
		{"status":"firing","fingerprint":"a4","labels":{"alertname":"KubeNodeNotReady","namespace":"monitoring","pod":"kube-state-metrics-x"}},
		{"status":"resolved","fingerprint":"a2","labels":{"alertname":"KubePodCrashLooping","namespace":"shop","deployment":"api"}},
		{"status":"firing","fingerprint":"a3","labels":{"alertname":"KubeNodeNotReady","node":"worker-2"}}]}`
	// Without the token: refused (by the Host check, before authentication).
	if code := post("", firing); code != http.StatusMisdirectedRequest && code != http.StatusUnauthorized {
		t.Errorf("without the token = %d, want it refused", code)
	}
	if code := post(testToken, "not json"); code != http.StatusBadRequest {
		t.Errorf("a bad payload = %d, want 400", code)
	}
	if code := post(testToken, firing); code != http.StatusAccepted {
		t.Fatalf("a notification = %d, want 202", code)
	}
	select {
	case body := <-got:
		// The pod alert is explained through its app.
		if !strings.Contains(body, "*api is down*") || !strings.Contains(body, "kubexray.example.com/?app=Deployment%2Fshop%2Fapi") {
			t.Errorf("Slack message = %s", body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing sent to Slack")
	}
	select {
	case body := <-got: // only the firing alert about an app is sent
		t.Errorf("a second message was sent: %s", body)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestServeModeCSPIgnoresMalformedHosts(t *testing.T) {
	t.Parallel()
	f := startRemote(t, Remote{Token: testToken})
	resp := f.do(req{path: "/api/history", host: "x;script-src*", bearer: testToken})
	if csp := resp.header.Get("Content-Security-Policy"); strings.Contains(csp, "script-src*") || !strings.Contains(csp, "connect-src 'self';") {
		t.Errorf("CSP for a malformed Host = %q", csp)
	}
}

func TestServeModeSignIn(t *testing.T) {
	t.Parallel()
	p := authtest.New(t)
	o, err := auth.NewOIDC(t.Context(), auth.Config{
		Issuer: p.URL, ClientID: "kubexray", ClientSecret: "s", PublicURL: "https://kubexray.example.com",
		Rules: auth.Rules{Domains: []string{"example.com"}}, SessionKey: []byte(strings.Repeat("k", 32)), SessionTTL: time.Hour,
	}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	f := startRemote(t, Remote{Token: testToken, OIDC: o, AllowedHosts: []string{"kubexray.example.com"}})
	host := "kubexray.example.com"

	// A page without a session goes to sign in, and comes back to it.
	resp := f.do(req{path: "/?view=changes", host: host})
	if resp.code != http.StatusFound || resp.header.Get("Location") != "/auth/login?return=%2F%3Fview%3Dchanges" {
		t.Errorf("page without a session = %d %q", resp.code, resp.header.Get("Location"))
	}
	if got := f.status(req{path: "/auth/login", host: host}); got != http.StatusFound {
		t.Errorf("/auth/login = %d, want a redirect to the provider", got)
	}
	// The API answers 401; the token link no longer signs browsers in.
	if got := f.status(req{path: "/api/history", host: host}); got != http.StatusUnauthorized {
		t.Errorf("API without a session = %d, want 401", got)
	}
	if resp := f.do(req{path: "/?token=" + testToken, host: host}); resp.code != http.StatusFound || len(resp.cookies) != 0 {
		t.Errorf("token link = %d, cookies %+v; want the sign-in redirect", resp.code, resp.cookies)
	}
	// Machines keep the token.
	if got := f.status(req{path: "/api/history", host: "kx.kubexray.svc", bearer: testToken}); got != http.StatusOK {
		t.Errorf("bearer token = %d, want 200", got)
	}

	// Signed in: the API answers, and says who.
	c, err := o.Session(auth.User{Email: "ada@example.com", Name: "Ada"})
	if err != nil {
		t.Fatal(err)
	}
	hr, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+f.host+"/api/me", nil)
	hr.Host = host
	hr.AddCookie(c)
	got, err := noRedirects.Do(hr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = got.Body.Close() }()
	body, _ := io.ReadAll(got.Body)
	if got.StatusCode != http.StatusOK || !strings.Contains(string(body), `"email":"ada@example.com"`) || !strings.Contains(string(body), `"signOut":"/auth/logout"`) {
		t.Errorf("/api/me = %d %s", got.StatusCode, body)
	}
}

func TestServeModeTokenSessionHasNoUser(t *testing.T) {
	t.Parallel()
	f := startRemote(t, Remote{Token: testToken})
	if resp := f.do(req{path: "/api/me", cookie: true}); resp.code != http.StatusOK || strings.TrimSpace(string(resp.body)) != "{}" {
		t.Errorf("/api/me with a token session = %d %s", resp.code, resp.body)
	}
}

func TestServeModeLimitsBadTokens(t *testing.T) {
	t.Parallel()
	f := startRemote(t, Remote{Token: testToken})
	for range badTokens {
		if got := f.status(req{path: "/api/history", bearer: "wrong-" + testToken}); got != http.StatusUnauthorized {
			t.Fatalf("bad token = %d, want 401", got)
		}
	}
	if got := f.status(req{path: "/api/history", bearer: "wrong-" + testToken}); got != http.StatusTooManyRequests {
		t.Errorf("bad token past the limit = %d, want 429", got)
	}
}

func TestServeModeStreamEndsWithTheSignIn(t *testing.T) {
	t.Parallel()
	p := authtest.New(t)
	o, err := auth.NewOIDC(t.Context(), auth.Config{
		Issuer: p.URL, ClientID: "kubexray", ClientSecret: "s", PublicURL: "http://localhost",
		Rules: auth.Rules{Domains: []string{"example.com"}}, SessionKey: []byte(strings.Repeat("k", 32)), SessionTTL: 2 * time.Second,
	}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	f := startRemote(t, Remote{Token: testToken, OIDC: o})
	c, err := o.Session(auth.User{Email: "ada@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	h := http.Header{}
	h.Set("Cookie", c.Name+"="+c.Value)
	h.Set("Origin", "http://"+f.host)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, "ws://"+f.host+"/api/stream", &websocket.DialOptions{HTTPHeader: h})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.CloseNow() }()
	for {
		if _, _, err := conn.Read(ctx); err != nil {
			if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
				t.Fatalf("stream ended with %v, want the sign-in ending", err)
			}
			return
		}
	}
}
