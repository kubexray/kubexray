package auth

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kubexray/kubexray/internal/auth/authtest"
)

var testKey = []byte(strings.Repeat("k", 32))

func newOIDC(t *testing.T, p *authtest.Provider, rules Rules) *OIDC {
	t.Helper()
	o, err := NewOIDC(context.Background(), Config{
		Issuer: p.URL, ClientID: "kubexray", ClientSecret: "s", PublicURL: "https://kx.example.com",
		Rules: rules, SessionKey: testKey, SessionTTL: time.Hour,
	}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	return o
}

// signIn runs the flow: login, then the provider's redirect back with code.
// claims are what the ID token says; the nonce is filled in unless set.
func signIn(t *testing.T, o *OIDC, p *authtest.Provider, ret, code string, claims map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	o.Login(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, LoginPath+"?return="+url.QueryEscape(ret), nil))
	if w.Code != http.StatusFound {
		t.Fatalf("login = %d", w.Code)
	}
	auth, _ := url.Parse(w.Header().Get("Location"))
	q := auth.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("redirect_uri") != "https://kx.example.com/auth/callback" {
		t.Fatalf("authorization URL = %s", auth)
	}
	full := map[string]any{"iss": p.URL, "aud": "kubexray", "sub": "1", "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "nonce": q.Get("nonce")}
	for k, v := range claims {
		full[k] = v
	}
	p.Expect(q.Get("code_challenge"), full)

	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, CallbackPath+"?code="+code+"&state="+q.Get("state"), nil)
	for _, c := range w.Result().Cookies() {
		r.AddCookie(c)
	}
	back := httptest.NewRecorder()
	o.Callback(back, r)
	return back
}

func session(w *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == SessionCookie && c.MaxAge > 0 {
			return c
		}
	}
	return nil
}

func TestSignIn(t *testing.T) {
	p := authtest.New(t)
	o := newOIDC(t, p, Rules{Domains: []string{"example.com"}})
	w := signIn(t, o, p, "/?view=changes", authtest.Code, map[string]any{"email": "Ada@Example.com", "email_verified": true, "name": "Ada"})
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/?view=changes" {
		t.Fatalf("callback = %d %q: %s", w.Code, w.Header().Get("Location"), w.Body)
	}
	c := session(w)
	if c == nil || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookie = %+v", c)
	}
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	r.AddCookie(c)
	if u, until, ok := o.User(r); !ok || u.Email != "Ada@Example.com" || u.Name != "Ada" || until.Before(time.Now().Add(59*time.Minute)) {
		t.Fatalf("user = %+v until %v, %v", u, until, ok)
	}

	out := httptest.NewRecorder()
	o.Logout(out, r)
	if !strings.Contains(out.Body.String(), `href="/"`) {
		t.Errorf("signed-out page = %s", out.Body)
	}
	if cs := out.Result().Cookies(); len(cs) != 1 || cs[0].Name != SessionCookie || cs[0].MaxAge >= 0 {
		t.Fatalf("logout cookies = %+v", cs)
	}
}

func TestSignInRefused(t *testing.T) {
	p := authtest.New(t)
	o := newOIDC(t, p, Rules{Emails: []string{"ada@example.com"}, Groups: []string{"sre"}})
	for _, tc := range []struct {
		name   string
		code   string
		claims map[string]any
		want   int
	}{
		{"not allowed", authtest.Code, map[string]any{"email": "eve@example.com", "email_verified": true}, http.StatusForbidden},
		{"unverified email", authtest.Code, map[string]any{"email": "ada@example.com", "email_verified": false}, http.StatusForbidden},
		{"wrong nonce", authtest.Code, map[string]any{"email": "ada@example.com", "email_verified": true, "nonce": "other"}, http.StatusUnauthorized},
		{"wrong audience", authtest.Code, map[string]any{"email": "ada@example.com", "email_verified": true, "aud": "someone-else"}, http.StatusUnauthorized},
		{"bad code", "bad-code", map[string]any{"email": "ada@example.com", "email_verified": true}, http.StatusBadGateway},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := signIn(t, o, p, "/", tc.code, tc.claims)
			if w.Code != tc.want || session(w) != nil {
				t.Fatalf("callback = %d, want %d; session %v", w.Code, tc.want, session(w))
			}
		})
	}

	// A group grants access, with or without an email (Entra ID sends no
	// email_verified).
	for _, claims := range []map[string]any{
		{"email": "bob@else.org", "email_verified": true, "groups": []string{"sre"}},
		{"preferred_username": "bob@else.org", "groups": []string{"sre"}},
	} {
		w := signIn(t, o, p, "/", authtest.Code, claims)
		if w.Code != http.StatusSeeOther || session(w) == nil {
			t.Fatalf("group member %v: %d %s", claims, w.Code, w.Body)
		}
	}
	// Some providers send email_verified as a string.
	if w := signIn(t, o, p, "/", authtest.Code, map[string]any{"email": "ada@example.com", "email_verified": "true"}); w.Code != http.StatusSeeOther {
		t.Fatalf(`email_verified "true": %d %s`, w.Code, w.Body)
	}
}

func TestSignInsInTwoTabs(t *testing.T) {
	p := authtest.New(t)
	o := newOIDC(t, p, Rules{Domains: []string{"example.com"}})
	first := httptest.NewRecorder()
	o.Login(first, httptest.NewRequestWithContext(t.Context(), http.MethodGet, LoginPath+"?return=/a", nil))
	second := httptest.NewRecorder()
	o.Login(second, httptest.NewRequestWithContext(t.Context(), http.MethodGet, LoginPath+"?return=/b", nil))

	// The first tab comes back with both cookies in the jar.
	auth, _ := url.Parse(first.Header().Get("Location"))
	q := auth.Query()
	p.Expect(q.Get("code_challenge"), map[string]any{"iss": p.URL, "aud": "kubexray", "sub": "1", "exp": time.Now().Add(time.Hour).Unix(), "nonce": q.Get("nonce"), "email": "ada@example.com", "email_verified": true})
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, CallbackPath+"?code="+authtest.Code+"&state="+q.Get("state"), nil)
	for _, c := range append(first.Result().Cookies(), second.Result().Cookies()...) {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	o.Callback(w, r)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/a" {
		t.Fatalf("first tab = %d %q: %s", w.Code, w.Header().Get("Location"), w.Body)
	}
}

func TestCallbackChecksState(t *testing.T) {
	p := authtest.New(t)
	o := newOIDC(t, p, Rules{Domains: []string{"example.com"}})
	login := httptest.NewRecorder()
	o.Login(login, httptest.NewRequestWithContext(t.Context(), http.MethodGet, LoginPath, nil))

	// No flow cookie: a callback someone else started.
	w := httptest.NewRecorder()
	o.Callback(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, CallbackPath+"?code=good-code&state=x", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("without flow = %d", w.Code)
	}
	// The flow cookie, but another state.
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, CallbackPath+"?code=good-code&state=forged", nil)
	for _, c := range login.Result().Cookies() {
		r.AddCookie(c)
	}
	w = httptest.NewRecorder()
	o.Callback(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("forged state = %d", w.Code)
	}
}

func TestLimiter(t *testing.T) {
	l := NewLimiter(3, time.Minute)
	for range 3 {
		if !l.Allow("192.0.2.1") {
			t.Fatal("refused before the limit")
		}
		l.Fail("192.0.2.1")
	}
	if l.Allow("192.0.2.1") {
		t.Fatal("allowed past the limit")
	}
	if !l.Allow("192.0.2.2") {
		t.Fatal("another address was limited")
	}
	l.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if !l.Allow("192.0.2.1") {
		t.Fatal("the limit should lift after the window")
	}
}

func TestNewOIDCRefuses(t *testing.T) {
	p := authtest.New(t)
	good := Config{Issuer: p.URL, ClientID: "c", ClientSecret: "s", PublicURL: "https://kx.example.com", Rules: Rules{Emails: []string{"a@b.c"}}, SessionKey: testKey, SessionTTL: time.Hour}
	for name, change := range map[string]func(*Config){
		"no rules":      func(c *Config) { c.Rules = Rules{} },
		"http":          func(c *Config) { c.PublicURL = "http://kx.example.com" },
		"short key":     func(c *Config) { c.SessionKey = []byte("short") },
		"no secret":     func(c *Config) { c.ClientSecret = "" },
		"no lifetime":   func(c *Config) { c.SessionTTL = 0 },
		"wrong issuer":  func(c *Config) { c.Issuer = p.URL + "/nope" },
		"no public URL": func(c *Config) { c.PublicURL = "" },
	} {
		cfg := good
		change(&cfg)
		if _, err := NewOIDC(context.Background(), cfg, slog.Default()); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	local := good
	local.PublicURL = "http://localhost:8080"
	if _, err := NewOIDC(context.Background(), local, slog.Default()); err != nil {
		t.Errorf("localhost over http: %v", err)
	}
}

func TestSafeReturn(t *testing.T) {
	for in, want := range map[string]string{
		"/":                "/",
		"/?view=changes":   "/?view=changes",
		"":                 "/",
		"//evil.com":       "/",
		"/\\evil.com":      "/",
		"https://evil.com": "/",
		"/x\r\nSet-Cookie": "/",
		"/\t/evil.com":     "/", // browsers drop the tab: //evil.com
		"/a\\b":            "/",
	} {
		if got := safeReturn(in); got != want {
			t.Errorf("safeReturn(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSealer(t *testing.T) {
	s, _ := NewSealer(testKey)
	now := time.Now()
	sealed, _ := s.Seal("session", User{Email: "a@b.c"}, now.Add(time.Hour))
	var u User
	opens := func(s *Sealer, purpose, sealed string, at time.Time) bool {
		_, ok := s.Open(purpose, sealed, at, &u)
		return ok
	}
	if until, ok := s.Open("session", sealed, now, &u); !ok || u.Email != "a@b.c" || until.Unix() != now.Add(time.Hour).Unix() {
		t.Fatal("round trip")
	}
	if opens(s, "flow", sealed, now) {
		t.Error("opened for another purpose")
	}
	if opens(s, "session", sealed, now.Add(2*time.Hour)) {
		t.Error("opened after expiry")
	}
	body, sig, _ := strings.Cut(sealed, ".")
	if opens(s, "session", body+"x."+sig, now) {
		t.Error("opened a changed value")
	}
	other, _ := NewSealer([]byte(strings.Repeat("o", 32)))
	if opens(other, "session", sealed, now) {
		t.Error("opened with another key")
	}
}

func TestRules(t *testing.T) {
	r := Rules{Emails: []string{"ada@example.com"}, Domains: []string{"@corp.example"}, Groups: []string{"sre"}}
	for _, tc := range []struct {
		email  string
		groups []string
		want   bool
	}{
		{"ADA@example.com", nil, true},
		{"bob@corp.example", nil, true},
		{"bob@sub.corp.example", nil, false},
		{"eve@example.com", nil, false},
		{"", []string{"sre"}, true},
		{"", []string{"dev"}, false},
		{"", nil, false},
	} {
		if got, _ := r.Allow(tc.email, tc.groups); got != tc.want {
			t.Errorf("Allow(%q, %v) = %v, want %v", tc.email, tc.groups, got, tc.want)
		}
	}
}

func TestCapGroups(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("g", maxGroup+1)
	var many []string
	for i := 0; i < 200; i++ {
		many = append(many, fmt.Sprintf("team-%03d", i))
	}
	kept, dropped := capGroups(append([]string{"admins", long}, many...))
	total := 0
	for _, g := range kept {
		total += len(g)
	}
	if kept[0] != "admins" || slices.Contains(kept, long) || total > maxGroupBytes || dropped == 0 || len(kept)+dropped != 202 {
		t.Errorf("kept %d (%d bytes), dropped %d", len(kept), total, dropped)
	}
}
