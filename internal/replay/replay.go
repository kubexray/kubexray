// Package replay writes and reads replay files (ADR-0008): a recorded time
// window as gzip-compressed NDJSON, with the cause engine's incidents computed
// at export time, so a static player can show exactly what kubexray saw.
package replay

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/kubexray/kubexray/internal/cause"
	"github.com/kubexray/kubexray/internal/graph"
)

// Ext is the replay file extension.
const Ext = ".kxr"

// Message types that only appear in replay files.
const (
	MessageHeader  = "header"
	MessageChanges = "changes"
)

// Limits for reading untrusted files (ADR-0008, section 4).
const (
	MaxUncompressed = 50 << 20
	MaxLine         = 16 << 20
)

// Header is the first line of a replay.
type Header struct {
	V          int       `json:"v"`
	Type       string    `json:"type" tstype:"\"header\""`
	From       time.Time `json:"from"`
	To         time.Time `json:"to"`
	Cluster    string    `json:"cluster"`
	Anonymized bool      `json:"anonymized"`
	Kubexray   string    `json:"kubexray"` // version that wrote it
}

// ChangesMessage is the last line: who changed what in the window.
type ChangesMessage struct {
	V       int            `json:"v"`
	Type    string         `json:"type" tstype:"\"changes\""`
	Changes []cause.Change `json:"changes"`
}

// Options for writing a replay.
type Options struct {
	From, To  time.Time
	Cluster   string
	Version   string
	Anonymize bool
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
	)
	// analyze writes the incidents at ts if they changed since last written.
	analyze := func(ts time.Time) error {
		checked = ts
		m := cause.NewIncidentsMessage(state, ts)
		key, err := json.Marshal([]any{m.Incidents, m.Groups})
		if err != nil {
			return err
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
		if anon != nil {
			anon.message(&m)
		}
		if err := catchUp(m.TS); err != nil {
			return err
		}
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

// File is a replay read back and validated.
type File struct {
	Header    Header
	Messages  []graph.Message // a snapshot, then deltas
	Incidents []cause.IncidentsMessage
	Changes   []cause.Change
}

// Read reads and validates an untrusted replay: size limits, schema version,
// message order and every operation (ADR-0008, section 4).
func Read(r io.Reader) (*File, error) { return read(r, MaxUncompressed) }

func read(r io.Reader, limit int64) (*File, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("replay: not a kubexray replay (gzip): %w", err)
	}
	limited := &io.LimitedReader{R: gz, N: limit + 1}
	tooLarge := fmt.Errorf("replay: larger than %d MiB uncompressed", limit>>20)
	sc := bufio.NewScanner(limited)
	sc.Buffer(make([]byte, 64<<10), MaxLine)

	f := &File{}
	var state *graph.State
	line := 0
	for sc.Scan() {
		if limited.N <= 0 {
			return nil, tooLarge // the last line was cut off at the limit
		}
		line++
		raw := sc.Bytes()
		var probe struct {
			V    int    `json:"v"`
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &probe); err != nil {
			return nil, fmt.Errorf("replay: line %d: %w", line, err)
		}
		if probe.V != graph.SchemaVersion {
			return nil, fmt.Errorf("replay: line %d: unsupported version %d", line, probe.V)
		}
		if line == 1 {
			if probe.Type != MessageHeader {
				return nil, errors.New("replay: the first line must be the header")
			}
			if err := json.Unmarshal(raw, &f.Header); err != nil {
				return nil, fmt.Errorf("replay: header: %w", err)
			}
			continue
		}
		switch probe.Type {
		case string(graph.MessageSnapshot), string(graph.MessageDelta):
			var m graph.Message
			if err := json.Unmarshal(raw, &m); err != nil {
				return nil, fmt.Errorf("replay: line %d: %w", line, err)
			}
			// Must start with a snapshot; every op of every delta is validated.
			if state, err = graph.Step(state, m); err != nil {
				return nil, fmt.Errorf("replay: line %d: %w", line, err)
			}
			f.Messages = append(f.Messages, m)
		case cause.MessageIncidents:
			var m cause.IncidentsMessage
			if err := json.Unmarshal(raw, &m); err != nil {
				return nil, fmt.Errorf("replay: line %d: %w", line, err)
			}
			f.Incidents = append(f.Incidents, m)
		case MessageChanges:
			var m ChangesMessage
			if err := json.Unmarshal(raw, &m); err != nil {
				return nil, fmt.Errorf("replay: line %d: %w", line, err)
			}
			f.Changes = m.Changes
		default:
			return nil, fmt.Errorf("replay: line %d: unknown message type %q", line, probe.Type)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("replay: %w", err)
	}
	if limited.N <= 0 {
		return nil, tooLarge
	}
	if line == 0 {
		return nil, errors.New("replay: empty file")
	}
	if len(f.Messages) == 0 {
		return nil, errors.New("replay: no cluster snapshot")
	}
	return f, nil
}
