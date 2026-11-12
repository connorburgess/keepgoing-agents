package supervise

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// rejectingWorld is a rate-limited primary with two fallbacks that pass their
// exact probe and then reject the handoff brief. Before briefs were counted,
// both were probed and sent to on every check.
func rejectingWorld(t *testing.T, reset bool) (*world, *fakeAdapter, map[string]*fakeAdapter) {
	t.Helper()
	w := newWorld(t, true, true) // max_reprompts 2, backoff 10m
	w.probeOK = map[string]bool{"claude-other": true, "codex-main": true}
	obs := limited
	if reset {
		obs.ResetsAt = t0.Add(5 * time.Hour)
	}
	p := w.primary(obs)
	fallbacks := map[string]*fakeAdapter{}
	for _, id := range []string{"claude-other", "codex-main"} {
		fallbacks[id] = &fakeAdapter{obs: decide.Observation{State: decide.Idle}, sendErr: errors.New("brief rejected")}
		w.adapters[id] = fallbacks[id]
	}
	return w, p, fallbacks
}

func exactProbes(w *world, id string) int {
	return strings.Count(strings.Join(w.probes, ",")+",", id+":exact,")
}

func TestRejectedHandoffBriefsAreCappedAndSpacedPerCandidate(t *testing.T) {
	for _, reset := range []bool{false, true} {
		name := "no reset reported"
		if reset {
			name = "reset reported"
		}
		t.Run(name, func(t *testing.T) {
			w, p, fallbacks := rejectingWorld(t, reset)
			var last Result
			briefAt := map[string][]int{}
			for minute := 0; minute < 40; minute++ {
				before := map[string]int{}
				for id, f := range fallbacks {
					before[id] = len(f.sent)
				}
				last = runTick(t, w, t0.Add(time.Duration(minute)*time.Minute))
				for id, f := range fallbacks {
					if len(f.sent) > before[id] {
						briefAt[id] = append(briefAt[id], minute)
					}
				}
			}
			for id, f := range fallbacks {
				at := briefAt[id]
				if len(f.sent) != 2 || len(at) != 2 || at[0] != 0 || at[1] != 10 || exactProbes(w, id) != 2 {
					t.Fatalf("%s: briefs at %v, exact probes %d", id, at, exactProbes(w, id))
				}
			}
			ctl, _ := control.Load(w.dir)
			st, _ := state.Load(w.dir)
			if st.Target() != state.Primary || st.HandoffBriefs["claude-other"].Failed != 2 || st.HandoffBriefs["codex-main"].Failed != 2 {
				t.Fatalf("state: target=%s briefs=%+v", st.Target(), st.HandoffBriefs)
			}
			if reset {
				// A known reset is waited for; the reason says why no fallback took over.
				if ctl.AwaitingUser || len(p.sent) != 0 || last.Action != "none" ||
					!strings.Contains(last.Reason, "limit resets in") || !strings.Contains(last.Reason, "every confirmed fallback is capped or backing off after rejecting its handoff brief") {
					t.Fatalf("hold=%t primary sends=%d last=%+v", ctl.AwaitingUser, len(p.sent), last)
				}
				if last.Handoff == nil || len(last.Handoff.Tried) != 0 || strings.Join(last.Handoff.Skipped, ",") != "claude-other: capped,codex-main: capped" {
					t.Fatalf("handoff note: %+v", last.Handoff)
				}
				return
			}
			// Without a reset time the existing re-prompt cap raises the hold.
			if !ctl.AwaitingUser || !strings.Contains(ctl.Note, "error persisted after re-prompts") || !strings.Contains(ctl.Note, "every confirmed fallback is capped") ||
				len(p.sent) != 2 || strings.Count(w.reportTypes(), "await_user") != 1 {
				t.Fatalf("hold=%t note=%q primary sends=%d reports=%s", ctl.AwaitingUser, ctl.Note, len(p.sent), w.reportTypes())
			}
		})
	}
}

// One fallback's rejections do not spend another's attempts: the second
// fallback is still offered its brief and takes over once it accepts.
func TestOneRejectingFallbackDoesNotBlockAnother(t *testing.T) {
	w, _, fallbacks := rejectingWorld(t, true)
	for minute := 0; minute < 25; minute++ {
		runTick(t, w, t0.Add(time.Duration(minute)*time.Minute))
	}
	if len(fallbacks["claude-other"].sent) != 2 || len(fallbacks["codex-main"].sent) != 2 {
		t.Fatalf("setup: %d %d", len(fallbacks["claude-other"].sent), len(fallbacks["codex-main"].sent))
	}
	// A new error episode clears every count; now only the first rejects.
	w.adapters[state.Primary].obs = decide.Observation{State: decide.Idle}
	runTick(t, w, t0.Add(30*time.Minute))
	if st, _ := state.Load(w.dir); len(st.HandoffBriefs) != 0 {
		t.Fatalf("episode not cleared: %+v", st.HandoffBriefs)
	}
	obs := limited
	obs.ResetsAt = t0.Add(5 * time.Hour)
	w.adapters[state.Primary].obs = obs
	fallbacks["codex-main"].sendErr = nil
	res := runTick(t, w, t0.Add(31*time.Minute))
	if res.Action != "handoff" || res.Target != "codex-main" || len(fallbacks["claude-other"].sent) != 3 || len(fallbacks["codex-main"].sent) != 3 {
		t.Fatalf("%+v sends %d %d", res, len(fallbacks["claude-other"].sent), len(fallbacks["codex-main"].sent))
	}
	if st, _ := state.Load(w.dir); len(st.HandoffBriefs) != 0 || st.Target() != "codex-main" {
		t.Fatalf("after handoff: %+v", st)
	}
}

// A fallback at its cap is skipped while another, still below its own cap,
// is tried on its own schedule.
func TestCappedFallbackIsSkippedWhileAnotherIsStillTried(t *testing.T) {
	w, _, fallbacks := rejectingWorld(t, true)
	// The first fallback already reached its cap earlier in this episode.
	if err := state.Save(w.dir, state.File{HandoffBriefs: map[string]state.BriefRecord{"claude-other": {Failed: 2, LastAt: t0.Add(-time.Hour)}}}); err != nil {
		t.Fatal(err)
	}
	res := runTick(t, w, t0)
	if len(fallbacks["claude-other"].sent) != 0 || exactProbes(w, "claude-other") != 0 || len(fallbacks["codex-main"].sent) != 1 ||
		res.Handoff == nil || strings.Join(res.Handoff.Skipped, ",") != "claude-other: capped" || strings.Join(res.Handoff.Tried, ",") != "codex-main" {
		t.Fatalf("%+v", res.Handoff)
	}
	if strings.Contains(res.Reason, "every confirmed fallback is capped") {
		t.Fatalf("reason overstates: %s", res.Reason)
	}
	fallbacks["codex-main"].sendErr = nil
	if res := runTick(t, w, t0.Add(10*time.Minute)); res.Action != "handoff" || res.Target != "codex-main" {
		t.Fatalf("%+v", res)
	}
}

// A failed activation and an unsupported send count like a failed send, every
// brief attempt is still reserved against max_sends, and a dry run previews
// the first fallback that would actually be offered a brief.
func TestRejectedBriefKindsSendAllowanceAndDryRun(t *testing.T) {
	w, _, fallbacks := rejectingWorld(t, true)
	w.cfg.Loop.Limits.MaxSends = 10
	fallbacks["claude-other"].activateErr = errors.New("cannot apply model")
	fallbacks["codex-main"].sendErr = adapter.ErrUnsupported{Op: "send"}
	for minute := 0; minute < 30; minute++ {
		runTick(t, w, t0.Add(time.Duration(minute)*time.Minute))
	}
	st, _ := state.Load(w.dir)
	if st.HandoffBriefs["claude-other"].Failed != 2 || st.HandoffBriefs["codex-main"].Failed != 2 || exactProbes(w, "claude-other") != 2 || exactProbes(w, "codex-main") != 2 {
		t.Fatalf("briefs %+v probes %v", st.HandoffBriefs, w.probes)
	}
	// Activation fails before a send is reserved; the two unsupported sends are counted.
	sendsUsed(t, w, 2)
	opts := w.opts(t0.Add(31 * time.Minute))
	opts.DryRun = true
	res, err := Tick(context.Background(), w.cfg, opts)
	if err != nil || res.Handoff == nil || res.Handoff.To != "" {
		t.Fatalf("dry run previews a capped fallback: %+v %v", res.Handoff, err)
	}
}

// Stop and pause win whatever the brief counts are, and probe nothing.
func TestStopAndPauseWinOverBriefAccounting(t *testing.T) {
	for _, hold := range []string{"pause", "stop"} {
		t.Run(hold, func(t *testing.T) {
			w, p, fallbacks := rejectingWorld(t, false)
			runTick(t, w, t0)
			probes := len(w.probes)
			if _, err := control.Update(w.dir, func(f *control.File) { f.Paused, f.Stop = hold == "pause", hold == "stop" }); err != nil {
				t.Fatal(err)
			}
			for _, at := range []time.Duration{10 * time.Minute, 20 * time.Minute} {
				if res := runTick(t, w, t0.Add(at)); res.Action != "none" {
					t.Fatalf("%+v", res)
				}
			}
			p.obs = decide.Observation{State: decide.Active}
			res := runTick(t, w, t0.Add(time.Hour))
			if want := map[string]string{"pause": "none", "stop": "stop"}[hold]; res.Action != want {
				t.Fatalf("%+v", res)
			}
			if len(w.probes) != probes || len(fallbacks["claude-other"].sent) != 1 || len(fallbacks["codex-main"].sent) != 1 {
				t.Fatalf("work under %s: probes %d->%d", hold, probes, len(w.probes))
			}
		})
	}
}

// The primary passes its switch-back probe but rejects the brief. Attempts are
// spaced by retry_every and end at the cap; the fallback keeps working.
func TestRejectedSwitchBackBriefsAreCapped(t *testing.T) {
	w, p, fb := switchBackWorld(t) // retry_every 30m, max_reprompts 2
	w.probeOK["primary"] = true
	p.obs = decide.Observation{State: decide.Idle}
	p.sendErr = errors.New("brief rejected")
	after := t0.Add(2*time.Hour + time.Minute)
	var warned int
	var last Result
	for i := 0; i < 30; i++ {
		last = runTick(t, w, after.Add(time.Duration(i)*10*time.Minute))
		if last.Target != "claude-other" || last.Action != "wake" {
			t.Fatalf("check %d left the fallback idle: %+v", i, last)
		}
		if strings.Contains(strings.Join(last.Warnings, " "), "switch-back stopped") {
			warned++
		}
	}
	if len(p.sent) != 2 || exactProbes(w, "primary") != 2 || warned == 0 || len(fb.sent) != 31 {
		t.Fatalf("primary briefs=%d probes=%d warned=%d fallback sends=%d", len(p.sent), exactProbes(w, "primary"), warned, len(fb.sent))
	}
	st, _ := state.Load(w.dir)
	if st.SwitchBackRejected != 2 || st.Target() != "claude-other" {
		t.Fatalf("state: %+v", st)
	}
	if ctl, _ := control.Load(w.dir); ctl.AwaitingUser {
		t.Fatal("capped switch-back raised a hold")
	}
}
