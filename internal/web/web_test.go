package web

import (
	"io/fs"
	"strings"
	"testing"
)

func TestAssetsServeAnIndexPage(t *testing.T) {
	t.Parallel()
	index, err := fs.ReadFile(Assets(), "index.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(index), "kubexray") {
		t.Errorf("index.html = %q", index)
	}
}
