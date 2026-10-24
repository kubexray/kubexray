package feed

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/kubexray/kubexray/internal/cause"
	"github.com/kubexray/kubexray/internal/graph"
)

// assessEvery is how often Rebuild judges verdicts in recorded time; live,
// the hub does it with every incident analysis.
const assessEvery = 30 * time.Second

// Rebuild fills a tracker from recorded history, so a restarted kubexray
// keeps its feed (ADR-0012, section 1). walk calls its function with every
// recorded message in order (recorder.Walk).
func Rebuild(t *Tracker, walk func(fn func(raw []byte) error) error) error {
	var (
		state    *graph.State
		assessed time.Time
	)
	assess := func(ts time.Time) {
		if state != nil {
			t.Assess(state, cause.Analyze(state, cause.DefaultOptions(ts)), ts)
			assessed = ts
		}
	}
	err := walk(func(raw []byte) error {
		var m graph.Message
		if err := json.Unmarshal(raw, &m); err != nil {
			return fmt.Errorf("feed: decoding recorded history: %w", err)
		}
		if m.Type == graph.MessageSnapshot {
			assess(m.TS)
			st, err := graph.NewState(m) // a new epoch: a new baseline
			if err != nil {
				return err
			}
			state = st
			return nil
		}
		if state == nil {
			return nil
		}
		t.Observe(state, m)
		if err := state.Apply(m); err != nil {
			return err
		}
		if m.TS.Sub(assessed) >= assessEvery {
			assess(m.TS)
		}
		return nil
	})
	if err != nil {
		return err
	}
	assess(time.Now())
	return nil
}
