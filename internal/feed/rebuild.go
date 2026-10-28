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
// recorded message in order (recorder.Walk). Fill a fresh tracker and Seed
// the live one with it: rebuilding takes a while, and the live feed goes on.
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
		// The cluster went quiet after the last message: judge it as it
		// stood then, as the live hub would have, before this one changes
		// it. Otherwise an app that recovered and stayed quiet looks broken
		// until the next change, or the next restart.
		if state != nil && assessed.Before(state.TS) && !m.TS.Before(state.TS.Add(assessEvery)) {
			assess(state.TS.Add(assessEvery))
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
		made := t.made()
		t.Observe(state, m)
		// New changes: note the incidents going on as they were made, which
		// the live hub knows to the second, so a fix gets its credit.
		if t.made() != made {
			t.during(made, cause.Analyze(state, cause.DefaultOptions(m.TS)))
		}
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
	// Judge what was recorded as of the last recorded moment: the live hub
	// takes over from there, so a rollout in flight at shutdown isn't
	// declared stuck by the gap.
	if state != nil {
		assess(state.TS)
	}
	return nil
}
