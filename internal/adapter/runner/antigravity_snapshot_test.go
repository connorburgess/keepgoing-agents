package runner

import (
	"os"
	"runtime"
	"testing"
)

func TestAntigravityTurnSnapshotReplacement(t *testing.T) {
	d := Dir(t.TempDir())
	oldTurn := "1111111111111111111111111111111111111111111111111111111111111111"
	newTurn := "2222222222222222222222222222222222222222222222222222222222222222"
	if err := d.saveAntigravityTurn(oldTurn); err != nil {
		t.Fatal(err)
	}
	// Publication occurs synchronously while the production content handle is
	// still open. There are no scheduling or timing assumptions in this test.
	fields, err := d.readAntigravityObjectOpened(antigravityTurnName, 2, func() {
		if err := d.saveAntigravityTurn(newTurn); err != nil {
			t.Fatalf("replacement while production reader is open: %v", err)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	var held string
	if !acpString(fields, "turn", &held) || held != oldTurn {
		t.Fatalf("held snapshot = %q", held)
	}
	fresh, ok, err := d.antigravityTurn()
	if err != nil || !ok || fresh != newTurn {
		t.Fatalf("fresh snapshot = %q, %v, %v", fresh, ok, err)
	}
}

func TestAntigravityTurnSnapshotMissing(t *testing.T) {
	d := Dir(t.TempDir())
	if _, ok, err := d.antigravityTurn(); err != nil || ok {
		t.Fatalf("never published: %v, %v", ok, err)
	}
	if runtime.GOOS != "windows" {
		return
	} // Persistent markers are Windows-only.
	if err := d.saveAntigravityTurn("1111111111111111111111111111111111111111111111111111111111111111"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(d.path(antigravityTurnName)); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := d.antigravityTurn(); err == nil || ok {
		t.Fatalf("published pointer missing must be unavailable: %v, %v", ok, err)
	}
}
