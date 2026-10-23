package synthetic

import (
	"testing"
	"time"
)

func TestClusterSize(t *testing.T) {
	t.Parallel()
	s := Size{Namespaces: 2, AppsPerNamespace: 3, PodsPerApp: 4, FailingEvery: 2}
	st := Cluster(s, time.Now())
	pods := 0
	for _, n := range st.Nodes {
		if n.Kind == "Pod" {
			pods++
		}
	}
	if pods != s.Pods() || s.Pods() != 24 {
		t.Errorf("pods = %d, want %d", pods, s.Pods())
	}
}
