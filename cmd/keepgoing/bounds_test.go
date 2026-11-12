//go:build unix

package main

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/config"
)

// The warning is advice only: validate and status keep exit code 0, change no
// controls or state, call no provider and leave supervision behaving as before.
func TestUnboundedLoopWarningInValidateAndStatus(t *testing.T) {
	cases := []struct {
		name   string
		limits string // appended under loop:
		want   []string
	}{
		{"no limits", "", []string{}},
		{"empty limits block", "  limits: {}\n", []string{}},
		{"explicit zeros", "  limits: {max_sends: 0, no_progress_timeout: 0s, deadline: ''}\n", []string{}},
		{"max_sends", "  limits: {max_sends: 20}\n", []string{"max_sends"}},
		{"deadline", "  limits: {deadline: '2030-01-01T18:00:00Z'}\n", []string{"deadline"}},
		{"no_progress_timeout", "  progress: {repositories: [.]}\n  limits: {no_progress_timeout: 2h}\n", []string{"no_progress_timeout"}},
		{"all three", "  progress: {repositories: [.]}\n  limits: {no_progress_timeout: 2h, max_sends: 20, deadline: '2030-01-01T18:00:00Z'}\n",
			[]string{"deadline", "max_sends", "no_progress_timeout"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			original, err := os.ReadFile(f.cfg)
			if err != nil {
				t.Fatal(err)
			}
			write(t, f.cfg, string(original)+tc.limits, 0600)
			unbounded := len(tc.want) == 0

			code, out, errs := f.run("validate")
			if code != exitOK || !strings.HasPrefix(out, "ok: ") {
				t.Fatalf("validate: code=%d out=%q err=%q", code, out, errs)
			}
			if warned := strings.Contains(errs, "warning: "+config.UnboundedWarning); warned != unbounded || strings.Contains(out, "warning") {
				t.Fatalf("validate warned=%t want %t: out=%q err=%q", warned, unbounded, out, errs)
			}
			if listed := strings.Contains(out, "bounds: "+strings.Join(tc.want, ", ")+"\n"); listed == unbounded {
				t.Fatalf("validate bounds line: %q", out)
			}

			code, out, errs = f.run("status", "--json")
			var status statusOut
			if code != exitOK || json.Unmarshal([]byte(out), &status) != nil {
				t.Fatalf("status --json: code=%d out=%q err=%q", code, out, errs)
			}
			b := status.WorkBounds
			if !reflect.DeepEqual(b.Limits, tc.want) || b.Unbounded != unbounded || (b.Warning == config.UnboundedWarning) != unbounded || (b.Warning == "") == unbounded {
				t.Fatalf("workBounds=%+v want %v", b, tc.want)
			}
			if !strings.Contains(out, `"limits": [`) {
				t.Fatalf("limits must be a JSON array, never null: %s", out)
			}

			code, out, errs = f.run("status")
			wantLine := "bounds:    " + strings.Join(tc.want, ", ") + "\n"
			if unbounded {
				wantLine = "bounds:    warning: " + config.UnboundedWarning + "\n"
			}
			if code != exitOK || !strings.Contains(out, wantLine) || strings.Count(out, "bounds:") != 1 {
				t.Fatalf("status: code=%d out=%q err=%q", code, out, errs)
			}

			// Read-only: no control or runtime record was created, no provider called.
			if entries, _ := os.ReadDir(f.dir + "/state"); len(entries) != 0 {
				t.Fatalf("validate/status wrote runtime files: %v", entries)
			}
			if f.read("calls") != "" || f.read("probes") != "" {
				t.Fatal("validate/status called a provider")
			}
		})
	}
}

// An unbounded loop is still supervised exactly as before: the warning applies
// no cap and creates no hold.
func TestUnboundedLoopWarningChangesNoBehavior(t *testing.T) {
	f := newFixture(t)
	if code, _, errs := f.run("enable"); code != exitOK {
		t.Fatal(errs)
	}
	for i := 0; i < 3; i++ {
		if got := f.tick(); got["action"] != "wake" {
			t.Fatalf("tick %d: %v", i, got)
		}
	}
	code, out, errs := f.run("status", "--json")
	var status statusOut
	if code != exitOK || json.Unmarshal([]byte(out), &status) != nil || !status.WorkBounds.Unbounded || status.Control.AwaitingUser || status.ControlState != "enabled" {
		t.Fatalf("status: code=%d out=%q err=%q", code, out, errs)
	}
	// Holds do not hide the configuration fact, and stop/pause are unaffected.
	if code, _, errs := f.run("pause"); code != exitOK {
		t.Fatal(errs)
	}
	if got := f.tick(); got["action"] != "none" || got["reason"] != "paused" {
		t.Fatalf("pause lost: %v", got)
	}
	if _, out, _ := f.run("status"); !strings.Contains(out, "bounds:    warning: ") {
		t.Fatalf("warning hidden while paused: %q", out)
	}
}
