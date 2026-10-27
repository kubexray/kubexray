package feed

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/kubexray/kubexray/internal/graph"
)

// Drift (ADR-0019): a hand edit to an object Helm, Argo CD or Flux manages is
// compared with what that tool last applied, as kubexray recorded it, and
// comes with the command that puts it back. Helm's own record of a release
// is a Secret, which kubexray never reads; its name says the revision.

// applies reports whether the object's latest write is by the tool that
// manages it: what it holds now is what the tool applied.
func applies(n graph.Node) bool {
	return n.Origin != nil && n.Change != nil && n.Change.Tool == n.Origin.Tool
}

// drifts reports whether a write is a hand edit to a managed object: by
// someone other than its tool and Kubernetes' controllers.
func drifts(n graph.Node, by *graph.Change) bool {
	return n.Origin != nil && by != nil && by.Tool != n.Origin.Tool && by.Tool != "controller"
}

// driftDiff is what differs between what the tool applied and now.
func driftDiff(applied, n graph.Node) []DiffLine {
	var d differ
	if applied.Spec != nil && n.Spec != nil {
		d.template(*applied.Spec, *n.Spec)
	}
	if applied.Data != nil && n.Data != nil {
		d.lines = append(d.lines, dataDiff(applied.Data, n.Data)...)
	}
	d.lines = append(d.lines, manifestDiff(applied, n)...)
	return d.lines
}

const helmRecord = "sh.helm.release.v1."

// restore is the command that puts a drifted object back as its tool
// applied it.
//
// For Helm it replaces just this object with the one in the release's
// manifest: "helm rollback" and "helm upgrade" merge, so fields and list
// items added by hand stay. The namespace is passed at every step: Helm's
// manifest has none, and kubectl would fill in the kubeconfig's. Helm's
// ownership annotations aren't in the manifest either (Helm adds them as it
// applies); without them the next upgrade refuses the object, so they're
// put back.
func restore(st *graph.State, n graph.Node) string {
	o := n.Origin
	if o == nil || o.Name == "" {
		return ""
	}
	ns := func(namespace string) string {
		if namespace == "" {
			return ""
		}
		return " -n " + namespace
	}
	switch o.Tool {
	case "helm":
		release := o.Namespace
		if release == "" {
			release = n.Namespace
		}
		rev := ""
		if r := helmRevision(st, o.Name, release); r > 0 {
			rev = " --revision " + strconv.Itoa(r)
		}
		owner := fmt.Sprintf(`.metadata.annotations += {"meta.helm.sh/release-name": %q, "meta.helm.sh/release-namespace": %q}`, o.Name, release)
		return fmt.Sprintf("helm get manifest %s%s%s | kubectl create%s --dry-run=client -o json -f - | jq 'select(.kind == %q and .metadata.name == %q) | %s' | kubectl replace%s -f -",
			o.Name, ns(release), rev, ns(n.Namespace), n.Kind, n.Name, owner, ns(n.Namespace))
	case "argocd":
		return "argocd app sync " + o.Name
	case "flux":
		kind := "kustomization"
		if o.Kind == "HelmRelease" {
			kind = "helmrelease"
		}
		return fmt.Sprintf("flux reconcile %s %s%s --with-source", kind, o.Name, ns(o.Namespace))
	}
	return ""
}

// helmRevision is the release's latest revision that Helm deployed, from
// the labels on its Secrets ("sh.helm.release.v1.storefront.v3": status,
// version), or 0 when they're unknown. A failed upgrade or rollback is
// skipped: its manifest may never have been applied.
func helmRevision(st *graph.State, release, namespace string) int {
	if st == nil {
		return 0
	}
	deployed, ok := 0, 0
	for _, n := range st.Nodes {
		if n.Kind != "Secret" || !strings.HasPrefix(n.Name, helmRecord) || n.Labels["name"] != release || (namespace != "" && n.Namespace != namespace) {
			continue
		}
		rev, err := strconv.Atoi(n.Labels["version"])
		if err != nil {
			continue
		}
		switch n.Labels["status"] {
		case "deployed":
			deployed = max(deployed, rev)
		case "superseded":
			ok = max(ok, rev)
		}
	}
	if deployed > 0 {
		return deployed
	}
	return ok
}
