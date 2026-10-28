// Package auth signs people in to kubexray with OpenID Connect (ADR-0013):
// the code flow with PKCE, an allow-list of emails, domains or groups, and
// signed session cookies.
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

// User is who signed in.
type User struct {
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
	// Who they are to Kubernetes, for checking what they may read
	// (ADR-0020): the username claim, and their groups, unprefixed.
	KubeUser string   `json:"kube,omitempty"`
	Groups   []string `json:"groups,omitempty"`
}

// MinKeyLength is the shortest session key accepted.
const MinKeyLength = 32

// Sealer signs and checks small values kept in cookies: sessions, and the
// sign-in flow's state. Anyone can read them; nobody can change them.
type Sealer struct{ key []byte }

// NewSealer returns a sealer with a secret key.
func NewSealer(key []byte) (*Sealer, error) {
	if len(key) < MinKeyLength {
		return nil, errors.New("auth: the session key must be at least 32 bytes")
	}
	return &Sealer{key: key}, nil
}

// Seal encodes v with an expiry and signs it, for a cookie with a purpose
// (sessions can't be replayed as flow state, or the other way round).
func (s *Sealer) Seal(purpose string, v any, until time.Time) (string, error) {
	payload, err := json.Marshal(struct {
		V any   `json:"v"`
		X int64 `json:"x"`
	}{v, until.Unix()})
	if err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	return body + "." + base64.RawURLEncoding.EncodeToString(s.mac(purpose, body)), nil
}

// Open checks a sealed value and decodes it into v, unless it was changed,
// sealed for another purpose, or has expired. It returns when it expires.
func (s *Sealer) Open(purpose, sealed string, now time.Time, v any) (time.Time, bool) {
	body, sig, ok := strings.Cut(sealed, ".")
	if !ok {
		return time.Time{}, false
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || subtle.ConstantTimeCompare(got, s.mac(purpose, body)) != 1 {
		return time.Time{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return time.Time{}, false
	}
	var w struct {
		V json.RawMessage `json:"v"`
		X int64           `json:"x"`
	}
	if json.Unmarshal(payload, &w) != nil || now.Unix() >= w.X {
		return time.Time{}, false
	}
	return time.Unix(w.X, 0), json.Unmarshal(w.V, v) == nil
}

func (s *Sealer) mac(purpose, body string) []byte {
	h := hmac.New(sha256.New, s.key)
	h.Write([]byte(purpose))
	h.Write([]byte{0})
	h.Write([]byte(body))
	return h.Sum(nil)
}

// Cookie names: the session, and the sign-ins in progress (one per
// sign-in, named after its state).
const (
	SessionCookie = "kubexray_signin"
	flowCookie    = "kubexray_signin_flow"
)

// cookie builds a cookie with kubexray's settings (ADR-0013, section 3). A
// negative maxAge deletes it.
func cookie(name, value string, maxAge time.Duration, secure bool) *http.Cookie {
	age := int(maxAge / time.Second)
	if maxAge < 0 {
		age = -1
	}
	// Secure whenever kubexray is opened over https; NewOIDC allows plain http
	// only on localhost.
	return &http.Cookie{ //nolint:gosec // see above
		Name: name, Value: value, Path: "/", MaxAge: age,
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, // Lax: the provider redirects back from its site
	}
}
