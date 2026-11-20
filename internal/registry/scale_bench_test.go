package registry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Fixture setup is outside the timed read. Referenced configs/state do not
// exist: Load must read only the registry, never follow its references.
func scaleRegistry(tb testing.TB, n int) (string, []byte) {
	tb.Helper()
	dir := filepath.Join(tb.TempDir(), "registry")
	if err := os.Mkdir(dir, 0700); err != nil {
		tb.Fatal(err)
	}
	f := File{SchemaVersion: 1, Entries: make([]Entry, n)}
	for i := range f.Entries {
		name := fmt.Sprintf("example-%03d", i)
		f.Entries[i] = Entry{ID: fmt.Sprintf("loop_%032x", i+1), Loop: name,
			ConfigPath: filepath.Join(dir, name+".yaml"), StateDir: filepath.Join(dir, name+"-state")}
	}
	body, err := json.Marshal(f)
	if err != nil {
		tb.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, FileName), body, 0600); err != nil {
		tb.Fatal(err)
	}
	return dir, body
}

func BenchmarkRegistryLoad(b *testing.B) {
	for _, n := range []int{1, 10, 100, 128} {
		b.Run(fmt.Sprintf("registrations=%d", n), func(b *testing.B) {
			dir, body := scaleRegistry(b, n)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				f, err := Load(dir)
				if err != nil || len(f.Entries) != n {
					b.Fatalf("entries=%d: %v", len(f.Entries), err)
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(len(body)), "input-bytes/op")
		})
	}
}

func TestScaleRegistryFixtureIsReadOnly(t *testing.T) {
	dir, before := scaleRegistry(t, 2)
	got, err := Load(dir)
	if err != nil || len(got.Entries) != 2 {
		t.Fatalf("%+v: %v", got, err)
	}
	after, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("read changed registry", err)
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 1 {
		t.Fatal("read created files", err)
	}
}
