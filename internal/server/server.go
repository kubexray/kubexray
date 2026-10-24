// Package server is kubexray's local web server (ADR-0007): the UI, a
// WebSocket stream of the cluster graph, and a read-only API over the
// recorder and the cause engine.
//
// It listens on 127.0.0.1 only and assumes every other web page in the
// user's browser is hostile: requests need a session cookie obtained with a
// token printed in the terminal, the Host header must be the server's own
// (DNS rebinding), WebSocket upgrades must come from the server's own origin
// (cross-site WebSocket hijacking), and responses carry a strict
// Content-Security-Policy.
package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/kubexray/kubexray/internal/alert"
	"github.com/kubexray/kubexray/internal/cause"
	"github.com/kubexray/kubexray/internal/feed"
	"github.com/kubexray/kubexray/internal/graph"
	"github.com/kubexray/kubexray/internal/recorder"
	"github.com/kubexray/kubexray/internal/replay"
)

// Client limits (ADR-0007).
const (
	MaxClients  = 16  // WebSocket clients at once
	ClientQueue = 256 // messages queued per client before it's dropped
)

const (
	maxClientFrame  = 4 << 10
	writeTimeout    = 10 * time.Second
	pingInterval    = 30 * time.Second
	incidentsPeriod = time.Second
)

// Info describes what this server is showing, for replay headers.
type Info struct {
	Cluster string // kubeconfig context name
	Version string // kubexray version
}

// Server is kubexray's local web server.
type Server struct {
	hub    *Hub
	rec    *recorder.Recorder
	assets fs.FS
	log    *slog.Logger
	info   Info
	now    func() time.Time

	token  string
	host   string // 127.0.0.1:<port>, set by Listen
	csp    string
	cookie string // session cookie name, set by Listen

	replay []byte     // player mode: the one replay file served (kubexray play)
	remote *Remote    // serve mode (kubexray serve): reachable over the network
	tokens *tokenFile // serve mode with a token file

	alerts    *alert.Notifier // serve mode: Alertmanager notifications (ADR-0011)
	publicURL string          // where people open kubexray, for links in alerts
}

// SetAlerts makes the server an Alertmanager receiver that sends kubexray's
// explanation to the notifier's destinations, linking to publicURL.
func (s *Server) SetAlerts(n *alert.Notifier, publicURL string) {
	s.alerts, s.publicURL = n, publicURL
}

// Remote configures serve mode (ADR-0010): the server runs in the cluster and
// is reachable over the network, so every request needs the token, or an
// auth proxy in front of a loopback address.
type Remote struct {
	Addr         string   // where to listen, e.g. ":8080"
	Token        string   // required unless NoAuth
	TokenFile    string   // where Token came from: re-read when it changes (rotation, GitOps)
	NoAuth       bool     // only with a loopback Addr: an auth proxy is the way in
	AllowedHosts []string // host names besides localhost, e.g. an Ingress's
	SecureCookie bool     // behind TLS (an Ingress)
}

// MinTokenLength is the shortest token serve mode accepts.
const MinTokenLength = 32

// New returns a server for the hub and recorder fed by the pump.
func New(hub *Hub, rec *recorder.Recorder, assets fs.FS, log *slog.Logger, info Info) (*Server, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("generating session token: %w", err)
	}
	return &Server{hub: hub, rec: rec, assets: assets, log: log, info: info, now: time.Now, token: base64.RawURLEncoding.EncodeToString(b)}, nil
}

// NewPlayer returns a server in player mode (kubexray play): the replay
// player and one already-validated replay file, behind the same protections
// as live mode (ADR-0008, section 5). It has no cluster access at all.
func NewPlayer(file []byte, assets fs.FS, log *slog.Logger) (*Server, error) {
	s, err := New(nil, nil, playerHome{assets}, log, Info{})
	if err != nil {
		return nil, err
	}
	s.replay = file
	return s, nil
}

// playerHome serves player.html as the home page (when the build has one).
type playerHome struct{ fs.FS }

func (p playerHome) Open(name string) (fs.File, error) {
	if name == "index.html" {
		if f, err := p.FS.Open("player.html"); err == nil {
			return f, nil
		}
	}
	return p.FS.Open(name)
}

// Listen binds to 127.0.0.1 on port (0 picks a free one).
func (s *Server) Listen(ctx context.Context, port int) (net.Listener, error) {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return nil, err
	}
	s.host = ln.Addr().String()
	s.csp = csp(s.host)
	// Browsers scope cookies by host, not port, so two kubexray instances
	// would overwrite each other's session; the port in the name prevents
	// that. Other servers on 127.0.0.1 still receive the cookie: abusing that
	// needs a malicious local process, which the threat model treats as a
	// compromised workstation.
	_, p, _ := net.SplitHostPort(s.host)
	s.cookie = "kubexray_session_" + p
	return ln, nil
}

// ListenRemote binds for serve mode. It refuses to run without
// authentication on an address other machines can reach.
func (s *Server) ListenRemote(ctx context.Context, r Remote) (net.Listener, error) {
	if err := r.Check(); err != nil {
		return nil, err
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", r.Addr)
	if err != nil {
		return nil, err
	}
	s.remote = &r
	s.token = r.Token
	s.host = ln.Addr().String()
	if r.TokenFile != "" {
		s.tokens = &tokenFile{path: r.TokenFile, token: r.Token, now: s.now}
	}
	return ln, nil
}

// cookieName is the session cookie's name for a request. Browsers scope
// cookies by host, not port: two kubexray instances port-forwarded to
// different local ports must not share one.
func (s *Server) cookieName(host string) string {
	if s.remote == nil {
		return s.cookie
	}
	if _, port, err := net.SplitHostPort(host); err == nil {
		return "kubexray_session_" + port
	}
	return "kubexray_session"
}

// tokenFile follows a token file (a mounted Secret): the kubelet updates it
// when the Secret changes, and so the token in use.
type tokenFile struct {
	path string
	now  func() time.Time

	mu      sync.Mutex
	token   string
	checked time.Time
	modTime time.Time
}

const tokenRecheck = 10 * time.Second

func (t *tokenFile) get() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if now := t.now(); now.Sub(t.checked) >= tokenRecheck {
		t.checked = now
		if fi, err := os.Stat(t.path); err == nil && !fi.ModTime().Equal(t.modTime) {
			// A token that can't be read or is too short keeps the old one.
			if b, err := os.ReadFile(t.path); err == nil {
				if tok := strings.TrimSpace(string(b)); len(tok) >= MinTokenLength {
					t.token, t.modTime = tok, fi.ModTime()
				}
			}
		}
	}
	return t.token
}

// Check refuses settings that would expose the cluster without
// authentication.
func (r Remote) Check() error {
	host, _, err := net.SplitHostPort(r.Addr)
	if err != nil {
		return fmt.Errorf("listen address %q: %w", r.Addr, err)
	}
	switch {
	case r.NoAuth && !loopback(host):
		return fmt.Errorf("refusing to serve without authentication on %s: only a loopback address (an auth proxy in the same pod) may skip it", r.Addr)
	case !r.NoAuth && len(r.Token) < MinTokenLength:
		return fmt.Errorf("the token must be at least %d characters", MinTokenLength)
	}
	return nil
}

func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// URL is the launch link: it carries the session token, so it is printed only
// in the user's terminal.
func (s *Server) URL() string { return "http://" + s.host + "/?token=" + s.token }

// Serve runs until ctx is cancelled, then shuts down gracefully.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	if s.hub != nil {
		go s.watchIncidents(ctx)
	}
	go func() {
		<-ctx.Done()
		// ctx is done by now; shutdown gets its own deadline.
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *Server) watchIncidents(ctx context.Context) {
	tick := time.NewTicker(incidentsPeriod)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if err := s.hub.UpdateIncidents(s.now()); err != nil {
				s.log.Warn("updating incidents", "err", err)
			}
		}
	}
}

// Handler is the full request pipeline: Host check, security headers,
// session, then routes.
func (s *Server) Handler() http.Handler {
	api := http.NewServeMux()
	if s.replay != nil {
		api.HandleFunc("GET /api/replay", s.serveReplay)
	} else {
		api.HandleFunc("GET /api/stream", s.stream)
		api.HandleFunc("GET /api/history", s.history)
		api.HandleFunc("GET /api/at", s.at)
		api.HandleFunc("GET /api/why", s.why)
		api.HandleFunc("GET /api/changes", s.changes)
		api.HandleFunc("GET /api/feed", s.feed)
		api.HandleFunc("GET /api/export", s.export)
		if s.alerts.Enabled() {
			api.HandleFunc("POST /api/alertmanager", s.alertmanager)
		}
	}
	api.Handle("GET /", http.FileServerFS(s.assets))

	secured := s.checkHost(s.headers(s.session(api)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Liveness and readiness probes: no data, no authentication, and any
		// Host (the kubelet uses the pod's IP).
		if s.remote != nil && r.URL.Path == "/healthz" {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = io.WriteString(w, "ok\n")
			return
		}
		secured.ServeHTTP(w, r)
	})
}

// checkHost rejects requests addressed to any other host name, which is what
// a DNS rebinding attack looks like.
func (s *Server) checkHost(next http.Handler) http.Handler {
	_, port, _ := net.SplitHostPort(s.host)
	allowed := map[string]bool{s.host: true, "localhost:" + port: true}
	ok := func(h string) bool { return allowed[h] }
	if s.remote != nil && s.remote.NoAuth {
		return next // the auth proxy in front is the only way in, and checks the host
	}
	if s.remote != nil {
		// Through kubectl port-forward, any local port; through an Ingress,
		// the names it was given.
		for _, h := range s.remote.AllowedHosts {
			allowed[h] = true
		}
		ok = func(h string) bool {
			name, _, err := net.SplitHostPort(h)
			if err != nil {
				name = h
			}
			return allowed[h] || allowed[name] || loopback(strings.Trim(name, "[]"))
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The check keeps cookies from reaching a rebound host name. Bearer
		// clients (Alertmanager, scripts) send none, and may use any name,
		// such as the in-cluster Service's; the token is still checked.
		bearer := s.remote != nil && strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !bearer && !ok(r.Host) {
			http.Error(w, "unexpected host", http.StatusMisdirectedRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func csp(host string) string {
	_, port, _ := net.SplitHostPort(host)
	return "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; " +
		"connect-src 'self' ws://" + host + " ws://localhost:" + port + "; " +
		"frame-ancestors 'none'; base-uri 'none'; form-action 'none'; object-src 'none'"
}

// validHost is a host name or address with an optional port.
var validHost = regexp.MustCompile(`^(\[[0-9a-fA-F:.]+\]|[a-zA-Z0-9.-]+)(:[0-9]{1,5})?$`)

func remoteCSP(host string) string {
	connect := "'self'"
	if host != "" {
		connect += " ws://" + host + " wss://" + host
	}
	return "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; " +
		"connect-src " + connect + "; frame-ancestors 'none'; base-uri 'none'; form-action 'none'; object-src 'none'"
}

func (s *Server) headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		policy := s.csp
		if s.remote != nil {
			// Bearer clients skip the Host allow-list, so check its form
			// before it goes into a header.
			host := r.Host
			if !validHost.MatchString(host) {
				host = ""
			}
			policy = remoteCSP(host)
		}
		h.Set("Content-Security-Policy", policy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// session exchanges the launch token for a cookie, and requires that cookie
// on every other request.
func (s *Server) session(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.remote != nil {
			if s.remote.NoAuth {
				next.ServeHTTP(w, r) // the auth proxy in front has checked
				return
			}
			// API clients (scripts, curl) send the token as a bearer token.
			if tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
				if !s.valid(tok) {
					http.Error(w, "invalid token", http.StatusUnauthorized)
					return
				}
				next.ServeHTTP(w, r)
				return
			}
		}
		if tok := r.URL.Query().Get("token"); tok != "" && r.URL.Path == "/" {
			if !s.valid(tok) {
				http.Error(w, "This link is out of date. Use the one kubexray printed when it started.", http.StatusUnauthorized)
				return
			}
			// Not Secure: the server speaks plain HTTP on loopback, and some
			// browsers drop Secure cookies there. Loopback-only, HttpOnly and
			// SameSite=Strict carry the protection (ADR-0007).
			http.SetCookie(w, &http.Cookie{ //nolint:gosec // see above
				Name: s.cookieName(r.Host), Value: tok, Path: "/",
				HttpOnly: true, SameSite: http.SameSiteStrictMode,
				Secure: s.remote != nil && s.remote.SecureCookie, // serve mode behind TLS
			})
			// Drop the token from the address bar and history. In player
			// mode, land on the player with the replay loaded.
			landing := "/"
			if s.replay != nil {
				landing = "/?replay=%2Fapi%2Freplay"
			}
			http.Redirect(w, r, landing, http.StatusSeeOther)
			return
		}
		c, err := r.Cookie(s.cookieName(r.Host))
		if err != nil || !s.valid(c.Value) {
			msg := "Open kubexray with the link it printed in your terminal."
			if s.remote != nil {
				msg = "Open kubexray with /?token=<token>. The token is in the kubexray Secret; see the chart's notes."
			}
			http.Error(w, msg, http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) valid(tok string) bool {
	return subtle.ConstantTimeCompare([]byte(tok), []byte(s.currentToken())) == 1
}

func (s *Server) currentToken() string {
	if s.tokens != nil {
		return s.tokens.get()
	}
	return s.token
}

// stream upgrades to a WebSocket and sends the graph: a snapshot, the current
// incidents, then every change.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	// Only the UI served from this origin may open the stream.
	if o := r.Header.Get("Origin"); o != "http://"+r.Host && (s.remote == nil || o != "https://"+r.Host) {
		http.Error(w, "cross-origin WebSocket refused", http.StatusForbidden)
		return
	}
	cl, first, err := s.hub.subscribe()
	switch {
	case errors.Is(err, errTooManyClients):
		http.Error(w, "too many open kubexray tabs", http.StatusServiceUnavailable)
		return
	case err != nil:
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	defer s.hub.unsubscribe(cl)

	conn, err := websocket.Accept(w, r, nil) // nil options: same-origin only
	if err != nil {
		return // Accept has already written the error response
	}
	defer func() { _ = conn.CloseNow() }()
	conn.SetReadLimit(maxClientFrame)
	ctx := conn.CloseRead(r.Context()) // clients don't send anything yet; this handles close and pings

	send := func(raw []byte) error {
		wctx, cancel := context.WithTimeout(ctx, writeTimeout)
		defer cancel()
		return conn.Write(wctx, websocket.MessageText, raw)
	}
	for _, raw := range first {
		if err := send(raw); err != nil {
			return
		}
	}
	ping := time.NewTicker(pingInterval)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case raw, ok := <-cl.send:
			if !ok {
				_ = conn.Close(websocket.StatusTryAgainLater, "fell behind; reconnect to resync")
				return
			}
			if err := send(raw); err != nil {
				return
			}
		case <-ping.C:
			pctx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := conn.Ping(pctx)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

func (s *Server) history(w http.ResponseWriter, _ *http.Request) {
	from, to, ok := s.rec.Bounds()
	if !ok {
		http.Error(w, "nothing recorded yet", http.StatusNotFound)
		return
	}
	writeJSON(w, HistoryResponse{From: from, To: to})
}

func (s *Server) at(w http.ResponseWriter, r *http.Request) {
	t, ok := timeParam(w, r, "t", time.Time{})
	if !ok {
		return
	}
	snap, err := s.rec.At(t)
	if errors.Is(err, recorder.ErrNotRecorded) {
		http.Error(w, "kubexray has no history for that time", http.StatusNotFound)
		return
	}
	if err != nil {
		s.log.Warn("rebuilding history", "err", err)
		http.Error(w, "couldn't rebuild that moment", http.StatusInternalServerError)
		return
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp := AtResponse{Snapshot: raw, Incidents: []cause.Incident{}, Groups: []cause.Group{}}
	if st, err := graph.NewState(snap); err == nil {
		m := cause.NewIncidentsMessage(st, t)
		resp.Incidents, resp.Groups = m.Incidents, m.Groups
	}
	writeJSON(w, resp)
}

func (s *Server) why(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}
	inc, err := s.hub.explain(id, s.now())
	switch {
	case errors.Is(err, errNotSynced):
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
	case errors.Is(err, cause.ErrNotAnApp):
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
	case err != nil:
		http.Error(w, err.Error(), http.StatusNotFound)
	default:
		writeJSON(w, inc)
	}
}

func (s *Server) serveReplay(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/gzip")
	_, _ = w.Write(s.replay)
}

// maxAlertPayload bounds an Alertmanager notification.
const maxAlertPayload = 1 << 20

// alertmanager receives Alertmanager's webhook and queues kubexray's
// explanation of every firing alert about a known app (ADR-0011).
func (s *Server) alertmanager(w http.ResponseWriter, r *http.Request) {
	var n alert.Notification
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAlertPayload)).Decode(&n); err != nil {
		http.Error(w, "not an Alertmanager notification", http.StatusBadRequest)
		return
	}
	ids := make([]string, 0, len(n.Alerts))
	for _, a := range n.Alerts {
		if id, _ := a.Target(); id != "" {
			ids = append(ids, id)
		}
	}
	incidents, err := s.hub.explainAll(ids, s.now()) // one index for the whole notification
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable) // Alertmanager retries
		return
	}
	for _, a := range n.Alerts {
		id, byPod := a.Target()
		inc, known := incidents[id]
		switch {
		case !known:
			continue // not about an app, or not one kubexray knows
		case a.Status == "resolved":
			s.alerts.Resolve(a.Name(), inc.App) // firing again soon is a regression worth a message
			continue
		case a.Status != "firing", byPod && inc.Healthy:
			// A pod label alone may be the scraped target's, not the alert's
			// subject: say nothing about an app that's fine.
			continue
		}
		at := inc.Since
		if at.IsZero() {
			at = a.StartsAt
		}
		s.alerts.Queue(alert.Message{Alert: a, Incident: inc, Link: alert.Link(s.publicURL, at, inc.App)})
	}
	w.WriteHeader(http.StatusAccepted)
}

// timeParam reads an RFC 3339 query parameter, or def when it's absent
// (a zero def makes it required). On a bad value it answers 400 and
// reports false.
func timeParam(w http.ResponseWriter, r *http.Request, name string, def time.Time) (time.Time, bool) {
	v := r.URL.Query().Get(name)
	if v == "" && !def.IsZero() {
		return def, true
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		http.Error(w, name+" must be an RFC 3339 time", http.StatusBadRequest)
		return time.Time{}, false
	}
	return t, true
}

// feed serves the change feed since ?since= (default: a week), newest first.
func (s *Server) feed(w http.ResponseWriter, r *http.Request) {
	now := s.now()
	since, ok := timeParam(w, r, "since", now.Add(-7*24*time.Hour))
	if !ok {
		return
	}
	sets := s.hub.Feed().Sets(since)
	if sets == nil {
		sets = []feed.Set{}
	}
	writeJSON(w, FeedResponse{Sets: sets, Now: now})
}

// changes lists who changed what since the recording began (cause.Changes).
func (s *Server) changes(w http.ResponseWriter, _ *http.Request) {
	from, _, ok := s.rec.Bounds()
	if !ok {
		from = s.now().Add(-time.Hour)
	}
	list, err := s.hub.changes(from)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, list)
}

// export downloads a replay of the recorded window from..to (ADR-0008).
// Both default to the whole recording; anonymize=1 replaces names.
func (s *Server) export(w http.ResponseWriter, r *http.Request) {
	first, _, ok := s.rec.Bounds()
	if !ok {
		http.Error(w, "nothing recorded yet", http.StatusNotFound)
		return
	}
	now := s.now()
	from, ok := timeParam(w, r, "from", first)
	if !ok {
		return
	}
	to, ok := timeParam(w, r, "to", now)
	if !ok {
		return
	}
	q := r.URL.Query()
	if to.Before(from) {
		http.Error(w, "to is before from", http.StatusBadRequest)
		return
	}
	// Export what was recorded: the recording starts at first and ends now.
	// Analyzing a future time would invent incidents that never happened.
	if from.Before(first) {
		from = first
	}
	if to.After(now) {
		to = now
	}
	// Read no more than a replay may hold; a window across a restart is cut
	// to the run it starts in, and the replay says so in its header.
	msgs, from, to, err := s.rec.Range(from, to, replay.MaxUncompressed)
	switch {
	case errors.Is(err, recorder.ErrTooLarge):
		http.Error(w, "that window holds more history than one replay can (50 MiB); export a shorter one", http.StatusRequestEntityTooLarge)
		return
	case err != nil:
		http.Error(w, "kubexray has no history for that window", http.StatusNotFound)
		return
	}
	anonymize := q.Get("anonymize") == "1"
	var buf bytes.Buffer
	opts := replay.Options{From: from, To: to, Cluster: s.info.Cluster, Version: s.info.Version, Anonymize: anonymize}
	switch err := replay.Write(r.Context(), &buf, msgs, opts); {
	case errors.Is(err, replay.ErrTooLarge):
		http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
		return
	case r.Context().Err() != nil:
		return // the browser went away
	case err != nil:
		s.log.Warn("exporting a replay", "err", err)
		http.Error(w, "couldn't write the replay", http.StatusInternalServerError)
		return
	}
	name := "kubexray-" + from.UTC().Format("20060102-150405") + replay.Ext
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	_, _ = w.Write(buf.Bytes())
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
