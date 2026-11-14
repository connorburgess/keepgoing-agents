//go:build unix

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/registry"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

func configReportFixture(t *testing.T) (*fixture, *config.Config, []byte, env) {
	t.Helper()
	f := newFixture(t)
	raw, err := os.ReadFile(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.Replace(string(raw), "[handoff, degraded, recovered, stop, await_user]", "[error]", 1))
	write(t, f.cfg, string(raw), 0o600)
	if code, _, errs := f.run("enable"); code != 0 {
		t.Fatal(errs)
	}
	cfg, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	var out, errb safeBuffer
	return f, cfg, raw, env{ctx: context.Background(), stdout: &out, stderr: &errb}
}

func TestConfigurationErrorAttemptSurvivesPassesAndRestart(t *testing.T) {
	f, cfg, raw, e := configReportFixture(t)
	if err := os.Remove(f.cfg); err != nil {
		t.Fatal(err)
	}
	since := updateDaemonConfiguration(e, cfg, false, false, nil, nil)
	if since == nil {
		t.Fatal("no first timestamp")
	}
	for i := 0; i < 3; i++ {
		// No in-memory timestamp: as after a daemon restart, only the saved
		// attempt and first timestamp can suppress another delivery.
		got := updateDaemonConfiguration(e, cfg, false, false, nil, nil)
		if got == nil || !got.Equal(*since) {
			t.Fatalf("first timestamp changed: %v %v", since, got)
		}
	}
	if got := strings.Count(f.hooks.types(), "keepgoing.error"); got != 1 {
		t.Fatalf("%d reports", got)
	}
	// A claimed successful reload is not enough: changed sources under the
	// lock must retain the original episode, even though the file is readable.
	write(t, f.cfg, string(raw)+"# changed after load\n", 0o600)
	if got := updateDaemonConfiguration(e, cfg, true, false, nil, since); got == nil || !got.Equal(*since) {
		t.Fatal("unverified reload cleared the episode")
	}
	fresh, err := config.Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if updateDaemonConfiguration(e, fresh, true, false, nil, since) != nil {
		t.Fatal("verified reload did not clear")
	}
	st, err := state.Load(cfg.Loop.StateDir)
	if err != nil || st.DaemonConfigUnavailable != nil {
		t.Fatalf("%+v %v", st.DaemonConfigUnavailable, err)
	}
	if err := os.Remove(f.cfg); err != nil {
		t.Fatal(err)
	}
	next := updateDaemonConfiguration(e, fresh, false, false, nil, nil)
	if next == nil || !next.After(*since) || strings.Count(f.hooks.types(), "keepgoing.error") != 2 {
		t.Fatalf("later outage: %v %s", next, f.hooks.types())
	}
	if f.read("calls") != "" {
		t.Fatal("diagnostics accessed a provider")
	}
}

func TestConfigurationErrorCrashWindowAndFailedPersistence(t *testing.T) {
	t.Run("attempt saved before crash", func(t *testing.T) {
		f, cfg, _, e := configReportFixture(t)
		at := time.Now().UTC().Add(-time.Minute)
		st, _ := state.Load(cfg.Loop.StateDir)
		// Emulate a crash after persistence but before any delivery. The report
		// is lost, rather than attempted again after the process restarts.
		st.DaemonConfigUnavailable = &state.DaemonConfigUnavailable{Since: at, ReportAttempted: true}
		if err := state.Save(cfg.Loop.StateDir, st); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(f.cfg); err != nil {
			t.Fatal(err)
		}
		got := updateDaemonConfiguration(e, cfg, false, false, nil, nil)
		if got == nil || !got.Equal(at) || f.hooks.types() != "" {
			t.Fatalf("%v %s", got, f.hooks.types())
		}
	})
	t.Run("save fails", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("requires an unprivileged filesystem writer")
		}
		f, cfg, _, e := configReportFixture(t)
		if err := os.Remove(f.cfg); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(cfg.Loop.StateDir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(cfg.Loop.StateDir, 0o700) })
		updateDaemonConfiguration(e, cfg, false, false, nil, nil)
		if !strings.Contains(e.stderr.(*safeBuffer).String(), "configuration diagnostic could not be saved") {
			t.Fatal("fixture did not reach the failed persistence")
		}
		if f.hooks.types() != "" {
			t.Fatal("sent without a persisted attempt")
		}
		st, err := state.Load(cfg.Loop.StateDir)
		if err != nil || st.DaemonConfigUnavailable != nil {
			t.Fatalf("%+v %v", st.DaemonConfigUnavailable, err)
		}
	})
	t.Run("dry run", func(t *testing.T) {
		f, cfg, _, e := configReportFixture(t)
		if err := os.Remove(f.cfg); err != nil {
			t.Fatal(err)
		}
		updateDaemonConfiguration(e, cfg, false, true, nil, nil)
		st, _ := state.Load(cfg.Loop.StateDir)
		if st.DaemonConfigUnavailable != nil || f.hooks.types() != "" {
			t.Fatal("dry run persisted or sent")
		}
	})
}

func TestMissingConfigurationIsShownByStatusDoctorAndPanel(t *testing.T) {
	f, _, raw, _ := configReportFixture(t)
	g := startGoneDaemonFixture(t, f)
	calls := f.read("calls")
	if _, err := registry.Add(filepath.Join(f.dir, "registry"), "example-loop", f.cfg, filepath.Join(f.dir, "state")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(f.cfg, g.moved); err != nil {
		t.Fatal(err)
	}
	g.control(t, "pause")
	waitFor(t, func() bool {
		return inspectDaemon(context.Background(), filepath.Join(f.dir, "state"), version, false).ConfigUnavailableSince != nil
	}, "live configuration diagnostic")
	if f.read("calls") != calls {
		t.Fatal("configuration diagnostic observed or messaged an agent while paused")
	}
	if strings.Count(f.hooks.types(), "keepgoing.error") != 1 {
		t.Fatalf("configuration report: %s", f.hooks.types())
	}
	d := inspectDaemon(context.Background(), filepath.Join(f.dir, "state"), version, false)
	if !strings.Contains(d.Message, "configuration file") || !strings.Contains(d.NextCheck, "configuration file") {
		t.Fatalf("%+v", d)
	}
	var doctorOut, doctorErr safeBuffer
	if code := run(context.Background(), []string{"doctor", "-c", g.moved, "--offline=false"}, &doctorOut, &doctorErr); code == exitUsage || !strings.Contains(doctorOut.String(), "configuration file") {
		t.Fatalf("%d %s %s", code, doctorOut.String(), doctorErr.String())
	}
	var out, errb safeBuffer
	if code := run(context.Background(), []string{"status", "-c", g.moved}, &out, &errb); code != exitOK || !strings.Contains(out.String(), "configuration file") {
		t.Fatalf("%d %s %s", code, out.String(), errb.String())
	}
	r := &dockReader{directory: filepath.Join(f.dir, "registry"), refresh: time.Second, staleAfter: defaultStaleAfter, query: inspectDaemon}
	panel := r.read(context.Background(), false)
	found := false
	for _, need := range panel.NeedsYou {
		if strings.Contains(need.Reason, "checks have stopped: the configuration file") && need.Since != nil && need.Since.Equal(*d.ConfigUnavailableSince) {
			found = true
		}
	}
	if !found {
		t.Fatalf("panel missing cause: %+v", panel.NeedsYou)
	}
	// Startup at the missing path fails, and cannot repeat the persisted report.
	g.stop()
	before := f.hooks.types()
	if code, _, _ := f.run("daemon"); code != exitUsage {
		t.Fatalf("missing-path restart: %d", code)
	}
	if f.hooks.types() != before {
		t.Fatal("restart repeated the report")
	}
	// A restarted daemon with a restored, validated file clears its episode.
	write(t, f.cfg, string(raw), 0o600)
	g2 := startGoneDaemonFixture(t, f)
	waitFor(t, func() bool {
		return inspectDaemon(context.Background(), filepath.Join(f.dir, "state"), version, false).ConfigUnavailableSince == nil
	}, "configuration recovery")
	g2.stop()
}

func TestConfigurationReportIsBoundedAndSavedBeforeDelivery(t *testing.T) {
	f, cfg, _, e := configReportFixture(t)
	saved := make(chan bool, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st, err := state.Load(cfg.Loop.StateDir)
		saved <- err == nil && st.DaemonConfigUnavailable != nil && st.DaemonConfigUnavailable.ReportAttempted
		time.Sleep(3 * time.Second)
		w.WriteHeader(503)
	}))
	defer srv.Close()
	t.Setenv("KEEPGOING_WEBHOOK_URL", srv.URL)
	if err := os.Remove(f.cfg); err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	updateDaemonConfiguration(e, cfg, false, false, nil, nil)
	if elapsed := time.Since(began); elapsed > 2750*time.Millisecond {
		t.Fatalf("report took %v", elapsed)
	}
	if !<-saved {
		t.Fatal("delivery started before its attempt was persisted")
	}
	updateDaemonConfiguration(e, cfg, false, false, nil, nil)
	select {
	case <-saved:
		t.Fatal("failed delivery repeated")
	default:
	}
}

func TestSavedStopPrecedesConfigurationErrorDelivery(t *testing.T) {
	f, _, _, _ := configReportFixture(t)
	stopped := make(chan bool, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stopped <- strings.Contains(f.read("calls"), "thread stop thr_primary")
		w.WriteHeader(202)
	}))
	defer srv.Close()
	t.Setenv("KEEPGOING_WEBHOOK_URL", srv.URL)
	g := startGoneDaemonFixture(t, f)
	if err := os.Rename(f.cfg, g.moved); err != nil {
		t.Fatal(err)
	}
	g.control(t, "stop")
	select {
	case yes := <-stopped:
		if !yes {
			t.Fatal("reported before carrying out the saved stop")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no error report")
	}
}
