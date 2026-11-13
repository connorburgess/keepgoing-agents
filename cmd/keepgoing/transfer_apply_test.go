package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

func transferApplyFixture(t *testing.T) (string, string) {
	t.Helper()
	t.Setenv("BB_THREAD_ID", "thr_source")
	path, dir := transferFixture(t)
	if err := control.Save(dir, control.File{Enabled: true, Paused: true, Stop: true, Completed: true, AwaitingUser: true, Note: "PRIVATE-NOTE", Ownership: &control.Ownership{ThreadID: "thr_source", Generation: 7}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := state.Save(dir, state.File{LastPromptSend: &state.PromptSend{Version: 1, CheckAt: now, Target: "primary", Action: "wake", Mode: "auto", Outcome: "adapter_returned"}, ActiveTarget: state.Primary, Fingerprint: "fixture", LastProgressAt: now, LastWakeAt: now, Reprompts: 3, Nudges: 4, Handoffs: 2, SwitchBackRejected: 2, ChildErrorsAnnounced: []string{"fictional-child-hash"}, ChildErrorNoticeFor: "fictional-pending", ChildErrorNoticeAttempts: 1, HandoffBriefs: map[string]state.BriefRecord{"fallback-example": {Failed: 2, LastAt: now}}, PrimaryResetsAt: now.Add(time.Hour), LastState: "error", StopRequestedSent: true, AwaitingReported: true, StallRetry: &state.StallRetry{Target: "primary", Waiting: true}, LastCheck: &state.Check{At: now, Target: "primary"}, CheckedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := control.WriteGoal(dir, "fixture", "thr_source", control.GoalSpec{Title: "PRIVATE-GOAL", ObjectiveRefs: []string{"x"}, AcceptanceRefs: []string{"y"}}, "", 0); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"sends.json", "progress.json", "deliveries.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("PRIVATE-UNCHANGED"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(dir, "targets", "primary")
	os.MkdirAll(target, 0700)
	os.WriteFile(filepath.Join(target, "bb-cursor.json"), []byte(`{"thread":"thr_source","lastSeq":999,"blocked":true}`), 0600)
	return path, dir
}

func callTransferApply(t *testing.T, path string, step func(string) error, args ...string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	code := transferApply(env{ctx: context.Background(), stdout: &out, stderr: &out}, append(args, "-c", path), transferRunner(t, nil), step)
	if strings.Contains(out.String(), "PRIVATE-") {
		t.Fatal("private data exposed")
	}
	return code, out.String()
}

func TestTransferApplyAndRecoveryBoundaries(t *testing.T) {
	for _, boundary := range []string{"none", "held", "state", "config"} {
		t.Run(boundary, func(t *testing.T) {
			path, dir := transferApplyFixture(t)
			beforeCtl, _ := control.Load(dir)
			beforeState, _ := state.Load(dir)
			kept := map[string][]byte{}
			for _, name := range []string{control.GoalName, "sends.json", "progress.json", "deliveries.json"} {
				kept[name], _ = os.ReadFile(filepath.Join(dir, name))
			}
			old, err := config.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			oldLease := leaseOf(beforeCtl)
			code, out := callTransferApply(t, path, func(step string) error {
				if step == boundary {
					return errors.New("fixture interruption")
				}
				return nil
			}, "apply", "--from", "thr_source", "--thread", "thr_destination")
			if boundary != "none" {
				if code != exitFailed {
					t.Fatal(code, out)
				}
				c, _ := control.Load(dir)
				if c.PendingTransfer == nil || !c.PendingTransfer.Valid() || !c.AwaitingUser {
					t.Fatal("no recoverable hold")
				}
				fresh, _ := config.Load(path)
				if checkTransferSources(fresh, false) == nil {
					t.Fatal("pending transfer authorized automatic work")
				}
				if checkTransferSources(fresh, true) != nil {
					t.Fatal("pending verified stop blocked")
				}
				var b bytes.Buffer
				e := env{ctx: context.Background(), stdout: &b, stderr: &b}
				if controlCmd(e, fresh, "resume", "") == exitOK || controlCmd(e, fresh, "enable", "") == exitOK {
					t.Fatal("pending transfer resumed/enabled")
				}
				if controlCmd(e, fresh, "pause", "") != exitOK {
					t.Fatal("pending pause refused")
				}
				if _, err := control.WriteGoal(dir, "fixture", "thr_source", control.GoalSpec{Title: "new", ObjectiveRefs: []string{"x"}, AcceptanceRefs: []string{"y"}}, "", 0); err == nil {
					t.Fatal("pending goal mutation accepted")
				}
				code, out = callTransferApply(t, path, nil, "recover", "--id", c.PendingTransfer.ID)
			}
			if code != exitOK {
				t.Fatal(code, out)
			}
			c, _ := control.Load(dir)
			fresh, err := config.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if c.PendingTransfer != nil || c.BBTarget != "thr_destination" || c.Ownership.ThreadID != "thr_destination" || c.Ownership.Generation != 8 || c.Note != beforeCtl.Note || c.Gate() != beforeCtl.Gate() {
				t.Fatalf("holds or ownership wrong: %+v", c)
			}
			if fresh.Loop.BB.Thread != "thr_destination" || checkTransferSources(fresh, false) != nil {
				t.Fatal("new target not bound")
			}
			if checkTransferSources(old, true) == nil || checkOwnership(dir, oldLease) == nil {
				t.Fatal("old snapshot/daemon not fenced")
			}
			alias := filepath.Join(filepath.Dir(path), "old-copy.yaml")
			// A separate old config file can still pass its own source check. The
			// persistent BB binding must refuse it, including identity-free schedulers.
			b, _ := os.ReadFile(path)
			b = bytes.ReplaceAll(b, []byte("thr_destination"), []byte("thr_source"))
			os.WriteFile(alias, b, 0600)
			copied, _ := config.Load(alias)
			if copied.CheckSources() != nil || checkTransferSources(copied, true) == nil {
				t.Fatal("old config alias not fenced")
			}
			st, _ := state.Load(dir)
			if st.StopRequestedSent || st.AwaitingReported || st.StallRetry != nil || st.LastCheck != nil || st.LastPromptSend != nil || st.LastState != "" || !st.PrimaryResetsAt.IsZero() || !st.CheckedAt.IsZero() {
				t.Fatal("old observations survived")
			}
			if len(st.ChildErrorsAnnounced) != 0 || st.ChildErrorNoticeFor != "" || st.ChildErrorNoticeAttempts != 0 {
				t.Fatal("child-error announcements for the old thread survived")
			}
			if len(st.HandoffBriefs) != 0 || st.SwitchBackRejected != 0 {
				t.Fatal("rejected takeover briefs for the old target survived")
			}
			if st.Fingerprint != beforeState.Fingerprint || st.LastProgressAt != beforeState.LastProgressAt || st.LastWakeAt != beforeState.LastWakeAt || st.Nudges != beforeState.Nudges || st.Reprompts != beforeState.Reprompts || st.Handoffs != beforeState.Handoffs {
				t.Fatal("budget/progress refunded")
			}
			for name, b := range kept {
				after, _ := os.ReadFile(filepath.Join(dir, name))
				if !bytes.Equal(b, after) {
					t.Fatalf("%s changed", name)
				}
			}
			if _, err := os.Stat(filepath.Join(dir, "targets", "primary", "bb-cursor.json")); !os.IsNotExist(err) {
				t.Fatal("cursor survived")
			}
		})
	}
}

func TestTransferApplyRefusalsBeforeWrites(t *testing.T) {
	for _, kind := range []string{"stale_from", "wrong_caller", "mismatch", "tick_lock", "control_lock", "config_change", "goal_corrupt", "generation_exhausted"} {
		t.Run(kind, func(t *testing.T) {
			path, dir := transferApplyFixture(t)
			if kind == "generation_exhausted" {
				c, err := control.Load(dir)
				if err != nil {
					t.Fatal(err)
				}
				c.Ownership.Generation = ^uint64(0)
				if err := control.Save(dir, c); err != nil {
					t.Fatal(err)
				}
			}
			args := []string{"apply", "-c", path, "--from", "thr_source", "--thread", "thr_destination"}
			if kind == "stale_from" {
				args[4] = "thr_other"
			}
			if kind == "wrong_caller" {
				t.Setenv("BB_THREAD_ID", "thr_other")
			}
			if kind == "goal_corrupt" {
				os.WriteFile(filepath.Join(dir, control.GoalName), []byte("PRIVATE-CORRUPT"), 0600)
			}
			// Windows byte-range locks also prohibit reads of the lock file.
			// Snapshot outside the held interval; assert refusal while it is held.
			before := transferFiles(t, dir)
			var held *lock.Lock
			if kind == "tick_lock" || kind == "control_lock" {
				name := lock.Tick
				if kind == "control_lock" {
					name = lock.Control
				}
				var err error
				held, err = lock.AcquireNamed(dir, name)
				if err != nil {
					t.Fatal(err)
				}
				defer held.Release()
			}
			runner := transferRunner(t, func(_ string, v map[string]any) {
				if kind == "mismatch" {
					v["thread"].(map[string]any)["providerId"] = "claude-code"
				}
				if kind == "config_change" {
					b, _ := os.ReadFile(path)
					os.WriteFile(path, append(b, '\n'), 0600)
				}
			})
			var out bytes.Buffer
			code := transferApply(env{ctx: context.Background(), stdout: &out, stderr: &out}, args, runner, nil)
			if err := held.Release(); err != nil {
				t.Fatal(err)
			}
			if code == exitOK || strings.Contains(out.String(), "PRIVATE-") {
				t.Fatal(code, out.String())
			}
			after := transferFiles(t, dir)
			delete(before, filepath.Join(dir, "tick.lock"))
			delete(after, filepath.Join(dir, "tick.lock"))
			delete(before, filepath.Join(dir, "control.lock"))
			delete(after, filepath.Join(dir, "control.lock"))
			if !reflect.DeepEqual(before, after) {
				t.Fatal("rejected transfer changed state")
			}
		})
	}
}

func TestTransferRecoveryRejectsUnrelatedConfigAndIdentity(t *testing.T) {
	path, dir := transferApplyFixture(t)
	code, _ := callTransferApply(t, path, func(string) error { return errors.New("interrupt") }, "apply", "--from", "thr_source", "--thread", "thr_destination")
	if code != exitFailed {
		t.Fatal(code)
	}
	c, _ := control.Load(dir)
	if code, _ := callTransferApply(t, path, nil, "recover", "--id", "move_wrong"); code == exitOK {
		t.Fatal("wrong ID accepted")
	}
	b, _ := os.ReadFile(path)
	os.WriteFile(path, append(b, []byte("# changed\n")...), 0600)
	if code, _ := callTransferApply(t, path, nil, "recover", "--id", c.PendingTransfer.ID); code == exitOK {
		t.Fatal("unrelated config accepted")
	}
	after, _ := control.Load(dir)
	if after.PendingTransfer == nil || !after.AwaitingUser {
		t.Fatal("recovery failure cleared hold")
	}
}

func TestTransferDestinationTakeoverAndGeneration(t *testing.T) {
	for _, sameOwner := range []bool{false, true} {
		t.Run(map[bool]string{false: "takeover", true: "same_owner"}[sameOwner], func(t *testing.T) {
			path, dir := transferApplyFixture(t)
			t.Setenv("BB_THREAD_ID", "thr_destination")
			if sameOwner {
				c, _ := control.Load(dir)
				c.Ownership.ThreadID = "thr_destination"
				control.Save(dir, c)
			}
			if !sameOwner {
				if code, _ := callTransferApply(t, path, nil, "apply", "--from", "thr_source", "--thread", "thr_destination"); code == exitOK {
					t.Fatal("implicit takeover accepted")
				}
			}
			if code, out := callTransferApply(t, path, nil, "apply", "--from", "thr_source", "--thread", "thr_destination", "--take-over", "--json"); code != exitOK {
				t.Fatal(out)
			}
			c, _ := control.Load(dir)
			if c.Ownership.Generation != 8 {
				t.Fatal("daemon generation not fenced")
			}
		})
	}
}

func TestTransferPendingStatusHidesDigests(t *testing.T) {
	path, dir := transferApplyFixture(t)
	callTransferApply(t, path, func(string) error { return errors.New("interrupt") }, "apply", "--from", "thr_source", "--thread", "thr_destination")
	c, _ := control.Load(dir)
	if c.PendingTransfer == nil {
		t.Fatal("no record")
	}
	cfg, _ := config.Load(path)
	var out bytes.Buffer
	e := env{ctx: context.Background(), stdout: &out, stderr: &out}
	// Remove unrelated deliberately-invalid ledgers so ordinary status succeeds.
	os.Remove(filepath.Join(dir, "deliveries.json"))
	if status(e, cfg, time.Now(), true, time.Hour) != exitOK {
		t.Fatal("status failed")
	}
	if strings.Contains(out.String(), c.PendingTransfer.Before) || strings.Contains(out.String(), c.PendingTransfer.After) {
		t.Fatal("private digest exposed")
	}
	var data map[string]any
	if json.Unmarshal(out.Bytes(), &data) != nil {
		t.Fatal("invalid status")
	}
}

func TestTransferPrivatePathsRefuseCheckout(t *testing.T) {
	path, dir := transferApplyFixture(t)
	if err := os.Mkdir(filepath.Join(filepath.Dir(path), ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	before := transferFiles(t, dir)
	if code, _ := callTransferApply(t, path, nil, "apply", "--from", "thr_source", "--thread", "thr_destination"); code == exitOK {
		t.Fatal("checkout mutation allowed")
	}
	if !reflect.DeepEqual(before, transferFiles(t, dir)) {
		t.Fatal("checkout refusal wrote state")
	}
}

func TestTransferRecoveryAfterWriteAndHistoryFailures(t *testing.T) {
	for _, kind := range []string{"cursor", "begin_history", "finish_history"} {
		t.Run(kind, func(t *testing.T) {
			path, dir := transferApplyFixture(t)
			var held *lock.Lock
			if kind == "begin_history" {
				var err error
				held, err = lock.AcquireNamed(dir, "incident")
				if err != nil {
					t.Fatal(err)
				}
				defer held.Release()
			}
			cursor := filepath.Join(dir, "targets", "primary", "bb-cursor.json")
			if kind == "cursor" {
				os.Remove(cursor)
				os.Mkdir(cursor, 0700)
			}
			step := func(s string) error {
				if kind == "finish_history" && s == "config" {
					var err error
					held, err = lock.AcquireNamed(dir, "incident")
					if err != nil {
						t.Fatal(err)
					}
				}
				return nil
			}
			code, out := callTransferApply(t, path, step, "apply", "--from", "thr_source", "--thread", "thr_destination", "--json")
			if held != nil {
				held.Release()
			}
			if code != exitFailed {
				t.Fatal(code, out)
			}
			c, _ := control.Load(dir)
			if kind == "finish_history" {
				if c.PendingTransfer != nil || c.BBTarget != "thr_destination" || !strings.Contains(out, `"outcome":"applied"`) {
					t.Fatal("diagnostic failure confused successful commit", out)
				}
				return
			}
			if c.PendingTransfer == nil || !c.AwaitingUser {
				t.Fatal("failure not fenced")
			}
			if kind == "cursor" {
				os.Remove(cursor)
			}
			if code, out := callTransferApply(t, path, nil, "recover", "--id", c.PendingTransfer.ID); code != exitOK {
				t.Fatal(code, out)
			}
		})
	}
}
