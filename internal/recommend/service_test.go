package recommend

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/kubexray/kubexray/internal/cause"
)

// fakeCluster serves the demo's objects and crash log.
type fakeCluster struct {
	t        *testing.T
	logs     string
	logCalls int
}

func (f *fakeCluster) Object(_ context.Context, kind, ns, name string) (*unstructured.Unstructured, error) {
	switch kind + "/" + ns + "/" + name {
	case "ConfigMap/shop/checkout-config":
		return configMap(f.t, "shop", "checkout-config", "info"), nil
	case "Deployment/shop/checkout":
		return obj(f.t, &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "checkout"},
			Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Name: "checkout", Image: "busybox",
				Command: []string{"sh", "-c", `case "$LOG_LEVEL" in debug|info|warn|error) exec sleep infinity;; *) echo "invalid LOG_LEVEL: $LOG_LEVEL"; exit 1;; esac`},
				Args:    []string{"--token=s3cret-value"},
			}}}}},
		}, "Deployment", "apps/v1"), nil
	}
	return nil, errors.New("not found")
}

func (f *fakeCluster) List(context.Context, string, string) ([]*unstructured.Unstructured, error) {
	return []*unstructured.Unstructured{configMap(f.t, "shop", "checkout-config", "info"), configMap(f.t, "shop", "api-config", "")}, nil
}

func (f *fakeCluster) PreviousLogs(_ context.Context, ns, pod, _ string) (string, error) {
	f.logCalls++
	if ns != "shop" || pod != "checkout-abc" {
		return "", errors.New("no such pod")
	}
	return f.logs, nil
}

func crashIncident(t *testing.T) cause.Incident {
	in := demoInput(t)
	inc := in.Incident
	inc.Symptom.BadPods = []string{"Pod/shop/checkout-abc"}
	inc.Causes = []cause.Cause{in.Cause}
	return inc
}

func TestServiceKeepsCrashLogsAndPrepares(t *testing.T) {
	c := &fakeCluster{t: t, logs: "checkout starting\ninvalid LOG_LEVEL: verbose\n"}
	s := NewService(&answers{}, c, slog.New(slog.DiscardHandler))
	inc := crashIncident(t)

	if _, err := s.Prepare(context.Background(), inc); !errors.Is(err, ErrNoLogs) {
		t.Fatalf("before capture: %v", err)
	}
	s.Capture(context.Background(), []cause.Incident{inc})
	s.Capture(context.Background(), []cause.Incident{inc}) // once per incident
	if c.logCalls != 1 {
		t.Errorf("logs read %d times", c.logCalls)
	}
	in, err := s.Prepare(context.Background(), inc)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(in.Logs, "|") != "checkout starting|invalid LOG_LEVEL: verbose" || len(in.Running) != 2 || in.Object.GetName() != "checkout-config" {
		t.Errorf("input = logs %q, running %d, object %s", in.Logs, len(in.Running), in.Object.GetName())
	}
	if len(in.Startup) != 1 || !strings.Contains(in.Startup[0], "debug|info|warn|error") || strings.Contains(in.Startup[0], "s3cret") {
		t.Errorf("startup = %q", in.Startup)
	}
	req := Request(in)
	if !strings.Contains(req, "Deployment shop/checkout was degraded") || !strings.Contains(req, "<startup>") {
		t.Errorf("request:\n%s", req)
	}
}

func TestServiceSkipsWhatItCantRecommendFor(t *testing.T) {
	c := &fakeCluster{t: t, logs: "x"}
	s := NewService(&answers{}, c, slog.New(slog.DiscardHandler))
	inc := crashIncident(t)
	inc.Causes[0].Kind = cause.KindNode
	s.Capture(context.Background(), []cause.Incident{inc})
	if c.logCalls != 0 {
		t.Error("read logs for an incident it can't recommend for")
	}
	if _, err := s.Prepare(context.Background(), inc); !errors.Is(err, ErrUnsupported) {
		t.Errorf("err = %v", err)
	}
}

// slowModel answers after a while, to test one draft at a time.
type slowModel struct{ answers }

func (s *slowModel) Complete(ctx context.Context, system, user string, schema map[string]any) (string, Usage, error) {
	time.Sleep(200 * time.Millisecond)
	return s.answers.Complete(ctx, system, user, schema)
}

func TestServiceDraftsOneAtATime(t *testing.T) {
	m := &slowModel{answers{replies: []string{answer(t, logLevels, 1)}}}
	s := NewService(m, &fakeCluster{t: t}, slog.New(slog.DiscardHandler))
	in := demoInput(t)
	done := make(chan error, 1)
	go func() {
		_, err := s.Recommend(context.Background(), "ada@example.com", in)
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	if _, err := s.Recommend(context.Background(), "bob@example.com", in); !errors.Is(err, ErrBusy) {
		t.Errorf("second draft: %v, want busy", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
