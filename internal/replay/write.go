package replay

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/kubexray/kubexray/internal/cause"
	"github.com/kubexray/kubexray/internal/feed"
	"github.com/kubexray/kubexray/internal/graph"
	"github.com/kubexray/kubexray/internal/redact"
)

// Options for writing a replay.
type Options struct {
	From, To  time.Time
	Cluster   string
	Version   string
	Anonymize bool
	// Values keeps the recorded values (ADR-0017). Anonymized replays
	// never have them.
	Values bool
	// Allow, when set, keeps values only in the namespaces it allows: those
	// the person exporting may read (ADR-0020).
	Allow func(ns string) bool
}

// ErrTooLarge is returned when a window doesn't fit in a replay that players
// will accept; pick a shorter window.
var ErrTooLarge = fmt.Errorf("replay: the window is larger than %d MiB uncompressed; pick a shorter one", MaxUncompressed>>20)

// recheck is how often incidents are re-analyzed while nothing changes, so
// problems that outlast the grace period appear on time (as live, ADR-0007).
const recheck = 5 * time.Second

// Write writes a replay of msgs, the recorder's messages for the window (a
// keyframe, then deltas), to w. It stops early if ctx is cancelled.
func Write(ctx context.Context, w io.Writer, msgs [][]byte, opts Options) error {
	gz := gzip.NewWriter(w)
	counted := &countingWriter{w: gz}
	enc := json.NewEncoder(counted)
	emit := func(v any) error {
		if err := enc.Encode(v); err != nil {
			return err
		}
		if counted.n > MaxUncompressed {
			return ErrTooLarge
		}
		return nil
	}

	cluster := opts.Cluster
	var anon *anonymizer
	if opts.Anonymize {
		anon = newAnonymizer()
		cluster = "cluster"
	}
	if err := emit(Header{V: graph.SchemaVersion, Type: MessageHeader, From: opts.From, To: opts.To, Cluster: cluster, Anonymized: opts.Anonymize, Kubexray: opts.Version}); err != nil {
		return err
	}

	var (
		state    *graph.State
		checked  time.Time
		lastSaid []byte
		tracker  = feed.New(feed.MaxSets)
		assessed time.Time
	)
	// analyze writes the incidents at ts if they changed since last written.
	analyze := func(ts time.Time) error {
		checked = ts
		m := cause.NewIncidentsMessage(state, ts)
		key, err := json.Marshal([]any{m.Incidents, m.Groups})
		if err != nil {
			return err
		}
		// Verdicts follow incidents, and rollouts settle: judge on changes
		// and every recheck, not on every message.
		if changed := !bytes.Equal(key, lastSaid); changed || ts.Sub(assessed) >= recheck {
			tracker.Assess(state, m.Incidents, ts)
			assessed = ts
		}
		if bytes.Equal(key, lastSaid) {
			return nil
		}
		lastSaid = key
		return emit(m)
	}
	// catchUp re-analyzes through a quiet stretch, every recheck, until t.
	catchUp := func(t time.Time) error {
		for next := checked.Add(recheck); state != nil && next.Before(t); next = checked.Add(recheck) {
			if err := analyze(next); err != nil {
				return err
			}
		}
		return nil
	}

	for i, raw := range msgs {
		if err := ctx.Err(); err != nil {
			return err
		}
		var m graph.Message
		if err := json.Unmarshal(raw, &m); err != nil {
			return fmt.Errorf("replay: decoding message %d: %w", i, err)
		}
		switch {
		case !opts.Values || anon != nil:
			graph.StripValues(&m)
		case opts.Allow != nil:
			graph.StripValuesWhere(&m, func(n graph.Node) bool { return !opts.Allow(n.Namespace) })
		}
		maskConfig(&m)
		if anon != nil {
			anon.message(&m)
		}
		if err := catchUp(m.TS); err != nil {
			return err
		}
		tracker.Observe(state, m)
		var err error
		if state, err = graph.Step(state, m); err != nil {
			return fmt.Errorf("replay: message %d: %w", i, err)
		}
		if err := emit(m); err != nil {
			return err
		}
		if err := analyze(m.TS); err != nil {
			return err
		}
	}
	if state == nil {
		return errors.New("replay: nothing recorded in that window")
	}
	if err := catchUp(opts.To); err != nil {
		return err
	}
	if err := analyze(opts.To); err != nil {
		return err
	}
	sets := tracker.Sets(opts.From)
	if sets == nil {
		sets = []feed.Set{}
	}
	if err := emit(FeedMessage{V: graph.SchemaVersion, Type: MessageFeed, Sets: sets}); err != nil {
		return err
	}
	changes := cause.Changes(state, opts.From, false)
	if changes == nil {
		changes = []cause.Change{}
	}
	if err := emit(ChangesMessage{V: graph.SchemaVersion, Type: MessageChanges, Changes: changes}); err != nil {
		return err
	}
	return gz.Close()
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// maskConfig hides ConfigMap values that look like credentials. kubexray
// shows ConfigMaps in full inside the cluster; a replay leaves it, so it
// masks them as it does every other value (ADR-0021).
func maskConfig(m *graph.Message) {
	for i := range m.Nodes {
		maskData(&m.Nodes[i])
	}
	for i, op := range m.Ops {
		if op.Node != nil && op.Node.Kind == "ConfigMap" {
			n := *op.Node
			maskData(&n)
			m.Ops[i].Node = &n
		}
	}
}

func maskData(n *graph.Node) {
	if n.Kind != "ConfigMap" || len(n.Data) == 0 {
		return
	}
	data := make(map[string]graph.Value, len(n.Data))
	for k, v := range n.Data {
		switch {
		case v.Text == "":
		case redact.SensitiveName(k):
			v.Text, v.Masked = "", "name"
		default:
			if t, hid := redact.Text(v.Text); hid {
				v.Text = t
				if v.Masked == "" {
					v.Masked = "value"
				}
			}
		}
		data[k] = v
	}
	n.Data = data
}
