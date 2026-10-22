package graph

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"
	"time"
)

func TestStateRejectsNonSnapshotStart(t *testing.T) {
	t.Parallel()
	if _, err := NewState(Message{Type: MessageDelta}); err == nil {
		t.Error("NewState accepted a delta")
	}
}

func TestStateDetectsGaps(t *testing.T) {
	t.Parallel()
	s, _ := NewState(Message{Type: MessageSnapshot, Seq: 5})
	if err := s.Apply(Message{Type: MessageDelta, Seq: 7}); !errors.Is(err, ErrGap) {
		t.Errorf("Apply(seq 7 after 5) = %v, want ErrGap", err)
	}
	if err := s.Apply(Message{Type: MessageDelta, Seq: 6}); err != nil {
		t.Errorf("Apply(seq 6 after 5) = %v", err)
	}
	if err := s.Apply(Message{Type: MessageSnapshot, Seq: 7}); err == nil {
		t.Error("Apply accepted a snapshot")
	}
}

// A client that applies a snapshot and then every delta must always end up
// with exactly the graph's own snapshot. Random operations shake out ordering
// and edge-visibility bugs.
func TestSnapshotPlusDeltasAlwaysConverges(t *testing.T) {
	t.Parallel()
	for seed := range uint64(20) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			t.Parallel()
			rng := rand.New(rand.NewPCG(seed, seed)) //nolint:gosec // seeded on purpose so failures are reproducible
			g := newTestGraph()
			id := func() string { return fmt.Sprintf("n%d", rng.IntN(8)) }

			mutate := func(steps int) {
				for range steps {
					switch rng.IntN(5) {
					case 0, 1:
						n := pod(id())
						n.Status.Restarts = rng.Int32N(3)
						g.UpsertNode(n)
					case 2:
						g.RemoveNode(id())
					case 3:
						g.UpsertEdge(NewEdge(EdgeOwns, id(), id()))
					case 4:
						g.RemoveEdge(NewEdge(EdgeOwns, id(), id()).ID)
					}
				}
			}

			mutate(20)
			g.Flush()
			client, err := NewState(g.Snapshot())
			if err != nil {
				t.Fatal(err)
			}
			for range 30 {
				mutate(rng.IntN(6))
				if msg, ok := g.Flush(); ok {
					if err := client.Apply(msg); err != nil {
						t.Fatal(err)
					}
				}
			}

			want, got := g.Snapshot(), client.Snapshot()
			if !reflect.DeepEqual(got.Nodes, want.Nodes) || !reflect.DeepEqual(got.Edges, want.Edges) || got.Seq != want.Seq {
				t.Errorf("client diverged\n got %d nodes %d edges seq %d\nwant %d nodes %d edges seq %d",
					len(got.Nodes), len(got.Edges), got.Seq, len(want.Nodes), len(want.Edges), want.Seq)
			}
		})
	}
}

func TestStateSnapshotOfEmptyState(t *testing.T) {
	t.Parallel()
	s, _ := NewState(Message{Type: MessageSnapshot, TS: time.Unix(0, 0)})
	snap := s.Snapshot()
	if snap.Nodes == nil || snap.Edges == nil {
		t.Error("empty snapshot must have empty, non-nil lists")
	}
}
