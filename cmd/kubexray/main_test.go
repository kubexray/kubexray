package main

import (
	"os"
	"path/filepath"
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
	tok := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tok, []byte("test-token-"+strings.Repeat("a", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	oidc := []string{"serve", "--auth", "oidc", "--token-file", tok}
	tests := []struct {
		args    []string
		wantErr string
		stdout  string
	}{
		{[]string{"--history", "0s"}, "--history must be positive", ""},
		{[]string{"--port", "1", "extra"}, `unknown command "extra"`, ""},
		{[]string{"--context", "prod", "why", "deploy/api"}, "put flags after the command", ""},
		{[]string{"-help"}, "", "flight recorder"},
		{[]string{"play"}, "which replay?", ""},
		{[]string{"play", "/no/such/file.kxr"}, "no such file", ""},
		{[]string{"version"}, "", "kubexray "},
		{[]string{"help"}, "", "flight recorder"},
		{[]string{"bogus"}, `unknown command "bogus"`, ""},
		{[]string{"why"}, "which app?", ""},
		{[]string{"why", "deploy/api", "-o", "yaml"}, "-o must be text or json", ""},
		{[]string{"why", "deploy/a", "deploy/b"}, "extra arguments", ""},
		{[]string{"changes", "--since", "-1h"}, "--since must be positive", ""},
		{[]string{"dump", "--history", "0s"}, "--history must be positive", ""},
		{[]string{"dump", "--history-max-mb", "0"}, "--history-max-mb must be between", ""},
		{[]string{"serve", "--token-file", "/no/such/token"}, "reading the token", ""},
		{[]string{"serve", "--auth", "none"}, "refusing to serve without authentication on :8080", ""},
		{[]string{"serve", "--auth", "none", "--listen", "0.0.0.0:8080"}, "refusing to serve without authentication", ""},
		{[]string{"serve", "--auth", "basic"}, `--auth must be "token", "oidc" or "none"`, ""},
		{[]string{"serve", "--auth", "none", "--listen", "127.0.0.1:8080", "--allow-domain", "example.com"}, "need --auth=oidc", ""},
		{oidc, "needs --oidc-client-secret-file and --session-key-file", ""},
		{append(oidc, "--oidc-client-secret-file", tok, "--session-key-file", tok, "--oidc-issuer", "https://idp.example", "--oidc-client-id", "c", "--public-url", "https://kx.example.com"), "set at least one allowed email, domain or group", ""},
		{append(oidc, "--oidc-client-secret-file", tok, "--session-key-file", tok, "--oidc-issuer", "https://idp.example", "--oidc-client-id", "c", "--allow-domain", "example.com"), "--public-url", ""},
		{[]string{"serve", "extra"}, "serve takes no arguments", ""},
		{[]string{"serve", "--auth", "none", "--listen", "127.0.0.1:8080", "--public-url", "kubexray.example.com"}, "--public-url must be an http(s) URL", ""},
		{[]string{"serve", "--auth", "none", "--listen", "127.0.0.1:8080", "--slack-webhook-file", "/no/such"}, "--slack-webhook-file", ""},
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

func TestReadToken(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "token")
	want := "test-token-" + strings.Repeat("a", 32)
	if err := os.WriteFile(path, []byte("  "+want+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if tok, err := readToken(path); err != nil || tok != want {
		t.Errorf("readToken = %q, %v; want the trimmed token", tok, err)
	}
	if err := os.WriteFile(path, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readToken(path); err == nil || !strings.Contains(err.Error(), "at least 32") {
		t.Errorf("readToken(short) = %v", err)
	}
}

func TestReadURL(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if u, err := readURL(write("slack", " https://hooks.slack.com/services/T0/B0/x\n"), true); err != nil || u != "https://hooks.slack.com/services/T0/B0/x" {
		t.Errorf("readURL(slack) = %q, %v", u, err)
	}
	if _, err := readURL(write("plain", "http://hooks.slack.com/x"), true); err == nil {
		t.Error("an http Slack URL was accepted")
	}
	if u, err := readURL(write("hook", "http://receiver.monitoring.svc/kubexray"), false); err != nil || u == "" {
		t.Errorf("readURL(in-cluster webhook) = %q, %v", u, err)
	}
	if _, err := readURL(write("bad", "secret-token-not-a-url"), false); err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Errorf("readURL(bad) = %v: want an error that doesn't echo the content", err)
	}
	if u, err := readURL("", true); err != nil || u != "" {
		t.Errorf("readURL(none) = %q, %v", u, err)
	}
}
