package recorder

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/kubexray/kubexray/internal/graph"
)

// Emit receives every message the pump produces, already encoded.
type Emit func(msg graph.Message, raw []byte) error

// Pump drives the stream: every interval it flushes the graph into one delta,
// records it, and hands it to emit. It starts by emitting a snapshot of the
// graph as it is, and takes keyframes as the recorder asks for them. It runs
// until ctx is cancelled.
func Pump(ctx context.Context, g *graph.Graph, rec *Recorder, interval time.Duration, emit Emit) error {
	g.Flush() // fold everything synced so far into the first snapshot
	snap := g.Snapshot()
	raw, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("encoding snapshot: %w", err)
	}
	if err := rec.Keyframe(snap, raw); err != nil {
		return err
	}
	if err := emit(snap, raw); err != nil {
		return err
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-ticker.C:
			if err := step(g, rec, emit, now); err != nil {
				return err
			}
		}
	}
}

func step(g *graph.Graph, rec *Recorder, emit Emit, now time.Time) error {
	if msg, ok := g.Flush(); ok {
		raw, err := json.Marshal(msg)
		if err != nil {
			return fmt.Errorf("encoding delta: %w", err)
		}
		if err := rec.Record(msg, raw); err != nil {
			return err
		}
		if err := emit(msg, raw); err != nil {
			return err
		}
	}
	if rec.NeedsKeyframe(now) {
		// Called right after Flush, so the snapshot matches the last delta.
		snap := g.Snapshot()
		raw, err := json.Marshal(snap)
		if err != nil {
			return fmt.Errorf("encoding keyframe: %w", err)
		}
		return rec.Keyframe(snap, raw)
	}
	return nil
}
