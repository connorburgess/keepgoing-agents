package supervise

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/health"
	"github.com/connorburgess/keepgoing-agents/internal/incident"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// An outage as observed on a supervised loop: the retry notice expired while
// the network was still down. Without a connectivity check the three allowed
// re-prompts were spent at once into the dead network (two of them before the
// network returned), leaving one failure away from a hold. With the check,
// nothing is spent while offline, each check records "network down" as its
// own reason, and the first check after connectivity returns re-prompts with
// the whole allowance intact.
func TestOutageDoesNotSpendRepromptsOrProbes(t *testing.T) {
	w := newWorld(t, true, true)
	w.cfg.Loop.Errors.MaxReprompts = 3
	for _, c := range w.cfg.FallbackSet.Candidates {
		w.probeOK[c.ID] = false // every probe would fail into the dead network
	}
	online, checks := false, 0
	retrying := decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown, Retrying: true, RetryKnown: true}
	expired := decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown, RetryKnown: true, RetryExpired: true}
	p := w.primary(decide.Observation{State: decide.Active, ErrorClass: decide.ErrUnknown, Retrying: true, RetryKnown: true})
	clock := func(h, m int) time.Time { return time.Date(2030, 3, 4, h, m, 0, 0, time.UTC) }
	for i, c := range []struct {
		at        time.Time
		obs       decide.Observation
		online    bool
		action    string
		reason    string
		reprompts int
	}{
		{clock(4, 50), p.obs, false, "none", "provider is retrying the turn", 0},
		{clock(5, 0), retrying, false, "none", "provider is retrying the turn", 0},
		{clock(5, 20), expired, false, "none", decide.NetworkDownReason, 0},
		{clock(5, 30), expired, false, "none", decide.NetworkDownReason, 0},
		{clock(5, 45), expired, false, "none", decide.NetworkDownReason, 0},
		{clock(6, 1), expired, false, "none", decide.NetworkDownReason, 0},
		{clock(7, 10), expired, true, "reprompt", "error: unknown", 1}, // the first check after connectivity returns
		{clock(7, 20), decide.Observation{State: decide.Active}, true, "none", "active", 1},
	} {
		p.obs, online = c.obs, c.online
		before := checks
		opts := w.opts(c.at)
		opts.Online = func(context.Context) bool { checks++; return online }
		r, err := Tick(context.Background(), w.cfg, opts)
		st, _ := state.Load(w.dir)
		if err != nil || r.Action != c.action || r.Reason != c.reason || st.Reprompts != c.reprompts || r.Outcome != "succeeded" {
			t.Fatalf("check %d (%s): %+v reprompts=%d %v", i, c.at.Format("15:04"), r, st.Reprompts, err)
		}
		if checks-before > 1 {
			t.Fatalf("check %d: connectivity checked %d times", i, checks-before)
		}
		if down := r.Network == "down"; down != !c.online || st.NetworkDownSince.IsZero() == !c.online && i < 7 {
			t.Fatalf("check %d: network=%q since=%v", i, r.Network, st.NetworkDownSince)
		}
		if !c.online {
			for _, probe := range r.Probes {
				if probe.Skipped != "network_down" {
					t.Fatalf("check %d: probe ran into the dead network: %+v", i, probe)
				}
			}
		}
	}
	if len(p.sent) != 1 || len(w.probes) != 2 {
		t.Fatalf("sends=%d probes=%v", len(p.sent), w.probes)
	}
	if ctl, _ := control.Load(w.dir); ctl.AwaitingUser {
		t.Fatal("the outage ended in a hold")
	}
	// Probes skipped while offline were not recorded as failures.
	reg, _ := health.Load(w.dir)
	for _, id := range reg.IDs() {
		if e := reg.Candidates[id]; e.Last == nil || !e.Last.At.Equal(clock(7, 10)) {
			t.Fatalf("%s: a probe was recorded during the outage: %+v", id, e.Last)
		}
	}
	// The wait is its own cause in incident history. Identical consecutive
	// checks are retained once.
	j, _ := incident.Load(w.dir)
	var causes []string
	var waiting *incident.Record
	for i, r := range j.Records {
		if len(causes) == 0 || causes[len(causes)-1] != r.Cause {
			causes = append(causes, r.Cause)
		}
		if r.Cause == "network_down" && waiting == nil {
			waiting = &j.Records[i]
		}
	}
	if got := strings.Join(causes, ","); got != "retrying,network_down,other,active" {
		t.Fatal(got)
	}
	if e := waiting.Decisions.Evaluations; len(e) != 2 || e[1].Stage != "network_down" || e[0].Action != "reprompt" || e[1].Action != "none" {
		t.Fatalf("%+v", e)
	}
}

// The check is made only when something that needs the network is about to
// be done, and a loop with the check off is never gated.
func TestConnectivityIsCheckedOnlyWhenNeeded(t *testing.T) {
	for _, tc := range []struct {
		name   string
		obs    decide.Observation
		checks int
		action string
	}{
		{"idle wake", decide.Observation{State: decide.Idle}, 0, "wake"},
		{"active", decide.Observation{State: decide.Active}, 0, "none"},
		{"provider retrying", decide.Observation{State: decide.Error, Retrying: true}, 0, "none"},
		{"queued after error", decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown, Queued: 1}, 0, "none"},
		{"error re-prompt", decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown}, 1, "none"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, true, false)
			p := w.primary(tc.obs)
			checks := 0
			opts := w.opts(t0)
			opts.Online = func(context.Context) bool { checks++; return false }
			r, err := Tick(context.Background(), w.cfg, opts)
			if err != nil || checks != tc.checks || r.Action != tc.action || (tc.action == "none" && len(p.sent) != 0) {
				t.Fatalf("%+v checks=%d %v", r, checks, err)
			}
		})
	}
	// No check configured: recovery is attempted as before.
	w := newWorld(t, true, false)
	p := w.primary(decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown})
	if r := runTick(t, w, t0); r.Action != "reprompt" || len(p.sent) != 1 || r.Network != "" {
		t.Fatalf("%+v", r)
	}
	// Stop and pause are handled without any check.
	for _, kind := range []string{"stop", "pause"} {
		w := newWorld(t, true, false)
		w.primary(decide.Observation{State: decide.Active})
		control.Update(w.dir, func(f *control.File) { f.Stop, f.Paused = kind == "stop", kind == "pause" })
		opts := w.opts(t0)
		opts.Online = func(context.Context) bool { t.Fatalf("%s checked connectivity", kind); return false }
		if _, err := Tick(context.Background(), w.cfg, opts); err != nil {
			t.Fatal(err)
		}
	}
}
