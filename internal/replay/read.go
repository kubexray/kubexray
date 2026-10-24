package replay

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/kubexray/kubexray/internal/cause"
	"github.com/kubexray/kubexray/internal/feed"
	"github.com/kubexray/kubexray/internal/graph"
)

// File is a replay read back and validated.
type File struct {
	Header    Header
	Messages  []graph.Message // a snapshot, then deltas
	Incidents []cause.IncidentsMessage
	Changes   []cause.Change
	Feed      []feed.Set
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
		case MessageFeed:
			var m FeedMessage
			if err := json.Unmarshal(raw, &m); err != nil {
				return nil, fmt.Errorf("replay: line %d: %w", line, err)
			}
			f.Feed = m.Sets
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
