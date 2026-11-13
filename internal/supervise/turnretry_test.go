package supervise

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/incident"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// retryingAdapter is a fake provider that can re-run a failed turn by
// reference. answer decides each retry; the default accepts it.
type retryingAdapter struct {
	*fakeAdapter
	retries int
	answer  func(n int) (adapter.TurnRetry, error)
}

func (r *retryingAdapter) RetryFailedTurn(context.Context) (adapter.TurnRetry, error) {
	r.retries++
	if r.answer != nil {
		return r.answer(r.retries)
	}
	return adapter.TurnRetry{Outcome: adapter.TurnRetried, Attempt: r.retries + 1}, nil
}

// retryWorld is a loop whose primary ended its turn in error and can retry it.
// The world's adapter map holds *fakeAdapter, so the retrying one is supplied
// through the tick options.
type retryWorld struct {
	*world
	a *retryingAdapter
}

func newRetryWorld(t *testing.T, class decide.ErrorClass) *retryWorld {
	t.Helper()
	w := newWorld(t, true, false) // max_reprompts 2, backoff 10m
	w.cfg.Loop.Report = &config.Report{Events: []string{"all"}}
	f := w.primary(decide.Observation{State: decide.Error, ErrorClass: class})
	return &retryWorld{world: w, a: &retryingAdapter{fakeAdapter: f}}
}

func (w *retryWorld) options(at time.Time) Options {
	opts := w.opts(at)
	opts.NewAdapter = func(config.Agent, string, string) (adapter.Adapter, error) { return w.a, nil }
	return opts
}

func (w *retryWorld) tick(t *testing.T, at time.Time) (Result, error) {
	t.Helper()
	return Tick(context.Background(), w.cfg, w.options(at))
}

func (w *retryWorld) mustTick(t *testing.T, at time.Time) Result {
	t.Helper()
	res, err := w.tick(t, at)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	return res
}

func savedState(t *testing.T, dir string) state.File {
	t.Helper()
	st, err := state.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// The stacked-prompt regression: a session that keeps failing used to receive
// a copy of the task prompt for every recovery attempt, and all of them ran
// when it came back. A failed turn is now retried by reference, and a loop
// that never recovers ends in the same hold without one prompt sent.
func TestFailedTurnIsRetriedByReferenceNotPromptedAgain(t *testing.T) {
	for _, class := range []decide.ErrorClass{decide.ErrNetwork, decide.ErrOverloaded, decide.ErrUnknown} {
		w := newRetryWorld(t, class)
		var actions []string
		for i := 0; i < 5; i++ {
			res := w.mustTick(t, t0.Add(time.Duration(i)*11*time.Minute))
			actions = append(actions, res.Action+"/"+res.Recovery)
		}
		// Two attempts spaced by the backoff, then the ordinary hold.
		if got := strings.Join(actions, " "); got != "reprompt/turn_retry reprompt/turn_retry await_user/ none/ none/" {
			t.Fatalf("%s: %s", class, got)
		}
		if w.a.retries != 2 || len(w.a.sent) != 0 {
			t.Fatalf("%s: %d retries, %d prompts sent", class, w.a.retries, len(w.a.sent))
		}
		if got := w.reportTypes(); got != "reprompt,reprompt,await_user" {
			t.Fatalf("%s: reports %s", class, got)
		}
		e := w.reports[0]
		if e.Data["recovery"] != "turn_retry" || e.Data["delivery"] != "retried" || e.Data["attempt"] != 2 || !strings.Contains(e.Text, "retried the failed turn on primary without a new prompt") {
			t.Fatalf("%+v", e)
		}
		st := savedState(t, w.dir)
		if st.LastTurnRetry == nil || st.LastTurnRetry.Outcome != "retried" || st.LastTurnRetry.Target != state.Primary || st.LastTurnRetry.Attempt != 3 || !st.LastTurnRetry.CheckAt.Equal(t0.Add(11*time.Minute)) || st.LastPromptSend != nil {
			t.Fatalf("%+v %+v", st.LastTurnRetry, st.LastPromptSend)
		}
		j, err := incident.Load(w.dir)
		if err != nil || j.Records[0].TurnRetry != "retried" || j.Records[0].Action != "reprompt" || j.Records[2].TurnRetry != "none" || j.Records[2].Action != "await_user" {
			t.Fatalf("%v %+v", err, j.Records)
		}
	}
}

// Counters persist with each check, so the spacing and the cap hold across
// restarts: every tick here loads its state from disk.
func TestTurnRetryHonoursBackoffAcrossRestarts(t *testing.T) {
	w := newRetryWorld(t, decide.ErrNetwork)
	for minute := 0; minute < 10; minute++ {
		w.mustTick(t, t0.Add(time.Duration(minute)*time.Minute))
	}
	st := savedState(t, w.dir)
	if w.a.retries != 1 || st.Reprompts != 1 || !st.LastRepromptAt.Equal(t0) {
		t.Fatalf("%d retries inside the backoff, state %+v", w.a.retries, st)
	}
	w.mustTick(t, t0.Add(10*time.Minute+time.Second))
	if w.a.retries != 2 || savedState(t, w.dir).Reprompts != 2 {
		t.Fatalf("%d retries", w.a.retries)
	}
}

func TestTurnRetryOutcomes(t *testing.T) {
	refused := errors.New("bb thread retry: refused (thread_host_offline)")
	for name, tc := range map[string]struct {
		answer    adapter.TurnRetry
		err       error
		prompts   int
		tickFails bool
		recovery  string
		marker    string
		reports   string
	}{
		"queued behind a busy thread": {adapter.TurnRetry{Outcome: adapter.TurnRetryQueued, Attempt: 2}, nil, 0, false, "turn_retry", "queued", "reprompt"},
		"no failed turn":              {adapter.TurnRetry{Outcome: adapter.TurnRetryNothing}, nil, 1, false, "prompt", "nothing_to_retry", "reprompt"},
		"a retry is already waiting":  {adapter.TurnRetry{Outcome: adapter.TurnRetryAlreadyQueued}, nil, 0, false, "turn_retry", "already_queued", ""},
		"refused":                     {adapter.TurnRetry{}, refused, 0, true, "turn_retry", "refused", "error"},
		"a BB without thread retry":   {adapter.TurnRetry{}, adapter.ErrUnsupported{Op: "turn retry"}, 1, false, "prompt", "unsupported", "reprompt"},
	} {
		w := newRetryWorld(t, decide.ErrNetwork)
		w.cfg.Loop.Limits.MaxSends = 5
		w.a.answer = func(int) (adapter.TurnRetry, error) { return tc.answer, tc.err }
		res, err := w.tick(t, t0)
		if (err != nil) != tc.tickFails || res.Action != "reprompt" || res.Recovery != tc.recovery {
			t.Fatalf("%s: %+v %v", name, res, err)
		}
		st := savedState(t, w.dir)
		// One recovery attempt and one reserved send, whatever the provider said.
		used, _ := state.LoadSends(w.dir)
		if w.a.retries != 1 || len(w.a.sent) != tc.prompts || st.Reprompts != 1 || !st.LastRepromptAt.Equal(t0) || used != 1 {
			t.Fatalf("%s: retries %d prompts %d reprompts %d sends used %d", name, w.a.retries, len(w.a.sent), st.Reprompts, used)
		}
		if st.LastTurnRetry == nil || st.LastTurnRetry.Outcome != tc.marker || w.reportTypes() != tc.reports {
			t.Fatalf("%s: %+v reports %q", name, st.LastTurnRetry, w.reportTypes())
		}
		if (st.LastPromptSend != nil) != (tc.prompts == 1) {
			t.Fatalf("%s: prompt record %+v", name, st.LastPromptSend)
		}
		if j, _ := incident.Load(w.dir); j.Records[0].TurnRetry != tc.marker {
			t.Fatalf("%s: incident marker %s", name, j.Records[0].TurnRetry)
		}
		// Inside the backoff nothing more is asked or sent.
		w.mustTick(t, t0.Add(5*time.Minute))
		if w.a.retries != 1 || len(w.a.sent) != tc.prompts {
			t.Fatalf("%s: repeated inside the backoff", name)
		}
	}
	// A provider that refuses every retry still ends in the hold, never a prompt.
	w := newRetryWorld(t, decide.ErrUnknown)
	w.a.answer = func(int) (adapter.TurnRetry, error) { return adapter.TurnRetry{}, refused }
	var last Result
	for i := 0; i < 4; i++ {
		last, _ = w.tick(t, t0.Add(time.Duration(i)*11*time.Minute))
	}
	ctl, _ := control.Load(w.dir)
	if w.a.retries != 2 || len(w.a.sent) != 0 || !ctl.AwaitingUser || last.Reason != "awaiting user" {
		t.Fatalf("retries %d prompts %d control %+v last %+v", w.a.retries, len(w.a.sent), ctl, last)
	}
}

func TestTurnRetryIsNotUsedWhereItCannotHelp(t *testing.T) {
	// A rate limit with no reported reset is re-prompted as before.
	w := newRetryWorld(t, decide.ErrRateLimit)
	w.cfg.Loop.Errors.FailoverOn = []string{}
	if res := w.mustTick(t, t0); res.Action != "reprompt" || res.Recovery != "prompt" || w.a.retries != 0 || len(w.a.sent) != 1 {
		t.Fatalf("rate limit: %+v retries %d prompts %d", res, w.a.retries, len(w.a.sent))
	}
	// An auth error needs a person: no retry, no prompt.
	w = newRetryWorld(t, decide.ErrAuth)
	if res := w.mustTick(t, t0); res.Action != "await_user" || w.a.retries != 0 || len(w.a.sent) != 0 {
		t.Fatalf("auth: %+v", res)
	}
	// An idle session is woken with the prompt; there is no failed turn.
	w = newRetryWorld(t, decide.ErrNetwork)
	w.a.obs = decide.Observation{State: decide.Idle}
	if res := w.mustTick(t, t0); res.Action != "wake" || res.Recovery != "" || w.a.retries != 0 || len(w.a.sent) != 1 {
		t.Fatalf("idle: %+v", res)
	}
	// An adapter that cannot retry a turn keeps the prompt path.
	plain := newWorld(t, true, false)
	p := plain.primary(decide.Observation{State: decide.Error, ErrorClass: decide.ErrNetwork})
	if res := runTick(t, plain, t0); res.Action != "reprompt" || res.Recovery != "prompt" || len(p.sent) != 1 {
		t.Fatalf("plain adapter: %+v", res)
	}
	if st := savedState(t, plain.dir); st.LastTurnRetry != nil {
		t.Fatalf("%+v", st.LastTurnRetry)
	}
}

func TestStopHoldsDryRunAndSendLimitWinOverTurnRetry(t *testing.T) {
	for _, hold := range []string{"paused", "disabled", "awaiting user", "completed"} {
		w := newRetryWorld(t, decide.ErrNetwork)
		if _, err := control.Update(w.dir, func(f *control.File) {
			switch hold {
			case "paused":
				f.Paused = true
			case "disabled":
				f.Enabled = false
			case "awaiting user":
				f.AwaitingUser = true
			default:
				f.Completed = true
			}
		}); err != nil {
			t.Fatal(err)
		}
		if res := w.mustTick(t, t0); res.Reason != hold || w.a.retries != 0 || len(w.a.sent) != 0 || savedState(t, w.dir).LastTurnRetry != nil {
			t.Fatalf("%s: %+v retries %d", hold, res, w.a.retries)
		}
	}
	w := newRetryWorld(t, decide.ErrNetwork)
	if _, err := control.Update(w.dir, func(f *control.File) { f.Stop = true }); err != nil {
		t.Fatal(err)
	}
	if res := w.mustTick(t, t0); w.a.retries != 0 || len(w.a.sent) != 0 || !strings.HasPrefix(res.Reason, "stop") {
		t.Fatalf("stop: %+v retries %d", res, w.a.retries)
	}
	// A dry run previews the method and asks the provider for nothing.
	w = newRetryWorld(t, decide.ErrNetwork)
	opts := w.options(t0)
	opts.DryRun = true
	res, err := Tick(context.Background(), w.cfg, opts)
	if err != nil || res.Action != "reprompt" || res.Recovery != "turn_retry" || w.a.retries != 0 || len(w.a.sent) != 0 || savedState(t, w.dir).Reprompts != 0 {
		t.Fatalf("dry run: %+v %v", res, err)
	}
	// A spent send allowance holds the loop before any retry.
	w = newRetryWorld(t, decide.ErrNetwork)
	w.cfg.Loop.Limits.MaxSends = 1
	if err := state.ReserveSend(w.dir, 1); err != nil {
		t.Fatal(err)
	}
	if res := w.mustTick(t, t0); res.Action != "await_user" || w.a.retries != 0 || len(w.a.sent) != 0 {
		t.Fatalf("send limit: %+v retries %d", res, w.a.retries)
	}
}

// Recovery must not depend on how long the session sat failed. The host was
// asleep for the whole gap, so no check ran; the first check afterwards makes
// exactly one recovery attempt, spends nothing else and raises no hold. This
// holds whatever the provider then says about a turn that old.
func TestTurnRetryAfterAnOutageOfAnyLength(t *testing.T) {
	for _, gap := range []time.Duration{20 * time.Minute, 4 * time.Hour, 72 * time.Hour, 21 * 24 * time.Hour} {
		for name, tc := range map[string]struct {
			answer  adapter.TurnRetry
			prompts int
			marker  string
		}{
			"still retryable":                  {adapter.TurnRetry{Outcome: adapter.TurnRetried, Attempt: 2}, 0, "retried"},
			"queued":                           {adapter.TurnRetry{Outcome: adapter.TurnRetryQueued, Attempt: 2}, 0, "queued"},
			"the provider has no failed turn":  {adapter.TurnRetry{Outcome: adapter.TurnRetryNothing}, 1, "nothing_to_retry"},
			"a retry was queued while offline": {adapter.TurnRetry{Outcome: adapter.TurnRetryAlreadyQueued}, 0, "already_queued"},
		} {
			label := fmt.Sprintf("%s after %s", name, gap)
			w := newRetryWorld(t, decide.ErrUnknown)
			w.cfg.Loop.Limits.MaxSends = 10
			// Before the outage the session was working; that check is the last one recorded.
			w.a.obs = decide.Observation{State: decide.Active}
			w.mustTick(t, t0)
			// The turn failed and the host slept. No check ran during the gap.
			w.a.obs = decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown}
			w.a.answer = func(int) (adapter.TurnRetry, error) { return tc.answer, nil }
			back := t0.Add(gap)
			res := w.mustTick(t, back)
			st := savedState(t, w.dir)
			ctl, _ := control.Load(w.dir)
			used, _ := state.LoadSends(w.dir)
			if res.Action != "reprompt" || w.a.retries != 1 || len(w.a.sent) != tc.prompts || st.Reprompts != 1 || used != 1 || ctl.AwaitingUser || st.LastTurnRetry.Outcome != tc.marker || !st.LastTurnRetry.CheckAt.Equal(back) {
				t.Fatalf("%s: %+v retries %d prompts %d reprompts %d used %d held %t", label, res, w.a.retries, len(w.a.sent), st.Reprompts, used, ctl.AwaitingUser)
			}
			// The next check, a minute later, adds nothing.
			w.mustTick(t, back.Add(time.Minute))
			if w.a.retries != 1 || len(w.a.sent) != tc.prompts {
				t.Fatalf("%s: a second action followed", label)
			}
			// The retried turn is running: still the same episode, nothing more to do.
			w.a.obs = decide.Observation{State: decide.Active}
			w.mustTick(t, back.Add(2*time.Minute))
			if w.a.retries != 1 || len(w.a.sent) != tc.prompts {
				t.Fatalf("%s: acted on a running turn", label)
			}
			// It finishes without an error: the episode is over and its count clears.
			w.a.obs = decide.Observation{State: decide.Idle}
			w.mustTick(t, back.Add(3*time.Minute))
			if st = savedState(t, w.dir); st.Reprompts != 0 {
				t.Fatalf("%s: reprompts %d after recovery", label, st.Reprompts)
			}
		}
	}
}
