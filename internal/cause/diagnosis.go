package cause

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/kubexray/kubexray/internal/graph"
)

// Diagnosis explains the symptom itself, in the terms Kubernetes reports it
// (ADR-0009): what is wrong, as opposed to what changed. Evidence lists the
// facts it's based on, so it can be checked with kubectl describe.
type Diagnosis struct {
	Summary  string   `json:"summary"`
	Evidence []string `json:"evidence,omitempty"`
}

// diagnose explains the app's most common pod problem, or a workload that
// can't create pods at all. It returns nil when there's nothing specific to
// say ("3 pods not ready" is already the symptom).
func (x *index) diagnose(app graph.Node, pods []graph.Node, s Symptom) *Diagnosis {
	if len(s.BadPods) == 0 {
		if app.Status.Message != "" {
			return refused(app)
		}
		return nil
	}
	var same []graph.Node // bad pods with the symptom's reason
	for _, p := range pods {
		if bad(p) && p.Status.Reason == s.Reason {
			same = append(same, p)
		}
	}
	if len(same) == 0 {
		return nil
	}
	p := same[0]
	n := len(same)
	switch r := s.Reason; {
	case r == "OOMKilled":
		return outOfMemory(p, n, s.Restarts)
	case r == "CrashLoopBackOff" || r == "Error" || strings.HasPrefix(r, "Init:") && p.Status.Exit != 0:
		return crashing(p, n, s.Restarts)
	case r == "Unschedulable":
		if d := x.pendingVolume(same); d != nil {
			return d
		}
		return unschedulable(p, n)
	case r == "Evicted":
		return evicted(p, n)
	case r == "CreateContainerConfigError" || r == "CreateContainerError" || r == "Init:CreateContainerConfigError":
		return &Diagnosis{
			Summary:  "Kubernetes can't create the " + containerName(p) + ": " + strings.TrimSuffix(p.Status.Message, "."),
			Evidence: said("the kubelet", p.Status.Message),
		}
	case pullReasons[r] || pullReasons[strings.TrimPrefix(r, "Init:")]:
		return cantPull(p, n)
	case r == "" || r == "ContainerCreating":
		if d := x.pendingVolume(same); d != nil {
			return d
		}
		if r == "" && p.Status.Phase == "Running" {
			return notReady(p, n)
		}
	}
	return nil
}

// containerName says which container, when the pod's status names one.
func containerName(p graph.Node) string {
	c := p.Status.Container
	kind := "container"
	if spec := container(p, c); spec != nil && spec.Init {
		kind = "init container"
	}
	if c == "" {
		return kind
	}
	return kind + " " + c
}

func container(p graph.Node, name string) *graph.Container {
	if p.Spec == nil {
		return nil
	}
	for i, c := range p.Spec.Containers {
		if c.Name == name || name == "" && !c.Init && !c.Ephemeral {
			return &p.Spec.Containers[i]
		}
	}
	return nil
}

func said(who, msg string) []string {
	if msg == "" {
		return nil
	}
	return []string{fmt.Sprintf("%s says: %q", who, msg)}
}

func outOfMemory(p graph.Node, n int, restarts int32) *Diagnosis {
	d := &Diagnosis{Evidence: []string{fmt.Sprintf("%s OOMKilled (exit code 137), %s in all", plural(n, "pod"), plural(int(restarts), "restart"))}}
	c := container(p, p.Status.Container)
	limit := ""
	if c != nil {
		limit = c.Limits.Memory
		if c.Requests.Memory != "" {
			d.Evidence = append(d.Evidence, "memory request "+c.Requests.Memory)
		}
	}
	if limit != "" {
		d.Summary = fmt.Sprintf("The %s runs out of memory: Kubernetes kills it when it uses more than its %s limit", containerName(p), limit)
		d.Evidence = append(d.Evidence, "memory limit "+limit)
	} else {
		d.Summary = fmt.Sprintf("The %s was killed for using too much memory; it has no memory limit, so its node ran out", containerName(p))
		d.Evidence = append(d.Evidence, "no memory limit set")
	}
	d.Evidence = append(d.Evidence, "kubexray can't see memory use; kubectl top can, with metrics-server")
	return d
}

// exitMeanings are what common exit codes usually mean.
var exitMeanings = map[int32]string{
	1:   "a general error; its logs say why",
	2:   "usually a usage or configuration error",
	126: "its command can't be run (permissions, or not an executable)",
	127: "its command wasn't found in the image",
	134: "it aborted (SIGABRT)",
	137: "it was killed (SIGKILL)",
	139: "it crashed (segmentation fault)",
	143: "it was stopped (SIGTERM)",
}

func crashing(p graph.Node, n int, restarts int32) *Diagnosis {
	code := p.Status.Exit
	if code == 0 {
		return nil // unknown: the symptom already says it's crashing
	}
	d := &Diagnosis{
		Summary:  fmt.Sprintf("The %s keeps exiting with code %d", containerName(p), code),
		Evidence: []string{fmt.Sprintf("%s failing, %s in all", plural(n, "pod"), plural(int(restarts), "restart"))},
	}
	if meaning, ok := exitMeanings[code]; ok {
		d.Summary += ": " + meaning
	}
	if c := container(p, p.Status.Container); c != nil && c.Probes.Liveness && (code == 137 || code == 143) {
		d.Summary += ", possibly by its liveness probe"
		d.Evidence = append(d.Evidence, "it has a liveness probe: when the probe fails, Kubernetes kills the container the same way")
	}
	return d
}

func notReady(p graph.Node, n int) *Diagnosis {
	if p.Spec == nil || !slices.ContainsFunc(p.Spec.Containers, func(c graph.Container) bool { return c.Probes.Readiness }) {
		return nil
	}
	return &Diagnosis{
		Summary:  fmt.Sprintf("%s running but failing %s readiness probe, so Services send %s no traffic", plural(n, "pod")+" "+isAre(n), itsTheir(n), itThem(n)),
		Evidence: []string{"containers running, pod not ready", "a readiness probe is defined; kubectl describe pod shows its failures"},
	}
}

func itThem(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}

func itsTheir(n int) string {
	if n == 1 {
		return "its"
	}
	return "their"
}

// schedulerReasons translates the reasons in a scheduler message, as said of
// one node and of several. Its wording isn't an API, so the message itself is
// always kept as evidence.
var schedulerReasons = []struct{ match, one, many string }{
	{"Insufficient memory", "has too little free memory for its memory request", "have too little free memory for its memory request"},
	{"Insufficient cpu", "has too little free CPU for its CPU request", "have too little free CPU for its CPU request"},
	{"untolerated taint", "has a taint it doesn't tolerate", "have taints it doesn't tolerate"},
	{"didn't match Pod's node affinity/selector", "doesn't match its node selector or affinity", "don't match its node selector or affinity"},
	{"didn't match pod anti-affinity rules", "is ruled out by its anti-affinity rules", "are ruled out by its anti-affinity rules"},
	{"didn't match pod affinity rules", "doesn't run the pods it must be placed with", "don't run the pods it must be placed with"},
	{"volume node affinity conflict", "is in a different zone from its volume", "are in a different zone from its volume"},
	{"Too many pods", "is at its pod limit", "are at their pod limit"},
	{"were unschedulable", "is cordoned", "are cordoned"},
}

// schedulerItem matches one reason in "0/3 nodes are available: 1 node(s)
// had untolerated taint(s), 2 Insufficient memory. preemption: …".
var schedulerItem = regexp.MustCompile(`^(\d+) (.+)$`)

// translateScheduler turns a scheduler message into reasons, the reason
// covering the most nodes first.
func translateScheduler(msg string) []string {
	type part struct {
		nodes int
		text  string
	}
	var parts []part
	_, list, ok := strings.Cut(msg, "are available: ")
	list, _, _ = strings.Cut(list, ". ")
	if ok {
		for item := range strings.SplitSeq(strings.TrimSuffix(list, "."), ", ") {
			m := schedulerItem.FindStringSubmatch(item)
			if m == nil {
				continue
			}
			n, _ := strconv.Atoi(m[1])
			for _, r := range schedulerReasons {
				if strings.Contains(m[2], r.match) {
					says := r.many
					if n == 1 {
						says = r.one
					}
					parts = append(parts, part{n, fmt.Sprintf("%s %s", plural(n, "node"), says)})
				}
			}
		}
	}
	if len(parts) == 0 { // no counts: e.g. "pod has unbound immediate PersistentVolumeClaims"
		if strings.Contains(msg, "unbound immediate PersistentVolumeClaims") {
			return []string{"its volume isn't bound yet"}
		}
		return nil
	}
	slices.SortStableFunc(parts, func(a, b part) int { return b.nodes - a.nodes })
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = p.text
	}
	return out
}

func unschedulable(p graph.Node, n int) *Diagnosis {
	msg := p.Status.Message
	d := &Diagnosis{Summary: fmt.Sprintf("The scheduler can't place %s", plural(n, "pod")), Evidence: said("the scheduler", msg)}
	if says := translateScheduler(msg); len(says) > 0 {
		d.Summary += ": " + strings.Join(says, "; ")
	}
	if c := container(p, ""); c != nil && (c.Requests.CPU != "" || c.Requests.Memory != "") {
		d.Evidence = append(d.Evidence, describeResources(graph.Container{Requests: c.Requests}))
	}
	return d
}

// pendingVolume finds a volume the pods use that isn't bound.
func (x *index) pendingVolume(pods []graph.Node) *Diagnosis {
	for _, id := range x.deps(pods) {
		v, ok := x.st.Nodes[id]
		if !ok || v.Kind != "PersistentVolumeClaim" || v.Status.Ready {
			continue
		}
		phase := cmp.Or(v.Status.Phase, "not bound")
		return &Diagnosis{
			Summary:  fmt.Sprintf("Its volume %s is %s: no storage has been provisioned for it, so the pods can't start", v.Name, phase),
			Evidence: []string{fmt.Sprintf("PersistentVolumeClaim %s is %s", v.Name, phase), "kubectl describe pvc shows why: a missing StorageClass, or no matching volume"},
		}
	}
	return nil
}

func evicted(p graph.Node, n int) *Diagnosis {
	msg := p.Status.Message
	why := "usually because its node ran low on memory or disk"
	switch {
	case strings.Contains(msg, "low on resource: memory"):
		why = "its node ran low on memory"
	case strings.Contains(msg, "low on resource: ephemeral-storage"), strings.Contains(msg, "low on resource: nodefs"), strings.Contains(msg, "low on resource: imagefs"):
		why = "its node ran low on disk"
	case strings.Contains(msg, "ephemeral local storage usage exceeds"):
		why = "it used more local disk than its limit"
	}
	return &Diagnosis{
		Summary:  fmt.Sprintf("The kubelet evicted %s: %s", plural(n, "pod"), why),
		Evidence: said("the kubelet", msg),
	}
}

func cantPull(p graph.Node, n int) *Diagnosis {
	image := ""
	if c := container(p, p.Status.Container); c != nil {
		image = c.Image
	}
	msg := strings.ToLower(p.Status.Message)
	why := ""
	switch {
	case strings.Contains(msg, "not found"), strings.Contains(msg, "manifest unknown"):
		why = ": the image or tag doesn't exist"
	case strings.Contains(msg, "unauthorized"), strings.Contains(msg, "denied"), strings.Contains(msg, "authentication required"):
		why = ": the registry refused access; check imagePullSecrets"
	case strings.Contains(msg, "invalid reference format"), p.Status.Reason == "InvalidImageName":
		why = ": the image name is invalid"
	}
	what := "its image"
	if image != "" {
		what = image
	}
	return &Diagnosis{
		Summary:  fmt.Sprintf("Kubernetes can't pull %s%s", what, why),
		Evidence: append([]string{fmt.Sprintf("%s %s", plural(n, "pod"), p.Status.Reason)}, said("the kubelet", p.Status.Message)...),
	}
}

func refused(app graph.Node) *Diagnosis {
	msg := app.Status.Message
	why := "Kubernetes refused to create its pods"
	switch {
	case strings.Contains(msg, "exceeded quota"):
		why += ": a ResourceQuota in " + app.Namespace + " is used up"
	case strings.Contains(msg, "admission webhook"):
		why += ": an admission webhook denied them"
	case strings.Contains(msg, "violates PodSecurity"):
		why += ": they break the namespace's pod security level"
	case strings.Contains(msg, "serviceaccount") && strings.Contains(msg, "not found"):
		why += ": its service account doesn't exist"
	}
	return &Diagnosis{Summary: why, Evidence: said("the controller", msg)}
}
