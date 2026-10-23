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
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"

	"github.com/kubexray/kubexray/internal/cause"
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
}

// New returns a server for the hub and recorder fed by the pump.
func New(hub *Hub, rec *recorder.Recorder, assets fs.FS, log *slog.Logger, info Info) (*Server, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("generating session token: %w", err)
	}
	return &Server{hub: hub, rec: rec, assets: assets, log: log, info: info, now: time.Now, token: base64.RawURLEncoding.EncodeToString(b)}, nil
}

// SetCluster names the cluster in replay headers, once it's known. Call it
// before Serve.
func (s *Server) SetCluster(name string) { s.info.Cluster = name }

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
	go s.watchIncidents(ctx)
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
	api.HandleFunc("GET /api/stream", s.stream)
	api.HandleFunc("GET /api/history", s.history)
	api.HandleFunc("GET /api/at", s.at)
	api.HandleFunc("GET /api/why", s.why)
	api.HandleFunc("GET /api/changes", s.changes)
	api.HandleFunc("GET /api/export", s.export)
	api.Handle("GET /", http.FileServerFS(s.assets))

	return s.checkHost(s.headers(s.session(api)))
}

// checkHost rejects requests addressed to any other host name, which is what
// a DNS rebinding attack looks like.
func (s *Server) checkHost(next http.Handler) http.Handler {
	_, port, _ := net.SplitHostPort(s.host)
	allowed := map[string]bool{s.host: true, "localhost:" + port: true}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowed[r.Host] {
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

func (s *Server) headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", s.csp)
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
		if tok := r.URL.Query().Get("token"); tok != "" && r.URL.Path == "/" {
			if !s.valid(tok) {
				http.Error(w, "This link is out of date. Use the one kubexray printed when it started.", http.StatusUnauthorized)
				return
			}
			// Not Secure: the server speaks plain HTTP on loopback, and some
			// browsers drop Secure cookies there. Loopback-only, HttpOnly and
			// SameSite=Strict carry the protection (ADR-0007).
			http.SetCookie(w, &http.Cookie{ //nolint:gosec // see above
				Name: s.cookie, Value: s.token, Path: "/",
				HttpOnly: true, SameSite: http.SameSiteStrictMode,
			})
			// Drop the token from the address bar and history.
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		c, err := r.Cookie(s.cookie)
		if err != nil || !s.valid(c.Value) {
			http.Error(w, "Open kubexray with the link it printed in your terminal.", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) valid(tok string) bool {
	return subtle.ConstantTimeCompare([]byte(tok), []byte(s.token)) == 1
}

// stream upgrades to a WebSocket and sends the graph: a snapshot, the current
// incidents, then every change.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	// Only the UI served from this origin may open the stream.
	if r.Header.Get("Origin") != "http://"+r.Host {
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
	t, err := time.Parse(time.RFC3339Nano, r.URL.Query().Get("t"))
	if err != nil {
		http.Error(w, "t must be an RFC 3339 time", http.StatusBadRequest)
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
	first, last, ok := s.rec.Bounds()
	if !ok {
		http.Error(w, "nothing recorded yet", http.StatusNotFound)
		return
	}
	from, to := first, last
	q := r.URL.Query()
	for name, dst := range map[string]*time.Time{"from": &from, "to": &to} {
		if v := q.Get(name); v != "" {
			t, err := time.Parse(time.RFC3339Nano, v)
			if err != nil {
				http.Error(w, name+" must be an RFC 3339 time", http.StatusBadRequest)
				return
			}
			*dst = t
		}
	}
	if to.Before(from) {
		http.Error(w, "to is before from", http.StatusBadRequest)
		return
	}
	if from.Before(first) {
		from = first // the recording starts later; export what there is
	}
	msgs, err := s.rec.Messages(from, to)
	if err != nil {
		http.Error(w, "kubexray has no history for that window", http.StatusNotFound)
		return
	}
	anonymize := q.Get("anonymize") == "1"
	var buf bytes.Buffer
	opts := replay.Options{From: from, To: to, Cluster: s.info.Cluster, Version: s.info.Version, Anonymize: anonymize}
	if err := replay.Write(&buf, msgs, opts); err != nil {
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
