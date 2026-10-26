package recommend

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/kubexray/kubexray/internal/cause"
	"github.com/kubexray/kubexray/internal/graph"
)

// Cluster is what a recommendation reads from the cluster, only when someone
// asks for one, or while an incident is on to keep its crash logs.
type Cluster interface {
	Object(ctx context.Context, kind, namespace, name string) (*unstructured.Unstructured, error)
	List(ctx context.Context, kind, namespace string) ([]*unstructured.Unstructured, error)
	PreviousLogs(ctx context.Context, namespace, pod, container string) (string, error)
}

// NewCluster reads with client-go.
func NewCluster(client kubernetes.Interface, dyn dynamic.Interface) Cluster {
	return &kube{client: client, dyn: dyn}
}

type kube struct {
	client kubernetes.Interface
	dyn    dynamic.Interface
}

func (k *kube) Object(ctx context.Context, kind, ns, name string) (*unstructured.Unstructured, error) {
	gvr, ok := resources[kind]
	if !ok {
		return nil, fmt.Errorf("kubexray doesn't read %s", kind)
	}
	o, err := k.dyn.Resource(gvr).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	o.SetKind(kind)
	o.SetAPIVersion(gvr.GroupVersion().String())
	return o, nil
}

// maxRunning bounds the objects tried in "accepts everything running now".
const maxRunning = 500

func (k *kube) List(ctx context.Context, kind, ns string) ([]*unstructured.Unstructured, error) {
	gvr, ok := resources[kind]
	if !ok {
		return nil, fmt.Errorf("kubexray doesn't read %s", kind)
	}
	l, err := k.dyn.Resource(gvr).Namespace(ns).List(ctx, metav1.ListOptions{Limit: maxRunning})
	if err != nil {
		return nil, err
	}
	out := make([]*unstructured.Unstructured, len(l.Items))
	for i := range l.Items {
		l.Items[i].SetKind(kind)
		l.Items[i].SetAPIVersion(gvr.GroupVersion().String())
		out[i] = &l.Items[i]
	}
	return out, nil
}

func (k *kube) PreviousLogs(ctx context.Context, ns, pod, container string) (string, error) {
	tail, limit := int64(maxLogLines), int64(maxLogBytes)
	b, err := k.client.CoreV1().Pods(ns).GetLogs(pod, &corev1.PodLogOptions{Container: container, Previous: true, TailLines: &tail, LimitBytes: &limit}).DoRaw(ctx)
	return string(b), err
}

// Service drafts recommendations for a running kubexray: it keeps crash
// logs while incidents are on, and drafts one recommendation at a time.
type Service struct {
	model   Model
	cluster Cluster
	log     *slog.Logger

	mu       sync.Mutex
	captured map[string]capture // by incident: app and start
	order    []string           // oldest first, to bound captured
	busy     bool
}

// capture is what was kept while an incident was on.
type capture struct {
	Logs    []string
	Startup []string
}

// maxCaptured bounds the incidents whose logs are kept.
const maxCaptured = 100

// NewService returns a recommendation service.
func NewService(m Model, c Cluster, log *slog.Logger) *Service {
	return &Service{model: m, cluster: c, log: log, captured: map[string]capture{}}
}

// ModelName names the model recommendations are drafted with.
func (s *Service) ModelName() string { return s.model.Name() }

func key(inc cause.Incident) string { return inc.App + "@" + inc.Since.UTC().Format(time.RFC3339) }

// crashing are the pod reasons whose previous container has logs to read.
var crashing = map[string]bool{"CrashLoopBackOff": true, "Error": true, "OOMKilled": true}

// Capture keeps the crash logs and startup commands of incidents that a
// recommendation could be drafted for later, once each. Pods that crash are
// often gone by the time the incident is fixed.
func (s *Service) Capture(ctx context.Context, incidents []cause.Incident) {
	for _, inc := range incidents {
		if !crashing[inc.Symptom.Reason] || len(inc.Symptom.BadPods) == 0 || supported(inc) == nil {
			continue
		}
		k := key(inc)
		s.mu.Lock()
		_, done := s.captured[k]
		s.mu.Unlock()
		if done {
			continue
		}
		c := capture{Startup: s.startup(ctx, inc)}
		_, ns, pod := graph.ParseID(inc.Symptom.BadPods[0])
		logs, err := s.cluster.PreviousLogs(ctx, ns, pod, "")
		if err != nil {
			s.log.Debug("keeping crash logs for a recommendation", "pod", ns+"/"+pod, "err", err)
			continue // try again on the next pass; the pod may not have restarted yet
		}
		c.Logs = Tail(logs)
		s.mu.Lock()
		s.captured[k] = c
		s.order = append(s.order, k)
		if len(s.order) > maxCaptured {
			delete(s.captured, s.order[0])
			s.order = s.order[1:]
		}
		s.mu.Unlock()
	}
}

// startup is how the app's containers start: command and args, redacted.
func (s *Service) startup(ctx context.Context, inc cause.Incident) []string {
	o, err := s.cluster.Object(ctx, inc.Kind, inc.Namespace, inc.Name)
	if err != nil {
		return nil
	}
	containers, _, _ := unstructured.NestedSlice(o.Object, "spec", "template", "spec", "containers")
	var out []string
	for _, c := range containers {
		m, _ := c.(map[string]any)
		name, _ := m["name"].(string)
		cmd, _, _ := unstructured.NestedStringSlice(m, "command")
		args, _, _ := unstructured.NestedStringSlice(m, "args")
		if len(cmd)+len(args) == 0 {
			continue // the image's own entrypoint: nothing to show
		}
		out = append(out, fmt.Sprintf("container %s: %s", name, strings.Join(slices.Concat(cmd, args), " ")))
	}
	return Redact(out)
}

// supported returns the cause a recommendation can be drafted for, if any.
func supported(inc cause.Incident) *cause.Cause {
	for i, c := range inc.Causes {
		if c.Kind == cause.KindConfig && c.Confidence != cause.Possible && strings.HasPrefix(c.Object, "ConfigMap/") {
			return &inc.Causes[i]
		}
	}
	return nil
}

// Errors before a draft.
var (
	ErrBusy   = errors.New("another recommendation is being drafted; try again when it's done")
	ErrNoLogs = errors.New("kubexray didn't keep this incident's crash logs: recommendations need them, and kubexray keeps them while an incident is on")
)

// Prepare gathers what a draft for an incident sends, without sending it:
// the consent dialog shows Request(in) (ADR-0014, section 5).
func (s *Service) Prepare(ctx context.Context, inc cause.Incident) (Input, error) {
	c := supported(inc)
	if c == nil {
		return Input{}, ErrUnsupported
	}
	s.mu.Lock()
	kept, ok := s.captured[key(inc)]
	s.mu.Unlock()
	if !ok || len(kept.Logs) == 0 {
		return Input{}, ErrNoLogs
	}
	kind, ns, name := graph.ParseID(c.Object)
	obj, err := s.cluster.Object(ctx, kind, ns, name)
	if err != nil {
		return Input{}, fmt.Errorf("reading %s %s/%s: %w", kind, ns, name, err)
	}
	running, err := s.cluster.List(ctx, kind, ns)
	if err != nil {
		return Input{}, fmt.Errorf("listing %ss in %s: %w", kind, ns, err)
	}
	return Input{Incident: inc, Cause: *c, Object: obj, Running: running, Logs: kept.Logs, Startup: kept.Startup}, nil
}

// Request is the text a draft sends to the model first.
func Request(in Input) string { return request(in) }

// Recommend drafts and proves, one at a time, and writes an audit line.
func (s *Service) Recommend(ctx context.Context, who string, in Input) (*Recommendation, error) {
	s.mu.Lock()
	if s.busy {
		s.mu.Unlock()
		return nil, ErrBusy
	}
	s.busy = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.busy = false
		s.mu.Unlock()
	}()
	start := time.Now()
	rec, err := Draft(ctx, s.model, in)
	attrs := []any{"audit", true, "event", "recommendation", "who", who, "app", in.Incident.App, "model", s.model.Name(), "seconds", int(time.Since(start).Seconds())}
	switch {
	case err != nil:
		s.log.Warn("safety recommendation failed", append(attrs, "err", err)...)
	default:
		checks := make([]string, len(rec.Proof.Checks))
		for i, c := range rec.Proof.Checks {
			checks[i] = fmt.Sprintf("%s=%v", c.Name, c.Passed)
		}
		s.log.Info("safety recommendation drafted", append(attrs, "proven", rec.Proof.Passed(), "attempts", rec.Attempts, "checks", checks, "tokens_in", rec.Usage.In, "tokens_out", rec.Usage.Out)...)
	}
	return rec, err
}
