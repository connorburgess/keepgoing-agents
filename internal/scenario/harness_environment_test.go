package scenario

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A stand-in that was ended early is reported as an environment failure, not
// left to fail some expectation about the product.
func TestHarnessNoticesAStandInEndedEarly(t *testing.T) {
	world := t.TempDir()
	write := func(name string, n int) {
		if err := os.WriteFile(filepath.Join(world, name), []byte(strings.Repeat(".", n)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got := standinTrouble(world, ""); got != "" {
		t.Fatalf("no stand-in has run: %q", got)
	}
	write("standin.started", 4)
	write("standin.ended", 4)
	if got := standinTrouble(world, "tick: bb thread show: exit status 1"); got != "" {
		t.Fatalf("every stand-in ended on its own: %q", got)
	}
	if got := standinTrouble(world, "tick: bb thread show: signal: killed"); !strings.Contains(got, "was killed") {
		t.Fatalf("a killed process in the command's error output: %q", got)
	}
	write("standin.started", 5)
	if got := standinTrouble(world, ""); got != "5 stand-in processes started and 4 ended on their own" {
		t.Fatalf("a stand-in that did not end: %q", got)
	}
	// One a test holds at the show gate is still running on purpose.
	write("standin.held", 1)
	if got := standinTrouble(world, ""); got != "" {
		t.Fatalf("a stand-in held at the gate: %q", got)
	}
	write("standin.started", 6)
	if got := standinTrouble(world, ""); got != "6 stand-in processes started and 4 ended on their own, with 1 held at the show gate" {
		t.Fatalf("a stand-in that did not end beside one held at the gate: %q", got)
	}
	write("standin.released", 1)
	write("standin.ended", 6)
	if got := standinTrouble(world, ""); got != "" {
		t.Fatalf("every stand-in ended after the gate opened: %q", got)
	}
}
