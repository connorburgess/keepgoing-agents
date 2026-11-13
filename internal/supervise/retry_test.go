package supervise

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/incident"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

func TestActiveRetryPreservesNudgesAndRecoveryGrace(t *testing.T) {
	w := newWorld(t, true, false)
	w.cfg.Loop.Progress.MaxNudges = 3
	w.cfg.Loop.Progress.Repositories = []string{"/example/project"}
	p := w.primary(decide.Observation{State: decide.Active, Retrying: true})
	if err := state.Save(w.dir, state.File{Fingerprint: "same", LastProgressAt: t0, LastState: "active", Nudges: 2}); err != nil {
		t.Fatal(err)
	}
	run := func(at time.Time, dry bool) Result {
		opts := w.opts(at)
		opts.DryRun = dry
		opts.Fingerprint = func([]string) (string, error) { return "same", nil }
		r, err := Tick(context.Background(), w.cfg, opts)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	for i := 1; i <= 6; i++ {
		r := run(t0.Add(time.Duration(i)*time.Hour), false)
		st, _ := state.Load(w.dir)
		ctl, _ := control.Load(w.dir)
		if r.Action != "none" || r.Reason != "provider is retrying the turn" || st.Nudges != 2 || ctl.AwaitingUser || len(p.sent) != 0 || !st.LastProgressAt.Equal(t0) {
			t.Fatalf("%+v %+v", r, st)
		}
	}
	// Failed refresh does not announce recovery or clear the pending episode.
	p.obs = decide.Observation{State: decide.Active, RetryUnverified: true}
	if r := run(t0.Add(7*time.Hour), false); r.Reason != "provider retry state unverified" {
		t.Fatal(r)
	}
	p.obs = decide.Observation{State: decide.Active}
	if r := run(t0.Add(8*time.Hour), true); r.Action != "none" {
		t.Fatal(r)
	}
	st, _ := state.Load(w.dir)
	if !st.StallRetry.Waiting {
		t.Fatal("dry run consumed recovery")
	}
	if r := run(t0.Add(9*time.Hour), false); r.Action != "none" {
		t.Fatal("no recovery grace", r)
	}
	if r := run(t0.Add(9*time.Hour+29*time.Minute), false); r.Action != "none" {
		t.Fatal("grace ended early", r)
	}
	if r := run(t0.Add(9*time.Hour+30*time.Minute), false); r.Action != "nudge" {
		t.Fatal("later stall was hidden", r)
	}
	st, _ = state.Load(w.dir)
	if st.Nudges != 3 || !st.LastProgressAt.Equal(t0) {
		t.Fatal("recovery forged Git progress or refunded nudges")
	}
	if r := run(t0.Add(11*time.Hour), false); r.Action != "await_user" {
		t.Fatal("real stall no longer escalates", r)
	}
	// Recovery must not silently clear the explicit hold.
	p.obs = decide.Observation{State: decide.Idle}
	if r := run(t0.Add(12*time.Hour), false); r.Reason != "awaiting user" {
		t.Fatal(r)
	}
}

func TestRetryHardBoundsAndHoldsRemainAuthoritative(t *testing.T) {
	for _, kind := range []string{"deadline", "sends", "progress", "stop", "pause", "completed"} {
		t.Run(kind, func(t *testing.T) {
			w := newWorld(t, true, false)
			p := w.primary(decide.Observation{State: decide.Active, Retrying: true})
			opts := w.opts(t0.Add(2 * time.Hour))
			opts.Fingerprint = func([]string) (string, error) { return "same", nil }
			want := "await_user"
			switch kind {
			case "deadline":
				w.cfg.Loop.Limits.Deadline = t0.Format(time.RFC3339)
			case "sends":
				w.cfg.Loop.Limits.MaxSends = 1
				if err := state.SpendForTest(w.dir, 1); err != nil {
					t.Fatal(err)
				}
			case "progress":
				w.cfg.Loop.Limits.NoProgressTimeout = config.Duration{Duration: time.Hour}
				w.cfg.Loop.Progress.Repositories = []string{"/example/project"}
				_, err := state.ObserveProgress(w.dir, nil, state.ProgressSources(w.cfg.Loop.Progress.Repositories), "same", t0, false)
				if err != nil {
					t.Fatal(err)
				}
			case "stop":
				control.Update(w.dir, func(c *control.File) { c.Stop = true })
				want = "stop"
			case "pause":
				control.Update(w.dir, func(c *control.File) { c.Paused = true })
				want = "none"
			case "completed":
				control.Update(w.dir, func(c *control.File) { c.Completed = true })
				want = "none"
			}
			r, err := Tick(context.Background(), w.cfg, opts)
			if err != nil || r.Action != want || len(p.sent) != 0 {
				t.Fatalf("%+v %v", r, err)
			}
			if kind == "stop" && p.stopped != 1 {
				t.Fatal("retry suppressed stop")
			}
		})
	}
}

func TestRetryGraceTargetIsolationAndRecord(t *testing.T) {
	w := newWorld(t, true, false)
	w.cfg.Loop.Progress.Repositories = []string{"/example/project"}
	w.primary(decide.Observation{State: decide.Active})
	st := state.File{Fingerprint: "same", LastProgressAt: t0, StallRetry: &state.StallRetry{Target: "old-target", Waiting: true}}
	if err := state.Save(w.dir, st); err != nil {
		t.Fatal(err)
	}
	opts := w.opts(t0.Add(time.Hour))
	opts.Fingerprint = func([]string) (string, error) { return "same", nil }
	r, err := Tick(context.Background(), w.cfg, opts)
	if err != nil || r.Action != "nudge" {
		t.Fatal("another target granted grace", r, err)
	}
	w.adapters[state.Primary].obs.Retrying = true
	opts.Now = t0.Add(2 * time.Hour)
	r, err = Tick(context.Background(), w.cfg, opts)
	if err != nil || r.Action != "none" {
		t.Fatal(r, err)
	}
	b, err := os.ReadFile(filepath.Join(w.dir, state.LogName))
	if err != nil || !strings.Contains(string(b), "provider is retrying the turn") {
		t.Fatal("reason missing from own history")
	}
}

// An errored target whose retry notice has expired gets the ordinary bounded
// recovery: re-prompts spaced by errors.backoff up to max_reprompts, then a
// persisted hold. An unknown error never moves the loop to a fallback.
func TestExpiredRetrySignalRecoversThenHolds(t *testing.T) {
	w := newWorld(t, true, true)
	w.cfg.Loop.Errors.FailoverOn = []string{"rate_limit", "network"}
	for _, c := range w.cfg.FallbackSet.Candidates {
		w.probeOK[c.ID] = true
	}
	p := w.primary(decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown, Retrying: true, RetryKnown: true})
	check := func(minute int, action, reason, signal string, sends int) {
		t.Helper()
		r := runTick(t, w, t0.Add(time.Duration(minute)*time.Minute))
		st, _ := state.Load(w.dir)
		if r.Action != action || r.Reason != reason || r.RetrySignal != signal || st.LastCheck.RetrySignal != signal || len(p.sent) != sends || r.Handoff != nil || st.Target() != state.Primary {
			t.Fatalf("+%dm: %+v sent=%d", minute, r, len(p.sent))
		}
	}
	check(0, "none", "provider is retrying the turn", "", 0)
	check(10, "none", "provider is retrying the turn", "", 0)
	p.obs = decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown, RetryKnown: true, RetryExpired: true}
	check(20, "reprompt", "error: unknown", "expired", 1)
	check(25, "none", "backing off after error", "expired", 1)
	check(30, "reprompt", "error: unknown", "expired", 2)
	check(40, "await_user", "error persisted after re-prompts", "expired", 2)
	if ctl, _ := control.Load(w.dir); !ctl.AwaitingUser {
		t.Fatal("hold was not persisted")
	}
	check(50, "none", "awaiting user", "", 2)
	j, err := incident.Load(w.dir)
	if err != nil || len(j.Records) != 7 {
		t.Fatal(len(j.Records), err)
	}
	var got []string
	for _, r := range j.Records {
		got = append(got, r.Observation.Retry)
	}
	if strings.Join(got, ",") != "retrying,retrying,expired,expired,expired,expired,unavailable" {
		t.Fatal(got)
	}
}

func TestExpiredRetrySignalDoesNotOverrideControls(t *testing.T) {
	for _, kind := range []string{"stop", "pause"} {
		t.Run(kind, func(t *testing.T) {
			w := newWorld(t, true, false)
			p := w.primary(decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown, RetryKnown: true, RetryExpired: true})
			if _, err := control.Update(w.dir, func(f *control.File) { f.Stop, f.Paused = kind == "stop", kind == "pause" }); err != nil {
				t.Fatal(err)
			}
			if r := runTick(t, w, t0); r.Action != "none" || len(p.sent) != 0 {
				t.Fatalf("%+v", r)
			}
		})
	}
}

// A re-prompt makes the session active, so observing it active must not
// refund the re-prompt allowance. Each row is one check of a session whose
// re-prompted turns keep failing; before the reset rule changed, the count
// returned to zero at every active check and the hold was never reached.
func TestRepromptCapSurvivesActiveChecksBetweenFailures(t *testing.T) {
	w := newWorld(t, true, true)
	for _, c := range w.cfg.FallbackSet.Candidates {
		w.probeOK[c.ID] = true
	}
	failed := decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown, RetryKnown: true, RetryExpired: true}
	retrying := decide.Observation{State: decide.Active, ErrorClass: decide.ErrUnknown, Retrying: true, RetryKnown: true}
	p := w.primary(failed)
	for i, c := range []struct {
		minute    int
		obs       decide.Observation
		action    string
		reprompts int
	}{
		{0, failed, "reprompt", 1},
		{1, retrying, "none", 1},                                 // our re-prompt started a turn
		{2, decide.Observation{State: decide.Active}, "none", 1}, // active alone is not a recovery
		{20, failed, "reprompt", 2},                              // it failed again
		{21, retrying, "none", 2},
		{40, failed, "await_user", 2}, // max_reprompts is 2
		{50, failed, "none", 2},
	} {
		p.obs = c.obs
		r := runTick(t, w, t0.Add(time.Duration(c.minute)*time.Minute))
		st, _ := state.Load(w.dir)
		if r.Action != c.action || st.Reprompts != c.reprompts || r.Handoff != nil {
			t.Fatalf("check %d (+%dm): %+v reprompts=%d", i, c.minute, r, st.Reprompts)
		}
	}
	if ctl, _ := control.Load(w.dir); !ctl.AwaitingUser || len(p.sent) != 2 {
		t.Fatalf("hold=%+v sends=%d", ctl, len(p.sent))
	}
}

// The episode ends, and the allowance starts over, when a turn finishes
// without an error or the active session makes verified Git progress.
func TestRepromptCountResetsOnCleanTurnOrGitProgress(t *testing.T) {
	for _, tc := range []struct {
		name      string
		obs       decide.Observation
		print     string
		reprompts int
	}{
		{"active_no_progress", decide.Observation{State: decide.Active}, "same", 1},
		{"active_git_progress", decide.Observation{State: decide.Active}, "changed", 0},
		{"idle", decide.Observation{State: decide.Idle}, "same", 0},
		{"error_git_progress", decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown}, "changed", 1},
		{"retrying", decide.Observation{State: decide.Active, Retrying: true}, "same", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, true, false)
			w.cfg.Loop.Progress.Repositories = []string{"/example/project"}
			p := w.primary(decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown})
			run := func(at time.Duration, print string) Result {
				opts := w.opts(t0.Add(at))
				opts.Fingerprint = func([]string) (string, error) { return print, nil }
				r, err := Tick(context.Background(), w.cfg, opts)
				if err != nil {
					t.Fatal(err)
				}
				return r
			}
			if r := run(0, "same"); r.Action != "reprompt" {
				t.Fatalf("%+v", r)
			}
			p.obs = tc.obs
			run(time.Minute, tc.print)
			if st, _ := state.Load(w.dir); st.Reprompts != tc.reprompts {
				t.Fatalf("reprompts=%d", st.Reprompts)
			}
		})
	}
}

// A retry notice that expired on an active thread is not a recovery: it earns
// no stall grace, and the thread is nudged under the ordinary bounds.
func TestExpiredRetryOnActiveThreadUsesOrdinaryStallBounds(t *testing.T) {
	w := newWorld(t, true, true)
	w.cfg.Loop.Progress.MaxNudges = 2
	w.cfg.Loop.Progress.Repositories = []string{"/example/project"}
	for _, c := range w.cfg.FallbackSet.Candidates {
		w.probeOK[c.ID] = true
	}
	p := w.primary(decide.Observation{State: decide.Active, ErrorClass: decide.ErrNetwork, Retrying: true, RetryKnown: true})
	if err := state.Save(w.dir, state.File{Fingerprint: "same", LastProgressAt: t0, LastState: "active"}); err != nil {
		t.Fatal(err)
	}
	for i, c := range []struct {
		hours   float64
		expired bool
		action  string
		reason  string
		nudges  int
	}{
		{1, false, "none", "provider is retrying the turn", 0},
		{2, true, "nudge", "no progress for 2h0m0s", 1},
		{2.5, true, "none", "active", 1}, // min_wake_interval since the nudge
		{3, false, "none", "provider is retrying the turn", 1},
		{4, true, "nudge", "no progress for 4h0m0s", 2},
		{6, true, "await_user", "no progress after 2 nudges", 2},
	} {
		p.obs.Retrying, p.obs.RetryExpired = !c.expired, c.expired
		opts := w.opts(t0.Add(time.Duration(c.hours * float64(time.Hour))))
		opts.Fingerprint = func([]string) (string, error) { return "same", nil }
		r, err := Tick(context.Background(), w.cfg, opts)
		st, _ := state.Load(w.dir)
		if err != nil || r.Action != c.action || r.Reason != c.reason || st.Nudges != c.nudges || r.Handoff != nil || st.Reprompts != 0 {
			t.Fatalf("check %d: %+v nudges=%d %v", i, r, st.Nudges, err)
		}
		if signal := map[bool]string{true: "expired"}[c.expired]; r.RetrySignal != signal {
			t.Fatalf("check %d: retry signal %q", i, r.RetrySignal)
		}
	}
	if ctl, _ := control.Load(w.dir); !ctl.AwaitingUser {
		t.Fatal("hold was not persisted")
	}
}

// A rate-limit retry notice on an errored target is honored only while a
// retry is queued or the reset is within wait_if_reset_within (30m here).
// With a far reset and nothing queued the ordinary limit rules decide.
func TestRateLimitRetryNoticeYieldsToLimitRules(t *testing.T) {
	for _, tc := range []struct {
		name      string
		fallbacks bool
		resetIn   time.Duration
		queued    int
		action    string
		reason    string
	}{
		{"far reset, healthy fallback", true, 5 * time.Hour, 0, "handoff", "error: rate_limit"},
		{"far reset, no fallback", false, 5 * time.Hour, 0, "none", "limit resets in 5h0m0s; no confirmed fallback"},
		{"far reset, queued retry, healthy fallback", true, 5 * time.Hour, 1, "none", "provider is retrying the turn"},
		{"far reset, queued retry, no fallback", false, 5 * time.Hour, 1, "none", "provider is retrying the turn"},
		{"near reset, healthy fallback", true, 20 * time.Minute, 0, "none", "provider is retrying the turn"},
		{"near reset, no fallback", false, 20 * time.Minute, 0, "none", "provider is retrying the turn"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, true, tc.fallbacks)
			if tc.fallbacks {
				for _, c := range w.cfg.FallbackSet.Candidates {
					w.probeOK[c.ID] = true
				}
			}
			p := w.primary(decide.Observation{State: decide.Error, ErrorClass: decide.ErrRateLimit, Retrying: true, RetryKnown: true, ResetsAt: t0.Add(tc.resetIn), Queued: tc.queued})
			r := runTick(t, w, t0)
			st, _ := state.Load(w.dir)
			if r.Action != tc.action || r.Reason != tc.reason || len(p.sent) != 0 || st.Reprompts != 0 || (r.Handoff != nil) != (tc.action == "handoff") {
				t.Fatalf("%+v", r)
			}
		})
	}
}

// A retry that BB has queued is left alone after its notice expires: no
// handoff with healthy fallbacks, no re-prompt, no spent allowance. Once the
// queue is empty the ordinary rules act.
func TestQueuedRetryOutlivesExpiredNotice(t *testing.T) {
	w := newWorld(t, true, true)
	w.cfg.Loop.Errors.FailoverOn = []string{"rate_limit", "network"}
	for _, c := range w.cfg.FallbackSet.Candidates {
		w.probeOK[c.ID] = true
	}
	p := w.primary(decide.Observation{State: decide.Error, ErrorClass: decide.ErrRateLimit, RetryKnown: true, RetryExpired: true, Queued: 1})
	for _, minutes := range []int{0, 30, 240} {
		r := runTick(t, w, t0.Add(time.Duration(minutes)*time.Minute))
		st, _ := state.Load(w.dir)
		if r.Action != "none" || r.Reason != "messages already queued after error" || r.RetrySignal != "expired" || r.Handoff != nil || len(p.sent) != 0 || st.Reprompts != 0 || st.Target() != state.Primary {
			t.Fatalf("+%dm: %+v", minutes, r)
		}
	}
	p.obs.Queued = 0
	if r := runTick(t, w, t0.Add(5*time.Hour)); r.Action != "handoff" {
		t.Fatalf("%+v", r)
	}
}

// A loop with no progress.repositories cannot nudge. An active target whose
// retry notice expired is held for a person after one stall interval plus
// max_nudges wake intervals of continued silence (here 30m + 3 x 1h), with
// one report. Activity in between starts the patience over.
func TestSilentActiveTargetWithoutGitTrackingIsHeld(t *testing.T) {
	w := newWorld(t, true, true)
	w.cfg.Loop.Progress.MaxNudges = 3
	for _, c := range w.cfg.FallbackSet.Candidates {
		w.probeOK[c.ID] = true
	}
	silent := decide.Observation{State: decide.Active, ErrorClass: decide.ErrUnknown, RetryKnown: true, RetryExpired: true}
	p := w.primary(decide.Observation{State: decide.Active, ErrorClass: decide.ErrUnknown, Retrying: true, RetryKnown: true})
	for i, c := range []struct {
		minute int
		obs    decide.Observation
		action string
	}{
		{0, p.obs, "none"},
		{20, silent, "none"},  // first seen expired
		{120, silent, "none"}, //
		{130, decide.Observation{State: decide.Active}, "none"}, // it logged something: the episode ends
		{140, silent, "none"},             // expired again: patience starts over
		{140 + 209, silent, "none"},       // 3h29m
		{140 + 210, silent, "await_user"}, // 3h30m
		{140 + 220, silent, "none"},       // held
	} {
		p.obs = c.obs
		r := runTick(t, w, t0.Add(time.Duration(c.minute)*time.Minute))
		if r.Action != c.action || len(p.sent) != 0 || r.Handoff != nil {
			t.Fatalf("check %d (+%dm): %+v", i, c.minute, r)
		}
	}
	ctl, _ := control.Load(w.dir)
	if !ctl.AwaitingUser || !strings.HasPrefix(ctl.Note, "provider retry went silent on an active session for 3h30m0s") || strings.Count(w.reportTypes(), "await_user") != 1 {
		t.Fatalf("%+v reports=%s", ctl, w.reportTypes())
	}
	j, _ := incident.Load(w.dir)
	if r := j.Records[len(j.Records)-2]; r.Cause != "retry_silent" || r.Observation.Retry != "expired" {
		t.Fatalf("%+v", r)
	}
}
