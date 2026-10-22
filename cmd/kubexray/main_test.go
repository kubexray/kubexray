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

// These paths fail before connecting to a cluster.
func TestRunWithoutACluster(t *testing.T) {
	t.Parallel()
	tests := []struct {
		args    []string
		wantErr string
		stdout  string
	}{
		{[]string{"--history", "0s"}, "--history must be positive", ""},
		{[]string{"--port", "1", "extra"}, `unknown command "extra"`, ""},
		{[]string{"--context", "prod", "why", "deploy/api"}, "put flags after the command", ""},
		{[]string{"-help"}, "", "flight recorder"},
		{[]string{"version"}, "", "kubexray "},
		{[]string{"help"}, "", "flight recorder"},
		{[]string{"bogus"}, `unknown command "bogus"`, ""},
		{[]string{"why"}, "which app?", ""},
		{[]string{"why", "deploy/api", "-o", "yaml"}, "-o must be text or json", ""},
		{[]string{"why", "deploy/a", "deploy/b"}, "extra arguments", ""},
		{[]string{"changes", "--since", "-1h"}, "--since must be positive", ""},
		{[]string{"dump", "--history", "0s"}, "--history must be positive", ""},
		{[]string{"dump", "--history-max-mb", "0"}, "--history-max-mb must be between", ""},
	}
	for _, tt := range tests {
		var stdout, stderr strings.Builder
		err := run(tt.args, &stdout, &stderr)
		switch {
		case tt.wantErr == "" && err != nil:
			t.Errorf("run(%v) = %v", tt.args, err)
		case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
			t.Errorf("run(%v) = %v, want error containing %q", tt.args, err, tt.wantErr)
		case !strings.Contains(stdout.String(), tt.stdout):
			t.Errorf("run(%v) stdout = %q, want it to contain %q", tt.args, stdout.String(), tt.stdout)
		}
	}
}
