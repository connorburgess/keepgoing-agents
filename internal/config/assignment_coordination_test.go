package config

import (
	"gopkg.in/yaml.v3"
	"strings"
	"testing"
	"time"
)

func TestAssignmentCoordinationOptInAndBounds(t *testing.T) {
	base := valid + "  assignment_lifecycle: true\n"
	c, err := Parse([]byte(base))
	if err != nil || c.Loop.AssignmentCoordination != nil {
		t.Fatal(c, err)
	}
	c, err = Parse([]byte(base + "  assignment_coordination: {enabled: true}\n"))
	if err != nil || c.Loop.AssignmentCoordination.MaxEvidenceAge.Duration != 30*time.Minute {
		t.Fatal(c, err)
	}
	for _, extra := range []string{"max_evidence_age: 1s", "max_evidence_age: 25h", "invented: true"} {
		if _, err := Parse([]byte(base + "  assignment_coordination: {enabled: true, " + extra + "}\n")); err == nil {
			t.Fatal(extra)
		}
	}
	if _, err := Parse([]byte(valid + "  assignment_coordination: {enabled: true}\n")); err == nil || !strings.Contains(err.Error(), "assignment_coordination") {
		t.Fatal(err)
	}
}

func TestAssignmentCoordinationOldReaderRefusesOptIn(t *testing.T) {
	var old struct {
		Loop struct {
			AssignmentLifecycle bool `yaml:"assignment_lifecycle"`
		} `yaml:"loop"`
	}
	d := yaml.NewDecoder(strings.NewReader("loop:\n  assignment_lifecycle: true\n  assignment_coordination: {enabled: true}\n"))
	d.KnownFields(true)
	if d.Decode(&old) == nil {
		t.Fatal("old strict reader silently ignored scheduling opt-in")
	}
}
