package config

import (
	"gopkg.in/yaml.v3"
	"strings"
	"testing"
)

func TestAssignmentLifecycleCompletionOptIn(t *testing.T) {
	var old struct {
		Loop struct {
			AssignmentLifecycle bool `yaml:"assignment_lifecycle"`
		} `yaml:"loop"`
	}
	d := yaml.NewDecoder(strings.NewReader("loop:\n  assignment_lifecycle: true\n  assignment_completion: true\n"))
	d.KnownFields(true)
	if d.Decode(&old) == nil {
		t.Fatal("old reader silently accepted completion opt-in")
	}
	var current struct {
		Loop Loop `yaml:"loop"`
	}
	if err := yaml.Unmarshal([]byte("loop:\n  assignment_completion: true\n"), &current); err != nil || !current.Loop.AssignmentCompletion {
		t.Fatal(err)
	}
	current.Loop = Loop{}
	if err := yaml.Unmarshal([]byte("loop: {}"), &current); err != nil || current.Loop.AssignmentCompletion {
		t.Fatal("completion not opt-in", err)
	}
}
