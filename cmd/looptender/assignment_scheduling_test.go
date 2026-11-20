//go:build unix

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/control"
	"github.com/looptender/looptender/internal/state"
)

// This baseline proves that recorded, acknowledged unfinished work alone does
// not bypass the existing child-work gate. It must remain true without the new
// explicit readiness/config opt-in, alongside the future positive journey.
func assignmentSchedulingFixture(t *testing.T, enabled bool) (*fixture, *config.Config, func(string, any)) {
	f := newFixture(t)
	t.Setenv("BB_THREAD_ID", "thr_example_coordinator")
	raw, err := os.ReadFile(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	write(t, f.cfg, strings.Replace(string(raw), "  name:", "  assignment_lifecycle: true\n  name:", 1), 0600)
	if enabled {
		raw, _ = os.ReadFile(f.cfg)
		write(t, f.cfg, strings.Replace(string(raw), "  name:", "  assignment_coordination: {enabled: true}\n  name:", 1), 0600)
	}
	if code, _, errs := f.run("enable"); code != 0 {
		t.Fatal(errs)
	}
	goal := filepath.Join(t.TempDir(), "goal.json")
	write(t, goal, `{"title":"Fictional coordination","objectiveRefs":["objectives.md"],"acceptanceRefs":["acceptance.md"]}`, 0600)
	if code, _, errs := f.run("goal", "create", "--file", goal); code != 0 {
		t.Fatal(errs)
	}
	write(t, filepath.Join(f.bbDir, "thr_primary.children"), `[{"id":"thr_example_ci"},{"id":"thr_example_docs"},{"id":"thr_example_preview"}]`, 0600)
	write(t, filepath.Join(f.bbDir, "thr_primary.show"), `{"thread":{"id":"thr_primary","status":"idle"}}`, 0600)
	specs := []control.AssignmentSpec{}
	for _, name := range []string{"ci", "docs", "preview"} {
		state := "idle"
		if name == "ci" {
			state = "active"
		}
		write(t, filepath.Join(f.bbDir, "thr_example_"+name+".show"), `{"thread":{"id":"thr_example_`+name+`","parentThreadId":"thr_primary","status":"`+state+`"}}`, 0600)
		specs = append(specs, control.AssignmentSpec{ID: name, Lane: "thr_example_" + name, ScopeRefs: []string{name + ".md"}, AuthorizationRefs: []string{"authorization.md"}})
	}
	save := func(path string, v any) {
		t.Helper()
		b, e := json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
		write(t, path, string(b), 0600)
	}
	plan := filepath.Join(t.TempDir(), "plan.json")
	save(plan, map[string]any{"assignments": specs})
	if code, out, errs := f.run("assignment", "review", "--file", plan, "--json"); code != 0 {
		t.Fatal(code, out, errs)
	}
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	mutate := func(command string, value any) {
		t.Helper()
		r, e := control.LoadAssignments(cfg.Loop.StateDir)
		if e != nil || r == nil {
			t.Fatal(r, e)
		}
		file := filepath.Join(t.TempDir(), "declaration.json")
		save(file, value)
		if code, out, errs := f.run("assignment", command, "--file", file, "--id", r.ID, "--revision", strconv.Itoa(len(r.Revisions)), "--json"); code != 0 {
			t.Fatal(code, out, errs)
		}
	}
	for _, name := range []string{"ci", "docs", "preview"} {
		availability := "available"
		if name == "ci" {
			availability = "busy"
		}
		mutate("handoff", control.AssignmentHandoff{Assignment: name, AssignmentRevision: 1, DeclaredBy: "thr_example_" + name, DeclaredAt: time.Now().UTC(), Outcome: "unfinished", Availability: availability, Blocker: "none", EvidenceRefs: []string{name + "-evidence.md"}, RemainingRefs: []string{name + "-remaining.md"}, NextOwner: "coordinator"})
		mutate("acknowledge", map[string]any{"assignment": name, "handoffRevision": 1})
	}
	if code, out, errs := f.run("assignment", "show", "--json"); code != 0 || !strings.Contains(out, `"unknown":0`) || !strings.Contains(out, `"unfinished":3`) {
		t.Fatal(code, out, errs)
	}
	return f, cfg, mutate
}
func TestAssignmentSchedulingOptOutBusyCI(t *testing.T) {
	f, _, _ := assignmentSchedulingFixture(t, false)
	got := f.tick()
	if got["action"] != "none" || got["reason"] != "child work running" {
		t.Fatal(got)
	}
	if strings.Contains(f.read("calls"), "thread tell") {
		t.Fatal("opt-out woke coordinator")
	}
}
func TestAssignmentSchedulingFullJourney(t *testing.T) {
	f, cfg, mutate := assignmentSchedulingFixture(t, true)
	if got := f.tick(); got["action"] != "none" {
		t.Fatal("missing readiness authorized wake", got)
	}
	mutate("ready", map[string]any{"independentlyActionable": true, "selections": []control.AssignmentWakeSelection{{Assignment: "docs", AssignmentRevision: 1, HandoffRevision: 1}, {Assignment: "preview", AssignmentRevision: 1, HandoffRevision: 1}}})
	beforeCI := f.read("thr_example_ci.show")
	got := f.tick()
	if got["action"] != "wake" {
		t.Fatal(got)
	}
	calls := f.read("calls")
	if strings.Count(calls, "thread tell") != 1 || !strings.Contains(calls, "thread tell thr_primary") {
		t.Fatal(calls)
	}
	for _, op := range []string{"thread spawn", "thread retry", "thread stop", "thread update"} {
		if strings.Contains(calls, op) {
			t.Fatal("child action", calls)
		}
	}
	if f.read("thr_example_ci.show") != beforeCI {
		t.Fatal("busy worker changed")
	}
	text := f.read("thr_primary.told")
	if !strings.Contains(text, "2 explicitly declared independent assignments") || strings.Contains(text, "Continue the fictional loop.") {
		t.Fatal(text)
	}
	r, err := control.LoadAssignments(cfg.Loop.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	w := r.Revisions[len(r.Revisions)-1].Wake
	if r.Version != 2 || w.AttemptedAt == nil || w.ReconciledAt != nil {
		t.Fatal(w, err)
	}
	// Each f.tick reloads config/state/control/assignment and constructs a fresh
	// adapter. This is restart replay coverage, not reuse of one in-memory tick.
	for i := 0; i < 2; i++ {
		if got := f.tick(); got["action"] != "none" {
			t.Fatal(got)
		}
	}
	if strings.Count(f.read("calls"), "thread tell") != 1 {
		t.Fatal("fresh loader replayed grant")
	}
	mutate("reconcile-wake", map[string]any{"grant": w.ID, "reviewed": true})
	r, err = control.LoadAssignments(cfg.Loop.StateDir)
	if err != nil || r.Revisions[len(r.Revisions)-1].Wake.ReconciledAt == nil {
		t.Fatal(r, err)
	}
	if code, out, errs := f.run("assignment", "show", "--json"); code != 0 || !strings.Contains(out, `"unfinished":3`) || !strings.Contains(out, `"state":"reviewed_attempt"`) {
		t.Fatal(code, out, errs)
	}
}
func TestAssignmentSchedulingOrdinaryWakeRetiresGrant(t *testing.T) {
	f, cfg, mutate := assignmentSchedulingFixture(t, true)
	mutate("ready", map[string]any{"independentlyActionable": true, "selections": []control.AssignmentWakeSelection{{Assignment: "docs", AssignmentRevision: 1, HandoffRevision: 1}, {Assignment: "preview", AssignmentRevision: 1, HandoffRevision: 1}}})
	write(t, filepath.Join(f.bbDir, "thr_example_ci.show"), `{"thread":{"id":"thr_example_ci","parentThreadId":"thr_primary","status":"idle"}}`, 0600)
	if got := f.tick(); got["action"] != "wake" {
		t.Fatal(got)
	}
	if !strings.Contains(f.read("thr_primary.told"), "Continue the fictional loop.") {
		t.Fatal("ordinary configured task was replaced")
	}
	r, err := control.LoadAssignments(cfg.Loop.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if w := r.Revisions[len(r.Revisions)-1].Wake; w == nil || w.Disposition != "superseded_by_ordinary_wake_attempt" || w.AttemptedAt == nil {
		t.Fatal(w)
	}
	write(t, filepath.Join(f.bbDir, "thr_example_ci.show"), `{"thread":{"id":"thr_example_ci","parentThreadId":"thr_primary","status":"active"}}`, 0600)
	if got := f.tick(); got["action"] != "none" {
		t.Fatal(got)
	}
	if strings.Count(f.read("calls"), "thread tell") != 1 {
		t.Fatal("ordinary wake leaked grant into duplicate exception")
	}
}

func TestAssignmentSchedulingReservationFailures(t *testing.T) {
	for _, fault := range []string{"allowance", "grant"} {
		t.Run(fault, func(t *testing.T) {
			f, cfg, mutate := assignmentSchedulingFixture(t, true)
			mutate("ready", map[string]any{"independentlyActionable": true, "selections": []control.AssignmentWakeSelection{{Assignment: "docs", AssignmentRevision: 1, HandoffRevision: 1}}})
			t.Setenv("FIXTURE_STATE", cfg.Loop.StateDir)
			mutation := `rm -f "$FIXTURE_STATE/sends.json"; mkdir "$FIXTURE_STATE/sends.json"`
			if fault == "grant" {
				mutation = `mv "$FIXTURE_STATE/assignments.json" "$FIXTURE_STATE/assignments-retained.json"; mkdir "$FIXTURE_STATE/assignments.json"`
			}
			inject := `"thread list") n=0; if [ -f "$d/coord-scans" ]; then n=$(cat "$d/coord-scans"); fi; n=$((n+1)); echo "$n" > "$d/coord-scans"; if [ "$n" = 2 ]; then ` + mutation + `; fi; t="$4";`
			script := strings.Replace(fakeBB, `"thread list") t="$4";`, inject, 1)
			write(t, filepath.Join(f.dir, "bin", "bb"), script, 0755)
			if code, out, errs := f.run("tick"); code == 0 && !strings.Contains(out, `"outcome":"failed"`) {
				t.Fatal("storage failure accepted", code, out, errs)
			}
			if strings.Contains(f.read("calls"), "thread tell") {
				t.Fatal("sent after reservation failure")
			}
			if fault == "grant" {
				if err := os.Remove(filepath.Join(cfg.Loop.StateDir, control.AssignmentName)); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(filepath.Join(cfg.Loop.StateDir, "assignments-retained.json"), filepath.Join(cfg.Loop.StateDir, control.AssignmentName)); err != nil {
					t.Fatal(err)
				}
				ledger, err := state.LoadLedger(cfg.Loop.StateDir)
				if err != nil || ledger.Seq == 0 {
					t.Fatal("grant failure refunded allowance reservation", ledger, err)
				}
			}
			r, err := control.LoadAssignments(cfg.Loop.StateDir)
			if err != nil || r.Revisions[len(r.Revisions)-1].Wake.AttemptedAt != nil {
				t.Fatal("failed reservation consumed grant", r, err)
			}
		})
	}
}
func TestAssignmentSchedulingUnknownSendAndStateFailureRestart(t *testing.T) {
	for _, ordinary := range []bool{false, true} {
		for _, fault := range []string{"send", "state"} {
			t.Run(strconv.FormatBool(ordinary)+"-"+fault, func(t *testing.T) {
				f, cfg, mutate := assignmentSchedulingFixture(t, true)
				mutate("ready", map[string]any{"independentlyActionable": true, "selections": []control.AssignmentWakeSelection{{Assignment: "docs", AssignmentRevision: 1, HandoffRevision: 1}}})
				if ordinary {
					write(t, filepath.Join(f.bbDir, "thr_example_ci.show"), `{"thread":{"id":"thr_example_ci","parentThreadId":"thr_primary","status":"idle"}}`, 0600)
				}
				script := fakeBB
				if fault == "send" {
					script = strings.Replace(script, `"thread tell") cat`, `"thread tell") exit 1; cat`, 1)
				} else {
					t.Setenv("FIXTURE_STATE", cfg.Loop.StateDir)
					script = strings.Replace(script, `"thread tell") cat`, `"thread tell") if [ -f "$FIXTURE_STATE/state.json" ]; then mv "$FIXTURE_STATE/state.json" "$FIXTURE_STATE/state-retained.json"; fi; mkdir "$FIXTURE_STATE/state.json"; cat`, 1)
				}
				write(t, filepath.Join(f.dir, "bin", "bb"), script, 0755)
				if code, out, errs := f.run("tick"); code == 0 && !strings.Contains(out, `"outcome":"failed"`) {
					t.Fatal("expected injected send/state error", code, out, errs)
				}
				r, err := control.LoadAssignments(cfg.Loop.StateDir)
				if err != nil || r.Revisions[len(r.Revisions)-1].Wake.AttemptedAt == nil {
					t.Fatal("attempt not durable before failure", r, err)
				}
				if fault == "state" {
					if err := os.Remove(filepath.Join(cfg.Loop.StateDir, "state.json")); err != nil {
						t.Fatal(err)
					}
					_ = os.Rename(filepath.Join(cfg.Loop.StateDir, "state-retained.json"), filepath.Join(cfg.Loop.StateDir, "state.json"))
				}
				write(t, filepath.Join(f.dir, "bin", "bb"), fakeBB, 0755)
				write(t, filepath.Join(f.bbDir, "thr_example_ci.show"), `{"thread":{"id":"thr_example_ci","parentThreadId":"thr_primary","status":"active"}}`, 0600)
				if got := f.tick(); got["action"] != "none" {
					t.Fatal("replayed after fresh load", got)
				}
				if strings.Count(f.read("calls"), "thread tell") != 1 {
					t.Fatal("duplicate uncertain send")
				}
			})
		}
	}
}

func TestAssignmentSchedulingChildChangesBetweenScans(t *testing.T) {
	for _, change := range []string{"added", "removed", "active", "interaction"} {
		t.Run(change, func(t *testing.T) {
			f, cfg, mutate := assignmentSchedulingFixture(t, true)
			mutate("ready", map[string]any{"independentlyActionable": true, "selections": []control.AssignmentWakeSelection{{Assignment: "docs", AssignmentRevision: 1, HandoffRevision: 1}}})
			mutation := `echo '[{"id":"thr_example_ci"},{"id":"thr_example_docs"}]' > "$d/thr_primary.children"`
			switch change {
			case "added":
				mutation = `echo '[{"id":"thr_example_ci"},{"id":"thr_example_docs"},{"id":"thr_example_preview"},{"id":"thr_example_new"}]' > "$d/thr_primary.children"; echo '{"thread":{"id":"thr_example_new","parentThreadId":"thr_primary","status":"idle"}}' > "$d/thr_example_new.show"`
			case "active":
				mutation = `echo '{"thread":{"id":"thr_example_docs","parentThreadId":"thr_primary","status":"active"}}' > "$d/thr_example_docs.show"`
			case "interaction":
				mutation = `echo '[{"threadId":"thr_example_docs","status":"pending"}]' > "$d/thr_example_docs.interactions"`
			}
			inject := `"thread list") n=0; if [ -f "$d/coord-scans" ]; then n=$(cat "$d/coord-scans"); fi; n=$((n+1)); echo "$n" > "$d/coord-scans"; if [ "$n" = 2 ]; then ` + mutation + `; fi; t="$4";`
			write(t, filepath.Join(f.dir, "bin", "bb"), strings.Replace(fakeBB, `"thread list") t="$4";`, inject, 1), 0755)
			got := f.tick()
			if got["action"] != "none" || got["reason"] != "assignment child evidence changed or unavailable" {
				t.Fatal(got)
			}
			if strings.Contains(f.read("calls"), "thread tell") {
				t.Fatal("sent after child change")
			}
			r, err := control.LoadAssignments(cfg.Loop.StateDir)
			if err != nil || r.Revisions[len(r.Revisions)-1].Wake.AttemptedAt != nil {
				t.Fatal("changed scan consumed grant", r, err)
			}
		})
	}
}
func TestAssignmentSchedulingOptOutQueryAndPromptParity(t *testing.T) {
	var calls, prompt string
	for _, explicitDisabled := range []bool{false, true} {
		t.Run(strconv.FormatBool(explicitDisabled), func(t *testing.T) {
			f, _, _ := assignmentSchedulingFixture(t, false)
			if explicitDisabled {
				raw, _ := os.ReadFile(f.cfg)
				write(t, f.cfg, strings.Replace(string(raw), "  name:", "  assignment_coordination: {enabled: false}\n  name:", 1), 0600)
			}
			write(t, filepath.Join(f.bbDir, "thr_example_ci.show"), `{"thread":{"id":"thr_example_ci","parentThreadId":"thr_primary","status":"idle"}}`, 0600)
			before := len(f.read("calls"))
			if got := f.tick(); got["action"] != "wake" {
				t.Fatal(got)
			}
			gotCalls := f.read("calls")[before:]
			gotPrompt := f.read("thr_primary.told")
			if strings.Count(gotCalls, "thread list") != 1 {
				t.Fatal("opt-out added child queries", gotCalls)
			}
			if !explicitDisabled {
				calls, prompt = gotCalls, gotPrompt
			} else if calls != gotCalls || prompt != gotPrompt {
				t.Fatal("explicit disable changed default queries/prompt", calls, gotCalls, prompt, gotPrompt)
			}
		})
	}
}

func TestAssignmentSchedulingAttemptSavedBeforeAdapter(t *testing.T) {
	f, cfg, mutate := assignmentSchedulingFixture(t, true)
	mutate("ready", map[string]any{"independentlyActionable": true, "selections": []control.AssignmentWakeSelection{{Assignment: "docs", AssignmentRevision: 1, HandoffRevision: 1}}})
	t.Setenv("FIXTURE_STATE", cfg.Loop.StateDir)
	check := `"thread tell") if ! grep -q '"attemptedAt"' "$FIXTURE_STATE/assignments.json"; then echo missing > "$d/attempt-not-saved"; fi; cat`
	write(t, filepath.Join(f.dir, "bin", "bb"), strings.Replace(fakeBB, `"thread tell") cat`, check, 1), 0755)
	if got := f.tick(); got["action"] != "wake" || got["outcome"] != "succeeded" {
		t.Fatal(got)
	}
	if f.read("attempt-not-saved") != "" {
		t.Fatal("adapter called before durable grant consumption")
	}
}
