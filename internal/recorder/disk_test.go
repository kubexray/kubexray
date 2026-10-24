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
