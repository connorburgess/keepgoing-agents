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

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/health"
	"github.com/connorburgess/keepgoing-agents/internal/incident"
	"github.com/connorburgess/keepgoing-agents/internal/probe"
	"github.com/connorburgess/keepgoing-agents/internal/registry"
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
	"payments-reconciler",      // running on a fallback whose last probe is old
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
  bb: {thread: thr_example0001, model: %s, cli: nonexistent-provider-fixture}
  prompt: Continue the fictional example task.
  errors:
    failover_on: [rate_limit]
`, name, model)
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
			children = []rungraph.Child{{Key: childKeyOf("thr_child0001"), Status: rungraph.ChildActive}, {Key: childKeyOf("thr_child0002"), Status: rungraph.ChildActive},
				{Key: childKeyOf("thr_child0003"), Status: rungraph.ChildActive}, {Key: childKeyOf("thr_child0004"), Status: rungraph.ChildErrored}}
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
				if err := state.SpendForTest(sd, 60); err != nil {
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
		case "data-export-cleanup":
			ctl.Enabled = false
		case "payments-reconciler":
			st.ActiveTarget = "claude-second-account"
			st.CheckedAt, st.LastCheck = recent, dockFixtureCheck(recent, "active", "none", "active")
			st.LastCheck.Target, st.LastCheck.ObservedTarget = st.ActiveTarget, st.ActiveTarget
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
		if withFallbacks {
			reg := &health.Registry{Candidates: map[string]*health.Entry{}}
			primary := cfg.Loop.PrimaryCandidate()
			cands := cfg.FallbackSet.Candidates
			probed := now.Add(-47 * time.Minute)
			reg.Candidates["primary"] = &health.Entry{Usage: dockUsageReading(primary, now.Add(-12*time.Minute),
				probe.UsageWindow{Key: "five_hour", Used: 0.38, ResetsAt: dockPtr(now.Add(3*time.Hour + 5*time.Minute))},
				probe.UsageWindow{Key: "seven_day", Used: 0.61, ResetsAt: dockPtr(now.Add(4 * 24 * time.Hour))})}
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
