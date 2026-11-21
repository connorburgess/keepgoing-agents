package runner

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestBuiltinOutputReadBoundaries(t *testing.T) {
	for _, spec := range []*Spec{claudeSpec, codexSpec} {
		t.Run(spec.Name, func(t *testing.T) {
			if spec.MaxOutput != 8<<20 {
				t.Fatalf("unexpected observation limit: %d", spec.MaxOutput)
			}
			d := Dir(t.TempDir())
			for _, extra := range []int{0, 1} {
				data := bytes.Repeat([]byte("x"), int(spec.MaxOutput)+extra)
				if err := os.WriteFile(filepath.Join(string(d), "stdout"), data, 0600); err != nil {
					t.Fatal(err)
				}
				got, err := d.readOutput(spec.MaxOutput)
				if extra == 0 {
					if err != nil || !bytes.Equal(got, data) {
						t.Fatalf("exact boundary: size=%d err=%v", len(got), err)
					}
				} else if !errors.Is(err, errOutputOverLimit) || len(got) != 0 {
					t.Fatalf("overflow exposed partial output: size=%d err=%v", len(got), err)
				}
			}
		})
	}
}
