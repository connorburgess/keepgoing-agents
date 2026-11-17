package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/looptender/looptender/internal/config"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/control"
)

type reconcileFixture struct {
	f          *inventoryFixture
	dest, plan string
	run        doctorRun
}

func newReconcileFixture(t *testing.T) *reconcileFixture {
	t.Helper()
	f := newInventoryFixture(t, true)
	raw, err := os.ReadFile(f.config)
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(filepath.Dir(f.config), "successor.yaml")
	s := strings.ReplaceAll(string(raw), "example-loop", "example-successor")
	s = strings.ReplaceAll(s, "./state", "./successor-state")
	s = strings.ReplaceAll(s, "thr_target", "thr_successor")
	reconcileWrite(t, dest, s, 0600)
	for _, dir := range []string{"state", "successor-state"} {
		if _, err := control.Update(filepath.Join(filepath.Dir(f.config), dir), func(c *control.File) { c.Paused = true; c.AwaitingUser = true; c.Stop = true }); err != nil {
			t.Fatal(err)
		}
	}
	plan := filepath.Join(filepath.Dir(f.config), "assignments.json")
	reconcileWrite(t, plan, `{"assignments":[{"id":"website","summary":"PRIVATE-ASSIGNMENT","declaredState":"active","laneThread":"thr_lane_a"},{"id":"rebrand","summary":"unfinished dormant work","declaredState":"dormant","laneThread":"thr_lane_b"}]}`, 0600)
	run, _ := f.runner(t, `[{"id":"thr_lane_a"},{"id":"thr_lane_b"}]`, func(id string, v map[string]any) {
		th := v["thread"].(map[string]any)
		if strings.HasPrefix(id, "thr_lane_") {
			th["parentThreadId"] = "thr_target"
		}
		if id == "thr_successor" {
			v["environment"].(map[string]any)["path"] = f.outside
		}
	})
	return &reconcileFixture{f, dest, plan, run}
}
func (f *reconcileFixture) command(t *testing.T, cmd string, extra ...string) (int, map[string]any, string) {
	t.Helper()
	var out, errs bytes.Buffer
	args := []string{"reconcile", cmd, "-c", f.dest, "--json"}
	if cmd != "show" {
		args = append(args, "--source-config", f.f.config, "--file", f.plan)
	}
	args = append(args, extra...)
	code := transferCommand(env{ctx: context.Background(), stdout: &out, stderr: &errs}, args, f.run)
	var r map[string]any
	if out.Len() != 0 {
		if err := json.Unmarshal(out.Bytes(), &r); err != nil {
			t.Fatal(out.String(), err)
		}
	}
	if strings.Contains(out.String()+errs.String(), "PRIVATE-") {
		t.Fatal("private assignment or prompt exposed")
	}
	return code, r, errs.String()
}
func TestTransferReconcileRecordsReviewWithoutActing(t *testing.T) {
	t.Setenv("BB_THREAD_ID", "")
	f := newReconcileFixture(t)
	baseRun := f.run
	f.run = func(ctx context.Context, cli string, args []string, env map[string]string, dir string) ([]byte, error) {
		b, err := baseRun(ctx, cli, args, env, dir)
		if len(args) == 4 && args[1] == "show" && args[2] == "thr_lane_a" {
			b = bytes.ReplaceAll(b, []byte(`"status":"idle"`), []byte(`"status":"active"`))
		}
		return b, err
	}
	sourceControls, _ := os.ReadFile(filepath.Join(filepath.Dir(f.dest), "state", control.FileName))
	destControls, _ := os.ReadFile(filepath.Join(filepath.Dir(f.dest), "successor-state", control.FileName))
	gitBefore := gitSnapshot(t, f.f.repo)
	code, p, e := f.command(t, "preview")
	if code != exitOK {
		t.Fatalf("preview %d %s", code, e)
	}
	digest, _ := p["digest"].(string)
	if len(digest) != 64 {
		t.Fatalf("missing reviewed digest: %v", p)
	}
	code, r, e := f.command(t, "apply", "--digest", digest)
	if code != exitOK {
		t.Fatalf("apply %d %s", code, e)
	}
	saved, err := control.LoadMigrationReview(filepath.Join(filepath.Dir(f.dest), "successor-state"))
	if err != nil || saved.Revisions[0].Observation.Lanes[0].Status != "active" {
		t.Fatal("healthy working lane not retained", err)
	}
	if r["unfinished"] != float64(2) || r["dormant"] != float64(1) || r["coverage"] != "direct_lanes_only" {
		t.Fatalf("lost assignment evidence: %v", r)
	}
	if code, _, e := f.command(t, "show"); code != exitOK {
		t.Fatal(code, e)
	}
	sourceAfter, _ := os.ReadFile(filepath.Join(filepath.Dir(f.dest), "state", control.FileName))
	destAfter, _ := os.ReadFile(filepath.Join(filepath.Dir(f.dest), "successor-state", control.FileName))
	if !bytes.Equal(sourceControls, sourceAfter) || !bytes.Equal(destControls, destAfter) {
		t.Fatal("controls/holds changed")
	}
	if !reflect.DeepEqual(gitBefore, gitSnapshot(t, f.f.repo)) {
		t.Fatal("Git metadata changed")
	}
	cfg, err := config.Load(f.dest)
	if err != nil {
		t.Fatal(err)
	}
	status, err := readStatus(context.Background(), cfg, time.Now(), 0, func(context.Context, string, string, bool) daemonStatus { return daemonStatus{State: "skipped"} })
	if err != nil || status.MigrationReview == nil || status.MigrationReview.Unfinished != 2 || status.MigrationReview.Digest != "" {
		t.Fatal("review undiscoverable or private digest exposed", err)
	}
}

func (f *reconcileFixture) record(t *testing.T) *control.MigrationReview {
	t.Helper()
	code, p, e := f.command(t, "preview")
	if code != exitOK {
		t.Fatalf("preview %d %s", code, e)
	}
	code, _, e = f.command(t, "apply", "--digest", p["digest"].(string))
	if code != exitOK {
		t.Fatalf("apply %d %s", code, e)
	}
	g, err := control.LoadMigrationReview(filepath.Join(filepath.Dir(f.dest), "successor-state"))
	if err != nil || g == nil {
		t.Fatal(g, err)
	}
	return g
}
func TestTransferReconcilePreservesRecordOnRefusal(t *testing.T) {
	for _, kind := range []string{"omitted", "stale-digest", "stale-revision", "wrong-owner", "incomplete", "duplicate", "wrong-parent", "changed-snapshot", "cancelled", "corrupt-goal", "symlink-plan", "symlink-record", "nonregular-plan", "nonregular-record", "worktree-mismatch", "missing-control", "pending-transfer", "bounded-list"} {
		t.Run(kind, func(t *testing.T) {
			t.Setenv("BB_THREAD_ID", "")
			f := newReconcileFixture(t)
			g := f.record(t)
			record := filepath.Join(g.Successor.StateDir, control.MigrationReviewName)
			before, err := os.ReadFile(record)
			if err != nil {
				t.Fatal(err)
			}
			revision := []string{"--id", g.ID, "--revision", "1"}
			code, p, e := f.command(t, "preview", revision...)
			if code != exitOK {
				t.Fatal(code, e)
			}
			digest := p["digest"].(string)
			oldRun := f.run
			switch kind {
			case "worktree-mismatch":
				raw, _ := os.ReadFile(f.plan)
				var plan map[string]any
				if err := json.Unmarshal(raw, &plan); err != nil {
					t.Fatal(err)
				}
				plan["assignments"].([]any)[0].(map[string]any)["expectedWorktree"] = f.f.outside
				b, _ := json.Marshal(plan)
				reconcileWrite(t, f.plan, string(b), 0600)
			case "missing-control":
				if err := os.Remove(filepath.Join(g.Source.StateDir, control.FileName)); err != nil {
					t.Fatal(err)
				}
			case "pending-transfer":
				if _, err := control.Update(g.Successor.StateDir, func(c *control.File) { c.PendingTransfer = &control.PendingBBTransfer{} }); err != nil {
					t.Fatal(err)
				}
			case "bounded-list":
				f.run = func(ctx context.Context, cli string, args []string, env map[string]string, dir string) ([]byte, error) {
					if args[1] == "list" {
						rows := make([]map[string]string, 200)
						for i := range rows {
							rows[i] = map[string]string{"id": fmt.Sprintf("thr_example_%d", i)}
						}
						return json.Marshal(rows)
					}
					return oldRun(ctx, cli, args, env, dir)
				}
			case "omitted":
				reconcileWrite(t, f.plan, `{"assignments":[{"id":"website","summary":"still needed","declaredState":"active","laneThread":"thr_lane_a"}]}`, 0600)
			case "stale-digest":
				digest = strings.Repeat("0", 64)
			case "stale-revision":
				revision = []string{"--id", g.ID, "--revision", "2"}
			case "wrong-owner":
				if _, err := control.Update(g.Successor.StateDir, func(c *control.File) { c.Ownership = &control.Ownership{ThreadID: "thr_other_owner", Generation: 1} }); err != nil {
					t.Fatal(err)
				}
				t.Setenv("BB_THREAD_ID", "thr_successor")
			case "incomplete", "duplicate", "wrong-parent", "changed-snapshot":
				lists := 0
				f.run = func(ctx context.Context, cli string, args []string, env map[string]string, dir string) ([]byte, error) {
					b, err := oldRun(ctx, cli, args, env, dir)
					if args[1] == "list" {
						lists++
						switch kind {
						case "incomplete":
							return []byte(`{"threads":[],"hasMore":true}`), nil
						case "duplicate":
							return []byte(`[{"id":"thr_lane_a"},{"id":"thr_lane_a"}]`), nil
						case "changed-snapshot":
							if lists > 1 {
								return []byte(`[]`), nil
							}
						}
					}
					if kind == "wrong-parent" && args[1] == "show" && args[2] == "thr_lane_a" {
						b = bytes.ReplaceAll(b, []byte(`"parentThreadId":"thr_target"`), []byte(`"parentThreadId":"thr_other"`))
					}
					return b, err
				}
			case "cancelled":
				f.run = func(ctx context.Context, cli string, args []string, env map[string]string, dir string) ([]byte, error) {
					return nil, context.Canceled
				}
			case "corrupt-goal":
				reconcileWrite(t, filepath.Join(g.Source.StateDir, control.GoalName), `{"version":900}`, 0600)
			case "symlink-plan":
				real := f.plan + ".saved"
				if err := os.Rename(f.plan, real); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(real, f.plan); err != nil {
					t.Skip("symlink unavailable")
				}
			case "symlink-record":
				real := record + ".saved"
				if err := os.Rename(record, real); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(real, record); err != nil {
					t.Skip("symlink unavailable")
				}
			case "nonregular-plan":
				if err := os.Remove(f.plan); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(f.plan, 0700); err != nil {
					t.Fatal(err)
				}
			case "nonregular-record":
				if err := os.Rename(record, record+".saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(record, 0700); err != nil {
					t.Fatal(err)
				}
			}
			args := append(append([]string{}, revision...), "--digest", digest)
			if code, _, _ := f.command(t, "apply", args...); code == exitOK {
				t.Fatal("unsafe apply succeeded")
			}
			compare := record
			if kind == "symlink-record" || kind == "nonregular-record" {
				compare = record + ".saved"
			}
			after, err := os.ReadFile(compare)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("saved review changed on refusal", err)
			}
		})
	}
}
func TestTransferReconcileRetainsAbsentDormantAndSeparatesActivity(t *testing.T) {
	t.Setenv("BB_THREAD_ID", "")
	f := newReconcileFixture(t)
	g := f.record(t)
	oldRun := f.run
	f.run = func(ctx context.Context, cli string, args []string, env map[string]string, dir string) ([]byte, error) {
		if args[1] == "list" {
			return []byte(`[{"id":"thr_lane_a"}]`), nil
		}
		return oldRun(ctx, cli, args, env, dir)
	}
	flags := []string{"--id", g.ID, "--revision", "1"}
	code, p, e := f.command(t, "preview", flags...)
	if code != exitOK {
		t.Fatal(code, e)
	}
	code, r, e := f.command(t, "apply", append(flags, "--digest", p["digest"].(string))...)
	if code != exitOK {
		t.Fatal(code, e)
	}
	if r["unfinished"] != float64(2) || r["dormant"] != float64(1) || r["absentAssignments"] != float64(1) {
		t.Fatal(r)
	}
	saved, err := control.LoadMigrationReview(g.Successor.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	latest := saved.Revisions[1]
	if latest.Plan.Assignments[0].DeclaredState != "active" || latest.Observation.Lanes[0].Status != "idle" || latest.Plan.Assignments[1].DeclaredState != "dormant" || !latest.Observation.ObservedAt.After(saved.Revisions[0].Observation.ObservedAt) {
		t.Fatal("activity rewrote declaration or lost evidence date")
	}
}

func reconcileWrite(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

func TestTransferReconcileStatusRejectsCopiedBinding(t *testing.T) {
	t.Setenv("BB_THREAD_ID", "")
	f := newReconcileFixture(t)
	g := f.record(t)
	for _, kind := range []string{"runtime", "config"} {
		t.Run(kind, func(t *testing.T) {
			raw, err := os.ReadFile(f.dest)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(filepath.Dir(f.dest), "copied-"+kind+".yaml")
			if kind == "runtime" {
				raw = bytes.ReplaceAll(raw, []byte("./successor-state"), []byte("./copied-state"))
				dir := filepath.Join(filepath.Dir(f.dest), "copied-state")
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
				record, err := os.ReadFile(filepath.Join(g.Successor.StateDir, control.MigrationReviewName))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, control.MigrationReviewName), record, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			status, err := readStatus(context.Background(), cfg, time.Now(), 0, func(context.Context, string, string, bool) daemonStatus { return daemonStatus{State: "skipped"} })
			if err != nil || status.MigrationReview != nil || status.MigrationReviewError != "binding_changed" {
				t.Fatal("copied review projected as current", status.MigrationReview, status.MigrationReviewError, err)
			}
		})
	}
}
