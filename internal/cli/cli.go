// Package cli parses targets and renders cause-engine results for the
// terminal commands (`kubexray why`, `kubexray changes`).
package cli

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/kubexray/kubexray/internal/cause"
	"github.com/kubexray/kubexray/internal/graph"
)

// kinds maps what people type (as with kubectl) to kinds kubexray knows.
var kinds = map[string]string{
	"deploy": "Deployment", "deployment": "Deployment", "deployments": "Deployment",
	"sts": "StatefulSet", "statefulset": "StatefulSet", "statefulsets": "StatefulSet",
	"ds": "DaemonSet", "daemonset": "DaemonSet", "daemonsets": "DaemonSet",
	"job": "Job", "jobs": "Job",
	"rs": "ReplicaSet", "replicaset": "ReplicaSet", "replicasets": "ReplicaSet",
	"po": "Pod", "pod": "Pod", "pods": "Pod",
}

// ParseTarget turns "deploy/api" into a node ID in namespace ns.
func ParseTarget(target, ns string) (string, error) {
	kind, name, ok := strings.Cut(target, "/")
	if !ok || name == "" {
		return "", fmt.Errorf("expected <kind>/<name>, like deploy/api or pod/api-7d9f-x2k, got %q", target)
	}
	k, ok := kinds[strings.ToLower(kind)]
	if !ok {
		return "", fmt.Errorf("kubexray explains deployments, statefulsets, daemonsets, jobs and pods; %q isn't one of them", kind)
	}
	return graph.ID(k, ns, name), nil
}

// Why renders an incident, or a healthy app, for the terminal.
func Why(w io.Writer, inc cause.Incident, now time.Time) error {
	p := &printer{w: w}
	title := fmt.Sprintf("%s (%s, %s)", inc.Name, inc.Kind, inc.Namespace)

	if inc.Healthy {
		p.line("%s is healthy: %d of %d pods ready", title, inc.Symptom.Ready, inc.Symptom.Desired)
		if c := inc.LastChange; c != nil {
			p.row("last change", "%s, %s", who(c), ago(c.At, now))
		}
		return p.err
	}

	state := "is degraded"
	if inc.Down() {
		state = "is down"
	}
	if inc.Since.IsZero() {
		p.line("%s %s: %d of %d pods ready", title, state, inc.Symptom.Ready, inc.Symptom.Desired)
	} else {
		p.line("%s %s: %d of %d pods ready, since %s (%s)", title, state, inc.Symptom.Ready, inc.Symptom.Desired, clock(inc.Since), ago(inc.Since, now))
	}
	for i, path := range inc.Impact {
		p.row(first(i, "impact"), "%s", path)
	}
	p.row("symptom", "%s", inc.Symptom.Summary)

	if len(inc.Causes) == 0 {
		p.row("cause", "none found: nothing about %s or what it uses changed in the last 30 minutes.", inc.Name)
		p.row("", "The cause may be outside the cluster: a dependency, the network, or traffic.")
	} else {
		top := inc.Causes[0]
		p.row(top.Confidence.String()+" cause", "%s", describe(top, now))
		for i, ev := range top.Evidence {
			p.row(first(i, "evidence"), "%s", ev)
		}
		for i, c := range inc.Causes[1:] {
			p.row(first(i, "also changed"), "%s  (%s)", describe(c, now), c.Confidence)
		}
	}
	width := 0
	for _, s := range inc.Next {
		width = max(width, len(s.Command))
	}
	for i, s := range inc.Next {
		p.row(first(i, "next"), "%-*s   # %s", width, s.Command, s.Note)
	}
	return p.err
}

// Changes renders recent changes, newest first.
func Changes(w io.Writer, changes []cause.Change, now time.Time, window time.Duration) error {
	p := &printer{w: w}
	if len(changes) == 0 {
		p.line("No changes in the last %s.", window)
		return p.err
	}
	for _, c := range changes {
		obj := c.Kind + " " + c.Name
		if c.NS != "" {
			obj = c.Kind + " " + c.NS + "/" + c.Name
		}
		author := ""
		if c.By != nil {
			author = "  by " + who(c.By)
		}
		p.line("%s  %-9s %s %s%s", clock(c.At), ago(c.At, now), obj, c.Summary, author)
	}
	p.line("")
	p.line("Showing the latest change per object. Keep kubexray running to record every change.")
	return p.err
}

func describe(c cause.Cause, now time.Time) string {
	s := c.Summary
	if c.By != nil {
		s += " by " + who(c.By)
	}
	if !c.At.IsZero() && c.Kind != cause.KindMissing {
		s += fmt.Sprintf(" at %s (%s)", clock(c.At), ago(c.At, now))
	}
	return s
}

// who names the tool behind a change, e.g. "kubectl patch" or "helm".
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

func clock(t time.Time) string { return t.Local().Format("15:04:05") }

func ago(t, now time.Time) string {
	d := now.Sub(t).Round(time.Second)
	switch {
	case d < 0:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func first(i int, label string) string {
	if i == 0 {
		return label
	}
	return ""
}

// printer writes lines and remembers the first error.
type printer struct {
	w   io.Writer
	err error
}

func (p *printer) line(format string, args ...any) {
	if p.err == nil {
		_, p.err = fmt.Fprintf(p.w, format+"\n", args...)
	}
}

func (p *printer) row(label, format string, args ...any) {
	p.line("  %-17s %s", label, fmt.Sprintf(format, args...)) // fits "very likely cause"
}
