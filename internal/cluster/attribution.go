package cluster

import (
	"cmp"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubexray/kubexray/internal/graph"
	"github.com/kubexray/kubexray/internal/redact"
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
	// Rewrite what they watch, now that kubexray diffs every kind (ADR-0018):
	// aggregated ClusterRoles, and webhooks' CA bundles.
	"clusterrole-aggregation-controller": true,
	"cainjector":                         true,
	"cert-manager-cainjector":            true,
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
// images, resources, probes, ports, what the pods read (environment
// variable names and sources, volumes) and the service account. Commands,
// args and literal environment values are fingerprinted, to show that they
// changed (ADR-0015), and kept as masked text for when values are on
// (ADR-0017); the watcher drops the text otherwise.
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
		Generation:     m.GetGeneration(),
		Revision:       m.GetAnnotations()[annotationRevision],
		Containers:     containers,
		ServiceAccount: capped(ps.ServiceAccountName),
		Volumes:        volumes(ps.Volumes),
	}
}

func container(c corev1.Container) graph.Container {
	gc := graph.Container{
		Name:     c.Name,
		Image:    c.Image,
		Requests: resources(c.Resources.Requests),
		Limits:   resources(c.Resources.Limits),
		Probes: graph.Probes{
			Liveness: c.LivenessProbe != nil, Readiness: c.ReadinessProbe != nil, Startup: c.StartupProbe != nil,
			LivenessCheck: probeCheck(c.LivenessProbe), ReadinessCheck: probeCheck(c.ReadinessProbe), StartupCheck: probeCheck(c.StartupProbe),
		},
	}
	for _, e := range c.Env {
		v := graph.EnvVar{Name: capped(e.Name)}
		switch f := e.ValueFrom; {
		case f == nil:
			v.Value = fingerprint(e.Value)
			v.Text, v.Masked = literal(e.Name, e.Value)
		case f.ConfigMapKeyRef != nil:
			v.From = "ConfigMap/" + capped(f.ConfigMapKeyRef.Name) + ":" + capped(f.ConfigMapKeyRef.Key)
		case f.SecretKeyRef != nil:
			v.From = "Secret/" + capped(f.SecretKeyRef.Name) + ":" + capped(f.SecretKeyRef.Key)
		case f.FieldRef != nil:
			v.From = "field:" + capped(f.FieldRef.FieldPath)
		case f.ResourceFieldRef != nil:
			v.From = "resource:" + capped(f.ResourceFieldRef.Resource)
		}
		gc.Env = append(gc.Env, v)
		if len(gc.Env) == maxListed {
			break
		}
	}
	for _, e := range c.EnvFrom {
		switch {
		case e.ConfigMapRef != nil:
			gc.EnvFrom = append(gc.EnvFrom, "ConfigMap/"+capped(e.ConfigMapRef.Name))
		case e.SecretRef != nil:
			gc.EnvFrom = append(gc.EnvFrom, "Secret/"+capped(e.SecretRef.Name))
		}
	}
	if len(c.Command)+len(c.Args) > 0 {
		gc.Command = fingerprint(strings.Join(c.Command, "\x00") + "\x01" + strings.Join(c.Args, "\x00"))
		text, masked := literal("", strings.Join(slices.Concat(c.Command, c.Args), " "))
		gc.Run = graph.Value{Text: text, Hash: gc.Command, Masked: masked}
	}
	for _, p := range c.Ports {
		gc.Ports = append(gc.Ports, fmt.Sprintf("%d/%s", p.ContainerPort, cmp.Or(string(p.Protocol), "TCP")))
	}
	return gc
}

// maxListed caps lists kept per container or pod, so a hostile object can't
// bloat every update.
const maxListed = 64

// probeCheck says what a probe checks, without a command's contents.
func probeCheck(p *corev1.Probe) string {
	switch {
	case p == nil:
		return ""
	case p.HTTPGet != nil:
		return fmt.Sprintf("http GET :%s%s", p.HTTPGet.Port.String(), capped(p.HTTPGet.Path))
	case p.TCPSocket != nil:
		return "tcp :" + p.TCPSocket.Port.String()
	case p.GRPC != nil:
		return fmt.Sprintf("grpc :%d", p.GRPC.Port)
	case p.Exec != nil:
		return "a command"
	}
	return ""
}

// volumes lists the objects a pod's volumes come from.
func volumes(vs []corev1.Volume) []string {
	var out []string
	for _, v := range vs {
		switch {
		case v.ConfigMap != nil:
			out = append(out, "ConfigMap/"+capped(v.ConfigMap.Name))
		case v.Secret != nil:
			out = append(out, "Secret/"+capped(v.Secret.SecretName))
		case v.PersistentVolumeClaim != nil:
			out = append(out, "PVC/"+capped(v.PersistentVolumeClaim.ClaimName))
		}
		if len(out) == maxListed {
			break
		}
	}
	return out
}

// fingerprintKey keys value fingerprints. It's random for each run and
// never stored, so a fingerprint in a recording or a shared replay can't
// be matched against guesses: it only shows that a value changed.
var fingerprintKey = func() []byte {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		panic(err) // crypto/rand doesn't fail on supported platforms
	}
	return k
}()

// maxValue caps one value's text; maxData caps a ConfigMap's (ADR-0017).
const (
	maxValue = 4 << 10
	maxData  = 64 << 10
)

// literal is a value's text as a diff may show it: masked when its name
// looks like a credential, redacted where it holds one, and cut when it's
// long. Masked says why.
func literal(name, value string) (text, masked string) {
	switch {
	case redact.SensitiveName(name):
		return "", "name"
	case len(value) > maxValue:
		cut := value[:maxValue]
		for len(cut) > 0 && !utf8.RuneStart(value[len(cut)]) {
			cut = cut[:len(cut)-1]
		}
		t, _ := redact.Text(cut)
		return t, "size"
	}
	if t, hid := redact.Text(value); hid {
		return t, "value"
	}
	return value, ""
}

// systemNamespaces hold the cluster's own configuration: kubexray keeps no
// values from them.
var systemNamespaces = map[string]bool{"kube-system": true, "kube-public": true, "kube-node-lease": true}

// configData is a ConfigMap's contents for diffs: each key's text (masked
// and capped like any value) and fingerprint. Binary data and keys past the
// size budget keep only a fingerprint.
func configData(cm *corev1.ConfigMap) map[string]graph.Value {
	if systemNamespaces[cm.Namespace] || len(cm.Data)+len(cm.BinaryData) == 0 {
		return nil
	}
	out := make(map[string]graph.Value, len(cm.Data)+len(cm.BinaryData))
	budget := maxData
	keys := slices.Sorted(maps.Keys(cm.Data))
	for _, k := range keys {
		v := cm.Data[k]
		val := graph.Value{Hash: fingerprint(v)}
		if budget -= len(v); budget < 0 {
			val.Masked = "size"
		} else {
			val.Text, val.Masked = literal(k, v)
		}
		out[capped(k)] = val
		if len(out) == maxListed {
			return out
		}
	}
	for k, v := range cm.BinaryData {
		out[capped(k)] = graph.Value{Hash: string(v), Masked: "binary"} // a fingerprint since stripObject
		if len(out) == maxListed {
			break
		}
	}
	return out
}

func fingerprint(value string) string {
	h := hmac.New(sha256.New, fingerprintKey)
	h.Write([]byte(value))
	return "fp:" + hex.EncodeToString(h.Sum(nil))[:12]
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
