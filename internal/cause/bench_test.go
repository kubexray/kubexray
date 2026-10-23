package cause_test

import (
	"testing"
	"time"

	"github.com/kubexray/kubexray/internal/cause"
	"github.com/kubexray/kubexray/internal/synthetic"
)

// 1,000 pods: 10 namespaces × 10 apps × 10 pods, every 7th app failing.
var thousand = synthetic.Size{Namespaces: 10, AppsPerNamespace: 10, PodsPerApp: 10, FailingEvery: 7}

func BenchmarkAnalyze1000Pods(b *testing.B) {
	now := time.Now()
	st := synthetic.Cluster(thousand, now)
	opts := cause.DefaultOptions(now)
	for b.Loop() {
		if incs := cause.Analyze(st, opts); len(incs) != 14 {
			b.Fatalf("incidents = %d, want 14", len(incs))
		}
	}
}

func BenchmarkExplain1000Pods(b *testing.B) {
	now := time.Now()
	st := synthetic.Cluster(thousand, now)
	opts := cause.DefaultOptions(now)
	for b.Loop() {
		if _, err := cause.Explain(st, "Deployment/team-00/svc-06", opts); err != nil {
			b.Fatal(err)
		}
	}
}
