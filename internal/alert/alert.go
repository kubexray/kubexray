// Package alert turns Prometheus Alertmanager notifications into messages
// with kubexray's explanation: the app's state, the diagnosis, the likely
// cause and who made the change (ADR-0011).
package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/kubexray/kubexray/internal/cause"
	"github.com/kubexray/kubexray/internal/graph"
)

// Notification is Alertmanager's webhook payload (version 4).
type Notification struct {
	Version     string  `json:"version"`
	Status      string  `json:"status"`
	Alerts      []Alert `json:"alerts"`
	ExternalURL string  `json:"externalURL"`
}

// Alert is one alert in a notification.
type Alert struct {
	Status      string            `json:"status"` // firing or resolved
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	StartsAt    time.Time         `json:"startsAt"`
	Fingerprint string            `json:"fingerprint"`
}

// Name is the alert's name.
func (a Alert) Name() string { return a.Labels["alertname"] }

// Target is the app an alert is about, as a node ID, or "" when it names
// none: namespace plus the first of deployment, statefulset, daemonset,
// job_name or pod, as kube-state-metrics labels them. A pod is explained
// through its app. byPod says only a pod label named it: Prometheus can add
// namespace and pod labels for the target it scraped, so a pod alone is
// weaker evidence of what the alert is about.
func (a Alert) Target() (id string, byPod bool) {
	ns := a.Labels["namespace"]
	if ns == "" {
		return "", false
	}
	for _, k := range []struct{ label, kind string }{
		{"deployment", "Deployment"}, {"statefulset", "StatefulSet"}, {"daemonset", "DaemonSet"}, {"job_name", "Job"},
	} {
		if name := a.Labels[k.label]; name != "" {
			return graph.ID(k.kind, ns, name), false
		}
	}
	if name := a.Labels["pod"]; name != "" {
		return graph.ID("Pod", ns, name), true
	}
	return "", false
}

// Message is what's sent for one alert.
type Message struct {
	Alert    Alert          `json:"alert"`
	Incident cause.Incident `json:"incident"`
	Link     string         `json:"link,omitempty"` // kubexray at the moment it started
}

// Link opens kubexray at a moment with an app selected.
func Link(publicURL string, at time.Time, app string) string {
	if publicURL == "" {
		return ""
	}
	q := url.Values{"app": {app}}
	if !at.IsZero() {
		q.Set("t", at.UTC().Format(time.RFC3339))
	}
	return strings.TrimSuffix(publicURL, "/") + "/?" + q.Encode()
}

// slackEscape makes cluster strings inert in Slack's markup.
var slackEscape = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// Slack formats a message for a Slack incoming webhook.
func Slack(m Message) ([]byte, error) {
	esc := slackEscape.Replace
	inc := m.Incident
	var b strings.Builder
	fmt.Fprintf(&b, "*%s* in %s", esc(m.Alert.Name()), esc(inc.Namespace))
	if s := m.Alert.Annotations["summary"]; s != "" {
		fmt.Fprintf(&b, ": %s", esc(s))
	}
	fmt.Fprintf(&b, "\n*%s %s*: %s", esc(inc.Name), inc.State(), esc(inc.Symptom.Summary))
	if d := inc.Diagnosis; d != nil {
		fmt.Fprintf(&b, "\nWhat's wrong: %s", esc(d.Summary))
	}
	switch {
	case len(inc.Causes) > 0:
		c := inc.Causes[0]
		fmt.Fprintf(&b, "\n%s cause: %s", capitalize(string(c.Confidence)), esc(c.Summary))
		if c.By != nil {
			fmt.Fprintf(&b, " by %s", esc(who(c.By)))
		}
		if !c.At.IsZero() {
			fmt.Fprintf(&b, " at %s UTC", c.At.UTC().Format("15:04:05"))
		}
	case inc.Healthy:
		b.WriteString("\nkubexray sees it as healthy now.")
	default:
		fmt.Fprintf(&b, "\nNo cause found in the cluster: %s.", esc(inc.NoCause))
	}
	if m.Link != "" {
		fmt.Fprintf(&b, "\n<%s|Open in kubexray at the moment it started>", m.Link)
	}
	return json.Marshal(map[string]string{"text": b.String()})
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// who names the author of a change the way the UI does.
func who(c *graph.Change) string {
	switch {
	case c.Tool == "kubectl" && c.Via != "":
		return "kubectl " + c.Via
	case c.Tool == "other" || c.Tool == "controller":
		return c.By
	default:
		return c.Tool
	}
}

// Notifier sends messages to Slack and a webhook, at most once per alert and
// app per Quiet, through a bounded queue.
type Notifier struct {
	slack, webhook string // destination URLs; either may be empty
	quiet          time.Duration
	client         *http.Client
	log            *slog.Logger
	now            func() time.Time

	mu      sync.Mutex
	sent    map[string]time.Time // key → when it was last delivered
	pending map[string]bool      // keys queued or being sent
	queue   chan Message
}

// Quiet is how long an alert stays quiet after it was delivered:
// Alertmanager repeats notifications.
const Quiet = 30 * time.Minute

// QueueSize bounds the messages waiting to be sent.
const QueueSize = 64

// NewNotifier returns a notifier for the destinations (either may be empty).
func NewNotifier(slack, webhook string, log *slog.Logger) *Notifier {
	return &Notifier{
		slack: slack, webhook: webhook, quiet: Quiet, log: log, now: time.Now,
		client: &http.Client{Timeout: 10 * time.Second},
		sent:   map[string]time.Time{}, pending: map[string]bool{}, queue: make(chan Message, QueueSize),
	}
}

// Enabled reports whether any destination is configured.
func (n *Notifier) Enabled() bool { return n != nil && (n.slack != "" || n.webhook != "") }

// Key identifies what a message is about: an alert on an app. Every pod of a
// broken app fires its own alert; they're one message.
func Key(alertName, app string) string { return alertName + "/" + app }

// Queue schedules a message unless the same alert on the same app was
// delivered within Quiet or is already on its way. A full queue drops the
// oldest message: alerts flood, and the newest matter most.
func (n *Notifier) Queue(m Message) {
	key := Key(m.Alert.Name(), m.Incident.App)
	n.mu.Lock()
	now := n.now()
	for k, t := range n.sent { // forget old deliveries
		if now.Sub(t) >= n.quiet {
			delete(n.sent, k)
		}
	}
	if _, quiet := n.sent[key]; quiet || n.pending[key] {
		n.mu.Unlock()
		return
	}
	n.pending[key] = true
	n.mu.Unlock()
	for {
		select {
		case n.queue <- m:
			return
		default:
			select {
			case dropped := <-n.queue:
				n.done(dropped, false) // not delivered: a repeat may try again
				n.log.Warn("alert queue full; dropping the oldest", "alert", dropped.Alert.Name())
			default:
			}
		}
	}
}

// Resolve forgets that an alert on an app was delivered, so firing again
// (a regression) is reported at once.
func (n *Notifier) Resolve(alertName, app string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	delete(n.sent, Key(alertName, app))
}

func (n *Notifier) done(m Message, delivered bool) {
	key := Key(m.Alert.Name(), m.Incident.App)
	n.mu.Lock()
	defer n.mu.Unlock()
	delete(n.pending, key)
	if delivered {
		n.sent[key] = n.now()
	}
}

// Run sends queued messages until ctx is cancelled.
func (n *Notifier) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-n.queue:
			n.done(m, n.send(ctx, m))
		}
	}
}

// send delivers a message to every destination, and reports whether all
// accepted it: if one failed, Alertmanager's next repeat tries again.
func (n *Notifier) send(ctx context.Context, m Message) bool {
	ok := true
	if n.slack != "" {
		body, err := Slack(m)
		ok = err == nil && n.post(ctx, "slack", n.slack, body) && ok
	}
	if n.webhook != "" {
		body, err := json.Marshal(m)
		ok = err == nil && n.post(ctx, "webhook", n.webhook, body) && ok
	}
	return ok
}

func (n *Notifier) post(ctx context.Context, dest, target string, body []byte) bool {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		n.log.Error("alert destination", "destination", dest, "err", err)
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		// The URL is a credential: log the error without it.
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		n.log.Warn("sending an alert", "destination", dest, "err", err)
		return false
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= 300 {
		n.log.Warn("sending an alert", "destination", dest, "status", resp.StatusCode)
		return false
	}
	return true
}
