package cluster

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/kubexray/kubexray/internal/graph"
)

var day0 = time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)

// minutes builds a history of n minutes before end, with values from f.
func minutes(end time.Time, n int, f func(i int, at time.Time) sample) history {
	var h history
	for i := n; i > 0; i-- {
		at := end.Add(-time.Duration(i) * time.Minute)
		s := f(i, at)
		s.at, s.rps = at, max(s.rps, 5)
		h = h.add(s)
	}
	return h
}

// wobble is a small, repeating variation around v.
func wobble(i int, v, by float64) float64 { return v + by*float64(i%5-2)/2 }

func TestBaselineEachAppAgainstItself(t *testing.T) {
	t.Parallel()
	now := day0.Add(12 * time.Hour)
	steady := minutes(now, 180, func(i int, _ time.Time) sample { return sample{errorPct: wobble(i, 0.3, 0.1)} })
	flaky := minutes(now, 180, func(i int, _ time.Time) sample { return sample{errorPct: wobble(i, 4, 1)} })
	for _, c := range []struct {
		name  string
		h     history
		value float64
		want  bool
	}{
		{"steady app at 8%", steady, 8, true},
		{"steady app at 0.6%: under the floor", steady, 0.6, false},
		{"flaky app at 6%: its usual wobble", flaky, 6, false},
		{"flaky app at 15%", flaky, 15, true},
		{"no history: 4% under the fixed 5%", nil, 4, false},
		{"no history: 6% over the fixed 5%", nil, 6, true},
	} {
		got, _, normal, _ := c.h.judge(now, sample{at: now, rps: 10, errorPct: c.value}, Normal)
		if got != c.want {
			t.Errorf("%s: break = %v, normal %+v", c.name, got, normal)
		}
	}
	if _, _, normal, _ := steady.judge(now, sample{rps: 10}, Normal); normal == nil || normal.low5 < 0.15 || normal.high95 > 0.45 {
		t.Errorf("steady normal range = %+v", normal)
	}
}

func TestBaselineSkipsBrokenMinutes(t *testing.T) {
	t.Parallel()
	now := day0.Add(12 * time.Hour)
	// The last hour was an outage; it mustn't become normal.
	h := minutes(now, 240, func(i int, _ time.Time) sample {
		if i <= 60 {
			return sample{errorPct: 40, broken: true}
		}
		return sample{errorPct: wobble(i, 0.3, 0.1)}
	})
	if got, _, _, _ := h.judge(now, sample{rps: 10, errorPct: 40}, Normal); !got {
		t.Error("a continuing outage stopped being a break")
	}
}

func TestBaselineByHourOfDay(t *testing.T) {
	t.Parallel()
	// Four days where 9 o'clock runs a batch with 2% errors; 0.2% otherwise.
	now := day0.Add(4*24*time.Hour + 9*time.Hour + 30*time.Minute)
	h := minutes(now, 4*24*60, func(i int, at time.Time) sample {
		if at.Hour() == 9 {
			return sample{errorPct: wobble(i, 2, 0.4)}
		}
		return sample{errorPct: wobble(i, 0.2, 0.1)}
	})
	if got, _, _, _ := h.judge(now, sample{rps: 10, errorPct: 2.4}, Normal); got {
		t.Error("9 o'clock's usual errors broke")
	}
	at3 := now.Add(-6 * time.Hour)
	if got, _, _, _ := h.judge(at3, sample{rps: 10, errorPct: 2.4}, Normal); !got {
		t.Error("the same errors at 3 o'clock didn't break")
	}
}

func TestLatencyFloors(t *testing.T) {
	t.Parallel()
	now := day0.Add(12 * time.Hour)
	fast := minutes(now, 120, func(i int, _ time.Time) sample { return sample{p99: wobble(i, 0.05, 0.01)} })
	if _, slow, _, _ := fast.judge(now, sample{rps: 10, p99: 0.15}, Normal); slow {
		t.Error("150ms broke: under the 200ms floor")
	}
	if _, slow, _, _ := fast.judge(now, sample{rps: 10, p99: 0.8}, Normal); !slow {
		t.Error("800ms against a usual 50ms didn't break")
	}
	quiet := sample{rps: 0.1, errorPct: 50, p99: 9}
	if e, s, _, _ := fast.judge(now, quiet, Normal); e || s {
		t.Error("an app with almost no traffic broke")
	}
}

func TestSensitivities(t *testing.T) {
	t.Parallel()
	now := day0.Add(12 * time.Hour)
	h := minutes(now, 180, func(i int, _ time.Time) sample { return sample{errorPct: wobble(i, 2, 0.5)} })
	cur := sample{rps: 10, errorPct: 4.2}
	strict, _, _, _ := h.judge(now, cur, Strict)
	calm, _, _, _ := h.judge(now, cur, Calm)
	if !strict || calm {
		t.Errorf("4.2%% against a usual 2%%: strict %v, calm %v", strict, calm)
	}
	if _, err := ParseSensitivity("loud"); err == nil {
		t.Error("parsed a sensitivity that doesn't exist")
	}
}

func TestSeedTraffic(t *testing.T) {
	t.Parallel()
	var msgs [][]byte
	for i := range 90 {
		ts := day0.Add(time.Duration(i) * time.Minute)
		n := graph.Node{ID: "Traffic/shop/deployment.api", Kind: graph.KindTraffic, Traffic: &graph.Traffic{RPS: 5, ErrorPct: 0.3}}
		raw, _ := json.Marshal(graph.Message{Type: graph.MessageDelta, TS: ts, Ops: []graph.Op{{Op: graph.OpUpsertNode, Node: &n}}})
		msgs = append(msgs, raw)
		if i == 50 { // a snapshot in the same minute: not a second sample
			raw, _ := json.Marshal(graph.Message{Type: graph.MessageSnapshot, TS: ts.Add(time.Second), Nodes: []graph.Node{n}})
			msgs = append(msgs, raw)
		}
	}
	walk := func(fn func([]byte) error) error {
		for _, m := range msgs {
			if err := fn(m); err != nil {
				return err
			}
		}
		return nil
	}
	learned, err := LearnTraffic(walk)
	if err != nil {
		t.Fatal(err)
	}
	// The watcher has been running a while already: what it read itself
	// stays, after what was recorded before it.
	const id = "Traffic/shop/deployment.api"
	first := day0.Add(89*time.Minute + 20*time.Second) // the last recorded minute, read again live
	w := &Watcher{now: func() time.Time { return first.Add(time.Minute) }, history: map[string]history{
		id: {{at: first, rps: 7}, {at: first.Add(time.Minute), rps: 8}},
	}}
	w.SeedTraffic(learned)
	h := w.history[id]
	if len(h) != 91 {
		t.Fatalf("seeded %d minutes, want the 89 recorded before the watcher started and its own 2", len(h))
	}
	if !h[88].at.Before(h[89].at) || h[89].rps != 7 || h[90].rps != 8 {
		t.Errorf("the watcher's own minutes didn't stay last: %+v", h[87:])
	}
}

func TestSeedTrafficDropsAppsLongGone(t *testing.T) {
	t.Parallel()
	gone := graph.Node{ID: "Traffic/shop/deployment.old", Kind: graph.KindTraffic, Traffic: &graph.Traffic{RPS: 5}}
	raw, _ := json.Marshal(graph.Message{Type: graph.MessageSnapshot, TS: day0, Nodes: []graph.Node{gone}})
	learned, err := LearnTraffic(func(fn func([]byte) error) error { return fn(raw) })
	if err != nil {
		t.Fatal(err)
	}
	w := &Watcher{now: func() time.Time { return day0.Add(keepHistory + time.Hour) }, history: map[string]history{}}
	w.SeedTraffic(learned)
	if _, ok := w.history[gone.ID]; ok {
		t.Error("seeded a baseline for an app with no traffic in over a week")
	}
}

func TestGoneAppsLoseTheirHistory(t *testing.T) {
	t.Parallel()
	w := newSignalWatcher()
	now := day0.Add(30 * 24 * time.Hour)
	w.now = func() time.Time { return now }
	w.history["Traffic/shop/deployment.gone"] = history{{at: now.Add(-keepHistory - time.Minute)}}
	w.history["Traffic/shop/deployment.quiet"] = history{{at: now.Add(-time.Hour)}}
	w.readTraffic(t.Context(), nil, nil) // Prometheus has neither of them this minute
	if _, ok := w.history["Traffic/shop/deployment.gone"]; ok {
		t.Error("an app gone for over a week kept its traffic history")
	}
	if _, ok := w.history["Traffic/shop/deployment.quiet"]; !ok {
		t.Error("an app missing for an hour lost its baseline")
	}
}
