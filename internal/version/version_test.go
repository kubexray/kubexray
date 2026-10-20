package version

import "testing"

func TestString(t *testing.T) {
	t.Parallel()

	if got, want := String(), "kubexray dev (none)"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}
