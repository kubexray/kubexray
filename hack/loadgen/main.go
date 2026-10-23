// Command loadgen runs the real kubexray pipeline and UI against a simulated
// cluster: many apps and pods held by a fake API server, with pods changing
// status continuously and every 7th app crash-looping after a recent
// ConfigMap edit, so incidents are exercised too. It's for checking that the
// UI stays smooth at scale (make perf); it never talks to a real cluster.
// The cluster shape mirrors internal/synthetic.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/signal"
	"syscall"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
	metadatafake "k8s.io/client-go/metadata/fake"
	"k8s.io/utils/ptr"

	"github.com/kubexray/kubexray/internal/cluster"
	"github.com/kubexray/kubexray/internal/graph"
	"github.com/kubexray/kubexray/internal/recorder"
	"github.com/kubexray/kubexray/internal/server"
	"github.com/kubexray/kubexray/internal/web"
)

func main() {
	namespaces := flag.Int("namespaces", 10, "namespaces")
	apps := flag.Int("apps", 10, "apps per namespace")
	pods := flag.Int("pods", 10, "pods per app")
	churn := flag.Duration("churn", 100*time.Millisecond, "how often a pod changes status")
	port := flag.Int("port", 0, "port on 127.0.0.1")
	flag.Parse()

	if err := run(*namespaces, *apps, *pods, *churn, *port); err != nil {
		fmt.Fprintln(os.Stderr, "loadgen:", err)
		os.Exit(1)
	}
}

func run(namespaces, apps, podsPerApp int, churn time.Duration, port int) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	typed, meta, podRefs := objects(namespaces, apps, podsPerApp)
	scheme := metadatafake.NewTestScheme()
	if err := metav1.AddMetaToScheme(scheme); err != nil {
		return err
	}
	client := kubefake.NewClientset(typed...)
	g := graph.New()
	if err := cluster.New(client, metadatafake.NewSimpleMetadataClient(scheme, meta...), g, log).Start(ctx); err != nil {
		return err
	}

	rec := recorder.New(recorder.DefaultOptions)
	hub := server.NewHub(server.MaxClients, server.ClientQueue)
	srv, err := server.New(hub, rec, web.Assets(), log, server.Info{Cluster: "loadgen", Version: "dev"})
	if err != nil {
		return err
	}
	ln, err := srv.Listen(ctx, port)
	if err != nil {
		return err
	}
	fmt.Printf("Simulated cluster: %d pods in %d apps, one pod changing every %v.\n", len(podRefs), namespaces*apps, churn)
	fmt.Printf("Open %s   (Ctrl-C to stop)\n", srv.URL())

	go churnPods(ctx, client, podRefs, churn, log)
	go func() { _ = recorder.Pump(ctx, g, rec, 100*time.Millisecond, log, hub.Emit) }()
	return srv.Serve(ctx, ln)
}

// churnPods restarts random healthy pods: they go not-ready, then recover.
// (The fake API server has no controllers, so Deployment counts don't move.)
func churnPods(ctx context.Context, client *kubefake.Clientset, pods []*corev1.Pod, every time.Duration, log *slog.Logger) {
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			p := pods[rand.IntN(len(pods))] //nolint:gosec // simulation, not security
			ready := p.Status.Conditions[0].Status != corev1.ConditionTrue
			p.Status.Conditions[0].Status = map[bool]corev1.ConditionStatus{true: corev1.ConditionTrue, false: corev1.ConditionFalse}[ready]
			p.Status.Conditions[0].LastTransitionTime = metav1.Now()
			p.Status.ContainerStatuses[0].Ready = ready
			if !ready {
				p.Status.ContainerStatuses[0].RestartCount++
			}
			if _, err := client.CoreV1().Pods(p.Namespace).UpdateStatus(ctx, p, metav1.UpdateOptions{}); err != nil && ctx.Err() == nil {
				log.Warn("churn: updating pod", "pod", p.Name, "err", err)
			}
		}
	}
}

func objects(namespaces, apps, podsPerApp int) (typed, meta []runtime.Object, pods []*corev1.Pod) {
	now := time.Now()
	created := metav1.NewTime(now.Add(-24 * time.Hour))
	edited := metav1.NewTime(now.Add(-5 * time.Minute))
	app := 0
	for n := range namespaces {
		ns := fmt.Sprintf("team-%02d", n)
		typed = append(typed, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}, Status: corev1.NamespaceStatus{Phase: corev1.NamespaceActive}})
		for a := range apps {
			app++
			failing := app%7 == 0
			name := fmt.Sprintf("svc-%02d", a)
			labels := map[string]string{"app": name}
			template := corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: name, Image: "registry.example/" + name + ":1.4.2"}}}}
			typed = append(typed,
				&appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, CreationTimestamp: created, Annotations: map[string]string{"deployment.kubernetes.io/revision": "1"}},
					Spec:       appsv1.DeploymentSpec{Replicas: ptr.To(int32(podsPerApp)), Template: template},                //nolint:gosec // small simulation sizes
					Status:     appsv1.DeploymentStatus{ReadyReplicas: int32(podsPerApp), UpdatedReplicas: int32(podsPerApp)}, //nolint:gosec // small simulation sizes
				},
				&appsv1.ReplicaSet{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: ns, Name: name + "-7d9f", CreationTimestamp: created,
						Annotations:     map[string]string{"deployment.kubernetes.io/revision": "1"},
						OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: name}},
					},
					Spec: appsv1.ReplicaSetSpec{Replicas: ptr.To(int32(podsPerApp)), Template: template}, //nolint:gosec // small simulation sizes
				},
				&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}, Spec: corev1.ServiceSpec{Selector: labels}},
			)
			cm := &metav1.PartialObjectMetadata{
				TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
				ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name + "-config", CreationTimestamp: created},
			}
			if failing {
				cm.ManagedFields = []metav1.ManagedFieldsEntry{{Manager: "kubectl-patch", Operation: metav1.ManagedFieldsOperationUpdate, Time: &edited}}
			}
			meta = append(meta, cm)
			for p := range podsPerApp {
				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: ns, Name: fmt.Sprintf("%s-7d9f-%03d", name, p), Labels: labels, CreationTimestamp: created,
						OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: name + "-7d9f"}},
					},
					Spec: corev1.PodSpec{
						NodeName: "worker-1", Containers: template.Spec.Containers,
						Volumes: []corev1.Volume{{VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: name + "-config"}}}}},
					},
					Status: corev1.PodStatus{
						Phase:             corev1.PodRunning,
						Conditions:        []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue, LastTransitionTime: created}},
						ContainerStatuses: []corev1.ContainerStatus{{Name: name, Ready: true}},
					},
				}
				if failing && p == 0 {
					// Started after the edit and crash-looping on it.
					restarted := metav1.NewTime(now.Add(-2 * time.Minute))
					pod.CreationTimestamp = restarted
					pod.Status.Conditions[0] = corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionFalse, LastTransitionTime: restarted}
					pod.Status.ContainerStatuses[0] = corev1.ContainerStatus{
						Name: name, RestartCount: 3,
						State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
					}
					typed = append(typed, pod)
					continue // not churned: it stays broken
				}
				typed = append(typed, pod)
				pods = append(pods, pod)
			}
		}
	}
	typed = append(typed, &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "worker-1"},
		Status:     corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}},
	})
	return typed, meta, pods
}
