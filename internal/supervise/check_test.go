package supervise

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

func TestCheckRecordPersistsOutcomeWithoutRawErrors(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		ctl                       control.File
		dry, fail                 bool
		action, outcome, observed string
	}{
		{"wake", control.File{Enabled: true}, false, false, "wake", "succeeded", "primary"},
		{"dry run", control.File{Enabled: true}, true, false, "wake", "dry_run", "primary"},
		{"observe failure", control.File{Enabled: true}, false, true, "none", "failed", "primary"},
		{"complete", control.File{Enabled: true, Completed: true}, false, false, "none", "succeeded", ""},
		{"stop holds", control.File{Stop: true, Paused: true, Completed: true}, false, false, "none", "succeeded", "primary"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, true, false)
			p := w.primary(decide.Observation{State: decide.Idle})
			if tc.fail {
				p.obsErr = errors.New("PRIVATE_PROVIDER_ERROR")
			}
			if err := control.Save(w.dir, tc.ctl); err != nil {
				t.Fatal(err)
			}
			opts := w.opts(t0)
			opts.Version = "example-build"
			opts.DryRun = tc.dry
			_, err := Tick(context.Background(), w.cfg, opts)
			if (err != nil) != tc.fail {
				t.Fatalf("tick: %v", err)
			}
			st, err := state.Load(w.dir)
			if err != nil {
				t.Fatal(err)
			}
			c := st.LastCheck
			if c == nil || c.Action != tc.action || c.Outcome != tc.outcome || c.ObservedTarget != tc.observed || c.Version != "example-build" || c.DryRun != tc.dry || !c.At.Equal(st.CheckedAt) {
				t.Fatalf("record: %+v", c)
			}
			b, _ := json.Marshal(c)
			if strings.Contains(string(b), "PRIVATE_PROVIDER_ERROR") {
				t.Fatal("check contains raw provider error")
			}
			if tc.dry && len(p.sent) != 0 {
				t.Fatal("dry run sent a prompt")
			}
		})
	}
}

func TestCheckRecordsFailedSendAndPendingInteraction(t *testing.T) {
	for _, pending := range []bool{false, true} {
		w := newWorld(t, true, false)
		p := w.primary(decide.Observation{State: decide.Idle, AwaitingInput: pending})
		if !pending {
			p.sendErr = errors.New("PRIVATE_SEND_ERROR")
		}
		_, err := Tick(context.Background(), w.cfg, w.opts(t0))
		if (err == nil) != pending {
			t.Fatalf("pending=%v error=%v", pending, err)
		}
		st, loadErr := state.Load(w.dir)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if pending {
			if !st.LastCheck.AwaitingInput || st.LastCheck.Action != "none" || len(p.sent) != 0 {
				t.Fatalf("pending: %+v", st.LastCheck)
			}
		} else if st.LastCheck.Outcome != "failed" || st.LastCheck.Action != "wake" {
			t.Fatalf("failed send: %+v", st.LastCheck)
		}
		b, _ := json.Marshal(st.LastCheck)
		if strings.Contains(string(b), "PRIVATE_SEND_ERROR") {
			t.Fatal("raw send error persisted in check")
		}
	}
}
