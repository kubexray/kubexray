package recorder

import (
	"errors"
	"slices"
	"sync"
)

// WalkFunc walks recorded history, as Walk does.
type WalkFunc = func(fn func(raw []byte) error) error

// errReadersDone stops a shared walk once no reader wants more.
var errReadersDone = errors.New("recorder: every reader stopped")

// Tee walks once and hands every message to each reader, which runs as if
// it walked on its own: reading history from disk once at startup instead
// of once per reader. Readers run concurrently and share the messages,
// which they must not modify. A reader that stops early (its function
// returns an error) stops getting messages; the others go on. It returns
// each reader's error, in order; a walk error reaches every reader still
// reading.
func Tee(walk WalkFunc, readers ...func(WalkFunc) error) []error {
	errs := make([]error, len(readers))
	pipes := make([]chan []byte, len(readers))
	done := make([]chan struct{}, len(readers))
	var walkErr error
	var wg sync.WaitGroup
	for i, read := range readers {
		pipes[i], done[i] = make(chan []byte, 64), make(chan struct{})
		in := pipes[i]
		wg.Go(func() {
			defer close(done[i])
			errs[i] = read(func(fn func(raw []byte) error) error {
				for raw := range in {
					if err := fn(raw); err != nil {
						return err
					}
				}
				return walkErr // set before the pipe closes
			})
		})
	}
	live := slices.Clone(pipes)
	err := walk(func(raw []byte) error {
		n := 0
		for i, in := range live {
			if in == nil {
				continue
			}
			select {
			case in <- raw:
				n++
			case <-done[i]:
				live[i] = nil // that reader is done
			}
		}
		if n == 0 {
			return errReadersDone
		}
		return nil
	})
	if !errors.Is(err, errReadersDone) {
		walkErr = err
	}
	for _, in := range pipes {
		close(in)
	}
	wg.Wait()
	return errs
}
