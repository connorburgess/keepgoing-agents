package probe

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestKeepFailureSavesAndPrunes(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 7; i++ {
		r := Result{At: at.Add(time.Duration(i) * time.Minute), Tier: Scheduled, Kind: "codex", ErrorClass: "unknown", Detail: "turn.failed", ExitCode: 1, Raw: []byte(`{"type":"turn.failed","error":{"message":"example"}}`)}
		if err := KeepFailure(dir, "codex-main", r); err != nil {
			t.Fatal(err)
		}
	}
	if err := KeepFailure(dir, "codex-main", Result{At: at.Add(time.Hour), OK: true, Raw: []byte("OK")}); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "probe-failures", "codex-main", "*.out"))
	if len(files) != keepFailures {
		t.Fatalf("kept %d files, want %d", len(files), keepFailures)
	}
	b, _ := os.ReadFile(files[len(files)-1])
	if !strings.Contains(string(b), "# class: unknown") || !strings.Contains(string(b), `"turn.failed"`) || !strings.HasSuffix(files[len(files)-1], "20261109T120600Z-scheduled.out") {
		t.Errorf("newest capture %s:\n%s", files[len(files)-1], b)
	}
	info, _ := os.Stat(files[0])
	if info.Mode().Perm() != 0o600 && os.PathSeparator == '/' {
		t.Errorf("mode = %v", info.Mode().Perm())
	}
}
