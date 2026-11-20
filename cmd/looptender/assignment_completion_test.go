//go:build unix

package main

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/control"
	"github.com/looptender/looptender/internal/lock"
	"github.com/looptender/looptender/internal/state"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The pre-fix baseline accepted this unreviewed completion with lifecycle
// enabled. The separate completion opt-in now refuses the same declaration.
func TestAssignmentCompletionBaselineUnreviewed(t *testing.T) {
	f := newFixture(t)
	t.Setenv("BB_THREAD_ID", "thr_example_owner")
	raw, _ := os.ReadFile(f.cfg)
	write(t, f.cfg, strings.Replace(string(raw), "  name:", "  assignment_lifecycle: true\n  assignment_completion: true\n  name:", 1), 0600)
	if c, _, e := f.run("enable"); c != 0 {
		t.Fatal(e)
	}
	goal := filepath.Join(t.TempDir(), "goal.json")
	write(t, goal, `{"title":"Whole fictional objective","objectiveRefs":["example:objective"],"acceptanceRefs":["example:acceptance"]}`, 0600)
	if c, _, e := f.run("goal", "create", "--file", goal); c != 0 {
		t.Fatal(e)
	}
	cfg, _ := config.Load(f.cfg)
	if c, _, e := f.run("complete"); c == 0 {
		t.Fatalf("completion accepted without any assignment review: %s", e)
	}
	ctl, _ := control.Load(cfg.Loop.StateDir)
	if ctl.Completed {
		t.Fatal("unreviewed completion persisted")
	}
}

func completionFixture(t *testing.T, children bool) (*fixture, *config.Config, string) {
	t.Helper()
	f := newFixture(t)
	t.Setenv("BB_THREAD_ID", "thr_example_owner")
	raw, _ := os.ReadFile(f.cfg)
	write(t, f.cfg, strings.Replace(string(raw), "  name:", "  assignment_lifecycle: true\n  assignment_completion: true\n  name:", 1), 0600)
	if c, _, e := f.run("enable"); c != 0 {
		t.Fatal(e)
	}
	goal := filepath.Join(t.TempDir(), "goal.json")
	write(t, goal, `{"title":"Whole fictional objective","objectiveRefs":["example:objective"],"acceptanceRefs":["example:acceptance"]}`, 0600)
	if c, _, e := f.run("goal", "create", "--file", goal); c != 0 {
		t.Fatal(e)
	}
	write(t, filepath.Join(f.bbDir, "thr_primary.show"), `{"thread":{"id":"thr_primary","status":"idle"}}`, 0600)
	plan := filepath.Join(t.TempDir(), "plan.json")
	body := `{"assignments":[]}`
	if children {
		write(t, filepath.Join(f.bbDir, "thr_primary.children"), `[{"id":"thr_example_docs"}]`, 0600)
		write(t, filepath.Join(f.bbDir, "thr_example_docs.show"), `{"thread":{"id":"thr_example_docs","parentThreadId":"thr_primary","status":"idle"}}`, 0600)
		body = `{"assignments":[{"id":"docs","lane":"thr_example_docs","scopeRefs":["example:docs"],"authorizationRefs":["example:authority"]}]}`
	}
	write(t, plan, body, 0600)
	if c, _, e := f.run("assignment", "review", "--file", plan); c != 0 {
		t.Fatal(e)
	}
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	return f, cfg, plan
}
func completionInput(t *testing.T, cfg *config.Config) string {
	t.Helper()
	r, err := control.LoadAssignments(cfg.Loop.StateDir)
	if err != nil || r == nil {
		t.Fatal(err)
	}
	x := r.Revisions[len(r.Revisions)-1]
	raw, _ := json.Marshal(completionDeclaration{true, x.Binding.Goal, x.Binding.GoalRevision, r.ID, x.Revision})
	path := filepath.Join(t.TempDir(), "completion.json")
	write(t, path, string(raw), 0600)
	return path
}
func completionHandoff(t *testing.T, f *fixture, cfg *config.Config, outcome string, ack bool) {
	t.Helper()
	h := control.AssignmentHandoff{Assignment: "docs", AssignmentRevision: 1, DeclaredBy: "thr_example_docs", DeclaredAt: time.Now().UTC(), Outcome: outcome, Availability: "available", Blocker: "none", EvidenceRefs: []string{"example:verified"}, RemainingRefs: []string{}, DependencyRefs: []string{}}
	if outcome == "unfinished" {
		h.RemainingRefs = []string{"example:unfinished"}
		h.NextOwner = "coordinator"
	}
	p := filepath.Join(t.TempDir(), "handoff.json")
	b, _ := json.Marshal(h)
	write(t, p, string(b), 0600)
	r, _ := control.LoadAssignments(cfg.Loop.StateDir)
	if c, _, e := f.run("assignment", "handoff", "--file", p, "--id", r.ID, "--revision", strconv.Itoa(len(r.Revisions))); c != 0 {
		t.Fatal(e)
	}
	if ack {
		r, _ = control.LoadAssignments(cfg.Loop.StateDir)
		write(t, p, `{"assignment":"docs","handoffRevision":1}`, 0600)
		if c, _, e := f.run("assignment", "acknowledge", "--file", p, "--id", r.ID, "--revision", strconv.Itoa(len(r.Revisions))); c != 0 {
			t.Fatal(e)
		}
	}
}
func completionRefused(t *testing.T, f *fixture, cfg *config.Config, input string) {
	t.Helper()
	// Refusal must leave every existing runtime file byte-for-byte intact.
	before := completionFiles(t, cfg.Loop.StateDir)
	if c, o, e := f.run("complete", "--review-file", input); c == 0 {
		t.Fatalf("completion accepted: %s %s", o, e)
	}
	if after := completionFiles(t, cfg.Loop.StateDir); !reflect.DeepEqual(before, after) {
		t.Fatal("refused completion changed runtime evidence")
	}
}
func completionFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			t.Fatal(err)
		}
		if !d.IsDir() {
			b, e := os.ReadFile(p)
			if e != nil {
				t.Fatal(e)
			}
			out[p] = string(b)
		}
		return nil
	})
	return out
}
func TestAssignmentCompletionJourneyAndHistoricalAbsence(t *testing.T) {
	f, cfg, plan := completionFixture(t, true)
	completionRefused(t, f, cfg, completionInput(t, cfg)) // missing handoff
	completionHandoff(t, f, cfg, "complete", false)
	completionRefused(t, f, cfg, completionInput(t, cfg)) // declaration not acknowledged
	r, _ := control.LoadAssignments(cfg.Loop.StateDir)
	p := filepath.Join(t.TempDir(), "ack.json")
	write(t, p, `{"assignment":"docs","handoffRevision":1}`, 0600)
	if c, _, e := f.run("assignment", "acknowledge", "--file", p, "--id", r.ID, "--revision", strconv.Itoa(len(r.Revisions))); c != 0 {
		t.Fatal(e)
	}
	// Disappearance is a changed snapshot until explicitly reviewed; a complete
	// acknowledged retained row can then be absent without losing history.
	old := completionInput(t, cfg)
	write(t, filepath.Join(f.bbDir, "thr_primary.children"), `[]`, 0600)
	completionRefused(t, f, cfg, old)
	r, _ = control.LoadAssignments(cfg.Loop.StateDir)
	if c, _, e := f.run("assignment", "review", "--file", plan, "--id", r.ID, "--revision", strconv.Itoa(len(r.Revisions))); c != 0 {
		t.Fatal(e)
	}
	r, _ = control.LoadAssignments(cfg.Loop.StateDir)
	if len(r.Revisions[len(r.Revisions)-1].Rows) != 1 {
		t.Fatal("historical obligation removed")
	}
	completionRefused(t, f, cfg, old) // stale review input
	if c, _, e := f.run("complete", "--review-file", completionInput(t, cfg)); c != 0 {
		t.Fatal(e)
	}
	if !mustAssignmentControl(t, cfg).Completed {
		t.Fatal("objective declaration not persisted")
	}
}
func TestAssignmentCompletionUnknownAndLiveNegatives(t *testing.T) {
	for _, kind := range []string{"unfinished-absent", "active", "pending", "stopping", "interaction", "partial", "added", "corrupt", "target", "goal", "caller", "held-self", "fallback", "damaged-state", "null-state", "symlink-state"} {
		t.Run(kind, func(t *testing.T) {
			f, cfg, plan := completionFixture(t, true)
			outcome := "complete"
			if kind == "unfinished-absent" {
				outcome = "unfinished"
			}
			completionHandoff(t, f, cfg, outcome, true)
			switch kind {
			case "unfinished-absent":
				write(t, filepath.Join(f.bbDir, "thr_primary.children"), `[]`, 0600)
				r, _ := control.LoadAssignments(cfg.Loop.StateDir)
				if c, _, e := f.run("assignment", "review", "--file", plan, "--id", r.ID, "--revision", strconv.Itoa(len(r.Revisions))); c != 0 {
					t.Fatal(e)
				}
			case "active", "pending", "stopping":
				write(t, filepath.Join(f.bbDir, "thr_example_docs.show"), `{"thread":{"id":"thr_example_docs","parentThreadId":"thr_primary","status":"`+kind+`"}}`, 0600)
			case "interaction":
				write(t, filepath.Join(f.bbDir, "thr_example_docs.interactions"), `[{"threadId":"thr_example_docs","status":"pending"}]`, 0600)
			case "partial":
				write(t, filepath.Join(f.bbDir, "thr_primary.children"), `null`, 0600)
			case "added":
				write(t, filepath.Join(f.bbDir, "thr_primary.children"), `[{"id":"thr_example_docs"},{"id":"thr_example_unreviewed"}]`, 0600)
			}
			input := completionInput(t, cfg)
			switch kind {
			case "corrupt":
				write(t, filepath.Join(cfg.Loop.StateDir, control.AssignmentName), `{"version":1}`, 0600)
			case "target":
				control.Update(cfg.Loop.StateDir, func(c *control.File) { c.BBTarget = "thr_example_other" })
			case "goal":
				g, _ := control.LoadGoal(cfg.Loop.StateDir)
				s := g.Revisions[0].Spec
				s.Title = "Explicit revised objective"
				if _, err := control.WriteGoal(cfg.Loop.StateDir, cfg.Loop.Name, os.Getenv("BB_THREAD_ID"), s, g.ID, 1); err != nil {
					t.Fatal(err)
				}
			case "fallback":
				write(t, filepath.Join(cfg.Loop.StateDir, state.FileName), `{"activeTarget":"example-fallback"}`, 0600)
			case "damaged-state":
				write(t, filepath.Join(cfg.Loop.StateDir, state.FileName), `{`, 0600)
			case "null-state":
				write(t, filepath.Join(cfg.Loop.StateDir, state.FileName), `null`, 0600)
			case "symlink-state":
				other := filepath.Join(t.TempDir(), "state.json")
				write(t, other, `{}`, 0600)
				if err := os.Symlink(other, filepath.Join(cfg.Loop.StateDir, state.FileName)); err != nil {
					t.Fatal(err)
				}
			case "caller":
				t.Setenv("BB_THREAD_ID", "thr_example_other")
			case "held-self":
				control.Update(cfg.Loop.StateDir, func(c *control.File) { c.AwaitingUser = true })
				t.Setenv(control.SupervisedLoopVar, cfg.Loop.StateDir)
			}
			// Precreate lock files before measuring refusal, as lock creation itself
			// is not an evidence mutation on first entry.
			l, err := lock.Acquire(cfg.Loop.StateDir)
			if err != nil {
				t.Fatal(err)
			}
			l.Release()
			completionRefused(t, f, cfg, input)
		})
	}
}
func TestAssignmentCompletionZeroChildrenAndControls(t *testing.T) {
	f, cfg, _ := completionFixture(t, false)
	input := completionInput(t, cfg)
	raw, _ := os.ReadFile(input)
	for _, bad := range []string{strings.Replace(string(raw), `true`, `false`, 1), strings.Replace(string(raw), `true`, `null`, 1), `{}`, strings.TrimSuffix(string(raw), "}") + `,"wholeObjectiveComplete":true}`, strings.TrimSuffix(string(raw), "}") + `,"private":"never echo"}`} {
		write(t, input, bad, 0600)
		completionRefused(t, f, cfg, input)
	}
	input = completionInput(t, cfg)
	for _, cmd := range []string{"pause", "stop"} {
		if c, _, e := f.run(cmd); c != 0 {
			t.Fatal(e)
		}
	}
	if c, _, e := f.run("complete", "--review-file", input); c != 0 {
		t.Fatal(e)
	}
	c := mustAssignmentControl(t, cfg)
	if !c.Completed || !c.Paused || !c.Stop {
		t.Fatal("pause/stop lost")
	}
}
func TestAssignmentCompletionOptOut(t *testing.T) {
	f := newFixture(t)
	if c, _, e := f.run("complete"); c != 0 {
		t.Fatal(e)
	}
	if strings.Contains(f.read("calls"), "interactions") || f.read("calls") != "" {
		t.Fatal("opt-out queried BB")
	}
}

func TestAssignmentCompletionPublicationRevalidation(t *testing.T) {
	for _, kind := range []string{"source", "goal", "owner", "target", "assignment", "retired", "active-route"} {
		t.Run(kind, func(t *testing.T) {
			f, cfg, _ := completionFixture(t, false)
			input := completionInput(t, cfg)
			raw, _ := os.ReadFile(input)
			d, err := parseCompletion(raw)
			if err != nil {
				t.Fatal(err)
			}
			e := env{ctx: context.WithValue(context.Background(), completionKey{}, d)}
			// Modify the relevant evidence during the structured read itself. The
			// returned snapshot still refers to the pre-query goal/record/owner.
			calls := 0
			invoke := func(ctx context.Context, cli string, args []string, overrides map[string]string, workdir string) ([]byte, error) {
				calls++
				if calls == 1 {
					switch kind {
					case "source":
						raw, _ := os.ReadFile(f.cfg)
						write(t, f.cfg, string(raw)+"\n# explicitly changed source\n", 0600)
					case "goal":
						g, _ := control.LoadGoal(cfg.Loop.StateDir)
						s := g.Revisions[0].Spec
						s.Title = "Revised whole objective"
						if _, err := control.WriteGoal(cfg.Loop.StateDir, cfg.Loop.Name, os.Getenv("BB_THREAD_ID"), s, g.ID, 1); err != nil {
							t.Fatal(err)
						}
					case "owner":
						control.Update(cfg.Loop.StateDir, func(c *control.File) { c.Ownership.Generation++ })
					case "target":
						control.Update(cfg.Loop.StateDir, func(c *control.File) { c.BBTarget = "thr_example_other" })
					case "assignment":
						r, _ := control.LoadAssignments(cfg.Loop.StateDir)
						r.Revisions[0].Inventory.ObservedAt = r.Revisions[0].Inventory.ObservedAt.Add(-time.Second)
						b, _ := json.Marshal(r)
						write(t, filepath.Join(cfg.Loop.StateDir, control.AssignmentName), string(b), 0600)
					case "active-route":
						write(t, filepath.Join(cfg.Loop.StateDir, state.FileName), `{"activeTarget":"example-fallback"}`, 0600)
					case "retired":
						control.Update(cfg.Loop.StateDir, func(c *control.File) { c.SupersededBy = "example-successor" })
					}
				}
				if args[1] == "list" {
					return []byte(`[]`), nil
				}
				return []byte(`{"thread":{"id":"thr_primary","status":"idle"}}`), nil
			}
			snap, err := prepareCompletionWith(e, cfg, invoke)
			if err != nil {
				t.Fatal("pre-query valid fixture unexpectedly failed before publication check", err)
			}
			if err := snap.check(cfg, mustAssignmentControl(t, cfg)); err == nil {
				t.Fatal("changed evidence accepted at control publication")
			}
		})
	}
}
func TestAssignmentCompletionScanRaceAndInput(t *testing.T) {
	f, cfg, _ := completionFixture(t, false)
	raw, _ := os.ReadFile(completionInput(t, cfg))
	d, _ := parseCompletion(raw)
	for _, kind := range []string{"added", "failed", "null", "changed-parent"} {
		lists := 0
		invoke := func(ctx context.Context, cli string, args []string, overrides map[string]string, workdir string) ([]byte, error) {
			if args[1] == "list" {
				lists++
				switch kind {
				case "added":
					if lists > 2 {
						return []byte(`[{"id":"thr_example_new"}]`), nil
					}
				case "failed":
					return nil, errors.New("example unavailable")
				case "null":
					return []byte(`null`), nil
				}
				return []byte(`[]`), nil
			}
			if kind == "changed-parent" {
				return []byte(`{"thread":{"id":"thr_example_other","status":"idle"}}`), nil
			}
			return []byte(`{"thread":{"id":"thr_primary","status":"idle"}}`), nil
		}
		e := env{ctx: context.WithValue(context.Background(), completionKey{}, d)}
		if _, err := prepareCompletionWith(e, cfg, invoke); err == nil {
			t.Fatalf("%s scan accepted", kind)
		}
	}
	for _, bad := range []string{`null`, `[]`, string(raw) + ` {}`, string(raw) + `x`, strings.Repeat(" ", completionInputLimit) + string(raw), strings.Replace(string(raw), `"goalRevision":1`, `"goalRevision":0`, 1)} {
		if _, err := parseCompletion([]byte(bad)); err == nil {
			t.Fatal("invalid declaration accepted")
		}
	}
	input := completionInput(t, cfg)
	linked := filepath.Join(t.TempDir(), "symlink.json")
	if err := os.Symlink(input, linked); err != nil {
		t.Fatal(err)
	}
	completionRefused(t, f, cfg, linked)
	completionRefused(t, f, cfg, filepath.Dir(input))
}

func TestAssignmentCompletionConfigValidation(t *testing.T) {
	f := newFixture(t)
	raw, _ := os.ReadFile(f.cfg)
	write(t, f.cfg, strings.Replace(string(raw), "  name:", "  assignment_completion: true\n  name:", 1), 0600)
	if _, err := config.Load(f.cfg); err == nil {
		t.Fatal("completion accepted without assignment lifecycle")
	}
}
