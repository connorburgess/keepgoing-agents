package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/allowance"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/health"
	"github.com/connorburgess/keepgoing-agents/internal/incident"
	"github.com/connorburgess/keepgoing-agents/internal/probe"
	"github.com/connorburgess/keepgoing-agents/internal/registry"
	rd "github.com/connorburgess/keepgoing-agents/internal/rundiscovery"
	"github.com/connorburgess/keepgoing-agents/internal/rungraph"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// dockLoops are fictional loops, one in each state the page distinguishes.
var dockLoops = []string{
	"docs-site-refresh",        // supervising, with child work
	"billing-api-migration",    // waiting for you, with a note
	"mobile-onboarding",        // paused
	"search-indexer",           // provider limit with its reset, send limit
	"release-notes-bot",        // finished
	"nightly-dependency-bumps", // not checked recently
	"data-export-cleanup",      // off
	"payments-reconciler",      // on its second route; the first is at a limit that resets in five days
	"inventory-sync",           // on its second route; the first route's reported reset has passed
	"report-builder",           // on its second route; no reset time was reported
}

const dockFallbacksYAML = `version: 1
redundancy:
  min_healthy: 1
  span: none
probe:
  every: 4h
  ttl: 6h
  timeout: 90s
candidates:
  - id: claude-second-account
    adapter: bb
    provider: claude-code
    group: second-account
    bb: {thread: thr_example0002, model: claude-sonnet-5-5, cli: nonexistent-provider-fixture}
  - id: codex-thread
    adapter: bb
    provider: codex
    group: codex
    bb: {thread: thr_example0003, cli: nonexistent-provider-fixture}
  - id: example-cli
    adapter: process
    provider: example-cli
    process:
      status_command: ["./nonexistent-status.sh"]
      resume_command: ["./nonexistent-resume.sh"]
    probe: {kind: command, command: ["./nonexistent-probe.sh"]}
    disabled: true
`

// dockLoopThread is the fictional BB thread a fixture loop supervises. Each
// loop has its own, so a lane can be recognised as a registered loop.
func dockLoopThread(name string) string {
	for i, loop := range dockLoops {
		if loop == name {
			return fmt.Sprintf("thr_example1%03d", i)
		}
	}
	return "thr_example1999"
}

// dockCodexFallbacksYAML is the route list of the two loops whose first route
// is a Codex thread. Its last route has no model, and a tool, provider and
// account group that are all "codex".
const dockCodexFallbacksYAML = `version: 1
redundancy:
  min_healthy: 1
  span: none
probe:
  every: 4h
  ttl: 12h
  timeout: 90s
candidates:
  - id: claude-second-account
    adapter: bb
    provider: claude-code
    group: second-account
    bb: {thread: thr_example0002, model: claude-sonnet-5-5, cli: nonexistent-provider-fixture}
  - id: codex-headless
    adapter: codex
    provider: codex
    codex: {workdir: ., cli: nonexistent-provider-fixture}
`

// dockOnCodex reports whether a fixture loop's first route is a Codex thread.
func dockOnCodex(name string) bool { return name == "inventory-sync" || name == "report-builder" }

func dockConfigYAML(name string, withFallbacks bool, maxSends int) string {
	model := "claude-opus-5-5"
	if !withFallbacks {
		model = "claude-haiku-5-5"
	}
	s := fmt.Sprintf(`version: 1
loop:
  name: %s
  state_dir: state
  adapter: bb
  provider: claude-code
  bb: {thread: %s, model: %s, reasoning: medium, cli: nonexistent-provider-fixture}
  prompt: Continue the fictional example task.
  errors:
    failover_on: [rate_limit]
`, name, dockLoopThread(name), model)
	if maxSends > 0 {
		s += fmt.Sprintf("  limits:\n    max_sends: %d\n", maxSends)
	}
	if withFallbacks {
		s += "  fallbacks: fallbacks.yaml\n"
	}
	return s
}

func dockWrite(t testing.TB, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func dockFixtureCheck(at time.Time, observed, action, reason string) *state.Check {
	return &state.Check{At: at, Target: state.Primary, ObservedTarget: state.Primary, ObservedState: observed, Action: action, Reason: reason, Outcome: "succeeded"}
}

func dockUsageReading(c config.Candidate, at time.Time, windows ...probe.UsageWindow) *probe.Usage {
	return &probe.Usage{At: at, Source: "probe", Identity: probe.UsageIdentity(c), Windows: windows}
}

func dockPtr(t time.Time) *time.Time { return &t }

// dockScratch builds a private registry of fictional loops under root and
// returns the registry directory. Times are relative to now.
func dockScratch(t testing.TB, root string, now time.Time) string {
	t.Helper()
	regDir := filepath.Join(root, "registry")
	for i, name := range dockLoops {
		dir := filepath.Join(root, "loops", name)
		withFallbacks := name != "release-notes-bot" && name != "data-export-cleanup"
		maxSends := 0
		if name == "search-indexer" {
			maxSends = 60
		}
		cfgYAML, fbYAML := dockConfigYAML(name, withFallbacks, maxSends), dockFallbacksYAML
		if name == "search-indexer" {
			// This loop runs on another pair of accounts, so its exhausted
			// limits are not readings of the routes the other loops use.
			cfgYAML = strings.Replace(cfgYAML, "  provider: claude-code\n", "  provider: claude-code\n  group: team-account\n", 1)
			fbYAML = strings.Replace(fbYAML, "group: second-account", "group: team-second-account", 1)
		}
		if dockOnCodex(name) {
			// inventory-sync returns by itself; report-builder has that off.
			back := "  switch_back: {enabled: true}\n"
			if name == "report-builder" {
				back = ""
			}
			cfgYAML = strings.Replace(cfgYAML, "  provider: claude-code\n", "  provider: codex\n"+back, 1)
			cfgYAML = strings.Replace(cfgYAML, "model: claude-opus-5-5, reasoning: medium, ", "", 1)
			fbYAML = dockCodexFallbacksYAML
		}
		if name == "payments-reconciler" {
			// Its first route is on an account group of its own, so the limit
			// it left on is not a limit on the routes other loops are using.
			cfgYAML = strings.Replace(cfgYAML, "  provider: claude-code\n", "  provider: claude-code\n  group: payments-account\n  switch_back: {enabled: true}\n", 1)
		}
		if name == "nightly-dependency-bumps" || name == "docs-site-refresh" {
			// Progress repositories, so the no-progress flag has a source.
			cfgYAML = strings.Replace(cfgYAML, "  prompt: ", "  progress:\n    repositories: [repo]\n  prompt: ", 1)
			if err := os.MkdirAll(filepath.Join(dir, "repo"), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		dockWrite(t, filepath.Join(dir, "keepgoing.yaml"), cfgYAML)
		if withFallbacks {
			dockWrite(t, filepath.Join(dir, "fallbacks.yaml"), fbYAML)
		}
		var out, errs bytes.Buffer
		if code := run(context.Background(), []string{"registry", "add", "-c", filepath.Join(dir, "keepgoing.yaml"), "--directory", regDir, "--json"}, &out, &errs); code != 0 {
			t.Fatalf("register %s: %s %s", name, out.String(), errs.String())
		}
		cfg, err := config.Load(filepath.Join(dir, "keepgoing.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		sd := cfg.Loop.StateDir
		if err := os.MkdirAll(sd, 0o700); err != nil {
			t.Fatal(err)
		}
		ctl := control.File{Enabled: true, UpdatedAt: now.Add(-time.Duration(i+1) * 26 * time.Hour)}
		st := state.File{}
		recent := now.Add(-3 * time.Minute)
		run := 0
		var children []rungraph.Child
		var waitAt time.Time
		switch name {
		case "docs-site-refresh":
			st.CheckedAt, st.LastCheck = recent, dockFixtureCheck(recent, "active", "none", "child work running")
			// The build that ran its last check, for the panel's build line.
			st.LastCheck.Version = "v0.1.5-dev.26f91db"
			// Its repositories changed 57 minutes ago: the no-progress flag is clear.
			st.Fingerprint, st.LastProgressAt, st.ProgressChangedAt = "fixture-fingerprint", now.Add(-57*time.Minute), now.Add(-57*time.Minute)
			// One lane is itself a registered loop: mobile-onboarding's thread.
			children = []rungraph.Child{{Key: childKeyOf("thr_child0001"), Status: rungraph.ChildActive}, {Key: childKeyOf(dockLoopThread("mobile-onboarding")), Status: rungraph.ChildActive},
				{Key: childKeyOf("thr_child0003"), Status: rungraph.ChildActive}, {Key: childKeyOf("thr_child0004"), Status: rungraph.ChildErrored}}
			// Its retained history: resumed yesterday, moved to another route
			// on a limit, returned, woke the session, then an unbroken run of
			// checks that sent nothing because lanes were working.
			on := &incident.Controls{Enabled: true}
			primaryKey, secondKey := incident.TargetKey(state.Primary), incident.TargetKey("claude-second-account")
			history := []incident.Record{
				{Kind: "control", CheckStartedAt: now.Add(-26 * time.Hour), ControlBefore: &incident.Controls{Enabled: true, AwaitingUser: true}, ControlAfter: on,
					ControlChange: &incident.ControlChange{Operation: "resume", SaveObservedAt: now.Add(-26 * time.Hour)}},
				{Kind: "tick", CheckStartedAt: now.Add(-(7*time.Hour + 39*time.Minute)), Action: "handoff", Outcome: "succeeded", Cause: "other", TargetKey: primaryKey, TargetKeyAfter: secondKey, ControlBefore: on, ControlAfter: on},
				{Kind: "tick", CheckStartedAt: now.Add(-(2*time.Hour + 26*time.Minute)), Action: "switch_back", Outcome: "succeeded", Cause: "other", TargetKey: secondKey, TargetKeyAfter: primaryKey, ControlBefore: on, ControlAfter: on},
				{Kind: "tick", CheckStartedAt: now.Add(-time.Hour), Action: "wake", Outcome: "succeeded", Cause: "idle", TargetKey: primaryKey, TargetKeyAfter: primaryKey, ControlBefore: on, ControlAfter: on},
			}
			for m := 57; m >= 3; m -= 6 {
				history = append(history, incident.Record{Kind: "tick", CheckStartedAt: now.Add(-time.Duration(m) * time.Minute), Action: "none", Outcome: "succeeded", Cause: "child_busy",
					TargetKey: primaryKey, TargetKeyAfter: primaryKey, ControlBefore: on, ControlAfter: on, LimitWaitReport: "none", TurnRetry: "none"})
			}
			for _, r := range history {
				if err := incident.Append(sd, r); err != nil {
					t.Fatal(err)
				}
			}
			run = 1
		case "billing-api-migration":
			// Held 2h14m ago by a check that found a child waiting on a
			// person; later held checks record only "awaiting user".
			held := now.Add(-(2*time.Hour + 14*time.Minute))
			ctl.AwaitingUser = true
			ctl.Note = "Needs a decision on the refund table: keep the old columns or migrate them? Answer in the thread, then resume."
			st.CheckedAt, st.LastCheck = recent, dockFixtureCheck(recent, "", "none", "awaiting user")
			st.LastCheck.ObservedTarget = ""
			children = []rungraph.Child{{Key: childKeyOf("thr_child0011"), Status: rungraph.ChildAwaiting}, {Key: childKeyOf("thr_child0012"), Status: rungraph.ChildActive}}
			run, waitAt = 2, held
			if err := incident.Append(sd, incident.Record{Kind: "tick", CheckStartedAt: held, Cause: "child_input", Action: "await_user", Outcome: "succeeded",
				ControlBefore: &incident.Controls{Enabled: true}, ControlAfter: &incident.Controls{Enabled: true, AwaitingUser: true}}); err != nil {
				t.Fatal(err)
			}
		case "mobile-onboarding":
			ctl.Paused = true
			at := now.Add(-40 * time.Minute)
			st.CheckedAt, st.LastCheck = at, dockFixtureCheck(at, "idle", "none", "paused")
		case "search-indexer":
			at := now.Add(-6 * time.Minute)
			st.CheckedAt, st.LastCheck = at, dockFixtureCheck(at, "error", "none", "rate limited; waiting for the reset")
			st.LimitWaitReport = &state.LimitWaitReport{Target: state.Primary, ResetsAt: now.Add(2*time.Hour + 20*time.Minute), DetectedAt: now.Add(-50 * time.Minute), Outcome: state.LimitWaitRequested, Reports: 1}
			for range 41 {
				if err := spendSends(sd, 60); err != nil {
					t.Fatal(err)
				}
			}
		case "release-notes-bot":
			ctl.Completed = true
			at := now.Add(-26 * time.Hour)
			st.CheckedAt, st.LastCheck = at, dockFixtureCheck(at, "idle", "none", "completed")
		case "nightly-dependency-bumps":
			at := now.Add(-5*time.Hour - 12*time.Minute)
			st.CheckedAt, st.LastCheck = at, dockFixtureCheck(at, "idle", "wake", "idle")
			// No change to its repositories in 30 hours, and a wake every three
			// hours since: eight sends, enough to raise the no-progress flag.
			st.Fingerprint, st.LastProgressAt, st.ProgressChangedAt = "fixture-fingerprint", now.Add(-30*time.Hour), now.Add(-30*time.Hour)
			on := &incident.Controls{Enabled: true}
			for k := 7; k >= 0; k-- {
				when := at.Add(-time.Duration(k) * 3 * time.Hour)
				if err := incident.Append(sd, incident.Record{Kind: "tick", CheckStartedAt: when, Action: "wake", Outcome: "succeeded", Cause: "idle",
					TargetKey: incident.TargetKey(state.Primary), TargetKeyAfter: incident.TargetKey(state.Primary), ControlBefore: on, ControlAfter: on}); err != nil {
					t.Fatal(err)
				}
			}
		case "data-export-cleanup":
			ctl.Enabled = false
		case "payments-reconciler", "inventory-sync", "report-builder":
			// Each left its first route on a usage limit and is on its second.
			st.ActiveTarget = "claude-second-account"
			st.CheckedAt, st.LastCheck = recent, dockFixtureCheck(recent, "active", "none", "active")
			st.LastCheck.Target, st.LastCheck.ObservedTarget = st.ActiveTarget, st.ActiveTarget
			st.LastHandoffAt, st.Handoffs = now.Add(-32*time.Hour), 1
			switch name {
			case "payments-reconciler":
				st.PrimaryResetsAt = now.Add(5 * 24 * time.Hour) // the reported reset is still ahead
			case "inventory-sync":
				st.PrimaryResetsAt = now.Add(-2 * time.Hour) // it has passed, and no return has been tried
			}
			// report-builder: the provider reported no reset time.
			on := &incident.Controls{Enabled: true}
			if err := incident.Append(sd, incident.Record{Kind: "tick", CheckStartedAt: st.LastHandoffAt, Action: "handoff", Outcome: "succeeded", Cause: "other",
				TargetKey: incident.TargetKey(state.Primary), TargetKeyAfter: incident.TargetKey(st.ActiveTarget), ControlBefore: on, ControlAfter: on,
				Observation: incident.Observation{Availability: "observed", State: "error", ErrorClass: "rate_limit"}}); err != nil {
				t.Fatal(err)
			}
		}
		if err := control.Save(sd, ctl); err != nil {
			t.Fatal(err)
		}
		if err := state.Save(sd, st); err != nil {
			t.Fatal(err)
		}
		if run > 0 {
			checkAt := st.CheckedAt
			if !waitAt.IsZero() {
				checkAt = waitAt
			}
			if err := rungraph.Update(sd, checkAt, func(g *rungraph.Graph) (bool, error) {
				return g.Sync(rungraph.Boundary{Gate: rungraph.Gate{Enabled: true}, Operation: "start", At: checkAt, NewRunID: fmt.Sprintf("run_%032x", run)})
			}); err != nil {
				t.Fatal("sync: ", err)
			}
			if err := rungraph.Update(sd, checkAt, func(g *rungraph.Graph) (bool, error) {
				return g.Wait(state.Primary, rungraph.ListingComplete, children, checkAt)
			}); err != nil {
				t.Fatal("wait: ", err)
			}
		}
		if name == "nightly-dependency-bumps" {
			// The same eight wakes as sends in its run graph, each returned by
			// the adapter, which is what the no-progress flag counts.
			at := now.Add(-5*time.Hour - 12*time.Minute)
			if err := rungraph.Update(sd, at.Add(-24*time.Hour), func(g *rungraph.Graph) (bool, error) {
				return g.Sync(rungraph.Boundary{Gate: rungraph.Gate{Enabled: true}, Operation: "start", At: at.Add(-24 * time.Hour), NewRunID: fmt.Sprintf("run_%032x", 9)})
			}); err != nil {
				t.Fatal("sync: ", err)
			}
			for k := 7; k >= 0; k-- {
				when := at.Add(-time.Duration(k) * 3 * time.Hour)
				id := fmt.Sprintf("attempt_%032x", k+1)
				if err := rungraph.Update(sd, when, func(g *rungraph.Graph) (bool, error) {
					if err := g.Begin(rungraph.Attempt{ID: id, Target: state.Primary, Action: "wake", Mode: "auto", RecordedAt: when}); err != nil {
						return false, err
					}
					return g.Finish(id, rd.AdapterReturned, when)
				}); err != nil {
					t.Fatal("attempt: ", err)
				}
			}
		}
		if withFallbacks {
			reg := &health.Registry{Candidates: map[string]*health.Entry{}}
			primary := cfg.Loop.PrimaryCandidate()
			cands := cfg.FallbackSet.Candidates
			probed := now.Add(-47 * time.Minute)
			reg.Candidates["primary"] = &health.Entry{Usage: dockUsageReading(primary, now.Add(-12*time.Minute),
				probe.UsageWindow{Key: "five_hour", Used: 0.38, ResetsAt: dockPtr(now.Add(3*time.Hour + 5*time.Minute))},
				probe.UsageWindow{Key: "seven_day", Used: 0.61, ResetsAt: dockPtr(now.Add(4 * 24 * time.Hour))})}
			// Three readings of the weekly window over 25 hours, a point an hour,
			// so it can be projected; the five-hour window has only the latest.
			week := allowance.Series{Key: "seven_day", ResetsAt: now.Add(4 * 24 * time.Hour), Samples: []allowance.Sample{
				{At: now.Add(-25*time.Hour - 12*time.Minute), Used: 0.36}, {At: now.Add(-12*time.Hour - 42*time.Minute), Used: 0.485}, {At: now.Add(-12 * time.Minute), Used: 0.61}}}
			reg.Candidates["primary"].UsageHistory = &allowance.History{Identity: probe.UsageIdentity(primary), Windows: []allowance.Series{week}}
			reg.Candidates[cands[0].ID] = &health.Entry{Last: &probe.Result{At: probed, OK: true, Kind: "bb"},
				Usage: dockUsageReading(cands[0], probed, probe.UsageWindow{Key: "five_hour", Used: 0.12, ResetsAt: dockPtr(now.Add(4 * time.Hour))})}
			reg.Candidates[cands[1].ID] = &health.Entry{Last: &probe.Result{At: now.Add(-2 * time.Hour), OK: false, Kind: "bb", ErrorClass: "auth"}}
			switch name {
			case "search-indexer":
				reg.Candidates["primary"].Usage = dockUsageReading(primary, now.Add(-6*time.Minute),
					probe.UsageWindow{Key: "five_hour", Used: 1, ResetsAt: dockPtr(now.Add(2*time.Hour + 20*time.Minute)), Status: "rejected"},
					probe.UsageWindow{Key: "seven_day", Used: 0.82, ResetsAt: dockPtr(now.Add(3 * 24 * time.Hour))})
				reg.Candidates[cands[0].ID] = &health.Entry{Last: &probe.Result{At: now.Add(-20 * time.Minute), OK: false, Kind: "bb", ErrorClass: "rate_limit"},
					Usage: dockUsageReading(cands[0], now.Add(-20*time.Minute), probe.UsageWindow{Key: "five_hour", Used: 1, ResetsAt: dockPtr(now.Add(time.Hour))})}
			case "payments-reconciler":
				// Handed off 32 hours ago; the route in use is not probed since.
				reg.Candidates[cands[0].ID] = &health.Entry{Last: &probe.Result{At: now.Add(-32 * time.Hour), OK: true, Kind: "bb"}}
				// The reading taken when it left: the weekly window was full.
				reg.Candidates["primary"].Usage = dockUsageReading(primary, now.Add(-32*time.Hour),
					probe.UsageWindow{Key: "seven_day", Used: 1, ResetsAt: dockPtr(now.Add(5 * 24 * time.Hour)), Status: "rejected"})
			case "inventory-sync", "report-builder":
				// The first route has no reading; the route in use was last
				// probed before the move; the last route passed a Codex probe
				// eight hours ago, inside its twelve-hour window.
				delete(reg.Candidates, "primary")
				reg.Candidates[cands[0].ID] = &health.Entry{Last: &probe.Result{At: now.Add(-32 * time.Hour), OK: true, Kind: "bb"}}
				reg.Candidates[cands[1].ID] = &health.Entry{Last: &probe.Result{At: now.Add(-8 * time.Hour), OK: true, Kind: "codex"}}
			case "nightly-dependency-bumps":
				// Old readings whose windows have since reset.
				reg.Candidates["primary"].Usage = dockUsageReading(primary, now.Add(-30*time.Hour),
					probe.UsageWindow{Key: "five_hour", Used: 0.9, ResetsAt: dockPtr(now.Add(-25 * time.Hour))})
				reg.Candidates[cands[0].ID] = &health.Entry{Last: &probe.Result{At: now.Add(-30 * time.Hour), OK: true, Kind: "bb"}}
			}
			if err := reg.Save(sd); err != nil {
				t.Fatal(err)
			}
		}
	}
	return regDir
}

// TestDockScratchForScreenshots builds the fictional registry into the
// directory named by KEEPGOING_DOCK_SCRATCH, for capturing the page by hand.
func TestDockScratchForScreenshots(t *testing.T) {
	root := os.Getenv("KEEPGOING_DOCK_SCRATCH")
	if root == "" {
		t.Skip("set KEEPGOING_DOCK_SCRATCH to an empty private directory to build the fictional registry")
	}
	if entries, err := os.ReadDir(root); err == nil && len(entries) > 0 {
		t.Fatal("KEEPGOING_DOCK_SCRATCH must be empty")
	}
	dir := dockScratch(t, root, time.Now().UTC())
	f, err := registry.Load(dir)
	if err != nil || len(f.Entries) != len(dockLoops) {
		t.Fatal(err)
	}
	t.Log("registry:", dir)
}
