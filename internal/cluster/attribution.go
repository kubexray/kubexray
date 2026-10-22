package cluster

import (
	"cmp"
	"strings"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubexray/kubexray/internal/graph"
)

// Annotations kept in memory (ADR-0005). Everything else, including
// kubectl.kubernetes.io/last-applied-configuration, is dropped on arrival.
const (
	annotationRevision    = "deployment.kubernetes.io/revision"
	annotationHelmRelease = "meta.helm.sh/release-name"
	annotationArgoTrack   = "argocd.argoproj.io/tracking-id"
)

var keptAnnotations = []string{annotationRevision, annotationHelmRelease, annotationArgoTrack}

// controllers are field managers whose writes are follow-ups to someone
// else's change; they're only blamed when nobody else has written the object.
// Distributions that embed the controllers (k3s, RKE2) write under their own
// name.
var controllers = map[string]bool{
	"kube-scheduler": true,
	"kubelet":        true,
	"k3s":            true,
	"rke2":           true,
}

func isController(manager string) bool {
	return controllers[manager] || strings.HasSuffix(manager, "-controller-manager")
}

// maxValueLen caps free-form strings kept from objects (manager names,
// allow-listed annotation values) so a hostile value can't bloat every update.
const maxValueLen = 64

// capped shortens s to at most maxValueLen bytes without splitting a UTF-8 rune.
func capped(s string) string {
	n := maxValueLen
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// lastChange attributes the most recent non-status write, from managedFields.
func lastChange(m metav1.Object) *graph.Change {
	var best, bestController *metav1.ManagedFieldsEntry
	fields := m.GetManagedFields()
	for i := range fields {
		e := &fields[i]
		if e.Subresource == "status" || e.Time == nil {
			continue // status is bookkeeping; untimed entries can't be ordered
		}
		target := &best
		if isController(e.Manager) {
			target = &bestController
		}
		if *target == nil || newer(e, *target) {
			*target = e
		}
	}
	if best == nil {
		best = bestController
	}
	if best == nil {
		return nil
	}
	tool, via := toolOf(best.Manager, best.Operation)
	return &graph.Change{At: best.Time.UTC(), By: capped(best.Manager), Tool: tool, Via: via}
}

// newer orders entries by time. Timestamps only have second precision, so
// ties are broken by manager name to keep the result stable.
func newer(a, b *metav1.ManagedFieldsEntry) bool {
	if !a.Time.Equal(b.Time) {
		return a.Time.After(b.Time.Time)
	}
	return cmp.Compare(a.Manager, b.Manager) < 0
}

// toolOf normalises a field manager name into a tool family and, for kubectl,
// the subcommand that made the change.
func toolOf(manager string, op metav1.ManagedFieldsOperationType) (tool, via string) {
	switch {
	case manager == "kubectl" || strings.HasPrefix(manager, "kubectl-"):
		via = strings.TrimPrefix(strings.TrimPrefix(manager, "kubectl"), "-")
		if via == "client-side-apply" || (via == "" && op == metav1.ManagedFieldsOperationApply) {
			via = "apply"
		}
		return "kubectl", via
	case manager == "kustomize-controller" || manager == "helm-controller" || strings.HasPrefix(manager, "flux"):
		return "flux", ""
	case strings.HasPrefix(manager, "argocd"):
		return "argocd", ""
	case manager == "helm" || strings.HasPrefix(manager, "helm-"):
		return "helm", ""
	case isController(manager):
		return "controller", ""
	default:
		return "other", ""
	}
}

// originOf reports where an object is managed from, using a fixed allow-list
// of labels and annotations. Flux is checked first because its Helm
// controller also sets Helm's annotations.
func originOf(m metav1.Object) *graph.Origin {
	a, l := m.GetAnnotations(), m.GetLabels()
	switch {
	case l["kustomize.toolkit.fluxcd.io/name"] != "":
		return &graph.Origin{Tool: "flux", Name: l["kustomize.toolkit.fluxcd.io/name"]}
	case l["helm.toolkit.fluxcd.io/name"] != "":
		return &graph.Origin{Tool: "flux", Name: l["helm.toolkit.fluxcd.io/name"]}
	case a[annotationArgoTrack] != "":
		// Format: <app>:<group>/<kind>:<namespace>/<name>
		app, _, _ := strings.Cut(a[annotationArgoTrack], ":")
		return &graph.Origin{Tool: "argocd", Name: app}
	case l["argocd.argoproj.io/instance"] != "":
		return &graph.Origin{Tool: "argocd", Name: l["argocd.argoproj.io/instance"]}
	case a[annotationHelmRelease] != "":
		return &graph.Origin{Tool: "helm", Name: a[annotationHelmRelease]}
	case l["app.kubernetes.io/managed-by"] == "Helm":
		return &graph.Origin{Tool: "helm", Name: l["app.kubernetes.io/instance"]}
	default:
		return nil
	}
}

// podSpec extracts the allow-listed desired state of a pod or pod template:
// images and resources, never commands, args or environment.
func podSpec(m metav1.Object, ps *corev1.PodSpec) *graph.Spec {
	containers := make([]graph.Container, 0, len(ps.InitContainers)+len(ps.Containers)+len(ps.EphemeralContainers))
	for _, c := range ps.InitContainers {
		c := container(c)
		c.Init = true
		containers = append(containers, c)
	}
	for _, c := range ps.Containers {
		containers = append(containers, container(c))
	}
	for _, c := range ps.EphemeralContainers { // added by kubectl debug
		ec := container(corev1.Container(c.EphemeralContainerCommon))
		ec.Ephemeral = true
		containers = append(containers, ec)
	}
	return &graph.Spec{
		Generation: m.GetGeneration(),
		Revision:   m.GetAnnotations()[annotationRevision],
		Containers: containers,
	}
}

func container(c corev1.Container) graph.Container {
	return graph.Container{
		Name:     c.Name,
		Image:    c.Image,
		Requests: resources(c.Resources.Requests),
		Limits:   resources(c.Resources.Limits),
	}
}

func resources(rl corev1.ResourceList) graph.Resources {
	var r graph.Resources
	if q, ok := rl[corev1.ResourceCPU]; ok {
		r.CPU = q.String()
	}
	if q, ok := rl[corev1.ResourceMemory]; ok {
		r.Memory = q.String()
	}
	return r
}
