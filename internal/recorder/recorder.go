// Package recorder keeps a rolling history of the cluster graph so it can be
// rewound to any recent moment (the "flight recorder").
//
// History is stored as segments: a keyframe (a full snapshot) followed by the
// deltas after it, as JSON in the wire format of ADR-0003. Rebuilding the
// cluster at time t means decoding one keyframe and applying the deltas up to
// t. Old segments are dropped once they fall out of the retention window or
// the size budget.
//
// With a directory (ADR-0010), every completed segment is written to disk as
// gzip NDJSON and only the one being recorded stays in memory, so history
// survives restarts. Each segment stands alone, so a restart or a gap in the
// stream starts a new epoch without losing what was recorded before it.
package recorder

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kubexray/kubexray/internal/graph"
)

var (
	// ErrNotRecorded is returned for moments outside the recorded history.
	ErrNotRecorded = errors.New("recorder: no history for that time")
	// ErrTooLarge is returned by Range when the messages exceed its limit.
	ErrTooLarge = errors.New("recorder: too much history for one request")
	// ErrNotSaved is returned by Keyframe when the finished segment couldn't
	// be written to disk. Recording goes on; that segment stays in memory.
	ErrNotSaved = errors.New("recorder: history segment not saved to disk")
)

// Options configure retention.
type Options struct {
	Window        time.Duration // how far back history is kept
	MaxBytes      int           // memory budget for encoded messages (in memory only)
	KeyframeEvery time.Duration // how often to start a new segment while things change
	Dir           string        // where to keep segments on disk; empty keeps them in memory
	MaxDiskBytes  int64         // disk budget for segments (with Dir)
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
	epoch    int // segments of one epoch form a continuous sequence
	keyframe entry
	deltas   []entry
	bytes    int // memory used by its messages; 0 once on disk

	last   entry  // the last message (seq and time; raw only while in memory)
	file   string // set once written to disk; its messages are then read from there
	onDisk int64  // file size
}

func (s *segment) lastSeq() uint64 { return s.last.seq }

// Recorder is safe for concurrent use: one writer (the pump) and any number
// of readers.
type Recorder struct {
	opts Options

	mu       sync.RWMutex
	segments []*segment
	bytes    int
	disk     int64
	epoch    int
	broken   bool // the stream broke: wait for a keyframe, then start a new epoch

	cacheMu sync.Mutex
	cache   map[string]*segment // segments read back from disk, by file
}

// New returns an empty recorder that keeps history in memory.
func New(opts Options) *Recorder {
	return &Recorder{opts: opts, cache: map[string]*segment{}}
}

// Open returns a recorder that keeps history in opts.Dir, with the segments
// already there. Recording continues as a new epoch.
func Open(opts Options) (*Recorder, error) {
	if err := os.MkdirAll(opts.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("recorder: %w", err)
	}
	r := New(opts)
	names, err := filepath.Glob(filepath.Join(opts.Dir, "seg-*"+ext))
	if err != nil {
		return nil, fmt.Errorf("recorder: %w", err)
	}
	// Segments a crash left half-written are of no use.
	if tmps, err := filepath.Glob(filepath.Join(opts.Dir, "seg-*.tmp")); err == nil {
		for _, tmp := range tmps {
			_ = os.Remove(tmp)
		}
	}
	for _, name := range names {
		s, ok := parseName(name)
		if !ok {
			continue // not ours, or left half-written by a crash (those end in .tmp)
		}
		if fi, err := os.Stat(name); err == nil {
			s.onDisk = fi.Size()
		}
		r.segments = append(r.segments, s)
		r.disk += s.onDisk
		r.epoch = max(r.epoch, s.epoch)
	}
	slices.SortFunc(r.segments, func(a, b *segment) int { return a.keyframe.ts.Compare(b.keyframe.ts) })
	r.broken = len(r.segments) > 0
	r.prune(time.Now())
	return r, nil
}

const ext = ".ndjson.gz"

// Segment files are named seg-<from>-<to>-<epoch>-<firstSeq>-<lastSeq>, with
// times in Unix nanoseconds, so the index can be rebuilt without reading
// them.
func fileName(s *segment) string {
	return fmt.Sprintf("seg-%019d-%019d-%d-%d-%d%s", s.keyframe.ts.UnixNano(), s.last.ts.UnixNano(), s.epoch, s.keyframe.seq, s.last.seq, ext)
}

func parseName(path string) (*segment, bool) {
	parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "seg-"), ext), "-")
	if len(parts) != 5 || !strings.HasSuffix(path, ext) {
		return nil, false
	}
	var n [5]int64
	for i, p := range parts {
		v, err := strconv.ParseInt(p, 10, 64)
		if err != nil || v < 0 {
			return nil, false
		}
		n[i] = v
	}
	epoch, err := strconv.Atoi(parts[2])
	if err != nil {
		return nil, false
	}
	return &segment{
		epoch:    epoch,
		keyframe: entry{seq: uint64(n[3]), ts: time.Unix(0, n[0]).UTC()}, //nolint:gosec // checked non-negative above
		last:     entry{seq: uint64(n[4]), ts: time.Unix(0, n[1]).UTC()}, //nolint:gosec // checked non-negative above
		file:     path,
	}, true
}

// Keyframe starts a new segment with a snapshot. The snapshot must match the
// recorded deltas (its Seq is the Seq of the last delta recorded), unless the
// stream broke or the process restarted: then a new epoch begins.
func (r *Recorder) Keyframe(snapshot graph.Message, raw []byte) error {
	if snapshot.Type != graph.MessageSnapshot {
		return fmt.Errorf("recorder: keyframe must be a snapshot, got %q", snapshot.Type)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cur := r.current()
	switch {
	case cur == nil || r.broken:
		if cur != nil {
			r.epoch++
		}
		r.broken = false
	case snapshot.Seq != cur.lastSeq():
		return fmt.Errorf("%w: keyframe at seq %d, last recorded %d", graph.ErrGap, snapshot.Seq, cur.lastSeq())
	}
	// A disk error (full, read-only) must not stop recording: the segment
	// stays in memory, within the memory budget, and recording goes on.
	saveErr := r.persist(cur)
	e := entry{snapshot.Seq, r.monotonic(snapshot.TS), raw}
	r.segments = append(r.segments, &segment{epoch: r.epoch, keyframe: e, last: e, bytes: len(raw)})
	r.bytes += len(raw)
	r.prune(snapshot.TS)
	if saveErr != nil {
		return fmt.Errorf("%w: %w", ErrNotSaved, saveErr)
	}
	return nil
}

// Record appends a delta. It must directly follow the last recorded message.
// After a break, deltas are dropped until the next keyframe, which includes
// them.
func (r *Recorder) Record(delta graph.Message, raw []byte) error {
	if delta.Type != graph.MessageDelta {
		return fmt.Errorf("recorder: can only record deltas, got %q", delta.Type)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.broken {
		return nil
	}
	cur := r.current()
	if cur == nil {
		return errors.New("recorder: record a keyframe first")
	}
	if delta.Seq != cur.lastSeq()+1 {
		return fmt.Errorf("%w: delta seq %d after %d", graph.ErrGap, delta.Seq, cur.lastSeq())
	}
	e := entry{delta.Seq, r.monotonic(delta.TS), raw}
	cur.deltas = append(cur.deltas, e)
	cur.last = e
	cur.bytes += len(raw)
	r.bytes += len(raw)
	r.prune(delta.TS)
	return nil
}

// NeedsKeyframe reports whether a new segment should start: there is none
// yet, the stream broke, or the current one has been collecting changes for
// KeyframeEvery. Age counts from the first change, so a quiet cluster doesn't
// get a full snapshot for every isolated change.
func (r *Recorder) NeedsKeyframe(now time.Time) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cur := r.current()
	return cur == nil || r.broken || cur.file != "" || (len(cur.deltas) > 0 && now.Sub(cur.deltas[0].ts) >= r.opts.KeyframeEvery)
}

// Prune drops history that has fallen out of the window. Recording prunes as
// it goes; calling Prune covers quiet periods with nothing to record.
func (r *Recorder) Prune(now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prune(now)
}

// Break marks a gap in the recorded stream: recording continues with the
// next keyframe, as a new epoch. What was recorded stays: every segment
// starts with its own keyframe.
func (r *Recorder) Break() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.broken = true
}

// Close writes the segment being recorded to disk, so a clean shutdown
// loses nothing.
func (r *Recorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.persist(r.current())
}

// persist writes a segment to disk and drops its messages from memory. It
// does nothing without a directory, or for a segment already written.
func (r *Recorder) persist(s *segment) error {
	if r.opts.Dir == "" || s == nil || s.file != "" {
		return nil
	}
	path := filepath.Join(r.opts.Dir, fileName(s))
	tmp := path + ".tmp"
	if err := writeSegment(tmp, s); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("recorder: writing segment: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("recorder: %w", err)
	}
	if fi, err := os.Stat(path); err == nil {
		s.onDisk = fi.Size()
	}
	r.disk += s.onDisk
	r.bytes -= s.bytes
	s.file, s.bytes, s.deltas, s.keyframe.raw, s.last.raw = path, 0, nil, nil, nil
	return nil
}

func writeSegment(path string, s *segment) (err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // a new file in the recorder's own directory
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}()
	zw := gzip.NewWriter(f)
	w := bufio.NewWriter(zw)
	for _, e := range append([]entry{s.keyframe}, s.deltas...) {
		if _, err := w.Write(append(e.raw, '\n')); err != nil {
			return err
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return f.Sync()
}

// load returns a segment with its messages, reading it from disk if needed.
// The returned segment must not be modified.
func (r *Recorder) load(s *segment) (*segment, error) {
	if s.file == "" {
		return s, nil
	}
	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()
	if c, ok := r.cache[s.file]; ok {
		return c, nil
	}
	c, err := readSegment(s)
	if err != nil {
		return nil, err
	}
	if len(r.cache) >= cacheSize {
		clear(r.cache) // simple and bounded; rewinding mostly reads neighbours
	}
	r.cache[s.file] = c
	return c, nil
}

const cacheSize = 8

func readSegment(s *segment) (*segment, error) {
	f, err := os.Open(s.file)
	if err != nil {
		return nil, fmt.Errorf("recorder: %w", err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("recorder: %s: %w", filepath.Base(s.file), err)
	}
	out := &segment{epoch: s.epoch, file: s.file}
	sc := bufio.NewScanner(zr)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	prev := s.keyframe.ts // times as indexed when recorded: never earlier than that
	for sc.Scan() {
		raw := slices.Clone(sc.Bytes())
		var head struct {
			Seq uint64    `json:"seq"`
			TS  time.Time `json:"ts"`
		}
		if err := json.Unmarshal(raw, &head); err != nil {
			return nil, fmt.Errorf("recorder: %s: %w", filepath.Base(s.file), err)
		}
		ts := head.TS
		if ts.Before(prev) {
			ts = prev // as recorded: never earlier than the message before
		}
		prev = ts
		e := entry{head.Seq, ts, raw}
		if out.keyframe.raw == nil {
			out.keyframe = e
		} else {
			out.deltas = append(out.deltas, e)
		}
		out.last = e
	}
	if err := sc.Err(); err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, fmt.Errorf("recorder: %s: %w", filepath.Base(s.file), err)
	}
	if out.keyframe.raw == nil {
		return nil, fmt.Errorf("recorder: %s is empty", filepath.Base(s.file))
	}
	return out, nil
}

// monotonic keeps the history index ordered even if the wall clock steps
// back (NTP): a timestamp is never earlier than the last one recorded.
func (r *Recorder) monotonic(ts time.Time) time.Time {
	if cur := r.current(); cur != nil && ts.Before(cur.last.ts) {
		return cur.last.ts
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
// goes once the next one starts before the retention window, or while a size
// budget is exceeded. The newest segment is always kept.
func (r *Recorder) prune(now time.Time) {
	cutoff := now.Add(-r.opts.Window)
	for len(r.segments) > 1 {
		old := r.segments[0]
		// Out of the window: superseded by a newer keyframe that is in it. A
		// segment shows the cluster until the next keyframe, except across a
		// restart: then nothing is known after its last message, so after an
		// outage it goes once that's before the window.
		next := r.segments[1]
		outside := !next.keyframe.ts.After(cutoff) || (next.epoch != old.epoch && old.last.ts.Before(cutoff))
		overMemory := r.bytes > r.opts.MaxBytes && old.file == "" // dropping a file frees no memory
		overDisk := r.opts.MaxDiskBytes > 0 && r.disk > r.opts.MaxDiskBytes
		if outside || overMemory || overDisk {
			r.bytes -= old.bytes
			if old.file != "" {
				r.disk -= old.onDisk
				_ = os.Remove(old.file)
				r.cacheMu.Lock()
				delete(r.cache, old.file)
				r.cacheMu.Unlock()
			}
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
	return r.segments[0].keyframe.ts, r.current().last.ts, true
}

// Size returns the memory used by recorded messages, in bytes.
func (r *Recorder) Size() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.bytes
}

// DiskSize returns the disk space used by recorded segments, in bytes.
func (r *Recorder) DiskSize() int64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.disk
}

// At rebuilds the cluster as it was at t, as a snapshot message.
func (r *Recorder) At(t time.Time) (graph.Message, error) {
	msgs, err := r.Messages(t, t)
	if err != nil {
		return graph.Message{}, err
	}
	var state *graph.State
	for _, raw := range msgs {
		var m graph.Message
		if err := json.Unmarshal(raw, &m); err != nil {
			return graph.Message{}, fmt.Errorf("recorder: decoding message: %w", err)
		}
		if state, err = graph.Step(state, m); err != nil {
			return graph.Message{}, err
		}
	}
	return state.Snapshot(), nil
}

// Messages returns the encoded messages needed to replay from..to: the last
// keyframe at or before from, then every delta up to and including to, as
// one continuous sequence (see Range). The returned slices must not be
// modified.
func (r *Recorder) Messages(from, to time.Time) ([][]byte, error) {
	msgs, _, _, err := r.Range(from, to, 0)
	return msgs, err
}

// Range is Messages with a size limit (0 for none) and the window the
// messages actually cover. A replay must be one continuous sequence, so if
// from..to spans a restart or a break, it keeps the epoch from falls in and
// ends where that epoch does. Segments are read from disk without holding
// the lock, so recording isn't held up by a large export.
func (r *Recorder) Range(from, to time.Time, maxBytes int) (msgs [][]byte, start, end time.Time, err error) {
	if to.Before(from) {
		return nil, start, end, fmt.Errorf("recorder: range ends (%v) before it starts (%v)", to, from)
	}
	r.mu.RLock()
	first, last := -1, -1
	for i, s := range r.segments {
		if s.keyframe.ts.After(to) {
			break
		}
		if !s.keyframe.ts.After(from) {
			first = i
		}
		last = i
	}
	if first < 0 {
		r.mu.RUnlock()
		return nil, start, end, ErrNotRecorded
	}
	start, end = from, to
	for last > first && r.segments[last].epoch != r.segments[first].epoch {
		last--
		end = r.segments[last].last.ts // the epoch ends before to
	}
	segs := copyForReading(r.segments[first : last+1])
	r.mu.RUnlock()

	size := 0
	add := func(raw []byte) error {
		size += len(raw)
		if maxBytes > 0 && size > maxBytes {
			return ErrTooLarge
		}
		msgs = append(msgs, raw)
		return nil
	}
	for i, s := range segs {
		loaded, err := r.load(s)
		if err != nil {
			return nil, start, end, err
		}
		if i == 0 {
			if err := add(loaded.keyframe.raw); err != nil {
				return nil, start, end, err
			}
		}
		for _, d := range loaded.deltas {
			if d.ts.After(end) {
				return msgs, start, end, nil
			}
			if err := add(d.raw); err != nil {
				return nil, start, end, err
			}
		}
	}
	return msgs, start, end, nil
}

// copyForReading copies segments for reading without the lock: the writer appends
// to the current segment and drops a segment's messages once it's on disk.
// Entries never change once recorded, so reading up to the copied length is
// safe. Hold at least the read lock.
func copyForReading(segs []*segment) []*segment {
	out := make([]*segment, 0, len(segs))
	for _, s := range segs {
		c := *s
		c.deltas = s.deltas[:len(s.deltas):len(s.deltas)]
		out = append(out, &c)
	}
	return out
}

// Walk calls fn with every recorded message from the oldest on: each
// epoch's first keyframe, then its deltas, as one stream with a snapshot
// wherever a restart or a break began a new sequence. Segments are read
// from disk without holding the lock.
func (r *Recorder) Walk(fn func(raw []byte) error) error {
	r.mu.RLock()
	segs := copyForReading(r.segments)
	r.mu.RUnlock()
	for i, s := range segs {
		loaded := s
		if s.file != "" {
			// Read past the cache: a full walk would only evict what
			// rewinding and exports keep warm.
			var err error
			if loaded, err = readSegment(s); err != nil {
				return err
			}
		}
		if i == 0 || segs[i-1].epoch != s.epoch {
			if err := fn(loaded.keyframe.raw); err != nil {
				return err
			}
		}
		for _, d := range loaded.deltas {
			if err := fn(d.raw); err != nil {
				return err
			}
		}
	}
	return nil
}
