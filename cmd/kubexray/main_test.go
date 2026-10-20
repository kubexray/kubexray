package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/kubexray/kubexray/internal/graph"
)

func TestDumpGraph(t *testing.T) {
	t.Parallel()
	g := graph.New()
	g.UpsertNode(graph.Node{ID: "Pod/default/a", Kind: "Pod", Name: "a"})

	r, w := io.Pipe()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- dumpGraph(ctx, g, w, time.Millisecond)
		_ = w.Close()
	}()

	lines := bufio.NewScanner(r)
	next := func() graph.Message {
		t.Helper()
		if !lines.Scan() {
			t.Fatalf("no more output: %v", lines.Err())
		}
		var m graph.Message
		if err := json.Unmarshal(lines.Bytes(), &m); err != nil {
			t.Fatalf("invalid JSON line %q: %v", lines.Text(), err)
		}
		return m
	}

	snap := next()
	if snap.Type != graph.MessageSnapshot || len(snap.Nodes) != 1 || snap.Seq != 1 {
		t.Errorf("first message = %+v, want snapshot with 1 node at seq 1", snap)
	}

	g.RemoveNode("Pod/default/a")
	delta := next()
	if delta.Type != graph.MessageDelta || delta.Seq != 2 || len(delta.Ops) != 1 || delta.Ops[0].Op != graph.OpRemoveNode {
		t.Errorf("second message = %+v, want removeNode delta at seq 2", delta)
	}

	cancel()
	go func() { _, _ = io.Copy(io.Discard, r) }()
	if err := <-done; err != nil {
		t.Errorf("dumpGraph returned %v", err)
	}
}
