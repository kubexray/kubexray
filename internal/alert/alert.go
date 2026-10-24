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
// through its app.
func (a Alert) Target() string {
	ns := a.Labels["namespace"]
	if ns == "" {
		return ""
	}
	for _, k := range []struct{ label, kind string }{
		{"deployment", "Deployment"}, {"statefulset", "StatefulSet"}, {"daemonset", "DaemonSet"}, {"job_name", "Job"}, {"pod", "Pod"},
	} {
		if name := a.Labels[k.label]; name != "" {
			return graph.ID(k.kind, ns, name)
		}
	}
	return ""
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

// Notifier sends messages to Slack and a webhook, at most once per alert per
// Quiet, through a bounded queue.
type Notifier struct {
	slack, webhook string // destination URLs; either may be empty
	quiet          time.Duration
	client         *http.Client
	log            *slog.Logger
	now            func() time.Time

	mu    sync.Mutex
	sent  map[string]time.Time // alert → when it was last sent
	queue chan Message
}

// Quiet is how long an alert stays quiet after it was sent: Alertmanager
// repeats notifications.
const Quiet = 30 * time.Minute

// NewNotifier returns a notifier for the destinations (either may be empty).
func NewNotifier(slack, webhook string, log *slog.Logger) *Notifier {
	return &Notifier{
		slack: slack, webhook: webhook, quiet: Quiet, log: log, now: time.Now,
		client: &http.Client{Timeout: 10 * time.Second},
		sent:   map[string]time.Time{}, queue: make(chan Message, QueueSize),
	}
}

// QueueSize bounds the messages waiting to be sent.
const QueueSize = 64

// Enabled reports whether any destination is configured.
func (n *Notifier) Enabled() bool { return n != nil && (n.slack != "" || n.webhook != "") }

// Queue schedules a message unless that alert was sent within Quiet. A full
// queue drops the oldest message: alerts flood, and the newest matter most.
func (n *Notifier) Queue(m Message) {
	key := m.Alert.Fingerprint
	if key == "" {
		key = m.Alert.Name() + "/" + m.Incident.App
	}
	n.mu.Lock()
	now := n.now()
	if last, ok := n.sent[key]; ok && now.Sub(last) < n.quiet {
		n.mu.Unlock()
		return
	}
	n.sent[key] = now
	for k, t := range n.sent { // forget old alerts
		if now.Sub(t) > n.quiet {
			delete(n.sent, k)
		}
	}
	n.mu.Unlock()
	for {
		select {
		case n.queue <- m:
			return
		default:
			select {
			case dropped := <-n.queue:
				n.log.Warn("alert queue full; dropping the oldest", "alert", dropped.Alert.Name())
			default:
			}
		}
	}
}

// Run sends queued messages until ctx is cancelled.
func (n *Notifier) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-n.queue:
			n.send(ctx, m)
		}
	}
}

func (n *Notifier) send(ctx context.Context, m Message) {
	if n.slack != "" {
		if body, err := Slack(m); err == nil {
			n.post(ctx, "slack", n.slack, body)
		}
	}
	if n.webhook != "" {
		if body, err := json.Marshal(m); err == nil {
			n.post(ctx, "webhook", n.webhook, body)
		}
	}
}

func (n *Notifier) post(ctx context.Context, dest, target string, body []byte) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		n.log.Error("alert destination", "destination", dest, "err", err)
		return
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
		return
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= 300 {
		n.log.Warn("sending an alert", "destination", dest, "status", resp.StatusCode)
	}
}
