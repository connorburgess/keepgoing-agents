package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/control"
	"github.com/looptender/looptender/internal/lock"
	"github.com/looptender/looptender/internal/rungraph"
	"github.com/looptender/looptender/internal/state"
)

func TestTransferSupersedeDurableResumeFence(t *testing.T) {
	path, dir := transferApplyFixture(t)
	before, _ := control.Load(dir)
	kept := map[string][]byte{}
	for _, name := range []string{control.GoalName, "state.json", "sends.json", "progress.json"} {
		kept[name], _ = os.ReadFile(filepath.Join(dir, name))
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	e := env{ctx: context.Background(), stdout: &out, stderr: &out}
	code := transferCommand(e, []string{"supersede", "-c", path, "--from", "thr_source", "--successor-thread", "thr_example_successor"}, nil)
	if code != exitOK {
		t.Fatalf("supersede = %d: %s", code, &out)
	}
	after, _ := control.Load(dir)
	if after.Summary() != "superseded" {
		t.Fatal("no durable supersession")
	}
	for _, cmd := range []string{"resume", "enable"} {
		if controlCmd(e, cfg, cmd, "", true) == exitOK {
			t.Fatalf("%s revived obsolete loop", cmd)
		}
	}
	t.Setenv("BB_THREAD_ID", "") // external terminal must also refuse
	if controlCmd(e, cfg, "resume", "") == exitOK {
		t.Fatal("terminal revived obsolete loop")
	}
	if checkTransferSources(cfg, false) == nil {
		t.Fatal("stale daemon can supervise")
	}
	if checkTransferSources(cfg, true) != nil {
		t.Fatal("stop no longer available")
	}
	after.SupersededBy = ""
	after.UpdatedAt = before.UpdatedAt
	if !reflect.DeepEqual(before, after) {
		t.Fatal("supersession changed ownership or holds")
	}

	for name, b := range kept {
		got, _ := os.ReadFile(filepath.Join(dir, name))
		if !bytes.Equal(b, got) {
			t.Fatalf("changed %s", name)
		}
	}
	if strings.Contains(out.String(), "PRIVATE-") {
		t.Fatal("private text exposed")
	}
}

func TestTransferSupersedeRejectsWithoutChangingFiles(t *testing.T) {
	for _, tc := range []string{"same", "wrong_source", "wrong_owner", "pending", "locked", "repeat"} {
		t.Run(tc, func(t *testing.T) {
			path, dir := transferApplyFixture(t)
			from, successor := "thr_source", "thr_example_successor"
			switch tc {
			case "same":
				successor = from
			case "wrong_source":
				from = "thr_other"
			case "wrong_owner":
				t.Setenv("BB_THREAD_ID", "thr_other")
			case "pending":
				_, err := control.Update(dir, func(f *control.File) { f.PendingTransfer = &control.PendingBBTransfer{} })
				if err != nil {
					t.Fatal(err)
				}
			case "repeat":
				_, err := control.Update(dir, func(f *control.File) { f.SupersededBy = "thr_example_previous" })
				if err != nil {
					t.Fatal(err)
				}
			}
			// Lock-file creation is permitted even when the declaration is refused.
			l, err := lock.Acquire(dir)
			if err != nil {
				t.Fatal(err)
			}
			if err := l.Release(); err != nil {
				t.Fatal(err)
			}
			before := transferFiles(t, filepath.Dir(path))
			// Windows refuses reads of the locked byte range. Snapshot before
			// acquiring it and after release, while testing the command under it.
			if tc == "locked" {
				l, err = lock.Acquire(dir)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = l.Release() })
			}
			var out bytes.Buffer
			code := transferCommand(env{ctx: context.Background(), stdout: &out, stderr: &out}, []string{"supersede", "-c", path, "--from", from, "--successor-thread", successor}, nil)
			if tc == "locked" {
				if err := l.Release(); err != nil {
					t.Fatal(err)
				}
				if code != exitSkipped {
					t.Fatalf("held check lock: got %d, want %d: %s", code, exitSkipped, &out)
				}
			}
			if code == exitOK {
				t.Fatal("invalid supersession succeeded")
			}
			if !reflect.DeepEqual(before, transferFiles(t, filepath.Dir(path))) {
				t.Fatal("rejected declaration changed files")
			}
		})
	}
}

func TestSupersededStatusAndPanelNeverOfferResume(t *testing.T) {
	dir, entry := clientFixture(t)
	cfg := mustSupersedeConfig(t, entry.ConfigPath)
	if err := state.Save(entry.StateDir, state.File{}); err != nil {
		t.Fatal(err)
	}
	if err := control.Save(entry.StateDir, control.File{Enabled: true, AwaitingUser: true}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := clientResumeBinding(entry, cfg, ""); err != nil {
		t.Fatalf("baseline resume binding unavailable: %v", err)
	}
	if _, err := control.Update(entry.StateDir, func(f *control.File) { f.SupersededBy = "thr_example_successor" }); err != nil {
		t.Fatal(err)
	}

	r := clientRead(t, dir, entry, false)
	if r.Snapshot == nil || r.Snapshot.ControlState != "superseded" || r.Snapshot.Controls.Enabled || !r.Snapshot.Controls.AwaitingUser {
		t.Fatalf("status = %+v", r)
	}
	in := dockEvidence{controls: r.Snapshot.Controls}.input()
	in.Client = r
	l, need := projectDockLoop(in, dockNow, time.Minute, false)
	if l.Phrase != "Superseded" || need != nil || slices.Contains(l.Can, "resume") {
		t.Fatalf("panel = %+v, need = %+v", l, need)
	}
	if !slices.Contains(l.Can, "stop") {
		t.Fatal("panel lost stop")
	}
	_, _, err := clientResumeBinding(entry, mustSupersedeConfig(t, entry.ConfigPath), "")
	if err == nil {
		t.Fatal("client/panel can prepare stale resume")
	}
}

func mustSupersedeConfig(t *testing.T, path string) *config.Config {
	t.Helper()
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestTransferSupersedeHistoryFailureKeepsFence(t *testing.T) {
	path, dir := transferApplyFixture(t)
	held, err := lock.AcquireNamed(dir, "incident")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	var out bytes.Buffer
	code := transferCommand(env{ctx: context.Background(), stdout: &out, stderr: &out}, []string{"supersede", "-c", path, "--from", "thr_source", "--successor-thread", "thr_example_successor"}, nil)
	f, err := control.Load(dir)
	if code != exitFailed || err != nil || f.SupersededBy != "thr_example_successor" || !strings.Contains(out.String(), "control saved") {
		t.Fatalf("code=%d ctl=%+v err=%v output=%s", code, f, err, &out)
	}
}

func TestSupersededResumeReviewIsRefused(t *testing.T) {
	d, entries := clientReviewWorld(t)
	entry := entries["mobile-onboarding"]
	if _, err := control.Update(entry.StateDir, func(f *control.File) { f.SupersededBy = "thr_example_successor" }); err != nil {
		t.Fatal(err)
	}
	got, code := clientReview(context.Background(), d.directory, dockKey(entry.ID), "resume", dockNow, time.UTC)
	if code == exitOK || got.Preview != nil || got.Result == nil || got.Result.Outcome != "refused" {
		t.Fatalf("impossible resume offered: %+v code=%d", got, code)
	}
	reviewed, refusal := panelReviewForCaller(context.Background(), d.directory, d, dockKey(entry.ID), "resume", "", dockNow)
	if refusal == nil || refusal.Outcome != "refused" || reviewed.preview.Confirm != "" {
		t.Fatalf("panel prepared impossible resume: %+v %+v", reviewed, refusal)
	}
}

func TestSupersessionDoesNotAdoptANewRun(t *testing.T) {
	for _, existing := range []string{"missing", "current", "completed"} {
		t.Run(existing, func(t *testing.T) {
			path, dir := transferApplyFixture(t)
			if _, err := control.Update(dir, func(f *control.File) {
				f.Paused, f.Stop, f.Completed, f.AwaitingUser = false, false, false, false
			}); err != nil {
				t.Fatal(err)
			}
			if existing != "missing" {
				recordRunBoundary(dir, "enable")
				if existing == "completed" {
					if _, err := control.Update(dir, func(f *control.File) { f.Completed = true }); err != nil {
						t.Fatal(err)
					}
					recordRunBoundary(dir, "complete")
				}
			}
			before, _ := rungraph.Load(dir)
			goal, err := os.ReadFile(filepath.Join(dir, control.GoalName))
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			code := transferCommand(env{ctx: context.Background(), stdout: &out, stderr: &out}, []string{"supersede", "-c", path, "--from", "thr_source", "--successor-thread", "thr_example_successor"}, nil)
			if code != exitOK {
				t.Fatalf("supersede = %d: %s", code, &out)
			}
			after, availability := rungraph.Load(dir)
			if (availability != rungraph.Available && availability != rungraph.Missing) || after.Completed != before.Completed || after.CurrentRun != before.CurrentRun || len(after.Runs) != len(before.Runs) || (len(before.Runs) > 0 && !reflect.DeepEqual(after.Runs, before.Runs)) || !reflect.DeepEqual(after.Attempts, before.Attempts) {
				t.Fatalf("retirement changed run evidence: before=%+v after=%+v availability=%s", before, after, availability)
			}
			got, err := os.ReadFile(filepath.Join(dir, control.GoalName))
			if err != nil || !bytes.Equal(got, goal) {
				t.Fatal("retirement changed goal evidence")
			}
		})
	}
}
