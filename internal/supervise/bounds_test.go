package supervise

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
)

// repeatedTurns is one round of a session that keeps finishing turns without
// ever changing Git: it is woken, stalls, ends the turn, is woken again and then
// errors. Each step is eligible for exactly one send, and every per-turn counter
// is reset before it can reach its cap.
var repeatedTurns = []struct {
	after time.Duration
	obs   decide.Observation
	want  string
}{
	{0, decide.Observation{State: decide.Idle}, "wake"},
	{90 * time.Minute, decide.Observation{State: decide.Active}, "nudge"},
	{100 * time.Minute, decide.Observation{State: decide.Idle}, "wake"},
	{110 * time.Minute, decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown}, "reprompt"},
}

const roundEvery = 4 * time.Hour

func TestRepeatedWorkAcrossCompletedTurns(t *testing.T) {
	const rounds = 12
	cases := []struct {
		name      string
		limits    config.Limits
		sendFails bool
		wantSends int
		wantHold  string // empty: no loop-wide limit ends the repetition
	}{
		// Twelve rounds of four eligible checks: per-turn caps of one never hold.
		{name: "no loop-wide limit", wantSends: 4 * rounds},
		{name: "max_sends", limits: config.Limits{MaxSends: 10}, wantSends: 10, wantHold: "loop.limits.max_sends"},
		{name: "max_sends counts failed sends", limits: config.Limits{MaxSends: 10}, sendFails: true, wantSends: 10, wantHold: "loop.limits.max_sends"},
		// Rounds start at 0h, 4h and 8h; the 9h cutoff lands inside the third.
		{name: "deadline", limits: config.Limits{Deadline: t0.Add(9 * time.Hour).Format(time.RFC3339)}, wantSends: 9, wantHold: "loop.limits.deadline"},
		// Git never changes after the 0h baseline, so the third round is held.
		{name: "no_progress_timeout", limits: config.Limits{NoProgressTimeout: config.Duration{Duration: 6 * time.Hour}}, wantSends: 8, wantHold: "loop.limits.no_progress_timeout"},
		{name: "earliest of several limits", limits: config.Limits{
			MaxSends: 10, Deadline: t0.Add(9 * time.Hour).Format(time.RFC3339), NoProgressTimeout: config.Duration{Duration: 6 * time.Hour},
		}, wantSends: 8, wantHold: "loop.limits.no_progress_timeout"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, true, false)
			w.cfg.Loop.Limits = tc.limits
			w.cfg.Loop.Progress.Repositories = []string{"/example/repo"}
			w.cfg.Loop.Progress.MaxNudges, w.cfg.Loop.Errors.MaxReprompts = 1, 1
			p := w.primary(decide.Observation{State: decide.Idle})
			if tc.sendFails {
				p.sendErr = errors.New("uncertain delivery")
			}
			check := func(now time.Time) Result {
				t.Helper()
				opts := w.opts(now)
				opts.Fingerprint = func([]string) (string, error) { return "unchanged", nil }
				res, err := Tick(context.Background(), w.cfg, opts)
				if err != nil && !tc.sendFails {
					t.Fatalf("tick: %v", err)
				}
				return res
			}
			runRounds := func(from, to int) {
				t.Helper()
				for round := from; round < to; round++ {
					for _, step := range repeatedTurns {
						p.obs = step.obs
						before := len(p.sent)
						res := check(t0.Add(time.Duration(round)*roundEvery + step.after))
						if sent := len(p.sent) > before; sent && res.Action != step.want {
							t.Fatalf("round %d +%v: sent on %+v, want %s", round, step.after, res, step.want)
						}
					}
				}
			}
			runRounds(0, rounds)
			if len(p.sent) != tc.wantSends {
				t.Fatalf("sends=%d want %d", len(p.sent), tc.wantSends)
			}
			ctl, err := control.Load(w.dir)
			if err != nil {
				t.Fatal(err)
			}
			holds := strings.Count(w.reportTypes(), "await_user")
			if tc.wantHold == "" {
				if ctl.AwaitingUser || holds != 0 {
					t.Fatalf("held without a loop-wide limit: note=%q reports=%s", ctl.Note, w.reportTypes())
				}
				return
			}
			if !ctl.AwaitingUser || !strings.Contains(ctl.Note, tc.wantHold) || holds != 1 {
				t.Fatalf("hold=%t note=%q await_user reports=%d", ctl.AwaitingUser, ctl.Note, holds)
			}
			// The hold persists: every check reloads control and state, and sends nothing.
			runRounds(rounds, rounds+3)
			if len(p.sent) != tc.wantSends || strings.Count(w.reportTypes(), "await_user") != 1 {
				t.Fatalf("after hold: sends=%d reports=%s", len(p.sent), w.reportTypes())
			}
			// Stop still reaches a running turn after the limit hold.
			if _, err := control.Update(w.dir, func(f *control.File) { f.Stop = true }); err != nil {
				t.Fatal(err)
			}
			p.obs, p.sendErr = decide.Observation{State: decide.Active}, nil
			if res := check(t0.Add((rounds + 4) * roundEvery)); res.Action != "stop" || p.stopped != 1 || len(p.sent) != tc.wantSends {
				t.Fatalf("stop after limit: %+v stopped=%d sends=%d", res, p.stopped, len(p.sent))
			}
		})
	}
}

// Pause and stop end the repetition at the next check with no limit configured
// and none reached.
func TestPauseAndStopEndRepeatedWorkWithoutLimits(t *testing.T) {
	for _, hold := range []string{"pause", "stop"} {
		t.Run(hold, func(t *testing.T) {
			w := newWorld(t, true, false)
			p := w.primary(decide.Observation{State: decide.Idle})
			for i := 0; i < 5; i++ {
				if res := runTick(t, w, t0.Add(time.Duration(i)*time.Minute)); res.Action != "wake" {
					t.Fatalf("%+v", res)
				}
			}
			if _, err := control.Update(w.dir, func(f *control.File) { f.Paused, f.Stop = hold == "pause", hold == "stop" }); err != nil {
				t.Fatal(err)
			}
			for i := 5; i < 10; i++ {
				if res := runTick(t, w, t0.Add(time.Duration(i)*time.Minute)); res.Action != "none" {
					t.Fatalf("%+v", res)
				}
			}
			if len(p.sent) != 5 {
				t.Fatalf("sends=%d", len(p.sent))
			}
			p.obs = decide.Observation{State: decide.Active}
			res := runTick(t, w, t0.Add(time.Hour))
			if want := map[string]string{"pause": "none", "stop": "stop"}[hold]; res.Action != want || len(p.sent) != 5 {
				t.Fatalf("active under %s: %+v sends=%d", hold, res, len(p.sent))
			}
		})
	}
}
