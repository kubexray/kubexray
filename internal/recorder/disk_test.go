package recorder

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kubexray/kubexray/internal/graph"
)

func diskOptions(dir string) Options {
	return Options{Window: 7 * 24 * time.Hour, MaxBytes: 1 << 30, KeyframeEvery: 30 * time.Second, Dir: dir, MaxDiskBytes: 1 << 30}
}

func open(t *testing.T, opts Options) *Recorder {
	t.Helper()
	r, err := Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func segments(t *testing.T, dir string) []string {
	t.Helper()
	names, err := filepath.Glob(filepath.Join(dir, "seg-*"))
	if err != nil {
		t.Fatal(err)
	}
	return names
}

func TestHistorySurvivesARestart(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	r := open(t, diskOptions(dir))
	keyframe(t, r, snapshot(0, at(0), "a"))
	record(t, r, delta(1, at(1), add("b")))
	keyframe(t, r, snapshot(1, at(30), "a", "b")) // the first segment goes to disk
	record(t, r, delta(2, at(31), remove("a")))
	if got := len(segments(t, dir)); got != 1 {
		t.Fatalf("%d segment files after the first segment, want 1", got)
	}
	if r.Size() == 0 || r.DiskSize() == 0 {
		t.Errorf("memory %d, disk %d: the current segment is in memory, the first on disk", r.Size(), r.DiskSize())
	}
	assertAt(t, r, at(1.5), "a", "b") // read back from disk
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	// A new process: the old history is there, and recording starts over.
	r = open(t, diskOptions(dir))
	from, to, ok := r.Bounds()
	if !ok || !from.Equal(at(0)) || !to.Equal(at(31)) {
		t.Errorf("bounds after restart = %v..%v, %v", from.Sub(t0), to.Sub(t0), ok)
	}
	assertAt(t, r, at(0.5), "a")
	assertAt(t, r, at(40), "b")
	if !r.NeedsKeyframe(at(60)) {
		t.Error("no keyframe asked for after a restart")
	}
	keyframe(t, r, snapshot(1, at(60), "c")) // the new process's sequence starts again
	record(t, r, delta(2, at(61), add("d")))
	assertAt(t, r, at(62), "c", "d")
	assertAt(t, r, at(31.5), "b") // still there
	// An export across the restart keeps the run it starts in, and says
	// where that run ends.
	msgs, start, end, err := r.Range(at(0), at(62), 0)
	if err != nil || len(msgs) != 3 || !start.Equal(at(0)) || !end.Equal(at(31)) {
		t.Errorf("an export across the restart = %d messages, %v..%v, %v; want the first run's 3, 0s..31s", len(msgs), start.Sub(t0), end.Sub(t0), err)
	}
	if _, _, _, err := r.Range(at(0), at(62), 10); !errors.Is(err, ErrTooLarge) {
		t.Errorf("Range over its size limit = %v, want ErrTooLarge", err)
	}
}

func TestDiskBudget(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	opts := diskOptions(dir)
	opts.MaxDiskBytes = 1 // every completed segment is over budget once there's a newer one
	r := open(t, opts)
	for i := range 5 {
		ts := at(float64(i * 30))
		keyframe(t, r, snapshot(uint64(i), ts, "a"))
		record(t, r, delta(uint64(i+1), ts.Add(time.Second), add("b")))
	}
	if got := len(segments(t, dir)); got > 1 {
		t.Errorf("%d segment files kept, want at most 1 over a 1-byte budget", got)
	}
	assertAt(t, r, at(121), "a", "b") // the current segment is always kept
}

func TestOpenIgnoresOtherFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, name := range []string{"seg-1-2-0-0-1.ndjson.gz.tmp", "notes.txt", "seg-x-y-z.ndjson.gz"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	r := open(t, diskOptions(dir))
	if _, _, ok := r.Bounds(); ok {
		t.Error("half-written or foreign files were loaded as history")
	}
}

func TestPruneByWindowDeletesFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	opts := diskOptions(dir)
	opts.Window = time.Minute
	r := open(t, opts)
	keyframe(t, r, snapshot(0, at(0), "a"))
	record(t, r, delta(1, at(1), add("b")))
	keyframe(t, r, snapshot(1, at(30), "a", "b"))
	record(t, r, delta(2, at(31), add("c")))
	keyframe(t, r, snapshot(2, at(120), "a", "b", "c"))
	if got := len(segments(t, dir)); got != 1 {
		t.Errorf("%d segment files, want only the one still in the window", got)
	}
}

func TestDiskErrorsKeepRecording(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	r := open(t, diskOptions(dir))
	keyframe(t, r, snapshot(0, at(0), "a"))
	record(t, r, delta(1, at(1), add("b")))
	if err := os.Chmod(dir, 0o500); err != nil { //nolint:gosec // a directory: the volume turns read-only
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) //nolint:gosec // a directory, restored for cleanup
	m := snapshot(1, at(30), "a", "b")
	if err := r.Keyframe(m, enc(t, m)); !errors.Is(err, ErrNotSaved) {
		t.Fatalf("Keyframe on a read-only disk = %v, want ErrNotSaved", err)
	}
	record(t, r, delta(2, at(31), add("c"))) // recording goes on
	assertAt(t, r, at(1.5), "a", "b")        // the unsaved segment is still in memory
	assertAt(t, r, at(32), "a", "b", "c")
}

func TestOpenRemovesHalfWrittenSegments(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	tmp := filepath.Join(dir, "seg-1-2-0-0-1.ndjson.gz.tmp")
	if err := os.WriteFile(tmp, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	open(t, diskOptions(dir))
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Errorf("a half-written segment survived Open: %v", err)
	}
}

func TestMemoryBudgetKeepsSegmentsOnDisk(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	opts := diskOptions(dir)
	opts.MaxBytes = 1 // the current segment alone is over budget
	r := open(t, opts)
	keyframe(t, r, snapshot(0, at(0), "a"))
	keyframe(t, r, snapshot(0, at(30), "a"))
	record(t, r, delta(1, at(31), add("b")))
	if got := len(segments(t, dir)); got != 1 {
		t.Errorf("%d segment files: the memory budget deleted history on disk", got)
	}
}

func TestStaleRunIsPrunedAfterAnOutage(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	opts := diskOptions(dir)
	opts.Window = time.Hour
	r := open(t, opts)
	keyframe(t, r, snapshot(0, at(0), "a"))
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	// Down for ten hours, then a new run.
	r = open(t, opts)
	keyframe(t, r, snapshot(0, at(36000), "b"))
	if from, _, _ := r.Bounds(); !from.Equal(at(36000)) {
		t.Errorf("history starts %v, want the new run: the old one is entirely before the window", from.Sub(t0))
	}
}

func TestWalkStreamsEveryEpoch(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	r := open(t, diskOptions(dir))
	keyframe(t, r, snapshot(0, at(0), "a"))
	record(t, r, delta(1, at(1), add("b")))
	keyframe(t, r, snapshot(1, at(30), "a", "b"))
	record(t, r, delta(2, at(31), add("c")))
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	r = open(t, diskOptions(dir)) // restarted: a new sequence
	keyframe(t, r, snapshot(1, at(60), "x"))
	record(t, r, delta(2, at(61), add("y")))

	var got []string
	err := r.Walk(func(raw []byte) error {
		var m graph.Message
		if err := json.Unmarshal(raw, &m); err != nil {
			return err
		}
		got = append(got, fmt.Sprintf("%s%d", m.Type[:1], m.Seq))
		return nil
	})
	// One keyframe per epoch, then every delta: the second segment's own
	// keyframe is skipped, its deltas continue the sequence.
	if want := "s0 d1 d2 s1 d2"; err != nil || strings.Join(got, " ") != want {
		t.Errorf("Walk = %q, %v; want %q", strings.Join(got, " "), err, want)
	}
}

// fourSegments records three segments to disk and a fourth in memory, and
// returns the second one's file.
func fourSegments(t *testing.T, r *Recorder, dir string) string {
	t.Helper()
	keyframe(t, r, snapshot(0, at(0), "a"))
	record(t, r, delta(1, at(1), add("b")))
	keyframe(t, r, snapshot(1, at(30), "a", "b"))
	record(t, r, delta(2, at(31), add("c")))
	keyframe(t, r, snapshot(2, at(60), "a", "b", "c"))
	record(t, r, delta(3, at(61), remove("a")))
	keyframe(t, r, snapshot(3, at(90), "b", "c"))
	files := segments(t, dir)
	if len(files) != 3 {
		t.Fatalf("%d segment files, want 3", len(files))
	}
	return files[1] // names sort by time
}

func walked(t *testing.T, r *Recorder) string {
	t.Helper()
	var got []string
	err := r.Walk(func(raw []byte) error {
		var m graph.Message
		if err := json.Unmarshal(raw, &m); err != nil {
			return err
		}
		got = append(got, fmt.Sprintf("%s%d", m.Type[:1], m.Seq))
		return nil
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	return strings.Join(got, " ")
}

func TestDamagedSegmentCostsOnlyItsOwnHistory(t *testing.T) {
	t.Parallel()
	damage := map[string]func(t *testing.T, path string){
		"truncated": func(t *testing.T, path string) {
			fi, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Truncate(path, fi.Size()-6); err != nil {
				t.Fatal(err)
			}
		},
		"bit flip": func(t *testing.T, path string) {
			b, err := os.ReadFile(path) //nolint:gosec // the test's own file
			if err != nil {
				t.Fatal(err)
			}
			b[len(b)/2] ^= 0x10
			if err := os.WriteFile(path, b, 0o600); err != nil { //nolint:gosec // the test's own file
				t.Fatal(err)
			}
		},
	}
	for name, spoil := range damage {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			r := open(t, diskOptions(dir))
			bad := fourSegments(t, r, dir)
			spoil(t, bad)

			// The walk skips it, and starts again after it from a
			// snapshot: no delta is applied across the gap.
			if got, want := walked(t, r), "s0 d1 s2 d3"; got != want {
				t.Errorf("Walk = %q, want %q", got, want)
			}
			if _, err := os.Stat(bad + badExt); err != nil {
				t.Errorf("the damaged segment wasn't set aside: %v", err)
			}
			if got := len(segments(t, dir)); got != 3 { // two segments and the .bad
				t.Errorf("%d files, want 3", got)
			}
			// What's on either side still rewinds, and a restart
			// doesn't trip over it again.
			assertAt(t, r, at(1.5), "a", "b")
			assertAt(t, r, at(62), "b", "c")
			r = open(t, diskOptions(dir))
			if got, want := walked(t, r), "s0 d1 s2 d3"; got != want {
				t.Errorf("Walk after a restart = %q, want %q", got, want)
			}
		})
	}
}

func TestExportStopsAtADamagedSegment(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	r := open(t, diskOptions(dir))
	bad := fourSegments(t, r, dir)
	if err := os.WriteFile(bad, []byte("this is not a gzip file at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	msgs, _, end, err := r.Range(at(0), at(70), 0)
	if err != nil || len(msgs) != 2 || !end.Equal(at(1)) {
		t.Errorf("an export across a damaged segment = %d messages, ending %v, %v; want the 2 before it, ending at 1s", len(msgs), end.Sub(t0), err)
	}
	if _, err := os.Stat(bad + badExt); err != nil {
		t.Errorf("the damaged segment wasn't set aside: %v", err)
	}
	// From there on, the history after the gap is its own sequence.
	msgs, _, end, err = r.Range(at(0), at(70), 0)
	if err != nil || len(msgs) != 2 || !end.Equal(at(1)) {
		t.Errorf("the same export again = %d messages, ending %v, %v", len(msgs), end.Sub(t0), err)
	}
	if msgs, _, _, err := r.Range(at(60), at(70), 0); err != nil || len(msgs) != 2 {
		t.Errorf("an export after the gap = %d messages, %v; want 2", len(msgs), err)
	}
}

func TestOpenSetsAsideEmptySegments(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	empty := filepath.Join(dir, "seg-0000000000000000001-0000000000000000002-0-0-1.ndjson.gz")
	oldBad := filepath.Join(dir, "seg-0000000000000000001-0000000000000000002-0-0-0.ndjson.gz.bad")
	for _, f := range []string{empty, oldBad} {
		if err := os.WriteFile(f, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(oldBad, old, old); err != nil {
		t.Fatal(err)
	}
	r := open(t, diskOptions(dir))
	if _, _, ok := r.Bounds(); ok {
		t.Error("an empty segment file was loaded as history")
	}
	if _, err := os.Stat(empty + badExt); err != nil {
		t.Errorf("an empty segment wasn't set aside: %v", err)
	}
	if _, err := os.Stat(oldBad); !os.IsNotExist(err) {
		t.Errorf("a segment set aside longer ago than the window is still there: %v", err)
	}
}

func TestPruneWaitsForReaders(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	opts := diskOptions(dir)
	opts.Window = time.Minute
	r := open(t, opts)
	keyframe(t, r, snapshot(0, at(0), "a"))
	keyframe(t, r, snapshot(0, at(30), "a"))
	first := segments(t, dir)[0]
	// A reader has the index and is about to open the first file, without
	// the lock, when the writer prunes it.
	r.mu.RLock()
	unpin := r.pin()
	r.mu.RUnlock()
	keyframe(t, r, snapshot(0, at(120), "a"))
	if _, err := os.Stat(first); err != nil {
		t.Fatalf("a pruned file was deleted under a reader: %v", err)
	}
	unpin()
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Errorf("a pruned file outlived its last reader: %v", err)
	}
}

func TestSavingDoesNotHoldUpReaders(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	r := open(t, diskOptions(dir))
	keyframe(t, r, snapshot(0, at(0), "a"))
	record(t, r, delta(1, at(1), add("b")))
	// The disk is slow: writing the finished segment takes a while.
	r.saveMu.Lock()
	done := make(chan error)
	go func() {
		m := snapshot(1, at(30), "a", "b")
		raw, _ := json.Marshal(m)
		done <- r.Keyframe(m, raw)
	}()
	read := make(chan struct{})
	go func() {
		defer close(read)
		// Once the new segment has started, while the old one is saved.
		for _, to, _ := r.Bounds(); !to.Equal(at(30)); _, to, _ = r.Bounds() {
			time.Sleep(time.Millisecond)
		}
		if _, err := r.At(at(1.5)); err != nil {
			t.Errorf("At while saving: %v", err)
		}
	}()
	select {
	case <-read:
	case <-time.After(5 * time.Second):
		t.Error("reading history waited for a segment to be written")
	}
	r.saveMu.Unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := len(segments(t, dir)); got != 1 {
		t.Errorf("%d segment files after saving, want 1", got)
	}
}
