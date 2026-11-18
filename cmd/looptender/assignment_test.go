//go:build unix

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/control"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAssignmentMissingIsUnknown(t *testing.T) {
	f := newFixture(t)
	code, out, errs := f.run("assignment", "show", "--json")
	var s struct {
		Availability string `json:"availability"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &s) != nil || s.Availability != "not_recorded" {
		t.Fatalf("missing lifecycle must be visible unknown, not absent/zero: code=%d out=%s err=%s", code, out, errs)
	}
}

func TestAssignmentInitialReconciliationJourney(t *testing.T) {
	f := newFixture(t)
	t.Setenv("BB_THREAD_ID", "thr_example_coordinator")
	raw, _ := os.ReadFile(f.cfg)
	write(t, f.cfg, strings.Replace(string(raw), "  name:", "  assignment_lifecycle: true\n  name:", 1), 0600)
	if code, _, err := f.run("enable"); code != 0 {
		t.Fatal(err)
	}
	goal := filepath.Join(t.TempDir(), "goal.json")
	write(t, goal, `{"title":"Fictional milestone","objectiveRefs":["PRIVATE-OBJECTIVE"],"acceptanceRefs":["PRIVATE-ACCEPTANCE"]}`, 0600)
	if code, _, err := f.run("goal", "create", "--file", goal); code != 0 {
		t.Fatal(err)
	}
	write(t, filepath.Join(f.bbDir, "thr_primary.show"), `{"thread":{"id":"thr_primary","status":"idle"}}`, 0600)
	// An idle pre-upgrade child has no declaration. Current execution never fills it.
	write(t, filepath.Join(f.bbDir, "thr_primary.children"), `[{"id":"thr_example_docs"},{"id":"thr_example_ci"}]`, 0600)
	for _, lane := range []string{"docs", "ci"} {
		write(t, filepath.Join(f.bbDir, "thr_example_"+lane+".show"), `{"thread":{"id":"thr_example_`+lane+`","parentThreadId":"thr_primary","status":"idle"}}`, 0600)
	}
	plan := filepath.Join(t.TempDir(), "plan.json")
	specs := []control.AssignmentSpec{
		{ID: "docs", Lane: "thr_example_docs", ScopeRefs: []string{"PRIVATE-DOCS"}, AuthorizationRefs: []string{"PRIVATE-AUTHORIZATION"}},
		{ID: "ci", Lane: "thr_example_ci", ScopeRefs: []string{"PRIVATE-CI"}, AuthorizationRefs: []string{"PRIVATE-AUTHORIZATION"}},
	}
	save := func(path string, v any) { t.Helper(); b, _ := json.Marshal(v); write(t, path, string(b), 0600) }
	save(plan, map[string]any{"assignments": specs})
	if code, out, err := f.run("assignment", "inventory", "--json"); code != 0 || !strings.Contains(out, `"execution":"idle"`) {
		t.Fatal(code, out, err)
	}
	if code, out, err := f.run("assignment", "review", "--file", plan, "--json"); code != 0 || strings.Contains(out, "PRIVATE-") {
		t.Fatal(code, out, err)
	}
	cfg, _ := config.Load(f.cfg)
	get := func() *control.AssignmentRecord {
		t.Helper()
		r, err := control.LoadAssignments(cfg.Loop.StateDir)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	mutate := func(cmd, path string) {
		t.Helper()
		r := get()
		code, out, err := f.run("assignment", cmd, "--file", path, "--id", r.ID, "--revision", strconv.Itoa(len(r.Revisions)), "--json")
		if code != 0 || strings.Contains(out+err, "PRIVATE-") {
			t.Fatal(code, out, err)
		}
	}
	assertHoldRefused := func() {
		t.Helper()
		before, _ := os.ReadFile(filepath.Join(cfg.Loop.StateDir, control.FileName))
		if code, _, _ := f.run("await-user"); code == 0 {
			t.Fatal("contradictory awaiting-user accepted")
		}
		after, _ := os.ReadFile(filepath.Join(cfg.Loop.StateDir, control.FileName))
		if !bytes.Equal(before, after) {
			t.Fatal("refused hold changed controls")
		}
	}
	assertHoldRefused()
	handoff := filepath.Join(t.TempDir(), "handoff.json")
	ack := filepath.Join(t.TempDir(), "ack.json")
	docs := control.AssignmentHandoff{Assignment: "docs", AssignmentRevision: 1, DeclaredBy: "thr_example_docs", DeclaredAt: time.Now().UTC(), Outcome: "unfinished", Availability: "available", Blocker: "none", EvidenceRefs: []string{"PRIVATE-REVIEW"}, RemainingRefs: []string{"PRIVATE-FIX"}, NextOwner: "coordinator"}
	save(handoff, docs)
	// Worker prepares a validated private document without loop access or caller
	// impersonation. Only the coordinator import below publishes it.
	var out, errs bytes.Buffer
	if code := run(context.Background(), []string{"assignment", "handoff", "--draft", "--file", handoff}, &out, &errs); code != 0 {
		t.Fatal(code, errs.String())
	}
	if get().Revisions[0].Rows[0].Handoff != nil {
		t.Fatal("draft published evidence")
	}
	mutate("handoff", handoff)
	if s := assignmentStatus(cfg, mustAssignmentControl(t, cfg)); s.Counts.Unacknowledged != 1 || s.Counts.Unknown != 2 {
		t.Fatal(s)
	}
	save(ack, map[string]any{"assignment": "docs", "handoffRevision": 1})
	mutate("acknowledge", ack)
	assertHoldRefused() // explicit actionable work remains, even though merge approval could be pending elsewhere
	ci := docs
	ci.Assignment = "ci"
	ci.DeclaredBy = "thr_example_ci"
	ci.Outcome = "complete"
	ci.RemainingRefs = nil
	ci.NextOwner = ""
	save(handoff, ci)
	mutate("handoff", handoff)
	save(ack, map[string]any{"assignment": "ci", "handoffRevision": 1})
	mutate("acknowledge", ack)
	docs.Outcome = "waiting"
	docs.Blocker = "dependency"
	docs.DependencyRefs = []string{"PRIVATE-CI-DEPENDENCY"}
	save(handoff, docs)
	mutate("handoff", handoff)
	save(ack, map[string]any{"assignment": "docs", "handoffRevision": 2})
	mutate("acknowledge", ack)
	assertHoldRefused() // dependency waiting is not person blocked
	docs.Blocker = "person"
	docs.DependencyRefs = []string{"PRIVATE-APPROVAL"}
	save(handoff, docs)
	mutate("handoff", handoff)
	assertHoldRefused() // even person-blocked evidence must be acknowledged
	save(ack, map[string]any{"assignment": "docs", "handoffRevision": 3})
	mutate("acknowledge", ack)
	if code, _, err := f.run("await-user"); code != 0 {
		t.Fatal(err)
	}
	// Metadata never clears an existing hold; explicit safety actions always work.
	for _, cmd := range []string{"pause", "stop"} {
		if code, _, err := f.run(cmd); code != 0 {
			t.Fatal(err)
		}
	}
	for _, cmd := range [][]string{{"status", "--json"}, {"assignment", "show", "--json"}} {
		if code, out, err := f.run(cmd...); code != 0 || strings.Contains(out+err, "PRIVATE-") {
			t.Fatal(code, out, err)
		}
	}
	ctl := mustAssignmentControl(t, cfg)
	if !ctl.Paused || !ctl.Stop || !ctl.AwaitingUser {
		t.Fatal("controls lost")
	}
	if f.read("probes") != "" {
		t.Fatal("metadata probed provider")
	}
	// Corrupt record is visible unavailable and never a false all-clear.
	write(t, filepath.Join(cfg.Loop.StateDir, control.AssignmentName), `{"version":2}`, 0600)
	if s := assignmentStatus(cfg, ctl); s.Availability != "unavailable" || s.Counts != nil {
		t.Fatal(s)
	}
}

func mustAssignmentControl(t *testing.T, cfg *config.Config) control.File {
	t.Helper()
	c, err := control.Load(cfg.Loop.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestAssignmentInventoryPartialAndNoSideEffects(t *testing.T) {
	for _, kind := range []string{"complete", "missing", "parent-change", "list-race", "capped"} {
		t.Run(kind, func(t *testing.T) {
			lists := 0
			invoke := func(ctx context.Context, cli string, args []string, overrides map[string]string, workdir string) ([]byte, error) {
				if cli != "fictional-bb" || len(overrides) != 0 || workdir != "" {
					t.Fatal("unexpected invocation")
				}
				switch args[1] {
				case "list":
					lists++
					if kind == "list-race" && lists == 2 {
						return []byte(`[]`), nil
					}
					if kind == "capped" {
						rows := []map[string]string{}
						for n := 0; n < 200; n++ {
							rows = append(rows, map[string]string{"id": fmt.Sprintf("thr_example_%d", n)})
						}
						return json.Marshal(rows)
					}
					return []byte(`[{"id":"thr_example_docs"}]`), nil
				case "show":
					if kind == "missing" {
						return nil, errors.New("unavailable")
					}
					parent := "thr_example_coordinator"
					if kind == "parent-change" {
						parent = "thr_example_other"
					}
					return json.Marshal(map[string]any{"thread": map[string]string{"id": args[2], "parentThreadId": parent, "status": "idle"}})
				default:
					t.Fatal("inventory attempted non-list/show operation", args)
				}
				return nil, nil
			}
			inv := assignmentInventory(context.Background(), "fictional-bb", "thr_example_coordinator", invoke)
			want := "partial"
			if kind == "complete" {
				want = "observed_direct_children"
			}
			if inv.Coverage != want {
				t.Fatal(inv)
			}
		})
	}
}

func TestAssignmentAwaitUserDoesNotAffectSafetyHolds(t *testing.T) {
	f := newFixture(t)
	t.Setenv("BB_THREAD_ID", "thr_example_coordinator")
	raw, _ := os.ReadFile(f.cfg)
	write(t, f.cfg, strings.Replace(string(raw), "  name:", "  assignment_lifecycle: true\n  limits: {deadline: '2000-01-01T00:00:00Z'}\n  name:", 1), 0600)
	if code, _, err := f.run("enable"); code != 0 {
		t.Fatal(err)
	}
	if code, _, _ := f.run("await-user"); code == 0 {
		t.Fatal("missing assignment evidence allowed explicit awaiting-user")
	}
	result := f.tick()
	cfg, _ := config.Load(f.cfg)
	ctl := mustAssignmentControl(t, cfg)
	if !ctl.AwaitingUser || result["action"] != "await_user" {
		t.Fatal("assignment precondition suppressed automatic safety hold", result, ctl)
	}
	if f.read("calls") != "" || f.read("probes") != "" {
		t.Fatal("deadline safety hold touched provider")
	}
	for _, cmd := range []string{"pause", "stop"} {
		if code, _, err := f.run(cmd); code != 0 {
			t.Fatal(err)
		}
	}
}

func TestAssignmentInitialPartialReviewPreservesObligations(t *testing.T) {
	f := newFixture(t)
	t.Setenv("BB_THREAD_ID", "thr_example_coordinator")
	raw, _ := os.ReadFile(f.cfg)
	write(t, f.cfg, strings.Replace(string(raw), "  name:", "  assignment_lifecycle: true\n  name:", 1), 0600)
	if code, _, err := f.run("enable"); code != 0 {
		t.Fatal(err)
	}
	goal := filepath.Join(t.TempDir(), "goal.json")
	write(t, goal, `{"title":"Fictional milestone","objectiveRefs":["objective"],"acceptanceRefs":["acceptance"]}`, 0600)
	if code, _, err := f.run("goal", "create", "--file", goal); code != 0 {
		t.Fatal(err)
	}
	cfg, _ := config.Load(f.cfg)
	write(t, filepath.Join(f.bbDir, "thr_primary.children"), `[{"id":"thr_example_old"}]`, 0600)
	write(t, filepath.Join(f.bbDir, "thr_primary.show"), `{"thread":{"id":"thr_primary","status":"idle"}}`, 0600)
	// Incorrect parent makes a structured partial read, never inferred idle/done.
	write(t, filepath.Join(f.bbDir, "thr_example_old.show"), `{"thread":{"id":"thr_example_old","parentThreadId":"thr_example_other","status":"idle"}}`, 0600)
	plan := filepath.Join(t.TempDir(), "plan.json")
	write(t, plan, `{"assignments":[{"id":"old","lane":"thr_example_old","scopeRefs":["remaining"],"authorizationRefs":["owner-ref"]}]}`, 0600)
	if code, _, err := f.run("assignment", "review", "--file", plan); code != 0 {
		t.Fatal(err)
	}
	s := assignmentStatus(cfg, mustAssignmentControl(t, cfg))
	if s.Coverage != "partial" || s.Counts.Unknown != 1 {
		t.Fatal(s)
	}
	if code, _, _ := f.run("await-user"); code == 0 {
		t.Fatal("partial initial inventory authorized hold")
	}
	r, err := control.LoadAssignments(cfg.Loop.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(cfg.Loop.StateDir, control.AssignmentName))
	// Disappearance cannot drop unfinished obligations, even with an empty plan.
	write(t, filepath.Join(f.bbDir, "thr_primary.children"), `[]`, 0600)
	write(t, plan, `{"assignments":[]}`, 0600)
	if code, _, _ := f.run("assignment", "review", "--file", plan, "--id", r.ID, "--revision", "1"); code == 0 {
		t.Fatal("unfinished disappeared")
	}
	after, _ := os.ReadFile(filepath.Join(cfg.Loop.StateDir, control.AssignmentName))
	if !bytes.Equal(before, after) {
		t.Fatal("failed review changed evidence")
	}
}

func TestAssignmentStaleTransferredConfigRefusesBeforeQueries(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprint(existing), func(t *testing.T) {
			f := newFixture(t)
			t.Setenv("BB_THREAD_ID", "thr_example_coordinator")
			raw, _ := os.ReadFile(f.cfg)
			write(t, f.cfg, strings.Replace(string(raw), "  name:", "  assignment_lifecycle: true\n  name:", 1), 0600)
			if c, _, e := f.run("enable"); c != 0 {
				t.Fatal(e)
			}
			goal := filepath.Join(t.TempDir(), "goal.json")
			write(t, goal, `{"title":"Example","objectiveRefs":["objective"],"acceptanceRefs":["acceptance"]}`, 0600)
			if c, _, e := f.run("goal", "create", "--file", goal); c != 0 {
				t.Fatal(e)
			}
			cfg, _ := config.Load(f.cfg)
			write(t, filepath.Join(f.bbDir, "thr_primary.show"), `{"thread":{"id":"thr_primary","status":"idle"}}`, 0600)
			write(t, filepath.Join(f.bbDir, "thr_primary.children"), `[]`, 0600)
			plan := filepath.Join(t.TempDir(), "plan.json")
			write(t, plan, `{"assignments":[]}`, 0600)
			var r *control.AssignmentRecord
			if existing {
				if c, _, e := f.run("assignment", "review", "--file", plan); c != 0 {
					t.Fatal(e)
				}
				r, _ = control.LoadAssignments(cfg.Loop.StateDir)
			}
			ctl := mustAssignmentControl(t, cfg)
			ctl.BBTarget = "thr_example_destination"
			if !existing {
				ctl.Ownership.Generation++
			}
			if e := control.Save(cfg.Loop.StateDir, ctl); e != nil {
				t.Fatal(e)
			}
			record := filepath.Join(cfg.Loop.StateDir, control.AssignmentName)
			before, _ := os.ReadFile(record)
			calls := f.read("calls")
			args := []string{"assignment", "review", "--file", plan}
			if r != nil {
				args = append(args, "--id", r.ID, "--revision", "1")
			}
			if c, out, e := f.run(args...); c == 0 {
				t.Fatalf("stale config saved review: %s %s", out, e)
			}
			if c, _, _ := f.run("assignment", "inventory"); c == 0 {
				t.Fatal("stale config queried inventory")
			}
			if f.read("calls") != calls {
				t.Fatal("stale config reached BB")
			}
			after, _ := os.ReadFile(record)
			if !bytes.Equal(before, after) {
				t.Fatal("stale config published evidence")
			}
			if existing {
				s := assignmentStatus(cfg, ctl)
				if s.Availability == "available" || s.Counts != nil {
					t.Fatal("stale source projected current review", s)
				}
			}
		})
	}
}

func TestAssignmentTransferDuringInventoryPreservesPreviousRecord(t *testing.T) {
	f := newFixture(t)
	t.Setenv("BB_THREAD_ID", "thr_example_coordinator")
	raw, _ := os.ReadFile(f.cfg)
	write(t, f.cfg, strings.Replace(string(raw), "  name:", "  assignment_lifecycle: true\n  name:", 1), 0600)
	if c, _, e := f.run("enable"); c != 0 {
		t.Fatal(e)
	}
	goal := filepath.Join(t.TempDir(), "goal.json")
	write(t, goal, `{"title":"Example","objectiveRefs":["objective"],"acceptanceRefs":["acceptance"]}`, 0600)
	if c, _, e := f.run("goal", "create", "--file", goal); c != 0 {
		t.Fatal(e)
	}
	cfg, _ := config.Load(f.cfg)
	plan := filepath.Join(t.TempDir(), "plan.json")
	write(t, plan, `{"assignments":[]}`, 0600)
	changed := false
	invoke := func(ctx context.Context, cli string, args []string, overrides map[string]string, workdir string) ([]byte, error) {
		if args[1] == "list" {
			if !changed {
				ctl := mustAssignmentControl(t, cfg)
				ctl.BBTarget = "thr_example_destination"
				ctl.Ownership.Generation++
				if e := control.Save(cfg.Loop.StateDir, ctl); e != nil {
					t.Fatal(e)
				}
				changed = true
			}
			return []byte(`[]`), nil
		}
		if args[1] != "show" {
			t.Fatal("unexpected operation", args)
		}
		return []byte(`{"thread":{"id":"thr_primary","status":"idle"}}`), nil
	}
	var out, errs bytes.Buffer
	c := assignmentCommand(env{ctx: context.Background(), stdout: &out, stderr: &errs}, []string{"review", "-c", f.cfg, "--file", plan}, invoke)
	if !changed || c == 0 {
		t.Fatal("post-inventory transferred binding accepted", c, errs.String())
	}
	if _, err := os.Stat(filepath.Join(cfg.Loop.StateDir, control.AssignmentName)); !os.IsNotExist(err) {
		t.Fatal("race published review", err)
	}
}
