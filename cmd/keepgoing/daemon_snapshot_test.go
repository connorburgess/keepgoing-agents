package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/control"
)

func TestDaemonWaitSnapshotAllowsReplacementDuringSignature(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "café-项目")
	if err := control.Save(dir, control.File{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	before := controlSignature(dir)
	opened := 0
	observed := controlSignatureWithOpen(dir, func(stateDir string) (*os.File, error) {
		file, err := control.OpenSnapshot(stateDir)
		if err != nil {
			return nil, err
		}
		opened++
		// Hold the exact production read handle while the writer replaces the
		// record, then let hashing continue on that older opened generation.
		if err := control.Save(dir, control.File{Enabled: true, Paused: true}); err != nil {
			_ = file.Close()
			t.Fatalf("polling reader blocked pause: %v", err)
		}
		return file, nil
	})
	after := controlSignature(dir)
	if opened != 1 || observed != before || after == before {
		t.Fatalf("snapshot signatures: opens=%d before=%q observed=%q after=%q", opened, before, observed, after)
	}
	current, err := control.Load(dir)
	if err != nil || !current.Paused {
		t.Fatalf("pause not saved: %+v %v", current, err)
	}
}
