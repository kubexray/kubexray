package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// Rules decide who gets in after signing in (ADR-0013, section 2).
type Rules struct {
	Emails  []string
	Domains []string
	Groups  []string
}

// Empty reports whether no rule is set: nobody would get in.
func (r Rules) Empty() bool { return len(r.Emails) == 0 && len(r.Domains) == 0 && len(r.Groups) == 0 }

// Allow reports whether someone gets in, and if not, why.
func (r Rules) Allow(email string, verified bool, groups []string) (bool, string) {
	if email == "" || !verified {
		return false, "no verified email"
	}
	email = strings.ToLower(email)
	for _, e := range r.Emails {
		if strings.EqualFold(e, email) {
			return true, ""
		}
	}
	if _, domain, ok := strings.Cut(email, "@"); ok {
		for _, d := range r.Domains {
			if strings.EqualFold(strings.TrimPrefix(d, "@"), domain) {
				return true, ""
			}
		}
	}
	for _, g := range groups {
		if slices.Contains(r.Groups, g) {
			return true, ""
		}
	}
	return false, "not on the allow-list"
}

// Config configures OpenID Connect sign-in.
type Config struct {
	Issuer       string        // e.g. https://accounts.google.com
	ClientID     string        //
	ClientSecret string        // from a Secret
	PublicURL    string        // where kubexray is opened; the redirect URL is PublicURL/auth/callback
	Scopes       []string      // beyond openid, email and profile
	GroupsClaim  string        // the ID token claim with groups, e.g. "groups"
	Rules        Rules         //
	SessionKey   []byte        // signs sessions and the flow state
	SessionTTL   time.Duration // how long a sign-in lasts
}

// OIDC signs people in with an OpenID Connect provider.
type OIDC struct {
	cfg      Config
	oauth    oauth2.Config
	verifier *oidc.IDTokenVerifier
	seal     *Sealer
	secure   bool
	log      *slog.Logger
	limit    *limiter
	now      func() time.Time
}

// Paths kubexray serves for sign-in.
const (
	LoginPath    = "/auth/login"
	CallbackPath = "/auth/callback"
	LogoutPath   = "/auth/logout"
)

// flowTTL is how long someone has to finish signing in at the provider.
const flowTTL = 10 * time.Minute

// NewOIDC discovers the provider and checks the configuration.
func NewOIDC(ctx context.Context, cfg Config, log *slog.Logger) (*OIDC, error) {
	switch {
	case cfg.Issuer == "" || cfg.ClientID == "" || cfg.ClientSecret == "":
		return nil, errors.New("auth: OIDC needs an issuer, a client ID and a client secret")
	case cfg.Rules.Empty():
		return nil, errors.New("auth: set at least one allowed email, domain or group: signing in alone lets in anyone with an account at the provider")
	case cfg.SessionTTL <= 0:
		return nil, errors.New("auth: the session lifetime must be positive")
	}
	public, err := url.Parse(cfg.PublicURL)
	if err != nil || public.Host == "" || (public.Scheme != "https" && !isLocal(public.Hostname())) {
		return nil, errors.New("auth: OIDC needs the https address people open kubexray at (--public-url)")
	}
	seal, err := NewSealer(cfg.SessionKey)
	if err != nil {
		return nil, err
	}
	provider, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("auth: discovering the OIDC provider %s: %w", cfg.Issuer, err)
	}
	if cfg.GroupsClaim == "" {
		cfg.GroupsClaim = "groups"
	}
	return &OIDC{
		cfg: cfg,
		oauth: oauth2.Config{
			ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, Endpoint: provider.Endpoint(),
			RedirectURL: strings.TrimSuffix(cfg.PublicURL, "/") + CallbackPath,
			Scopes:      append([]string{oidc.ScopeOpenID, "email", "profile"}, cfg.Scopes...),
		},
		verifier: provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
		seal:     seal,
		secure:   public.Scheme == "https",
		log:      log,
		limit:    newLimiter(10, time.Minute),
		now:      time.Now,
	}, nil
}

func isLocal(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// flow is the sign-in in progress, kept in a sealed cookie between the
// redirect to the provider and its redirect back.
type flow struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	Return   string `json:"r"`
}

// User returns who a request is signed in as.
func (o *OIDC) User(r *http.Request) (User, bool) {
	c, err := r.Cookie(SessionCookie)
	if err != nil {
		return User{}, false
	}
	var u User
	ok := o.seal.Open("session", c.Value, o.now(), &u)
	return u, ok
}

// Session is the cookie that signs u in, until the session lifetime ends.
func (o *OIDC) Session(u User) (*http.Cookie, error) {
	sealed, err := o.seal.Seal("session", u, o.now().Add(o.cfg.SessionTTL))
	if err != nil {
		return nil, err
	}
	return cookie(SessionCookie, sealed, o.cfg.SessionTTL, o.secure), nil
}

// Login sends the browser to the provider. ?return= is where to come back to:
// a path on this site, never another one.
func (o *OIDC) Login(w http.ResponseWriter, r *http.Request) {
	f := flow{State: random(), Nonce: random(), Verifier: oauth2.GenerateVerifier(), Return: safeReturn(r.URL.Query().Get("return"))}
	sealed, err := o.seal.Seal("flow", f, o.now().Add(flowTTL))
	if err != nil {
		http.Error(w, "couldn't start signing in", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, cookie(flowCookie, sealed, flowTTL, o.secure))
	http.Redirect(w, r, o.oauth.AuthCodeURL(f.State, oidc.Nonce(f.Nonce), oauth2.S256ChallengeOption(f.Verifier)), http.StatusFound)
}

// Callback finishes signing in: it checks the state, exchanges the code (with
// the PKCE verifier), verifies the ID token and its nonce, applies the rules
// and sets the session.
func (o *OIDC) Callback(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if !o.limit.Allow(ip) {
		http.Error(w, "too many failed sign-ins; wait a minute", http.StatusTooManyRequests)
		return
	}
	fail := func(code int, msg, reason string) {
		o.limit.Fail(ip)
		o.log.Warn("sign-in failed", "audit", true, "event", "signin_failed", "reason", reason, "ip", ip)
		http.SetCookie(w, cookie(flowCookie, "", -1, o.secure))
		http.Error(w, msg, code)
	}
	var f flow
	c, err := r.Cookie(flowCookie)
	if err != nil || !o.seal.Open("flow", c.Value, o.now(), &f) {
		fail(http.StatusBadRequest, "This sign-in expired. Open kubexray again.", "no or expired flow")
		return
	}
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		fail(http.StatusUnauthorized, "The sign-in was cancelled or refused.", "provider: "+e)
		return
	}
	if q.Get("state") == "" || q.Get("state") != f.State {
		fail(http.StatusBadRequest, "This sign-in doesn't match the one started. Open kubexray again.", "state mismatch")
		return
	}
	token, err := o.oauth.Exchange(r.Context(), q.Get("code"), oauth2.VerifierOption(f.Verifier))
	if err != nil {
		fail(http.StatusBadGateway, "The sign-in couldn't be completed with the provider.", "code exchange: "+err.Error())
		return
	}
	raw, _ := token.Extra("id_token").(string)
	id, err := o.verifier.Verify(r.Context(), raw)
	if err != nil || id.Nonce != f.Nonce {
		fail(http.StatusUnauthorized, "The provider's answer couldn't be verified.", "id token")
		return
	}
	var claims map[string]any
	if err := id.Claims(&claims); err != nil {
		fail(http.StatusUnauthorized, "The provider's answer couldn't be read.", "claims")
		return
	}
	email, _ := claims["email"].(string)
	verified, _ := claims["email_verified"].(bool)
	name, _ := claims["name"].(string)
	groups := stringsOf(claims[o.cfg.GroupsClaim])
	if ok, why := o.cfg.Rules.Allow(email, verified, groups); !ok {
		o.limit.Fail(ip)
		o.log.Warn("access denied", "audit", true, "event", "access_denied", "email", email, "reason", why, "ip", ip)
		http.SetCookie(w, cookie(flowCookie, "", -1, o.secure))
		http.Error(w, fmt.Sprintf("%s doesn't have access to this kubexray. Ask whoever runs it to add you.", cmpOr(email, "This account")), http.StatusForbidden)
		return
	}
	session, err := o.Session(User{Email: email, Name: name})
	if err != nil {
		http.Error(w, "couldn't finish signing in", http.StatusInternalServerError)
		return
	}
	o.log.Info("signed in", "audit", true, "event", "signin", "email", email, "ip", ip)
	http.SetCookie(w, cookie(flowCookie, "", -1, o.secure))
	http.SetCookie(w, session)
	http.Redirect(w, r, f.Return, http.StatusSeeOther)
}

// Logout clears the session.
func (o *OIDC) Logout(w http.ResponseWriter, r *http.Request) {
	if u, ok := o.User(r); ok {
		o.log.Info("signed out", "audit", true, "event", "signout", "email", u.Email, "ip", clientIP(r))
	}
	http.SetCookie(w, cookie(SessionCookie, "", -1, o.secure))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(signedOut))
}

// signedOut is the page after signing out: static, so it needs no styles
// the CSP would have to allow.
const signedOut = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Signed out · kubexray</title></head>
<body><p>You're signed out of kubexray. <a href="/">Sign in again</a></p></body></html>
`

// FailedToken records a bad bearer token from a request's address, and
// reports whether that address may still try.
func (o *OIDC) FailedToken(r *http.Request) bool {
	ip := clientIP(r)
	o.limit.Fail(ip)
	return o.limit.Allow(ip)
}

// TokenAllowed reports whether a request's address may present a token now.
func (o *OIDC) TokenAllowed(r *http.Request) bool { return o.limit.Allow(clientIP(r)) }

func random() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// safeReturn keeps a return path on this site: "/x" yes, "//evil" or
// "https://evil" no.
func safeReturn(p string) string {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.HasPrefix(p, "/\\") || strings.ContainsAny(p, "\r\n") {
		return "/"
	}
	return p
}

func stringsOf(v any) []string {
	var out []string
	switch x := v.(type) {
	case []any:
		for _, g := range x {
			if s, ok := g.(string); ok {
				out = append(out, s)
			}
		}
	case string:
		out = append(out, x)
	}
	return out
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// clientIP is the address a request came from. Behind an Ingress that's the
// Ingress's address: limits then apply to all clients together, which is
// the safe direction.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// limiter allows a number of failures per address in a window.
type limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	seen   map[string]*bucket
	now    func() time.Time
}

type bucket struct {
	fails int
	reset time.Time
}

func newLimiter(maxFails int, window time.Duration) *limiter {
	return &limiter{max: maxFails, window: window, seen: map[string]*bucket{}, now: time.Now}
}

func (l *limiter) Allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.seen[ip]
	return !ok || l.now().After(b.reset) || b.fails < l.max
}

func (l *limiter) Fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if len(l.seen) > 10000 { // bounded: forget old addresses
		for k, b := range l.seen {
			if now.After(b.reset) {
				delete(l.seen, k)
			}
		}
	}
	b, ok := l.seen[ip]
	if !ok || now.After(b.reset) {
		b = &bucket{reset: now.Add(l.window)}
		l.seen[ip] = b
	}
	b.fails++
}
