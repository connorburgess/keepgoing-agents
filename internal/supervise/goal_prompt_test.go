package supervise

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/adapter"
	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/control"
	"github.com/looptender/looptender/internal/decide"
	"github.com/looptender/looptender/internal/prompt"
	"github.com/looptender/looptender/internal/state"
)

func TestGoalPromptRevisionsAcrossTicksAndPrivateHistory(t *testing.T) {
	w := newWorld(t, true, false)
	w.cfg.Loop.PromptGoal = true
	p := w.primary(decide.Observation{State: decide.Idle})
	spec := control.GoalSpec{Title: "fictional-private-old-title", ObjectiveRefs: []string{"fictional-private-objective"}, AcceptanceRefs: []string{"acceptance"}}
	g, err := control.WriteGoal(w.dir, w.cfg.Loop.Name, "", spec, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	runTick(t, w, t0)
	first, err := state.Load(w.dir)
	if err != nil || first.LastPromptSend == nil || first.LastPromptSend.GoalID != g.ID || first.LastPromptSend.GoalRevision != 1 || first.LastPromptSend.Outcome != "adapter_returned" {
		t.Fatalf("first recorded send: %+v %v", first.LastPromptSend, err)
	}
	spec.Title = "fictional-private-new-title"
	if _, err := control.WriteGoal(w.dir, w.cfg.Loop.Name, "", spec, g.ID, 1); err != nil {
		t.Fatal(err)
	}
	runTick(t, w, t0.Add(time.Minute))
	second, err := state.Load(w.dir)
	if err != nil || second.LastPromptSend == nil || second.LastPromptSend.GoalID != g.ID || second.LastPromptSend.GoalRevision != 2 || !second.LastPromptSend.CheckAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("second recorded send: %+v %v", second.LastPromptSend, err)
	}
	if len(p.sent) != 2 || !strings.Contains(p.sent[0], `"revision": 1`) || !strings.Contains(p.sent[1], `"revision": 2`) || strings.Contains(p.sent[1], "old-title") {
		t.Fatalf("sent: %v", p.sent)
	}
	for _, name := range []string{"runs.jsonl", "state.json", "incidents.json"} {
		b, err := os.ReadFile(filepath.Join(w.dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "fictional-private") {
			t.Fatalf("private context in %s", name)
		}
	}
	b, _ := json.Marshal(w.reports)
	if strings.Contains(string(b), "fictional-private") {
		t.Fatal("private context in reports")
	}
}

func TestMissingPromptGoalDoesNotSendOrSpendAndHoldsStillWin(t *testing.T) {
	for _, hold := range []string{"none", "paused", "completed", "stop", "disabled"} {
		t.Run(hold, func(t *testing.T) {
			w := newWorld(t, true, false)
			w.cfg.Loop.PromptGoal = true
			w.cfg.Loop.Limits.MaxSends = 3
			p := w.primary(decide.Observation{State: decide.Idle})
			if hold == "stop" {
				p.obs.State = decide.Active
			}
			if _, err := control.Update(w.dir, func(f *control.File) {
				switch hold {
				case "paused":
					f.Paused = true
				case "completed":
					f.Completed = true
				case "stop":
					f.Stop = true
				case "disabled":
					f.Enabled = false
				}
			}); err != nil {
				t.Fatal(err)
			}
			_, err := Tick(context.Background(), w.cfg, w.opts(t0))
			if (hold == "none") != (err != nil) {
				t.Fatalf("hold %s: %v", hold, err)
			}
			if len(p.sent) != 0 {
				t.Fatal("sent despite absent goal")
			}
			st, err := state.Load(w.dir)
			if err != nil || st.LastPromptSend != nil {
				t.Fatalf("invented send evidence: %+v %v", st.LastPromptSend, err)
			}
			// Nothing was counted against an allowance, and the only intended
			// send recorded is the stop, which is not a prompt.
			if l, err := state.LoadLedger(w.dir); err != nil || *l.Used != 0 || len(l.Pending) > 1 || (len(l.Pending) == 1 && (hold != "stop" || l.Pending[0].Action != "stop")) {
				t.Fatalf("reserved a send: %+v %v", l, err)
			}
			if hold == "stop" && p.stopped != 1 {
				t.Fatal("goal blocked stop")
			}
		})
	}
}

func TestPromptSendFailureAndNonSendingTicks(t *testing.T) {
	for _, sendErr := range []error{errors.New("fictional-private-send-error"), adapter.ErrUnsupported{Op: "send"}} {
		t.Run(sendErr.Error(), func(t *testing.T) {
			w := newWorld(t, true, false)
			w.cfg.Loop.PromptGoal = true
			g, err := control.WriteGoal(w.dir, w.cfg.Loop.Name, "", control.GoalSpec{Title: "private-title", ObjectiveRefs: []string{"private-objective"}, AcceptanceRefs: []string{"private-acceptance"}}, "", 0)
			if err != nil {
				t.Fatal(err)
			}
			p := w.primary(decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown})
			p.sendErr = sendErr
			Tick(context.Background(), w.cfg, w.opts(t0))
			st, err := state.Load(w.dir)
			if err != nil || st.LastPromptSend == nil || st.LastPromptSend.GoalID != g.ID || st.LastPromptSend.GoalRevision != 1 || st.LastPromptSend.Action != "reprompt" || st.LastPromptSend.Outcome != "adapter_error" {
				t.Fatalf("failed-send evidence: %+v %v", st.LastPromptSend, err)
			}
			before, _ := json.Marshal(st.LastPromptSend)
			if strings.Contains(string(before), "private-") {
				t.Fatal("private send data recorded")
			}
			for _, kind := range []string{"active", "dry_run", "paused"} {
				p.obs.State = decide.Active
				o := w.opts(t0.Add(time.Minute))
				if kind == "dry_run" {
					p.obs.State = decide.Idle
					o.DryRun = true
				}
				if kind == "paused" {
					if _, err := control.Update(w.dir, func(f *control.File) { f.Paused = true }); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := Tick(context.Background(), w.cfg, o); err != nil {
					t.Fatal(err)
				}
				st, err = state.Load(w.dir)
				after, _ := json.Marshal(st.LastPromptSend)
				if err != nil || string(before) != string(after) || len(p.sent) != 1 {
					t.Fatalf("%s rewrote historical attempt: %s %v", kind, after, err)
				}
			}
		})
	}
}

func TestQueuedPromptRetainsHistoricalGoalRevision(t *testing.T) {
	w := newWorld(t, true, false)
	w.cfg.Loop.PromptGoal = true
	w.cfg.Loop.Progress.Repositories = []string{"/fictional/repo"}
	spec := control.GoalSpec{Title: "old", ObjectiveRefs: []string{"o"}, AcceptanceRefs: []string{"a"}}
	g, err := control.WriteGoal(w.dir, w.cfg.Loop.Name, "", spec, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	p := w.primary(decide.Observation{State: decide.Active})
	for _, at := range []time.Time{t0, t0.Add(time.Hour)} {
		o := w.opts(at)
		o.Fingerprint = func([]string) (string, error) { return "stable", nil }
		if _, err := Tick(context.Background(), w.cfg, o); err != nil {
			t.Fatal(err)
		}
	}
	spec.Title = "new"
	if _, err := control.WriteGoal(w.dir, w.cfg.Loop.Name, "", spec, g.ID, 1); err != nil {
		t.Fatal(err)
	}
	st, err := state.Load(w.dir)
	if err != nil || st.LastPromptSend == nil || st.LastPromptSend.GoalRevision != 1 || st.LastPromptSend.Mode != "queue" || st.LastPromptSend.Action != "nudge" || len(p.sent) != 1 || !strings.Contains(p.sent[0], `"revision": 1`) {
		t.Fatalf("queue evidence: %+v, messages %v, %v", st.LastPromptSend, p.sent, err)
	}
}

func TestGoalOnChangeSendsFullThenShortAndFullAgainOnEachTrigger(t *testing.T) {
	w := newWorld(t, true, false)
	w.cfg.Loop.PromptGoal, w.cfg.Loop.PromptGoalOnChange = true, true
	p := w.primary(decide.Observation{State: decide.Idle})
	p.convKey, p.convKnown = "example-session-1", true
	spec := control.GoalSpec{Title: "fictional-private-title", ObjectiveRefs: []string{"o"}, AcceptanceRefs: []string{"a"}}
	g, err := control.WriteGoal(w.dir, w.cfg.Loop.Name, "", spec, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	at := t0
	step := func(wantForm string) state.File {
		t.Helper()
		at = at.Add(time.Minute)
		n := len(p.sent)
		if _, err := Tick(context.Background(), w.cfg, w.opts(at)); err != nil && p.sendErr == nil {
			t.Fatalf("tick: %v", err)
		}
		if len(p.sent) != n+1 {
			t.Fatalf("no send at %s", at)
		}
		st, err := state.Load(w.dir)
		if err != nil || st.LastPromptSend == nil {
			t.Fatalf("%+v %v", st.LastPromptSend, err)
		}
		last := p.sent[len(p.sent)-1]
		if st.LastPromptSend.GoalForm != wantForm || st.LastPromptSend.Bytes != len(last) {
			t.Fatalf("send %d: %+v, want %s and %d bytes", len(p.sent), st.LastPromptSend, wantForm, len(last))
		}
		if full := strings.Contains(last, "fictional-private-title"); full != (wantForm == "full") {
			t.Fatalf("send %d form %s: %q", len(p.sent), wantForm, last)
		}
		return st
	}
	step("full")
	step("short")
	if st := step("short"); st.GoalCopy == nil || st.GoalCopy.ShortSends != 2 {
		t.Fatalf("%+v", st.GoalCopy)
	}
	spec.Title = "fictional-private-title revised"
	if _, err := control.WriteGoal(w.dir, w.cfg.Loop.Name, "", spec, g.ID, 1); err != nil {
		t.Fatal(err)
	}
	step("full") // revision
	step("short")
	p.convKey = "" // a session reset: the next turn starts a new conversation
	step("full")
	step("full") // still no saved session
	p.convKey = "example-session-2"
	step("short") // the conversation the full copy started
	p.convKey = "example-session-3"
	step("full")
	p.sendErr = errors.New("example failure")
	p.convKey = ""
	step("full")
	p.sendErr, p.convKey = nil, "example-session-4"
	step("full") // the failed full copy cleared the record
	for i := 0; i < 12; i++ {
		step("short")
	}
	step("full") // short sends at the limit
	for _, name := range []string{"state.json", "runs.jsonl"} {
		b, _ := os.ReadFile(filepath.Join(w.dir, name))
		if strings.Contains(string(b), "fictional-private") || strings.Contains(string(b), "example-session") {
			t.Fatalf("private text or conversation identity in %s", name)
		}
	}
}

func TestGoalOnChangeUnsetSendsTheSameBytes(t *testing.T) {
	w := newWorld(t, true, false)
	w.cfg.Loop.PromptGoal = true
	p := w.primary(decide.Observation{State: decide.Idle})
	p.convKey, p.convKnown = "example-session-1", true
	if _, err := control.WriteGoal(w.dir, w.cfg.Loop.Name, "", control.GoalSpec{Title: "fictional-title", ObjectiveRefs: []string{"o"}, AcceptanceRefs: []string{"a"}}, "", 0); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		runTick(t, w, t0.Add(time.Duration(i)*time.Minute))
	}
	if len(p.sent) != 3 || p.sent[1] != p.sent[0] || p.sent[2] != p.sent[0] || !strings.Contains(p.sent[0], "fictional-title") {
		t.Fatalf("sent: %q", p.sent)
	}
}

// prompt_goal, prompt_goal_on_change and prompt_context compose in every
// combination, in one fixed order: the BB wake header, the person's prompt,
// the goal (in full or as the short line), and the supervision context block
// last. A first tick with prompt_context off leaves a full goal copy, so the
// measured tick carries the short line and the context together.
func TestGoalAndContextPartsKeepTheirOrderInEveryCombination(t *testing.T) {
	const (
		fullGoal  = "Current keepgoing goal context (explicitly shared by this loop):"
		shortGoal = "Current keepgoing goal: goal_"
	)
	for _, c := range []struct{ goal, onChange, context bool }{
		{false, false, false}, {false, false, true},
		{true, false, false}, {true, false, true},
		{true, true, false}, {true, true, true},
	} {
		t.Run(fmt.Sprintf("goal=%v,on_change=%v,context=%v", c.goal, c.onChange, c.context), func(t *testing.T) {
			w := newWorld(t, true, false)
			w.cfg.Loop.PromptGoal, w.cfg.Loop.PromptGoalOnChange = c.goal, c.onChange
			p := w.primary(decide.Observation{State: decide.Idle})
			p.convKey, p.convKnown = "example-session-1", true
			if _, err := control.WriteGoal(w.dir, w.cfg.Loop.Name, "", control.GoalSpec{Title: "fictional-title", ObjectiveRefs: []string{"o"}, AcceptanceRefs: []string{"a"}}, "", 0); err != nil {
				t.Fatal(err)
			}
			tick := func(at time.Duration) string {
				t.Helper()
				o := w.opts(t0.Add(at))
				o.NewAdapter = func(config.Agent, string, string) (adapter.Adapter, error) { return bbNamedAdapter{p}, nil }
				n := len(p.sent)
				if res, err := Tick(context.Background(), w.cfg, o); err != nil || len(p.sent) != n+1 {
					t.Fatalf("at %v: %+v %v", at, res, err)
				}
				return p.sent[n]
			}
			tick(time.Minute) // context off: leaves the full goal copy
			w.cfg.Loop.PromptContext = c.context
			text := tick(2 * time.Minute)

			parts := []string{prompt.WakeHeader(w.cfg.Loop.Name), w.cfg.Loop.Prompt}
			switch {
			case c.onChange:
				parts = append(parts, shortGoal)
			case c.goal:
				parts = append(parts, fullGoal)
			}
			if c.context {
				parts = append(parts, contextTag)
			}
			if !strings.HasPrefix(text, parts[0]) {
				t.Fatalf("wake header not first: %q", text)
			}
			at := 0
			for _, part := range parts {
				i := strings.Index(text[at:], part)
				if i < 0 {
					t.Fatalf("%q missing or out of order in %q", part, text)
				}
				at += i + len(part)
			}
			for marker, want := range map[string]bool{fullGoal: c.goal && !c.onChange, shortGoal: c.onChange, contextTag: c.context} {
				if got := strings.Count(text, marker); got != map[bool]int{true: 1, false: 0}[want] {
					t.Fatalf("%q appears %d times in %q", marker, got, text)
				}
			}
			if c.context && strings.Contains(text[strings.Index(text, contextTag):], "Current keepgoing goal") {
				t.Fatalf("goal after the context block: %q", text)
			}
			st, err := state.Load(w.dir)
			if err != nil || st.LastPromptSend == nil || st.LastPromptSend.Bytes != len(text) || (st.LastPromptSend.ContextBytes > 0) != c.context {
				t.Fatalf("record %+v for %d bytes: %v", st.LastPromptSend, len(text), err)
			}
		})
	}
}

// A handoff brief carries the goal in full even with prompt_goal_on_change,
// and the supervision context block still comes after it, last. The recorded
// size is the whole message, context block included.
func TestHandoffBriefCarriesTheFullGoalBeforeTheContext(t *testing.T) {
	w, _, f := newThreadWorld(t)
	w.cfg.Loop.PromptGoal, w.cfg.Loop.PromptGoalOnChange, w.cfg.Loop.PromptContext = true, true, true
	if _, err := control.WriteGoal(w.dir, w.cfg.Loop.Name, "", control.GoalSpec{Title: "fictional-title", ObjectiveRefs: []string{"o"}, AcceptanceRefs: []string{"a"}}, "", 0); err != nil {
		t.Fatal(err)
	}
	res, err := threadTick(t, w, f, t0, map[string]string{})
	if err != nil || res.Action != "handoff" || len(f.spawned) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	first := f.spawned[0].Prompt
	g, c := strings.Index(first, "Current keepgoing goal context (explicitly shared by this loop):"), strings.Index(first, "\n\n"+contextTag+"\n")
	if g < 0 || c < g || strings.Contains(first, "Current keepgoing goal: goal_") || strings.Contains(first[c:], "Current keepgoing goal") {
		t.Fatalf("brief: %q", first)
	}
	st, _ := state.Load(w.dir)
	if p := st.LastPromptSend; p == nil || p.GoalForm != "full" || p.Bytes != len(first) || p.ContextBytes != len(first)-c {
		t.Fatalf("send=%+v", st.LastPromptSend)
	}
}
