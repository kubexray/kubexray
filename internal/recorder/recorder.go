// Package recorder keeps a rolling history of the cluster graph so it can be
// rewound to any recent moment (the "flight recorder").
//
// History is stored as segments: a keyframe (a full snapshot) followed by the
// deltas after it, as JSON in the wire format of ADR-0003. Rebuilding the
// cluster at time t means decoding one keyframe and applying the deltas up to
// t. Old segments are dropped once they fall out of the retention window or
// the memory budget.
package recorder

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/kubexray/kubexray/internal/graph"
)

// ErrNotRecorded is returned for moments outside the recorded history.
var ErrNotRecorded = errors.New("recorder: no history for that time")

// Options configure retention.
type Options struct {
	Window        time.Duration // how far back history is kept
	MaxBytes      int           // memory budget for encoded messages
	KeyframeEvery time.Duration // how often to start a new segment while things change
}

// DefaultOptions keep an hour of history in at most 256 MiB, with a keyframe
// every 30 seconds (ADR-0003).
var DefaultOptions = Options{Window: time.Hour, MaxBytes: 256 << 20, KeyframeEvery: 30 * time.Second}

type entry struct {
	seq uint64
	ts  time.Time
	raw []byte // the encoded message, never modified after recording
}

type segment struct {
	keyframe entry
	deltas   []entry
	bytes    int
}

func (s *segment) lastSeq() uint64 {
	if n := len(s.deltas); n > 0 {
		return s.deltas[n-1].seq
	}
	return s.keyframe.seq
}

// Recorder is safe for concurrent use: one writer (the pump) and any number
// of readers.
type Recorder struct {
	opts Options

	mu       sync.RWMutex
	segments []*segment
	bytes    int
}

// New returns an empty recorder.
func New(opts Options) *Recorder {
	return &Recorder{opts: opts}
}

// Keyframe starts a new segment with a snapshot. The snapshot must match the
// recorded deltas: its Seq is the Seq of the last delta recorded.
func (r *Recorder) Keyframe(snapshot graph.Message, raw []byte) error {
	if snapshot.Type != graph.MessageSnapshot {
		return fmt.Errorf("recorder: keyframe must be a snapshot, got %q", snapshot.Type)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if cur := r.current(); cur != nil && snapshot.Seq != cur.lastSeq() {
		return fmt.Errorf("%w: keyframe at seq %d, last recorded %d", graph.ErrGap, snapshot.Seq, cur.lastSeq())
	}
	r.segments = append(r.segments, &segment{keyframe: entry{snapshot.Seq, r.monotonic(snapshot.TS), raw}, bytes: len(raw)})
	r.bytes += len(raw)
	r.prune(snapshot.TS)
	return nil
}

// Record appends a delta. It must directly follow the last recorded message.
func (r *Recorder) Record(delta graph.Message, raw []byte) error {
	if delta.Type != graph.MessageDelta {
		return fmt.Errorf("recorder: can only record deltas, got %q", delta.Type)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cur := r.current()
	if cur == nil {
		return errors.New("recorder: record a keyframe first")
	}
	if delta.Seq != cur.lastSeq()+1 {
		return fmt.Errorf("%w: delta seq %d after %d", graph.ErrGap, delta.Seq, cur.lastSeq())
	}
	cur.deltas = append(cur.deltas, entry{delta.Seq, r.monotonic(delta.TS), raw})
	cur.bytes += len(raw)
	r.bytes += len(raw)
	r.prune(delta.TS)
	return nil
}

// NeedsKeyframe reports whether a new segment should start: there is none
// yet, or the current one has been collecting changes for KeyframeEvery. Age
// counts from the first change, so a quiet cluster doesn't get a full
// snapshot for every isolated change.
func (r *Recorder) NeedsKeyframe(now time.Time) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cur := r.current()
	return cur == nil || (len(cur.deltas) > 0 && now.Sub(cur.deltas[0].ts) >= r.opts.KeyframeEvery)
}

// Prune drops history that has fallen out of the window. Recording prunes as
// it goes; calling Prune covers quiet periods with nothing to record.
func (r *Recorder) Prune(now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prune(now)
}

// Reset discards all history; recording restarts with the next keyframe.
func (r *Recorder) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.segments, r.bytes = nil, 0
}

// monotonic keeps the history index ordered even if the wall clock steps
// back (NTP): a timestamp is never earlier than the last one recorded.
func (r *Recorder) monotonic(ts time.Time) time.Time {
	cur := r.current()
	if cur == nil {
		return ts
	}
	last := cur.keyframe.ts
	if n := len(cur.deltas); n > 0 {
		last = cur.deltas[n-1].ts
	}
	if ts.Before(last) {
		return last
	}
	return ts
}

func (r *Recorder) current() *segment {
	if len(r.segments) == 0 {
		return nil
	}
	return r.segments[len(r.segments)-1]
}

// prune drops whole segments that are no longer needed: the oldest segment
// goes once the next one starts before the retention window, or while the
// memory budget is exceeded. The newest segment is always kept.
func (r *Recorder) prune(now time.Time) {
	cutoff := now.Add(-r.opts.Window)
	for len(r.segments) > 1 {
		if !r.segments[1].keyframe.ts.After(cutoff) || r.bytes > r.opts.MaxBytes {
			r.bytes -= r.segments[0].bytes
			r.segments[0] = nil
			r.segments = r.segments[1:]
			continue
		}
		break
	}
}

// Bounds returns the first and last recorded moments.
func (r *Recorder) Bounds() (from, to time.Time, ok bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.segments) == 0 {
		return time.Time{}, time.Time{}, false
	}
	last := r.current()
	to = last.keyframe.ts
	if n := len(last.deltas); n > 0 {
		to = last.deltas[n-1].ts
	}
	return r.segments[0].keyframe.ts, to, true
}

// Size returns the memory used by recorded messages, in bytes.
func (r *Recorder) Size() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.bytes
}

// At rebuilds the cluster as it was at t, as a snapshot message.
func (r *Recorder) At(t time.Time) (graph.Message, error) {
	msgs, err := r.Messages(t, t)
	if err != nil {
		return graph.Message{}, err
	}
	var state *graph.State
	for i, raw := range msgs {
		var m graph.Message
		if err := json.Unmarshal(raw, &m); err != nil {
			return graph.Message{}, fmt.Errorf("recorder: decoding message: %w", err)
		}
		if i == 0 {
			state, err = graph.NewState(m)
		} else {
			err = state.Apply(m)
		}
		if err != nil {
			return graph.Message{}, err
		}
	}
	return state.Snapshot(), nil
}

// Messages returns the encoded messages needed to replay from..to: the last
// keyframe at or before from, then every delta up to and including to. The
// returned slices must not be modified.
func (r *Recorder) Messages(from, to time.Time) ([][]byte, error) {
	if to.Before(from) {
		return nil, fmt.Errorf("recorder: range ends (%v) before it starts (%v)", to, from)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	start := -1
	for i, s := range r.segments {
		if s.keyframe.ts.After(from) {
			break
		}
		start = i
	}
	if start < 0 {
		return nil, ErrNotRecorded
	}

	out := [][]byte{r.segments[start].keyframe.raw}
	for _, s := range r.segments[start:] {
		for _, d := range s.deltas {
			if d.ts.After(to) {
				return out, nil
			}
			out = append(out, d.raw)
		}
	}
	return out, nil
}
