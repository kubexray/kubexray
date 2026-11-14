package recorder

import (
	"errors"
	"strings"
	"testing"
)

func TestTeeWalksOnceForEveryReader(t *testing.T) {
	t.Parallel()
	walks := 0
	walk := func(fn func(raw []byte) error) error {
		walks++
		for _, m := range []string{"a", "b", "c", "d"} {
			if err := fn([]byte(m)); err != nil {
				return err
			}
		}
		return nil
	}
	all := func(out *string) func(WalkFunc) error {
		return func(walk WalkFunc) error {
			return walk(func(raw []byte) error { *out += string(raw); return nil })
		}
	}
	stop := errors.New("enough")
	var first, second, third string
	errs := Tee(walk, all(&first), func(walk WalkFunc) error {
		return walk(func(raw []byte) error {
			if second += string(raw); len(second) == 2 {
				return stop // stops early: the others go on
			}
			return nil
		})
	}, all(&third))
	if walks != 1 {
		t.Errorf("history walked %d times, want once", walks)
	}
	if first != "abcd" || third != "abcd" || second != "ab" {
		t.Errorf("readers got %q, %q, %q", first, second, third)
	}
	if errs[0] != nil || !errors.Is(errs[1], stop) || errs[2] != nil {
		t.Errorf("errors = %v", errs)
	}
}

func TestTeePassesWalkErrors(t *testing.T) {
	t.Parallel()
	broken := errors.New("disk on fire")
	walk := func(fn func(raw []byte) error) error {
		if err := fn([]byte("a")); err != nil {
			return err
		}
		return broken
	}
	var got strings.Builder
	errs := Tee(walk, func(walk WalkFunc) error {
		return walk(func(raw []byte) error { got.Write(raw); return nil })
	})
	if !errors.Is(errs[0], broken) || got.String() != "a" {
		t.Errorf("reader got %q, %v; want what came before the error, then the error", got.String(), errs[0])
	}
	// Readers that all stop early end the walk.
	ended := Tee(walk, func(WalkFunc) error { return nil })
	if ended[0] != nil {
		t.Errorf("a reader that read nothing = %v", ended[0])
	}
}
