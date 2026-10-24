package recorder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/kubexray/kubexray/internal/graph"
)

// Emit receives every message the pump produces, already encoded.
type Emit func(msg graph.Message, raw []byte) error

// Pump drives the stream: every interval it flushes the graph into one delta,
// records it, and hands it to emit. It starts by emitting a snapshot of the
// graph as it is, and takes keyframes as the recorder asks for them. It runs
// until ctx is cancelled or emit fails.
//
// The pump must be the graph's only caller of Flush: anything else that
// wants the stream (a WebSocket server, for instance) subscribes through emit.
//
// History is best effort: if recording fails, the history is reset and
// rebuilt from the next keyframe, and the live stream carries on.
func Pump(ctx context.Context, g *graph.Graph, rec *Recorder, interval time.Duration, log *slog.Logger, emit Emit) error {
	p := pump{g: g, rec: rec, log: log, emit: emit}
	if err := p.start(); err != nil {
		return err
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-ticker.C:
			if err := p.step(now); err != nil {
				return err
			}
		}
	}
}

type pump struct {
	g    *graph.Graph
	rec  *Recorder
	log  *slog.Logger
	emit Emit
}

// start emits the first snapshot and records it as the first keyframe.
func (p *pump) start() error {
	_, _, snap := p.g.FlushAndSnapshot() // fold everything synced so far into it
	raw, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("encoding snapshot: %w", err)
	}
	p.history(p.rec.Keyframe(snap, raw))
	return p.emit(snap, raw)
}

func (p *pump) step(now time.Time) error {
	var (
		msg      graph.Message
		changed  bool
		snap     graph.Message
		keyframe = p.rec.NeedsKeyframe(now)
	)
	if keyframe {
		msg, changed, snap = p.g.FlushAndSnapshot()
	} else {
		msg, changed = p.g.Flush()
	}

	if changed {
		raw, err := json.Marshal(msg)
		if err != nil {
			return fmt.Errorf("encoding delta: %w", err)
		}
		p.history(p.rec.Record(msg, raw))
		if err := p.emit(msg, raw); err != nil {
			return err
		}
	}
	if keyframe {
		raw, err := json.Marshal(snap)
		if err != nil {
			return fmt.Errorf("encoding keyframe: %w", err)
		}
		p.history(p.rec.Keyframe(snap, raw))
	}
	p.rec.Prune(now)
	return nil
}

// history handles a recording error without stopping the live stream.
func (p *pump) history(err error) {
	if err == nil {
		return
	}
	if errors.Is(err, ErrNotSaved) {
		p.log.Error("history isn't being saved to disk; it's kept in memory", "err", err)
		return
	}
	p.log.Warn("history broke; recording continues from the next keyframe", "err", err)
	p.rec.Break()
}
