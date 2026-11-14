//go:build unix

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/session"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// goneDaemon is a fixture loop whose daemon has made its first check.
type goneDaemon struct {
	f         *fixture
	out, errb *safeBuffer
	cancel    func()
	done      chan int
	started   time.Time
	moved     string
}

func startGoneDaemon(t *testing.T, args ...string) *goneDaemon {
	t.Helper()
	return startGoneDaemonFixture(t, newFixture(t), args...)
}

func startGoneDaemonFixture(t *testing.T, f *fixture, args ...string) *goneDaemon {
	t.Helper()
	if code, _, errs := f.run("enable"); code != 0 {
		t.Fatal(errs)
	}
	write(t, filepath.Join(f.bbDir, "thr_primary.show"), `{"thread":{"status":"active"}}`, 0o600)
	g := &goneDaemon{f: f, out: &safeBuffer{}, errb: &safeBuffer{}, done: make(chan int, 1), started: time.Now().UTC(), moved: filepath.Join(f.dir, "moved.yaml")}
	ctx, cancel := context.WithCancel(context.Background())
	g.cancel = cancel
	go func() { g.done <- run(ctx, append([]string{"daemon", "-c", f.cfg}, args...), g.out, g.errb) }()
	t.Cleanup(g.stop)
	waitFor(t, func() bool { return strings.Contains(g.out.String(), "\n") }, "first daemon check")
	return g
}

func (g *goneDaemon) stop() {
	g.cancel()
	select {
	case <-g.done:
	case <-time.After(5 * time.Second):
	}
}

// control runs a control command against the file at its new name.
func (g *goneDaemon) control(t *testing.T, args ...string) {
	t.Helper()
	var out, errb safeBuffer
	if code := run(context.Background(), append(args, "-c", g.moved), &out, &errb); code != 0 {
		t.Fatalf("%v: %d %s", args, code, errb.String())
	}
}

func lastCheck(t *testing.T, g *goneDaemon) *state.Check {
	t.Helper()
	st, err := state.Load(filepath.Join(g.f.dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	return st.LastCheck
}

// A loop's configuration file is moved while its daemon runs, and a stop is
// asked for. The daemon cannot read its configuration, but stop wins: the
// stop, and nothing else, is carried out from the last configuration it read,
// and the check's record says so. Then the daemon waits as for any loop.
func TestDaemonCarriesOutAStopWhenItsConfigurationIsGone(t *testing.T) {
	for _, how := range []string{"moved", "unreadable"} {
		t.Run(how, func(t *testing.T) {
			g := startGoneDaemon(t)
			if how == "moved" {
				if err := os.Rename(g.f.cfg, g.moved); err != nil {
					t.Fatal(err)
				}
			} else {
				// The stop command reads a copy; the daemon's own file is
				// still there but cannot be read.
				raw, err := os.ReadFile(g.f.cfg)
				if err != nil {
					t.Fatal(err)
				}
				write(t, g.moved, string(raw), 0o600)
				if err := os.Chmod(g.f.cfg, 0); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { os.Chmod(g.f.cfg, 0o600) })
			}
			told := g.f.read("thr_primary.told")
			asked := time.Now()
			g.control(t, "stop")
			waitFor(t, func() bool { return strings.Contains(g.f.read("calls"), "thread stop thr_primary") }, "the stop")
			if d := time.Since(asked); d > 5*time.Second {
				t.Fatalf("the stop took %v", d)
			}
			if g.f.read("thr_primary.told") != told {
				t.Fatal("a prompt was sent from the last configuration")
			}
			waitFor(t, func() bool { c := lastCheck(t, g); return c != nil && c.ConfigUnavailable != nil }, "the stop's record")
			c := lastCheck(t, g)
			if c.Action != "stop" || c.ConfigUnavailable.LastReadAt.Before(g.started.Add(-time.Minute)) || c.ConfigUnavailable.LastReadAt.After(asked) {
				t.Fatalf("%+v %+v", c, c.ConfigUnavailable)
			}
			waitFor(t, func() bool {
				log, _ := os.ReadFile(filepath.Join(g.f.dir, "state", state.LogName))
				return strings.Contains(string(log), `"configUnavailable":{"lastReadAt":"`)
			}, "runs.jsonl records the last configuration")
			if !strings.Contains(g.errb.String(), "the configuration file cannot be read; carrying out the saved stop from the configuration read at ") {
				t.Fatalf("%s", g.errb.String())
			}
			// The stop is carried out: no fast retry, so the daemon's skipped
			// passes come once an interval (30 s), not every control poll.
			before := strings.Count(g.errb.String(), "no tick performed")
			time.Sleep(time.Second)
			if n := strings.Count(g.errb.String(), "no tick performed") - before; n > 1 {
				t.Fatalf("%d skipped passes in a second after the stop was carried out", n)
			}
			if n := strings.Count(g.f.read("calls"), "thread stop thr_primary"); n != 1 {
				t.Fatalf("%d stops", n)
			}
		})
	}
}

// The stop from the last configuration still waits while another loop's live
// check holds the session, retried at the control poll; while it retries, the
// daemon ends each pass, so the overdue rule reads checks not being recorded
// and not a computer asleep.
func TestAStopFromTheLastConfigurationWaitsForAnotherLoopsCheck(t *testing.T) {
	g := startGoneDaemon(t)
	dir, _, err := session.Dir(true)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := session.Take(dir, session.Identity{Kind: "bb", ID: "thr_primary"}, session.Holder{Loop: "example-loop-b", PID: os.Getpid(), Since: time.Now(), Until: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	defer claim.Release()
	if err := os.Rename(g.f.cfg, g.moved); err != nil {
		t.Fatal(err)
	}
	g.control(t, "stop")
	waitFor(t, func() bool { return strings.Count(g.errb.String(), "being checked by loop example-loop-b") >= 3 }, "retried stops")
	if strings.Contains(g.f.read("calls"), "thread stop thr_primary") {
		t.Fatal("stopped while another loop's check held the session")
	}
	var out, errb safeBuffer
	if code := run(context.Background(), []string{"status", "--json", "-c", g.moved}, &out, &errb); code != 0 {
		t.Fatal(errb.String())
	}
	var s struct {
		Daemon   struct{ LastPassStartedAt *time.Time }
		Schedule struct{ Slept, NotRecorded bool }
	}
	if err := json.Unmarshal([]byte(out.String()), &s); err != nil {
		t.Fatal(err)
	}
	first := lastCheck(t, g)
	if s.Daemon.LastPassStartedAt == nil || !s.Daemon.LastPassStartedAt.After(first.At) || s.Schedule.Slept || !s.Schedule.NotRecorded {
		t.Fatalf("daemon %+v schedule %+v, last check %v", s.Daemon, s.Schedule, first.At)
	}
	claim.Release()
	waitFor(t, func() bool { return strings.Contains(g.f.read("calls"), "thread stop thr_primary") }, "the stop")
}

// Only a stop is carried out from the last configuration: a pause, which
// sends nothing anyway, is a pause, and no check observes or sends.
func TestAPauseWithTheConfigurationGoneSendsNothing(t *testing.T) {
	g := startGoneDaemon(t)
	if err := os.Rename(g.f.cfg, g.moved); err != nil {
		t.Fatal(err)
	}
	calls := g.f.read("calls")
	g.control(t, "pause")
	waitFor(t, func() bool { return strings.Count(g.errb.String(), "no tick performed") >= 1 }, "a skipped pass")
	time.Sleep(500 * time.Millisecond)
	if got := g.f.read("calls"); got != calls {
		t.Fatalf("calls after the configuration went: %q", strings.TrimPrefix(got, calls))
	}
	if c := lastCheck(t, g); c.ConfigUnavailable != nil {
		t.Fatalf("%+v", c)
	}
}

// A configuration file that can be read but no longer loads is being edited:
// it says what the loop should do, so nothing is carried out from the last
// one, not even a stop, until it loads.
func TestAStopWaitsForAConfigurationThatCanBeReadButDoesNotLoad(t *testing.T) {
	g := startGoneDaemon(t)
	raw, err := os.ReadFile(g.f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(g.moved, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	// The daemon's own file breaks; the stop command reads a good copy.
	write(t, g.f.cfg, "version: [broken\n", 0o600)
	g.control(t, "stop")
	waitFor(t, func() bool { return strings.Count(g.errb.String(), "config reload failed") >= 3 }, "retried passes")
	if strings.Contains(g.f.read("calls"), "thread stop thr_primary") {
		t.Fatal("a stop from the last configuration for a readable file")
	}
	write(t, g.f.cfg, string(raw), 0o600)
	waitFor(t, func() bool { return strings.Contains(g.f.read("calls"), "thread stop thr_primary") }, "the stop once the file loads")
	if c := lastCheck(t, g); c.ConfigUnavailable != nil {
		t.Fatalf("%+v", c)
	}
}

// A dry-run daemon carries out nothing, from any configuration.
func TestADryRunDaemonDoesNotStopFromTheLastConfiguration(t *testing.T) {
	g := startGoneDaemon(t, "--dry-run")
	if err := os.Rename(g.f.cfg, g.moved); err != nil {
		t.Fatal(err)
	}
	g.control(t, "stop")
	time.Sleep(1500 * time.Millisecond)
	if strings.Contains(g.f.read("calls"), "thread stop") {
		t.Fatal("a dry-run daemon stopped the session")
	}
}

// The transfer rule holds for a stop from the last configuration too: a loop
// moved to another BB thread since that configuration was read is not
// stopped on the old one.
func TestAStopFromTheLastConfigurationKeepsTheTransferRule(t *testing.T) {
	g := startGoneDaemon(t)
	if _, err := control.Update(filepath.Join(g.f.dir, "state"), func(f *control.File) { f.BBTarget = "thr_moved" }); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(g.f.cfg, g.moved); err != nil {
		t.Fatal(err)
	}
	var out, errb safeBuffer
	if code := run(context.Background(), []string{"stop", "-c", g.moved}, &out, &errb); code != 0 {
		t.Fatal(errb.String())
	}
	waitFor(t, func() bool {
		return strings.Count(g.errb.String(), "does not match the transferred BB target; use the current loop configuration; no stop performed") >= 2
	}, "refused stops")
	if strings.Contains(g.f.read("calls"), "thread stop") {
		t.Fatal("stopped a thread the loop was moved away from")
	}
}

// A stop already carried out is not retried at the control poll: when later
// passes are skipped (here, a configuration that no longer loads), they come
// once an interval, not every poll.
func TestACarriedOutStopIsNotRetriedAtTheControlPoll(t *testing.T) {
	f := newFixture(t)
	raw, err := os.ReadFile(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	good := strings.Replace(string(raw), "interval: 30s", "interval: 10s", 1)
	write(t, f.cfg, good, 0o600)
	if code, _, errs := f.run("enable"); code != 0 {
		t.Fatal(errs)
	}
	write(t, filepath.Join(f.bbDir, "thr_primary.show"), `{"thread":{"status":"active"}}`, 0o600)
	if code, _, errs := f.run("stop"); code != 0 {
		t.Fatal(errs)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	var out, errb safeBuffer
	go func() { done <- run(ctx, []string{"daemon", "-c", f.cfg}, &out, &errb) }()
	defer func() { cancel(); <-done }()
	waitFor(t, func() bool { return strings.Contains(f.read("calls"), "thread stop thr_primary") }, "the stop")
	write(t, f.cfg, good+"broken: [\n", 0o600)
	// The next pass, at the 10 s interval, is skipped; nothing comes
	// between it and the one after.
	deadline := time.Now().Add(15 * time.Second)
	for !strings.Contains(errb.String(), "no tick performed") && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(errb.String(), "no tick performed") {
		t.Fatal("no skipped pass")
	}
	time.Sleep(2 * time.Second)
	if n := strings.Count(errb.String(), "no tick performed"); n > 1 {
		t.Fatalf("%d skipped passes within 2 s at a 10 s interval: the carried-out stop is being retried", n)
	}
}

// A recorded failed stop is not a completed stop: with the source still
// unavailable, the next control poll must try the stop-only path again.
func TestAFailedStopFromTheLastConfigurationIsRetried(t *testing.T) {
	f := newFixture(t)
	script := strings.Replace(fakeBB, `"thread show")`, `"thread stop") if [ -f "$d/fail-stop" ]; then rm "$d/fail-stop"; echo 'fictional stop failure' >&2; exit 1; fi; echo '{"ok":true}' ;;
"thread show")`, 1)
	write(t, filepath.Join(f.dir, "bin", "bb"), script, 0o700)
	write(t, filepath.Join(f.bbDir, "fail-stop"), "once", 0o600)
	g := startGoneDaemonFixture(t, f)
	if err := os.Rename(g.f.cfg, g.moved); err != nil {
		t.Fatal(err)
	}
	g.control(t, "stop")
	waitFor(t, func() bool {
		st, err := state.Load(filepath.Join(f.dir, "state"))
		return err == nil && st.StopRequestedSent
	}, "the failed stop is retried without waiting for the interval")
	if n := strings.Count(f.read("calls"), "thread stop thr_primary"); n != 2 {
		t.Fatalf("%d stops, want one failed attempt and one successful attempt", n)
	}
	log, err := os.ReadFile(filepath.Join(f.dir, "state", state.LogName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), `"outcome":"failed"`) {
		t.Fatalf("failed stop was not recorded: %s", log)
	}
}

// Model a restore after the daemon's preliminary eligibility check but before
// stopFromLastConfig takes the tick lock. No timing or live writer is needed.
func TestLastConfigurationStopRechecksRestoredSourcesUnderLock(t *testing.T) {
	for _, restored := range []string{"unchanged", "changed"} {
		t.Run(restored, func(t *testing.T) {
			f := newFixture(t)
			if code, _, errs := f.run("stop"); code != 0 {
				t.Fatal(errs)
			}
			write(t, filepath.Join(f.bbDir, "thr_primary.show"), `{"thread":{"status":"active"}}`, 0o600)
			cfg, err := config.Load(f.cfg)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(f.cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(f.cfg); err != nil {
				t.Fatal(err)
			}
			if !cfg.SourcesUnavailable() {
				t.Fatal("initial eligibility check did not see missing source")
			}
			if restored == "changed" {
				raw = []byte(strings.Replace(string(raw), "thr_primary", "thr_replacement", 1))
			}
			write(t, f.cfg, string(raw), 0o600)
			var out, errb safeBuffer
			code := stopFromLastConfig(env{ctx: context.Background(), stdout: &out, stderr: &errb}, cfg, nil)
			if code != exitSkipped || !strings.Contains(errb.String(), "no stop performed; reload and retry") {
				t.Fatalf("%d %s", code, errb.String())
			}
			if strings.Contains(f.read("calls"), "thread stop") {
				t.Fatal("stopped from configuration restored after the preliminary check")
			}
		})
	}
}
