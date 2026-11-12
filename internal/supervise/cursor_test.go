package supervise

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
)

func TestCursorHoldsSuppressProviderWork(t *testing.T) {
	for _, hold := range []string{"disabled", "paused", "completed", "awaiting", "stop"} {
		t.Run(hold, func(t *testing.T) {
			w := newWorld(t, true, false)
			// The real factory constructs Cursor. A mistaken send would attempt an
			// absent executable and leave a turn directory, making the test fail.
			w.cfg.Loop.Agent = config.Agent{Adapter: "cursor", Provider: "cursor", Cursor: &config.Cursor{CLI: filepath.Join(w.dir, "must-not-run"), Workdir: w.dir}}
			_, err := control.Update(w.dir, func(f *control.File) {
				switch hold {
				case "disabled":
					f.Enabled = false
				case "paused":
					f.Paused = true
				case "completed":
					f.Completed = true
				case "awaiting":
					f.AwaitingUser = true
				case "stop":
					f.Stop = true
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				result, err := Tick(context.Background(), w.cfg, Options{Now: time.Now()})
				if err != nil || result.Action == "wake" || result.Action == "reprompt" || result.Action == "handoff" {
					t.Fatalf("hold %s: %+v %v", hold, result, err)
				}
			}
			if _, err := os.Stat(filepath.Join(w.dir, "targets", "primary")); !os.IsNotExist(err) {
				t.Fatal("held loop touched provider turn records")
			}
		})
	}
}
