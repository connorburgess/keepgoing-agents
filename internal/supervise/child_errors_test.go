package supervise

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

func children(ids ...string) []string { return ids }

func notes(f *fakeAdapter) []int {
	var at []int
	for i, text := range f.sent {
		if strings.Contains(text, "[keepgoing:child-errors count=") {
			at = append(at, i)
		}
	}
	return at
}

// An outage puts every child into error. The idle parent is told once, with
// the count, in a wake it would have received anyway; it is told again only
// when a child newly enters error, never because the set shrank.
func TestErroredChildrenAreAnnouncedOnlyWhenOneNewlyFails(t *testing.T) {
	w := newWorld(t, true, false)
	p := w.primary(decide.Observation{State: decide.Idle})
	all := children("thr_child_a", "thr_child_b", "thr_child_c", "thr_child_d", "thr_child_e")
	steps := []struct {
		name     string
		errored  []string
		wantNote int // count announced on this check, 0 for none
	}{
		{"no children in error", nil, 0},
		{"outage: five in error", all, 5},
		{"still five", all, 0},
		{"still five, listed in another order", children("thr_child_e", "thr_child_d", "thr_child_c", "thr_child_b", "thr_child_a"), 0},
		{"one retried and recovered: the set only shrank", all[:4], 0},
		{"still four", all[:4], 0},
		{"another recovered", all[:3], 0},
		{"the one that recovered first fails again", children("thr_child_a", "thr_child_b", "thr_child_c", "thr_child_e"), 4},
		{"still those four", children("thr_child_a", "thr_child_b", "thr_child_c", "thr_child_e"), 0},
		{"all recovered", nil, 0},
		{"still none", nil, 0},
		{"the same five fail again", all, 5},
	}
	for i, step := range steps {
		p.obs.ErroredChildren = step.errored
		before := len(p.sent)
		res := runTick(t, w, t0.Add(time.Duration(i)*time.Minute))
		// Exactly the ordinary idle wake, one per check: errored children add no sends.
		if res.Action != "wake" || res.Reason != "idle" || len(p.sent) != before+1 || res.ErroredChildren != len(step.errored) || res.ChildErrorNote != (step.wantNote > 0) {
			t.Fatalf("%s: %+v sends=%d", step.name, res, len(p.sent)-before)
		}
		text := p.sent[len(p.sent)-1]
		if step.wantNote == 0 {
			if text != "keep going" {
				t.Fatalf("%s: unexpected notice: %q", step.name, text)
			}
			continue
		}
		want := "[keepgoing:child-errors count=" + string(rune('0'+step.wantNote)) + "]"
		if !strings.HasPrefix(text, "keep going\n\n"+want+"\n") || strings.Contains(text, "thr_child") {
			t.Fatalf("%s: %q", step.name, text)
		}
	}
	st, _ := state.Load(w.dir)
	if !reflect.DeepEqual(st.ChildErrorsAnnounced, decide.ChildHashes(all)) || st.LastCheck.ErroredChildren != 5 {
		t.Fatalf("state: announced=%v last=%+v", st.ChildErrorsAnnounced, st.LastCheck)
	}
	raw, _ := os.ReadFile(filepath.Join(w.dir, state.FileName))
	if strings.Contains(string(raw), "thr_child") {
		t.Fatalf("state.json holds a child ID: %s", raw)
	}
	if ctl, _ := control.Load(w.dir); ctl.AwaitingUser {
		t.Fatal("errored children raised a hold")
	}
}

// The notice follows the wake. Whatever defers or forbids the wake defers or
// forbids the notice, and it is delivered on the next wake that happens.
func TestChildErrorNoticeFollowsTheWakeRules(t *testing.T) {
	errored := children("thr_child_a", "thr_child_b")
	cases := []struct {
		name   string
		block  func(w *world, p *fakeAdapter)
		action string
	}{
		{"child discovery incomplete", func(w *world, p *fakeAdapter) { p.obs.ChildrenUnverified = true }, "none"},
		{"parent turn running", func(w *world, p *fakeAdapter) { p.obs.State = decide.Active }, "none"},
		{"parent waiting on a person", func(w *world, p *fakeAdapter) { p.obs.AwaitingInput = true }, "none"},
		{"messages already queued", func(w *world, p *fakeAdapter) { p.obs.Queued = 1 }, "none"},
		{"paused", func(w *world, p *fakeAdapter) { control.Update(w.dir, func(f *control.File) { f.Paused = true }) }, "none"},
		{"parent errored: re-prompt without the notice", func(w *world, p *fakeAdapter) { p.obs.State, p.obs.ErrorClass = decide.Error, decide.ErrUnknown }, "reprompt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, true, false)
			p := w.primary(decide.Observation{State: decide.Idle, ErroredChildren: errored})
			tc.block(w, p)
			for i := 0; i < 3; i++ {
				res, err := Tick(context.Background(), w.cfg, w.opts(t0.Add(time.Duration(i)*20*time.Minute)))
				if err != nil || (i == 0 && res.Action != tc.action) || res.ChildErrorNote {
					t.Fatalf("check %d: %+v %v", i, res, err)
				}
			}
			if len(notes(p)) != 0 {
				t.Fatalf("notice sent while blocked: %q", p.sent)
			}
			// The block lifts: the next wake carries the notice, once.
			control.Update(w.dir, func(f *control.File) { f.Paused, f.AwaitingUser = false, false })
			p.obs = decide.Observation{State: decide.Idle, ErroredChildren: errored}
			sent := len(p.sent)
			for i := 0; i < 3; i++ {
				runTick(t, w, t0.Add(2*time.Hour+time.Duration(i)*time.Minute))
			}
			if got := notes(p); len(got) != 1 || got[0] != sent {
				t.Fatalf("notices at %v after %d earlier sends: %q", got, sent, p.sent)
			}
		})
	}
}

// A child waiting on a person and a stop request keep their precedence, and
// neither sends anything to the parent.
func TestChildInputAndStopWinOverErroredChildren(t *testing.T) {
	w := newWorld(t, true, false)
	p := w.primary(decide.Observation{State: decide.Idle, ErroredChildren: children("thr_child_a", "thr_child_b"), AwaitingChildren: children("thr_child_a")})
	res := runTick(t, w, t0)
	if res.Action != "await_user" || len(p.sent) != 0 || !strings.Contains(res.Reason, "children waiting on a person") {
		t.Fatalf("%+v", res)
	}
	if _, err := control.Update(w.dir, func(f *control.File) { f.Stop = true }); err != nil {
		t.Fatal(err)
	}
	p.obs = decide.Observation{State: decide.Active, ErroredChildren: children("thr_child_a", "thr_child_b")}
	if res := runTick(t, w, t0.Add(time.Minute)); res.Action != "stop" || p.stopped != 1 || len(p.sent) != 0 {
		t.Fatalf("%+v", res)
	}
}

// A notice that was not delivered is not marked as announced, the loop-wide
// send limit still applies, and a dry run changes nothing.
func TestChildErrorNoticeDeliveryLimitsAndDryRun(t *testing.T) {
	errored := children("thr_child_a", "thr_child_b")
	for name, sendErr := range map[string]error{"failed": errors.New("uncertain delivery"), "unsupported": adapter.ErrUnsupported{Op: "send"}} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t, true, false)
			p := w.primary(decide.Observation{State: decide.Idle, ErroredChildren: errored})
			p.sendErr = sendErr
			Tick(context.Background(), w.cfg, w.opts(t0))
			if st, _ := state.Load(w.dir); len(st.ChildErrorsAnnounced) != 0 || st.ChildErrorNoticeAttempts != 1 || len(notes(p)) != 1 {
				t.Fatalf("after a %s send: announced=%v attempts=%d", name, st.ChildErrorsAnnounced, st.ChildErrorNoticeAttempts)
			}
			p.sendErr = nil
			runTick(t, w, t0.Add(time.Minute))
			runTick(t, w, t0.Add(2*time.Minute))
			if got := notes(p); len(got) != 2 || got[1] != 1 {
				t.Fatalf("notices at %v", got)
			}
		})
	}
	t.Run("send limit", func(t *testing.T) {
		w := newWorld(t, true, false)
		w.cfg.Loop.Limits.MaxSends = 2
		p := w.primary(decide.Observation{State: decide.Idle, ErroredChildren: errored})
		for i := 0; i < 5; i++ {
			runTick(t, w, t0.Add(time.Duration(i)*time.Minute))
		}
		sendsUsed(t, w, 2)
		if ctl, _ := control.Load(w.dir); len(p.sent) != 2 || len(notes(p)) != 1 || !ctl.AwaitingUser || !strings.Contains(ctl.Note, "loop.limits.max_sends") {
			t.Fatalf("sends=%d notices=%v hold=%+v", len(p.sent), notes(p), ctl)
		}
	})
	t.Run("dry run", func(t *testing.T) {
		w := newWorld(t, true, false)
		p := w.primary(decide.Observation{State: decide.Idle, ErroredChildren: errored})
		opts := w.opts(t0)
		opts.DryRun = true
		res, err := Tick(context.Background(), w.cfg, opts)
		st, _ := state.Load(w.dir)
		if err != nil || res.Action != "wake" || !res.ChildErrorNote || res.ErroredChildren != 2 || len(p.sent) != 0 || len(st.ChildErrorsAnnounced) != 0 {
			t.Fatalf("%+v announced=%v %v", res, st.ChildErrorsAnnounced, err)
		}
	})
}

// The record of what was announced survives a restart and an older state
// file, and an incomplete child listing does not forget it.
func TestChildErrorNoticeStatePersists(t *testing.T) {
	errored := children("thr_child_a", "thr_child_b")
	w := newWorld(t, true, false)
	p := w.primary(decide.Observation{State: decide.Idle, ErroredChildren: errored})
	runTick(t, w, t0)
	// A restart keeps nothing in memory.
	p = w.primary(decide.Observation{State: decide.Idle, ErroredChildren: errored})
	runTick(t, w, t0.Add(time.Minute))
	if len(notes(p)) != 0 {
		t.Fatal("announced again after a restart")
	}
	// A check that could not list children completely neither wakes nor forgets.
	p.obs = decide.Observation{State: decide.Idle, ChildrenUnverified: true}
	runTick(t, w, t0.Add(2*time.Minute))
	p.obs = decide.Observation{State: decide.Idle, ErroredChildren: errored}
	runTick(t, w, t0.Add(3*time.Minute))
	if len(notes(p)) != 0 {
		t.Fatalf("announced again after an incomplete listing: %q", p.sent)
	}
}

// The one wake errored children cause: the parent is idle and would be left
// alone because another child is still running. It is woken once when a child
// newly fails, with the notice alone and no task prompt, then left alone
// again. A set that only shrinks never wakes it.
func TestErroredChildrenWakeAParentWaitingOnOtherChildrenOnce(t *testing.T) {
	w := newWorld(t, true, false) // min_wake_interval 1h
	w.cfg.Loop.Limits.MaxSends = 10
	p := w.primary(decide.Observation{State: decide.Idle, ActiveChildren: 1})
	steps := []struct {
		name       string
		at         time.Duration
		errored    []string
		active     int
		wantAction string
		wantReason string
		wantNote   bool
	}{
		{"children running, none in error", 0, nil, 1, "none", "child work running", false},
		{"two fail while one still runs", time.Minute, children("thr_child_a", "thr_child_b"), 1, "wake", decide.ChildErrorWakeReason, true},
		{"same set: left alone again", 2 * time.Minute, children("thr_child_a", "thr_child_b"), 1, "none", "child work running", false},
		{"still the same set an hour later", 70 * time.Minute, children("thr_child_a", "thr_child_b"), 1, "none", "child work running", false},
		{"a third fails", 71 * time.Minute, children("thr_child_a", "thr_child_b", "thr_child_c"), 1, "wake", decide.ChildErrorWakeReason, true},
		{"a fourth fails inside the wake interval: waits", 80 * time.Minute, children("thr_child_a", "thr_child_b", "thr_child_c", "thr_child_d"), 1, "none", "child work running", false},
		{"still waiting just before the interval ends", 130 * time.Minute, children("thr_child_a", "thr_child_b", "thr_child_c", "thr_child_d"), 1, "none", "child work running", false},
		{"the interval has passed", 131 * time.Minute, children("thr_child_a", "thr_child_b", "thr_child_c", "thr_child_d"), 1, "wake", decide.ChildErrorWakeReason, true},
		// The parent retries one child, which runs again; the errored set shrank.
		{"one is retried and runs again", 200 * time.Minute, children("thr_child_b", "thr_child_c", "thr_child_d"), 2, "none", "child work running", false},
		{"another is archived", 300 * time.Minute, children("thr_child_c", "thr_child_d"), 2, "none", "child work running", false},
		{"still shrunk hours later", 600 * time.Minute, children("thr_child_c", "thr_child_d"), 2, "none", "child work running", false},
		{"the retried child fails again", 601 * time.Minute, children("thr_child_a", "thr_child_c", "thr_child_d"), 1, "wake", decide.ChildErrorWakeReason, true},
		{"the last child finishes: ordinary wake, nothing new to announce", 602 * time.Minute, children("thr_child_a", "thr_child_c", "thr_child_d"), 0, "wake", "idle", false},
	}
	sends := 0
	for _, step := range steps {
		p.obs.ErroredChildren, p.obs.ActiveChildren = step.errored, step.active
		res := runTick(t, w, t0.Add(step.at))
		if res.Action != step.wantAction || res.Reason != step.wantReason || res.ChildErrorNote != step.wantNote {
			t.Fatalf("%s: %+v", step.name, res)
		}
		if step.wantAction == "wake" {
			sends++
		}
		if len(p.sent) != sends {
			t.Fatalf("%s: sends=%d want %d", step.name, len(p.sent), sends)
		}
		if step.wantNote {
			// Notice only, with both counts; never the "keep going" task prompt.
			want := fmt.Sprintf("[keepgoing:child-errors count=%d running=%d]\n", len(step.errored), step.active)
			if text := p.sent[len(p.sent)-1]; !strings.HasPrefix(text, want) || strings.Contains(text, "keep going") || strings.Contains(text, "thr_child") {
				t.Fatalf("%s: %q", step.name, text)
			}
		}
	}
	if last := p.sent[len(p.sent)-1]; last != "keep going" {
		t.Fatalf("ordinary wake: %q", last)
	}
	// Each of those wakes was reserved against the loop-wide send limit.
	sendsUsed(t, w, 5)
	if got := notes(p); len(got) != 4 {
		t.Fatalf("notices at %v", got)
	}
}

// Holds, limits and a notice that fails to deliver bound that wake too.
func TestChildErrorWakeRespectsHoldsLimitsAndFailedSends(t *testing.T) {
	errored := children("thr_child_a", "thr_child_b")
	obs := decide.Observation{State: decide.Idle, ActiveChildren: 1, ErroredChildren: errored}
	for _, hold := range []string{"paused", "awaiting user", "completed", "send limit", "outside window", "parent active", "discovery incomplete"} {
		t.Run(hold, func(t *testing.T) {
			w := newWorld(t, true, false)
			p := w.primary(obs)
			switch hold {
			case "paused":
				control.Update(w.dir, func(f *control.File) { f.Paused = true })
			case "awaiting user":
				control.Update(w.dir, func(f *control.File) { f.AwaitingUser = true })
			case "completed":
				control.Update(w.dir, func(f *control.File) { f.Completed = true })
			case "send limit":
				w.cfg.Loop.Limits.MaxSends = 1
				if err := state.ReserveSend(w.dir, 1); err != nil {
					t.Fatal(err)
				}
			case "outside window":
				w.cfg.Loop.Window = &config.Window{TZ: "UTC", Days: map[string][]string{"Sat": {"01:00", "02:00"}}}
			case "parent active":
				p.obs.State = decide.Active
			case "discovery incomplete":
				p.obs.ChildrenUnverified = true
			}
			for i := 0; i < 3; i++ {
				res, err := Tick(context.Background(), w.cfg, w.opts(t0.Add(time.Duration(i)*2*time.Hour)))
				if err != nil || res.Action == "wake" || len(p.sent) != 0 {
					t.Fatalf("%s check %d: %+v %v", hold, i, res, err)
				}
			}
		})
	}
	t.Run("stop", func(t *testing.T) {
		w := newWorld(t, true, false)
		p := w.primary(obs)
		control.Update(w.dir, func(f *control.File) { f.Stop = true })
		if res := runTick(t, w, t0); res.Action != "none" || len(p.sent) != 0 {
			t.Fatalf("%+v", res)
		}
	})
	for name, sendErr := range map[string]error{"failed": errors.New("uncertain delivery"), "unsupported": adapter.ErrUnsupported{Op: "send"}} {
		t.Run(name+" send is spaced, capped, then given up", func(t *testing.T) {
			w := newWorld(t, true, false) // max_reprompts 2, min_wake_interval 1h
			p := w.primary(obs)
			p.sendErr = sendErr
			var attempts []int
			var last Result
			for minute := 0; minute <= 720; minute += 10 {
				before := len(p.sent)
				last, _ = Tick(context.Background(), w.cfg, w.opts(t0.Add(time.Duration(minute)*time.Minute)))
				if len(p.sent) > before {
					attempts = append(attempts, minute)
				}
			}
			if len(attempts) != 2 || attempts[0] != 0 || attempts[1] != 60 {
				t.Fatalf("attempts at %v", attempts)
			}
			st, _ := state.Load(w.dir)
			if last.Action != "none" || last.Reason != "child work running" || !last.ChildErrorNoticeStopped || !st.LastCheck.ChildErrorNoticeStopped || st.ChildErrorNoticeAttempts != 2 || len(st.ChildErrorsAnnounced) != 0 {
				t.Fatalf("%+v state=%+v", last, st)
			}
			if ctl, _ := control.Load(w.dir); ctl.AwaitingUser {
				t.Fatal("a given-up notice raised a hold")
			}
			// A different child failing is a new announcement with its own attempts.
			p.obs.ErroredChildren = children("thr_child_a", "thr_child_b", "thr_child_c")
			p.sendErr = nil
			res := runTick(t, w, t0.Add(800*time.Minute))
			if res.Action != "wake" || !res.ChildErrorNote || res.ChildErrorNoticeStopped || !strings.HasPrefix(p.sent[len(p.sent)-1], "[keepgoing:child-errors count=3 running=1]") {
				t.Fatalf("%+v", res)
			}
		})
	}
}

// The notice on ordinary idle wakes is capped too: a send the adapter keeps
// rejecting may still be arriving, so after the cap later wakes go without it.
func TestRejectedNoticeOnOrdinaryWakesIsGivenUp(t *testing.T) {
	w := newWorld(t, true, false) // max_reprompts 2
	p := w.primary(decide.Observation{State: decide.Idle, ErroredChildren: children("thr_child_a", "thr_child_b")})
	p.sendErr = errors.New("uncertain delivery")
	var last Result
	for i := 0; i < 6; i++ {
		last, _ = Tick(context.Background(), w.cfg, w.opts(t0.Add(time.Duration(i)*time.Minute)))
	}
	if got := notes(p); len(p.sent) != 6 || len(got) != 2 || got[1] != 1 || last.ChildErrorNote || !last.ChildErrorNoticeStopped {
		t.Fatalf("sends=%d notices at %v last=%+v", len(p.sent), got, last)
	}
}

// Children belong to one thread. A handoff or switch-back moves the loop to
// another, so what was announced is forgotten, as on retarget and transfer.
func TestChildAnnouncementsAreForgottenWhenTheTargetMoves(t *testing.T) {
	errored := children("thr_child_a", "thr_child_b")
	seed := func(dir string, target string) {
		st, _ := state.Load(dir)
		st.ChildErrorsAnnounced, st.ChildErrorNoticeFor, st.ChildErrorNoticeAttempts = decide.ChildHashes(errored), "pending", 1
		if err := state.Save(dir, st); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("handoff", func(t *testing.T) {
		w := newWorld(t, true, true)
		w.probeOK = map[string]bool{"claude-other": true, "codex-main": true}
		obs := limited
		obs.ErroredChildren = errored
		w.primary(obs)
		seed(w.dir, state.Primary)
		if res := runTick(t, w, t0); res.Action != "handoff" {
			t.Fatalf("%+v", res)
		}
		if st, _ := state.Load(w.dir); len(st.ChildErrorsAnnounced) != 0 || st.ChildErrorNoticeFor != "" || st.ChildErrorNoticeAttempts != 0 {
			t.Fatalf("kept after handoff: %+v", st)
		}
	})
	t.Run("switch-back", func(t *testing.T) {
		w, p, fb := switchBackWorld(t)
		w.probeOK["primary"] = true
		p.obs = decide.Observation{State: decide.Idle}
		fb.obs.ErroredChildren = errored
		seed(w.dir, "claude-other")
		if res := runTick(t, w, t0.Add(2*time.Hour+time.Minute)); res.Action != "switch_back" {
			t.Fatalf("%+v", res)
		}
		if st, _ := state.Load(w.dir); len(st.ChildErrorsAnnounced) != 0 || st.ChildErrorNoticeFor != "" || st.ChildErrorNoticeAttempts != 0 {
			t.Fatalf("kept after switch-back: %+v", st)
		}
	})
}

// A count from an incomplete child listing is marked as a lower bound.
func TestPartialChildScanIsMarked(t *testing.T) {
	w := newWorld(t, true, false)
	w.primary(decide.Observation{State: decide.Idle, ChildrenUnverified: true, ErroredChildren: children("thr_child_a")})
	res := runTick(t, w, t0)
	st, _ := state.Load(w.dir)
	if res.Action != "none" || res.ErroredChildren != 1 || !res.ErroredChildrenPartial || !st.LastCheck.ErroredChildrenPartial {
		t.Fatalf("%+v last=%+v", res, st.LastCheck)
	}
}
