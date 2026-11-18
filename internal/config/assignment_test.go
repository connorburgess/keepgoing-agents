package config

import (
	"gopkg.in/yaml.v3"
	"strings"
	"testing"
)

func TestAssignmentLifecycleOptInAndOldReader(t *testing.T) {
	// Strict readers predating this key refuse activation rather than silently
	// omit the explicit await-user precondition. No record migration is implicit.
	var old struct {
		Version int `yaml:"version"`
		Loop    struct {
			Name string `yaml:"name"`
		} `yaml:"loop"`
	}
	d := yaml.NewDecoder(strings.NewReader("version: 1\nloop:\n  name: example\n  assignment_lifecycle: true\n"))
	d.KnownFields(true)
	if d.Decode(&old) == nil {
		t.Fatal("strict old reader ignored new opt-in")
	}
	var current struct {
		Loop Loop `yaml:"loop"`
	}
	if err := yaml.Unmarshal([]byte("loop:\n  assignment_lifecycle: true\n"), &current); err != nil || !current.Loop.AssignmentLifecycle {
		t.Fatal(err)
	}
	current.Loop = Loop{}
	if err := yaml.Unmarshal([]byte("loop: {}"), &current); err != nil || current.Loop.AssignmentLifecycle {
		t.Fatal(err)
	}
}
