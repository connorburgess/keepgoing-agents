package state

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestProgressLedgerContinuity(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	sources := ProgressSources([]string{"/example/b", "/example/a"})
	if sources != ProgressSources([]string{"/example/a", "/example/b", "/example/a"}) {
		t.Fatal("source order or duplicate changed identity")
	}
	p, err := ObserveProgress(dir, nil, sources, "a", now, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, offset := range []time.Duration{0, time.Minute, time.Hour} {
		previous, err := LoadProgress(dir)
		if err != nil {
			t.Fatal(err)
		}
		p, err = ObserveProgress(dir, previous, sources, "a", now.Add(offset), false)
		if err != nil || !p.ChangedAt.Equal(now) {
			t.Fatalf("repeat reset: %+v %v", p, err)
		}
	}
	if !p.Reached(now.Add(time.Hour), time.Hour) || p.Reached(now.Add(time.Hour-time.Nanosecond), time.Hour) {
		t.Fatal("cutoff boundary")
	}
	// A configured source change is not evidence of progress.
	p, err = ObserveProgress(dir, &p, "new-source", "b", now.Add(2*time.Hour), false)
	if err != nil || !p.ChangedAt.Equal(now) {
		t.Fatal("source change replenished time")
	}
	p, err = ObserveProgress(dir, &p, "new-source", "c", now.Add(3*time.Hour), false)
	if err != nil || !p.ChangedAt.Equal(now.Add(3*time.Hour)) {
		t.Fatal("verified change did not replenish")
	}
	before, _ := os.ReadFile(filepath.Join(dir, ProgressName))
	if _, err := ObserveProgress(dir, &p, "new-source", "d", now.Add(4*time.Hour), true); err != nil {
		t.Fatal(err)
	}
	if _, err := ObserveProgress(dir, &p, "new-source", "d", now.Add(2*time.Hour), false); !errors.Is(err, ErrProgressLedger) {
		t.Fatal("clock rollback accepted")
	}
	after, _ := os.ReadFile(filepath.Join(dir, ProgressName))
	if string(before) != string(after) {
		t.Fatal("dry run or failed clock check mutated ledger")
	}
	info, _ := os.Stat(filepath.Join(dir, ProgressName))
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
}

func TestProgressLedgerUnknownAndWriteFailure(t *testing.T) {
	for _, data := range []string{"", "{}", "null", `{"version":2}`, `{"version":1,"sources":"a","fingerprint":"b","changedAt":"2030-01-01T00:00:00Z","observedAt":"2029-01-01T00:00:00Z"}`} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ProgressName), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadProgress(dir); !errors.Is(err, ErrProgressLedger) {
			t.Fatalf("accepted %q", data)
		}
	}
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ProgressName), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := ObserveProgress(dir, nil, "source", "fp", time.Now(), false); !errors.Is(err, ErrProgressLedger) {
		t.Fatal("write failure ignored")
	}
}
