package config

import (
	"fmt"
	"testing"
)

func TestMaxSendsConfig(t *testing.T) {
	for _, v := range []int64{0, 1, 9223372036854775807, -1} {
		cfg, err := Parse([]byte(valid + fmt.Sprintf("  limits: {max_sends: %d}\n", v)))
		if v < 0 {
			if err == nil {
				t.Fatal("negative accepted")
			}
			continue
		}
		if err != nil || cfg.Loop.Limits.MaxSends != v {
			t.Fatalf("%d %v", v, err)
		}
	}
}
