package recorder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/kubexray/kubexray/internal/graph"
)

var t0 = time.Date(2026, 10, 22, 14, 0, 0, 0, time.UTC)

func at(s float64) time.Time { return t0.Add(time.Duration(s * float64(time.Second))) }

func node(id string) graph.Node { return graph.Node{ID: id, Kind: "Pod", Name: id} }

func snapshot(seq uint64, ts time.Time, ids ...string) graph.Message {
	nodes := make([]graph.Node, 0, len(ids))
	for _, id := range ids {
		nodes = append(nodes, node(id))
	}
	return graph.Message{V: 1, Type: graph.MessageSnapshot, Seq: seq, TS: ts, Nodes: nodes, Edges: []graph.Edge{}}
}

func delta(seq uint64, ts time.Time, ops ...graph.Op) graph.Message {
	return graph.Message{V: 1, Type: graph.MessageDelta, Seq: seq, TS: ts, Ops: ops}
}

func add(id string) graph.Op {
	n := node(id)
	return graph.Op{Op: graph.OpUpsertNode, Node: &n}
}

func remove(id string) graph.Op { return graph.Op{Op: graph.OpRemoveNode, ID: id} }

func enc(t *testing.T, m graph.Message) []byte {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func keyframe(t *testing.T, r *Recorder, m graph.Message) {
	t.Helper()
	if err := r.Keyframe(m, enc(t, m)); err != nil {
		t.Fatalf("Keyframe(seq %d): %v", m.Seq, err)
	}
}

func record(t *testing.T, r *Recorder, m graph.Message) {
	t.Helper()
	if err := r.Record(m, enc(t, m)); err != nil {
		t.Fatalf("Record(seq %d): %v", m.Seq, err)
	}
}

func ids(m graph.Message) []string {
	out := make([]string, 0, len(m.Nodes))
	for _, n := range m.Nodes {
		out = append(out, n.ID)
	}
	return out
}

func assertAt(t *testing.T, r *Recorder, when time.Time, want ...string) {
	t.Helper()
	got, err := r.At(when)
	if err != nil {
		t.Fatalf("At(%v): %v", when.Sub(t0), err)
	}
	if fmt.Sprint(ids(got)) != fmt.Sprint(want) {
		t.Errorf("At(+%v) = %v, want %v", when.Sub(t0), ids(got), want)
	}
}

func TestAtRebuildsThePast(t *testing.T) {
	t.Parallel()
	r := New(DefaultOptions)
	keyframe(t, r, snapshot(0, at(0), "a"))
	record(t, r, delta(1, at(1), add("b")))
	record(t, r, delta(2, at(2), remove("a")))

	assertAt(t, r, at(0), "a")
	assertAt(t, r, at(1.5), "a", "b")
	assertAt(t, r, at(2), "b")
	assertAt(t, r, at(99), "b")
	if _, err := r.At(at(-1)); !errors.Is(err, ErrNotRecorded) {
		t.Errorf("At(before history) = %v, want ErrNotRecorded", err)
	}
}

func TestAtUsesTheNearestKeyframe(t *testing.T) {
	t.Parallel()
	r := New(DefaultOptions)
	keyframe(t, r, snapshot(0, at(0), "a"))
	record(t, r, delta(1, at(1), add("b")))
	keyframe(t, r, snapshot(1, at(40), "a", "b"))
	record(t, r, delta(2, at(41), remove("b")))

	assertAt(t, r, at(1), "a", "b")
	assertAt(t, r, at(40.5), "a", "b")
	assertAt(t, r, at(41), "a")

	msgs, err := r.Messages(at(40.5), at(41))
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Errorf("Messages from the second keyframe = %d messages, want keyframe + 1 delta", len(msgs))
	}
}

func TestRecordValidation(t *testing.T) {
	t.Parallel()
	r := New(DefaultOptions)
	if err := r.Record(delta(1, at(1)), nil); err == nil {
		t.Error("Record before any keyframe succeeded")
	}
	keyframe(t, r, snapshot(5, at(0)))
	if err := r.Record(delta(7, at(1)), nil); !errors.Is(err, graph.ErrGap) {
		t.Errorf("Record with a gap = %v, want ErrGap", err)
	}
	if err := r.Record(snapshot(6, at(1)), nil); err == nil {
		t.Error("Record accepted a snapshot")
	}
	if err := r.Keyframe(delta(6, at(1)), nil); err == nil {
		t.Error("Keyframe accepted a delta")
	}
	record(t, r, delta(6, at(1)))
	if err := r.Keyframe(snapshot(5, at(2)), nil); !errors.Is(err, graph.ErrGap) {
		t.Errorf("stale keyframe = %v, want ErrGap", err)
	}
}

func TestNeedsKeyframe(t *testing.T) {
	t.Parallel()
	r := New(DefaultOptions)
	if !r.NeedsKeyframe(at(0)) {
		t.Error("empty recorder must ask for a keyframe")
	}
	keyframe(t, r, snapshot(0, at(0)))
	if r.NeedsKeyframe(at(60)) {
		t.Error("quiet cluster asked for a redundant keyframe")
	}
	record(t, r, delta(1, at(10), add("a")))
	if r.NeedsKeyframe(at(29)) {
		t.Error("asked for a keyframe before KeyframeEvery")
	}
	if !r.NeedsKeyframe(at(30)) {
		t.Error("no keyframe after KeyframeEvery with changes")
	}
}

func TestPruneByWindow(t *testing.T) {
	t.Parallel()
	r := New(Options{Window: time.Minute, MaxBytes: 1 << 30, KeyframeEvery: 30 * time.Second})
	keyframe(t, r, snapshot(0, at(0), "a"))
	record(t, r, delta(1, at(1), add("b")))
	keyframe(t, r, snapshot(1, at(40), "a", "b"))
	record(t, r, delta(2, at(41), add("c")))
	keyframe(t, r, snapshot(2, at(80), "a", "b", "c"))
	record(t, r, delta(3, at(130), add("d")))

	// The window starts at +70s; the segment starting at +40s still covers it.
	from, to, ok := r.Bounds()
	if !ok || !from.Equal(at(40)) || !to.Equal(at(130)) {
		t.Errorf("Bounds = %v..%v, want +40s..+130s", from.Sub(t0), to.Sub(t0))
	}
	if _, err := r.At(at(1)); !errors.Is(err, ErrNotRecorded) {
		t.Errorf("At(pruned time) = %v, want ErrNotRecorded", err)
	}
	assertAt(t, r, at(70), "a", "b", "c")
}

func TestPruneByMemoryBudget(t *testing.T) {
	t.Parallel()
	r := New(Options{Window: time.Hour, MaxBytes: 600, KeyframeEvery: time.Second})
	var seq uint64
	for i := range 20 {
		keyframe(t, r, snapshot(seq, at(float64(i*2)), "a"))
		seq++
		record(t, r, delta(seq, at(float64(i*2+1)), add(fmt.Sprintf("n%d", i))))
	}
	if r.Size() > 600+400 { // at most one segment over budget, which is never dropped
		t.Errorf("Size = %d, want around the 600 byte budget", r.Size())
	}
	from, _, _ := r.Bounds()
	if !from.After(at(0)) {
		t.Error("oldest history was not dropped")
	}
	if _, err := r.At(from); err != nil {
		t.Errorf("At(oldest kept moment) = %v", err)
	}
}

func TestBoundsOfEmptyRecorder(t *testing.T) {
	t.Parallel()
	if _, _, ok := New(DefaultOptions).Bounds(); ok {
		t.Error("empty recorder reported bounds")
	}
}

func TestConcurrentReadersAndWriter(t *testing.T) {
	t.Parallel()
	r := New(Options{Window: time.Minute, MaxBytes: 1 << 20, KeyframeEvery: time.Second})
	keyframe(t, r, snapshot(0, at(0), "a"))

	var wg sync.WaitGroup
	done := make(chan struct{})
	for range 4 {
		wg.Go(func() {
			for {
				select {
				case <-done:
					return
				default:
					_, to, _ := r.Bounds()
					if _, err := r.At(to); err != nil {
						t.Errorf("At: %v", err)
						return
					}
				}
			}
		})
	}
	for i := 1; i <= 300; i++ {
		ts := at(float64(i) / 2)
		record(t, r, delta(uint64(i), ts, add(fmt.Sprintf("n%d", i%10))))
		if r.NeedsKeyframe(ts) {
			snap, err := r.At(ts)
			if err != nil {
				t.Fatal(err)
			}
			keyframe(t, r, snap)
		}
	}
	close(done)
	wg.Wait()
}

// The pump end to end: a real graph, a short interval, and a recorder that
// must be able to rebuild what the graph holds.
func TestPumpRecordsAndEmits(t *testing.T) {
	t.Parallel()
	g := graph.New()
	g.UpsertNode(node("a"))
	r := New(Options{Window: time.Hour, MaxBytes: 1 << 20, KeyframeEvery: 20 * time.Millisecond})

	var mu sync.Mutex
	var emitted []graph.Message
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- Pump(ctx, g, r, 5*time.Millisecond, func(m graph.Message, raw []byte) error {
			var back graph.Message
			if err := json.Unmarshal(raw, &back); err != nil || back.Seq != m.Seq {
				t.Errorf("raw doesn't match message %d: %v", m.Seq, err)
			}
			mu.Lock()
			emitted = append(emitted, m)
			mu.Unlock()
			return nil
		})
	}()

	time.Sleep(20 * time.Millisecond)
	g.UpsertNode(node("b"))
	time.Sleep(40 * time.Millisecond) // long enough for a delta and a keyframe
	g.RemoveNode("a")
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Pump: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(emitted) < 3 || emitted[0].Type != graph.MessageSnapshot {
		t.Fatalf("emitted %d messages, want a snapshot followed by deltas", len(emitted))
	}
	for i, m := range emitted[1:] {
		if m.Type != graph.MessageDelta || m.Seq != emitted[i].Seq+1 {
			t.Errorf("message %d: type %s seq %d after seq %d", i+1, m.Type, m.Seq, emitted[i].Seq)
		}
	}

	_, to, _ := r.Bounds()
	got, err := r.At(to)
	if err != nil {
		t.Fatal(err)
	}
	if want := ids(g.Snapshot()); !slices.Equal(ids(got), want) {
		t.Errorf("recorder rebuilt %v, graph has %v", ids(got), want)
	}
	if !slices.Equal(ids(got), []string{"b"}) {
		t.Errorf("final state = %v, want [b]", ids(got))
	}
}

func TestPumpStopsOnEmitError(t *testing.T) {
	t.Parallel()
	boom := errors.New("client went away")
	err := Pump(t.Context(), graph.New(), New(DefaultOptions), time.Millisecond, func(graph.Message, []byte) error { return boom })
	if !errors.Is(err, boom) {
		t.Errorf("Pump = %v, want the emit error", err)
	}
}
