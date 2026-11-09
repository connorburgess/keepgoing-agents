package main

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"testing"
)

func TestExamplesValidate(t *testing.T) {
	files, _ := filepath.Glob("../../examples/keepgoing.*.yaml")
	if len(files) < 3 {
		t.Fatalf("expected example configs, found %v", files)
	}
	for _, f := range files {
		var out bytes.Buffer
		if code := run(context.Background(), []string{"validate", "-c", f}, &out, io.Discard); code != 0 {
			t.Errorf("%s does not validate", f)
		}
	}
}
