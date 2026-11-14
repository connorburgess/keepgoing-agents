package main

import (
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"path/filepath"
	"testing"
)

func TestExamplesValidate(t *testing.T) {
	files, _ := filepath.Glob("../../examples/looptender.*.yaml")
	if len(files) < 3 {
		t.Fatalf("expected example configs, found %v", files)
	}
	for _, f := range files {
		if _, err := config.Load(f); err != nil {
			t.Errorf("%s does not validate: %v", f, err)
		}
	}
}
