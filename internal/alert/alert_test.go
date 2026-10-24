package alert

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kubexray/kubexray/internal/cause"
	"github.com/kubexray/kubexray/internal/graph"
)

var t0 = time.Date(2026, 10, 24, 14, 0, 0, 0, time.UTC)

func TestTarget(t *testing.T) {
	t.Parallel()
	for labels, want := range map[string]string{
		"namespace=shop deployment=api":                "Deployment/shop/api",
		"namespace=shop statefulset=db pod=db-0":       "StatefulSet/shop/db",
		"namespace=shop pod=api-7d9f-x":                "Pod/shop/api-7d9f-x",
		"namespace=shop job_name=backup":               "Job/shop/backup",
		"namespace=shop daemonset=agent":               "DaemonSet/shop/agent",
		"node=worker-2":                                "", // not about an app
		"deployment=api":                               "", // no namespace
		"namespace=shop alertname=KubeQuotaAlmostFull": "",
	} {
		a := Alert{Labels: map[string]string{}}
		for kv := range strings.FieldsSeq(labels) {
			k, v, _ := strings.Cut(kv, "=")
			a.Labels[k] = v
		}
		got, byPod := a.Target()
		if got != want || byPod != strings.HasPrefix(want, "Pod/") {
			t.Errorf("Target(%s) = %q (by pod: %v), want %q", labels, got, byPod, want)
		}
	}
}

func message() Message {
	return Message{
		Alert: Alert{
			Status: "firing", Fingerprint: "f1", StartsAt: t0,
			Labels:      map[string]string{"alertname": "KubePodCrashLooping", "namespace": "shop", "deployment": "checkout"},
			Annotations: map[string]string{"summary": "Pod is crash looping <script>"},
		},
		Incident: cause.Incident{
			App: "Deployment/shop/checkout", Kind: "Deployment", Namespace: "shop", Name: "checkout", Since: t0,
			Symptom:   cause.Symptom{Summary: "1 pod CrashLoopBackOff", Ready: 2, Desired: 3, BadPods: []string{"x"}},
			Diagnosis: &cause.Diagnosis{Summary: "The container checkout keeps exiting with code 1"},
			Causes: []cause.Cause{{
				Kind: cause.KindConfig, Summary: "ConfigMap checkout-config was edited", Confidence: cause.Likely,
				At: t0.Add(-time.Minute), By: &graph.Change{By: "kubectl-patch", Tool: "kubectl", Via: "patch"},
			}},
		},
		Link: Link("https://kubexray.example.com/", t0, "Deployment/shop/checkout"),
	}
}

func TestSlackMessage(t *testing.T) {
	t.Parallel()
	body, err := Slack(message())
	if err != nil {
		t.Fatal(err)
	}
	var got struct{ Text string }
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	want := "*KubePodCrashLooping* in shop: Pod is crash looping &lt;script&gt;\n" +
		"*checkout is degraded*: 1 pod CrashLoopBackOff\n" +
		"What's wrong: The container checkout keeps exiting with code 1\n" +
		"Likely cause: ConfigMap checkout-config was edited by kubectl patch at 13:59:00 UTC\n" +
		"<https://kubexray.example.com/?app=Deployment%2Fshop%2Fcheckout&t=2026-10-24T14%3A00%3A00Z|Open in kubexray at the moment it started>"
	if got.Text != want {
		t.Errorf("Slack text:\n%s\nwant:\n%s", got.Text, want)
	}
}

// recorder is a destination that records what it receives.
type recorder struct {
	mu     sync.Mutex
	bodies []string
	got    chan struct{}
}

func (r *recorder) ServeHTTP(_ http.ResponseWriter, req *http.Request) {
	b, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	r.bodies = append(r.bodies, string(b))
	r.mu.Unlock()
	r.got <- struct{}{}
}

func TestNotifierSendsOncePerAlert(t *testing.T) {
	t.Parallel()
	slack, hook := &recorder{got: make(chan struct{}, 8)}, &recorder{got: make(chan struct{}, 8)}
	ss, hs := httptest.NewServer(slack), httptest.NewServer(hook)
	defer ss.Close()
	defer hs.Close()
	n := NewNotifier(ss.URL, hs.URL, slog.New(slog.DiscardHandler))
	now := t0
	n.now = func() time.Time { return now }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go n.Run(ctx)

	n.Queue(message())
	n.Queue(message()) // Alertmanager repeats it: quiet
	for _, r := range []*recorder{slack, hook} {
		select {
		case <-r.got:
		case <-time.After(5 * time.Second):
			t.Fatal("nothing sent")
		}
	}
	settle(n) // delivered, then move the clock
	n.mu.Lock()
	now = now.Add(Quiet + time.Minute)
	n.mu.Unlock()
	n.Queue(message()) // still firing half an hour later: sent again
	<-slack.got
	<-hook.got

	slack.mu.Lock()
	defer slack.mu.Unlock()
	hook.mu.Lock()
	defer hook.mu.Unlock()
	if len(slack.bodies) != 2 || len(hook.bodies) != 2 {
		t.Errorf("sent %d to Slack and %d to the webhook, want 2 each", len(slack.bodies), len(hook.bodies))
	}
	var m Message
	if err := json.Unmarshal([]byte(hook.bodies[0]), &m); err != nil || m.Incident.App != "Deployment/shop/checkout" || m.Link == "" {
		t.Errorf("webhook body = %s (%v)", hook.bodies[0], err)
	}
}

func TestQueueDropsTheOldestWhenFull(t *testing.T) {
	t.Parallel()
	n := NewNotifier("http://127.0.0.1:1", "", slog.New(slog.DiscardHandler))
	name := func(i int) string { return fmt.Sprintf("Alert%03d", i) }
	for i := range QueueSize + 10 {
		m := message()
		m.Alert.Labels = map[string]string{"alertname": name(i)}
		n.Queue(m) // nothing sends: the queue fills
	}
	if len(n.queue) != QueueSize {
		t.Fatalf("queue holds %d, want %d", len(n.queue), QueueSize)
	}
	if first := <-n.queue; first.Alert.Name() != name(10) {
		t.Errorf("oldest kept = %q, want the 11th: the first 10 were dropped", first.Alert.Name())
	}
	// A dropped alert wasn't delivered: when Alertmanager repeats it, it goes.
	m := message()
	m.Alert.Labels = map[string]string{"alertname": name(0)}
	n.Queue(m)
	if got := len(n.queue); got != QueueSize { // it took a slot (dropping the oldest again)
		t.Errorf("a repeat of a dropped alert wasn't queued: queue holds %d", got)
	}
}

func TestOneMessagePerAppAndRetriesAfterFailures(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	status, calls := http.StatusInternalServerError, 0
	got := make(chan struct{}, 8)
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		w.WriteHeader(status)
		mu.Unlock()
		got <- struct{}{}
	}))
	defer dest.Close()
	n := NewNotifier("", dest.URL, slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go n.Run(ctx)
	wait := func() {
		t.Helper()
		select {
		case <-got:
		case <-time.After(5 * time.Second):
			t.Fatal("nothing sent")
		}
	}

	// Two pods of one app fire the same alert: one message.
	a, b := message(), message()
	a.Alert.Fingerprint, b.Alert.Fingerprint = "pod-a", "pod-b"
	n.Queue(a)
	n.Queue(b)
	wait()
	settle(n)
	// The destination failed: Alertmanager's repeat is sent again.
	mu.Lock()
	status = http.StatusOK
	mu.Unlock()
	n.Queue(a)
	wait()
	settle(n)
	n.Queue(a) // delivered now: quiet
	// Resolved, then firing again (a regression): sent at once.
	n.Resolve(a.Alert.Name(), a.Incident.App)
	n.Queue(a)
	wait()
	settle(n)
	mu.Lock()
	defer mu.Unlock()
	if calls != 3 {
		t.Errorf("destination called %d times, want 3 (failed, retried, regression)", calls)
	}
}

// settle waits until the notifier has finished with everything queued: a
// destination sees a message before the notifier records its delivery.
func settle(n *Notifier) {
	for {
		n.mu.Lock()
		idle := len(n.pending) == 0
		n.mu.Unlock()
		if idle {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}
