package server

import (
	"mime"
	"net/http"
	"net/url"
	"sync"
)

// sameOrigin refuses requests that change something when they come from
// another site. Cookies are SameSite, but an auth proxy's cookie may not be,
// and its session would then carry a forged form post from any page.
//
// A request is refused when it's a form a page could submit across sites
// (form and plain-text bodies), when the browser says another site sent it
// (Sec-Fetch-Site), or, from a browser that doesn't say, when its Origin is
// another host. Scripts and Alertmanager send none of these and pass.
func sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if ct := r.Header.Get("Content-Type"); ct != "" {
			mt, _, err := mime.ParseMediaType(ct)
			if err != nil || mt == "application/x-www-form-urlencoded" || mt == "multipart/form-data" || mt == "text/plain" {
				http.Error(w, "send JSON", http.StatusUnsupportedMediaType)
				return
			}
		}
		if !fromThisSite(r) {
			http.Error(w, "cross-site request refused", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func fromThisSite(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "none":
		return true
	case "":
		// An older browser, or not a browser: fall back to Origin.
	default:
		return false // same-site or cross-site: another page sent it
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // not sent by a page
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false // "null" and the like: sandboxed or opaque
	}
	return u.Host == r.Host || u.Host == r.Header.Get("X-Forwarded-Host")
}

// Alert nodes come from whoever may post to /api/alertmanager. They stay
// until resolved and are recorded, so how many there can be is bounded.
const (
	maxAlertsPerApp = 20
	maxAlerts       = 500
)

// alertIndex is the alert nodes written into the graph, by app.
type alertIndex struct {
	mu    sync.Mutex
	byApp map[string]map[string]bool
	n     int
}

// add reports whether an alert node may be written: it's already there, or
// there's room for it.
func (x *alertIndex) add(app, id string) bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.byApp[app][id] {
		return true
	}
	if x.n >= maxAlerts || len(x.byApp[app]) >= maxAlertsPerApp {
		return false
	}
	if x.byApp == nil {
		x.byApp = map[string]map[string]bool{}
	}
	if x.byApp[app] == nil {
		x.byApp[app] = map[string]bool{}
	}
	x.byApp[app][id] = true
	x.n++
	return true
}

func (x *alertIndex) remove(app, id string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if !x.byApp[app][id] {
		return
	}
	delete(x.byApp[app], id)
	if len(x.byApp[app]) == 0 {
		delete(x.byApp, app)
	}
	x.n--
}
