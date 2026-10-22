package main

import (
	"strings"
	"testing"

	"github.com/kubexray/kubexray/internal/graph"
)

func TestNDJSONWritesOneLinePerMessage(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	emit := ndjson(&out)
	for _, raw := range []string{`{"seq":1}`, `{"seq":2}`} {
		if err := emit(graph.Message{}, []byte(raw)); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := out.String(), "{\"seq\":1}\n{\"seq\":2}\n"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}
