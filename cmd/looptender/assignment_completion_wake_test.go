//go:build unix

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/control"
)

// All declarations use the supported CLI and every command/Tick reloads saved
// records. A notification attempt is deliberately left unreconciled: delivery
// uncertainty is separate from whether the objective's obligations are done.
func completionWakeFixture(t *testing.T, kind string) (*fixture, *config.Config, func(string, any)) {
	t.Helper()
	f, _, mutate := assignmentSchedulingFixture(t, true)
	raw, err := os.ReadFile(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	write(t, f.cfg, strings.Replace(string(raw), "  name:", "  assignment_completion: true\n  name:", 1), 0600)
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	mutate("ready", map[string]any{"independentlyActionable": true, "selections": []control.AssignmentWakeSelection{{Assignment: "docs", AssignmentRevision: 1, HandoffRevision: 1}}})
	if kind == "ordinary" {
		write(t, filepath.Join(f.bbDir, "thr_example_ci.show"), `{"thread":{"id":"thr_example_ci","parentThreadId":"thr_primary","status":"idle"}}`, 0600)
	}
	if kind == "unknown" {
		script := strings.Replace(fakeBB, `"thread tell") cat`, `"thread tell") exit 1; cat`, 1)
		write(t, filepath.Join(f.dir, "bin", "bb"), script, 0755)
	}
	if kind != "unattempted" {
		if kind == "unknown" {
			if code, out, errs := f.run("tick"); code == 0 && !strings.Contains(out, `"outcome":"failed"`) {
				t.Fatal("expected unknown send", code, out, errs)
			}
			write(t, filepath.Join(f.dir, "bin", "bb"), fakeBB, 0755)
		} else if got := f.tick(); got["action"] != "wake" {
			t.Fatal(got)
		}
	}
	r, err := control.LoadAssignments(cfg.Loop.StateDir)
	if err != nil || r == nil {
		t.Fatal(r, err)
	}
	w := r.Revisions[len(r.Revisions)-1].Wake
	if r.Version != 2 || w == nil || w.ReconciledAt != nil || (w.AttemptedAt == nil) != (kind == "unattempted") {
		t.Fatal("unexpected grant history", w)
	}
	if kind != "unattempted" {
		disposition := "assignment_notice_attempt"
		if kind == "ordinary" {
			disposition = "superseded_by_ordinary_wake_attempt"
		}
		if w.Disposition != disposition || strings.Count(f.read("calls"), "thread tell") != 1 {
			t.Fatal(w, f.read("calls"))
		}
	}
	// The explicit new inventory accounts for the finished CI execution. Its
	// declared work is still unfinished until a new handoff says otherwise.
	specs := []control.AssignmentSpec{}
	for _, name := range []string{"ci", "docs", "preview"} {
		write(t, filepath.Join(f.bbDir, "thr_example_"+name+".show"), `{"thread":{"id":"thr_example_`+name+`","parentThreadId":"thr_primary","status":"idle"}}`, 0600)
		specs = append(specs, control.AssignmentSpec{ID: name, Lane: "thr_example_" + name, ScopeRefs: []string{name + ".md"}, AuthorizationRefs: []string{"authorization.md"}})
	}
	mutate("review", map[string]any{"assignments": specs})
	return f, cfg, mutate
}

func completionWakeHandoff(t *testing.T, mutate func(string, any), name string, ack bool) {
	t.Helper()
	mutate("handoff", control.AssignmentHandoff{Assignment: name, AssignmentRevision: 1, DeclaredBy: "thr_example_" + name, DeclaredAt: time.Now().UTC(), Outcome: "complete", Availability: "available", Blocker: "none", EvidenceRefs: []string{"example:whole-objective-evidence"}, RemainingRefs: []string{}, DependencyRefs: []string{}})
	if ack {
		mutate("acknowledge", map[string]any{"assignment": name, "handoffRevision": 2})
	}
}

func TestAssignmentCompletionWakeHistoryAndObligations(t *testing.T) {
	for _, kind := range []string{"attempted", "unknown", "ordinary", "unattempted"} {
		for _, obligation := range []string{"terminal", "unfinished", "unacknowledged"} {
			t.Run(kind+"/"+obligation, func(t *testing.T) {
				f, cfg, mutate := completionWakeFixture(t, kind)
				completionWakeHandoff(t, mutate, "ci", true)
				completionWakeHandoff(t, mutate, "docs", true)
				stale := completionInput(t, cfg)
				if obligation != "unfinished" {
					completionWakeHandoff(t, mutate, "preview", obligation == "terminal")
				}
				if obligation != "terminal" {
					completionRefused(t, f, cfg, completionInput(t, cfg))
					return
				}
				completionRefused(t, f, cfg, stale) // exact review revision still wins
				assignmentPath := filepath.Join(cfg.Loop.StateDir, control.AssignmentName)
				before, err := os.ReadFile(assignmentPath)
				if err != nil {
					t.Fatal(err)
				}
				calls := strings.Count(f.read("calls"), "thread tell")
				if code, out, errs := f.run("complete", "--review-file", completionInput(t, cfg)); code != 0 {
					t.Fatal(code, out, errs)
				}
				if !mustAssignmentControl(t, cfg).Completed {
					t.Fatal("whole objective not completed")
				}
				beforeTicks := f.read("calls")
				for i := 0; i < 2; i++ {
					if got := f.tick(); got["action"] != "none" {
						t.Fatal("completed loop acted", got)
					}
				}
				if f.read("calls") != beforeTicks {
					t.Fatal("completed loop queried provider metadata")
				}
				after, err := os.ReadFile(assignmentPath)
				if err != nil || string(before) != string(after) {
					t.Fatal("completion or restart changed grant/history", err)
				}
				if strings.Count(f.read("calls"), "thread tell") != calls {
					t.Fatal("completed loop sent or replayed pending grant")
				}
				r, err := control.LoadAssignments(cfg.Loop.StateDir)
				if err != nil || r.Revisions[len(r.Revisions)-1].Wake.ReconciledAt != nil {
					t.Fatal("invented delivery reconciliation", r, err)
				}
			})
		}
	}
}

func TestAssignmentCompletionWakePreservesControlHolds(t *testing.T) {
	for _, hold := range []string{"pause", "stop"} {
		t.Run(hold, func(t *testing.T) {
			f, cfg, mutate := completionWakeFixture(t, "unattempted")
			for _, name := range []string{"ci", "docs", "preview"} {
				completionWakeHandoff(t, mutate, name, true)
			}
			if c, o, e := f.run(hold); c != 0 {
				t.Fatal(c, o, e)
			}
			before := mustAssignmentControl(t, cfg)
			if c, o, e := f.run("complete", "--review-file", completionInput(t, cfg)); c != 0 {
				t.Fatal(c, o, e)
			}
			after := mustAssignmentControl(t, cfg)
			if !after.Completed || before.Paused != after.Paused || before.Stop != after.Stop || before.Enabled != after.Enabled {
				t.Fatal("completion cleared hold", before, after)
			}
			calls := strings.Count(f.read("calls"), "thread tell")
			if got := f.tick(); got["action"] == "wake" {
				t.Fatal(got)
			}
			if strings.Count(f.read("calls"), "thread tell") != calls {
				t.Fatal("held completed loop sent")
			}
		})
	}
}

func TestAssignmentCompletionRejectsDamagedWakeHistory(t *testing.T) {
	f, cfg, mutate := completionWakeFixture(t, "attempted")
	for _, name := range []string{"ci", "docs", "preview"} {
		completionWakeHandoff(t, mutate, name, true)
	}
	input := completionInput(t, cfg)
	path := filepath.Join(cfg.Loop.StateDir, control.AssignmentName)
	good, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, damage := range []string{"drop", "clear-attempt", "change-selection"} {
		t.Run(damage, func(t *testing.T) {
			var r control.AssignmentRecord
			if err := json.Unmarshal(good, &r); err != nil {
				t.Fatal(err)
			}
			x := &r.Revisions[len(r.Revisions)-1]
			switch damage {
			case "drop":
				x.Wake = nil
			case "clear-attempt":
				x.Wake.AttemptedAt = nil
				x.Wake.Disposition = ""
			case "change-selection":
				x.Wake.Selections[0].HandoffRevision++
			}
			raw, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			write(t, path, string(raw), 0600)
			completionRefused(t, f, cfg, input)
			after, err := os.ReadFile(path)
			if err != nil || !reflect.DeepEqual(raw, after) {
				t.Fatal("damaged history repaired", err)
			}
			write(t, path, string(good), 0600)
		})
	}
}
