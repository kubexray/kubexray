package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSameOrigin(t *testing.T) {
	t.Parallel()
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) })
	h := sameOrigin(ok)
	for _, c := range []struct {
		name    string
		method  string
		headers map[string]string
		want    int
	}{
		{"a read from anywhere", http.MethodGet, map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusAccepted},
		{"Alertmanager", http.MethodPost, map[string]string{"Content-Type": "application/json"}, http.StatusAccepted},
		{"a script without a body", http.MethodPost, nil, http.StatusAccepted},
		{"the UI", http.MethodPost, map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "https://kx.example.com"}, http.StatusAccepted},
		{"the UI from an older browser", http.MethodPost, map[string]string{"Origin": "https://kx.example.com"}, http.StatusAccepted},
		{"behind a proxy that rewrites Host", http.MethodPost, map[string]string{"Origin": "https://kubexray.example.com", "X-Forwarded-Host": "kubexray.example.com"}, http.StatusAccepted},
		{"a form on another site", http.MethodPost, map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, http.StatusUnsupportedMediaType},
		{"a plain-text post", http.MethodPost, map[string]string{"Content-Type": "text/plain;charset=UTF-8"}, http.StatusUnsupportedMediaType},
		{"a multipart form", http.MethodPost, map[string]string{"Content-Type": "multipart/form-data; boundary=x"}, http.StatusUnsupportedMediaType},
		{"a fetch from another site", http.MethodPost, map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		{"a fetch from a sibling subdomain", http.MethodPost, map[string]string{"Sec-Fetch-Site": "same-site"}, http.StatusForbidden},
		{"an older browser on another site", http.MethodPost, map[string]string{"Origin": "https://evil.example"}, http.StatusForbidden},
		{"a sandboxed frame", http.MethodPost, map[string]string{"Origin": "null"}, http.StatusForbidden},
	} {
		r := httptest.NewRequestWithContext(t.Context(), c.method, "https://kx.example.com/api/alertmanager", nil)
		for k, v := range c.headers {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != c.want {
			t.Errorf("%s: %d, want %d", c.name, w.Code, c.want)
		}
	}
}

func TestAlertIndexIsBounded(t *testing.T) {
	t.Parallel()
	var x alertIndex
	for i := range maxAlertsPerApp {
		if !x.add("Deployment/shop/api", fmt.Sprint(i)) {
			t.Fatalf("alert %d refused", i)
		}
	}
	if x.add("Deployment/shop/api", "one too many") {
		t.Error("an app got more alerts than the limit")
	}
	if !x.add("Deployment/shop/api", "0") {
		t.Error("an alert already shown was refused")
	}
	x.remove("Deployment/shop/api", "0")
	if !x.add("Deployment/shop/api", "room again") {
		t.Error("no room after an alert resolved")
	}
	for i := 0; x.n < maxAlerts; i++ {
		x.add(fmt.Sprintf("Deployment/shop/app-%d", i), "a")
	}
	if x.add("Deployment/shop/one-more", "a") {
		t.Error("more alerts than the overall limit")
	}
}
