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

	"github.com/connorburgess/keepgoing-agents/internal/control"
)

var damagedRecords = map[string]string{
	"empty":     "",
	"truncated": `{"enabled": tr`,
	"garbage":   "\x00not json",
}

// damageControl replaces control.json and returns its original bytes.
func damageControl(t *testing.T, f *fixture, damaged string) []byte {
	t.Helper()
	path := filepath.Join(f.dir, "state", control.FileName)
	orig, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(damaged), 0o600); err != nil {
		t.Fatal(err)
	}
	return orig
}

// Stop and pause win over every other rule, so they work with control.json
// damaged: they are saved in a hold marker beside it, every check honours
// the marker first, status names it, resume refuses until control.json is
// restored and then releases it. The damaged file is never rewritten.
func TestStopAndPauseWorkWithAnUnreadableControlRecord(t *testing.T) {
	for name, damaged := range damagedRecords {
		t.Run("stop/"+name, func(t *testing.T) {
			f := newFixture(t)
			f.run("enable")
			f.tick() // wakes the idle session
			write(t, filepath.Join(f.bbDir, "thr_primary.show"), `{"thread":{"status":"active"}}`, 0o600)
			orig := damageControl(t, f, damaged)
			path := filepath.Join(f.dir, "state", control.FileName)

			code, out, errs := f.run("stop")
			if code != 0 || !strings.Contains(out, "example-loop: stopped (control record unreadable)") || !strings.Contains(errs, "saved in control-hold.json instead") {
				t.Fatalf("stop: exit %d\n%s%s", code, out, errs)
			}
			marker := filepath.Join(f.dir, "state", control.HoldFileName)
			if info, err := os.Stat(marker); err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("hold marker: %v %v", info, err)
			}

			// The next check stops the running session once, and the one after
			// sends nothing more.
			before := f.read("calls")
			v := f.tick()
			if v["action"] != "stop" || strings.Count(strings.TrimPrefix(f.read("calls"), before), "thread stop thr_primary") != 1 {
				t.Fatalf("check under the marker: %v\ncalls: %s", v, strings.TrimPrefix(f.read("calls"), before))
			}
			before = f.read("calls")
			if v := f.tick(); v["action"] != "none" || strings.Contains(strings.TrimPrefix(f.read("calls"), before), "thread stop") || strings.Contains(strings.TrimPrefix(f.read("calls"), before), "thread tell") {
				t.Fatalf("second check: %v", v)
			}

			code, out, _ = f.run("status")
			if code != exitFailed || !strings.Contains(out, "control:   stopped (control record unreadable)") || !strings.Contains(out, "hold:      control-hold.json holds the loop stopped") {
				t.Fatalf("status: exit %d\n%s", code, out)
			}
			_, js, _ := f.run("status", "--json")
			var st struct {
				ControlHold *control.Hold
				Runtime     struct{ State string }
			}
			if json.Unmarshal([]byte(js), &st) != nil || st.ControlHold == nil || !st.ControlHold.Stop || st.Runtime.State != "stopped" {
				t.Fatalf("status --json: %s", js)
			}
			if _, doc, _ := f.run("doctor"); !strings.Contains(doc, "supervisor.control_hold") {
				t.Errorf("doctor does not report the hold:\n%s", doc)
			}

			// resume refuses until control.json is restored; the marker stays.
			if code, _, errs := f.run("resume"); code != exitFailed || !strings.Contains(errs, "restore it from a copy of the state directory first") {
				t.Fatalf("resume with control.json damaged: exit %d %s", code, errs)
			}
			if _, err := os.Stat(marker); err != nil {
				t.Fatal("resume removed the marker while control.json was unreadable")
			}
			if b, _ := os.ReadFile(path); string(b) != damaged {
				t.Fatal("control.json was rewritten")
			}

			// Restored: resume releases the hold and the loop carries on.
			if err := os.WriteFile(path, orig, 0o600); err != nil {
				t.Fatal(err)
			}
			if code, _, errs := f.run("resume"); code != 0 || !strings.Contains(errs, "released the hold saved in control-hold.json") {
				t.Fatalf("resume after restoring: exit %d %s", code, errs)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("the marker is still there after resume")
			}
			if v := f.tick(); v["action"] == "stop" || v["control"] != "enabled" {
				t.Fatalf("after resume: %v", v)
			}
		})

		t.Run("pause/"+name, func(t *testing.T) {
			f := newFixture(t)
			f.run("enable")
			f.tick()
			damageControl(t, f, damaged)
			if code, out, _ := f.run("pause"); code != 0 || !strings.Contains(out, "paused (control record unreadable)") {
				t.Fatalf("pause: exit %d %s", code, out)
			}
			before := f.read("calls")
			if v := f.tick(); v["action"] != "none" || f.read("calls") != before {
				t.Fatalf("paused check queried or sent: %v\n%s", v, strings.TrimPrefix(f.read("calls"), before))
			}
			// A stop after the pause is kept as well, and wins.
			f.run("stop")
			h, err := control.LoadHold(filepath.Join(f.dir, "state"))
			if err != nil || h == nil || !h.Stop || !h.Paused {
				t.Fatalf("marker after pause then stop: %+v %v", h, err)
			}
		})
	}
}

// A marker that is itself damaged is a stop.
func TestADamagedHoldMarkerIsAStop(t *testing.T) {
	f := newFixture(t)
	f.run("enable")
	f.tick()
	write(t, filepath.Join(f.bbDir, "thr_primary.show"), `{"thread":{"status":"active"}}`, 0o600)
	damageControl(t, f, "")
	write(t, filepath.Join(f.dir, "state", control.HoldFileName), "{", 0o600)
	if v := f.tick(); v["action"] != "stop" {
		t.Fatalf("%v", v)
	}
}

// With control.json readable, a marker left from earlier still holds the
// loop until resume.
func TestTheHoldMarkerWinsOverAReadableControlRecord(t *testing.T) {
	f := newFixture(t)
	f.run("enable")
	if _, err := control.SaveHold(filepath.Join(f.dir, "state"), false, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	if v := f.tick(); v["action"] != "none" || f.read("thr_primary.told") != "" {
		t.Fatalf("a check ignored the marker: %v", v)
	}
	if code, _, errs := f.run("resume"); code != 0 || !strings.Contains(errs, "released the hold") {
		t.Fatalf("resume: %d %s", code, errs)
	}
	if v := f.tick(); v["action"] != "wake" {
		t.Fatalf("after resume: %v", v)
	}
}

// A running daemon honours a stop saved in the marker at its next check,
// without waiting for its interval.
func TestDaemonHonoursAHoldMarker(t *testing.T) {
	f := newFixture(t)
	f.run("enable")
	write(t, filepath.Join(f.bbDir, "thr_primary.show"), `{"thread":{"status":"active"}}`, 0o600)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	var out, errb safeBuffer
	go func() { done <- run(ctx, []string{"daemon", "-c", f.cfg}, &out, &errb) }()
	waitFor(t, func() bool { return strings.Count(out.String(), "\n") >= 1 }, "first check")
	// Damaging control.json changes it, which wakes the daemon by itself.
	// Let that check pass first, so the only change left is the marker.
	damageControl(t, f, "{")
	waitFor(t, func() bool { return strings.Count(out.String(), "\n")+strings.Count(errb.String(), "\n") >= 2 }, "the check after the damage")
	// The daemon keeps running, and does not retry the failing check on
	// every control poll: several polls pass with at most one more failure.
	failures := strings.Count(errb.String(), "\n")
	time.Sleep(500 * time.Millisecond) // five control polls
	if more := strings.Count(errb.String(), "\n") - failures; more > 1 {
		t.Fatalf("%d failed checks in five control polls: the daemon loops on a damaged record\n%s", more, errb.String())
	}
	select {
	case code := <-done:
		t.Fatalf("the daemon exited (%d) over a damaged control record\n%s", code, errb.String())
	default:
	}
	start := time.Now()
	if code, _, errs := f.run("stop"); code != 0 {
		t.Fatalf("stop: %d %s", code, errs)
	}
	waitFor(t, func() bool { return strings.Contains(f.read("calls"), "thread stop thr_primary") }, "the daemon to stop the session")
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("the stop took %v; the interval is 30s, so the daemon must notice the marker", d)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the daemon did not exit on cancel")
	}
}

// The dock shows a loop held by the marker as stopped, and says why.
func TestDockShowsAStopKeptBesideAnUnreadableControlRecord(t *testing.T) {
	for _, c := range []struct {
		controls      *clientControls
		state, phrase string
	}{
		{&clientControls{Enabled: true, Stop: true}, "stopped", "Stopped (control record unreadable)"},
		{&clientControls{Enabled: true, Paused: true}, "paused", "Paused (control record unreadable)"},
	} {
		in := dockEvidence{controls: c.controls}.input()
		in.Client.Snapshot.ControlUnreadable = true
		in.Chain = &dockChainInput{}
		in.Chain.Primary.ID = "primary"
		l, need := projectDockLoop(in, dockNow, defaultStaleAfter, false)
		if l.State != c.state || l.Phrase != c.phrase || !strings.Contains(l.Detail, "Restore the record, then resume") || need == nil {
			t.Errorf("%+v %+v", l, need)
		}
	}
}

// A damaged incident journal does not fail a check that did its work: it
// exits 0, says the history is unavailable, reports degraded once and
// recovered once, and status and doctor say so.
func TestADamagedIncidentJournalDoesNotFailTheCheck(t *testing.T) {
	f := newFixture(t)
	f.run("enable")
	f.tick()
	journal := filepath.Join(f.dir, "state", "incidents.json")
	if err := os.WriteFile(journal, []byte("{not a journal"), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		code, out, errs := f.run("tick")
		var v map[string]any
		if err := json.Unmarshal([]byte(out), &v); err != nil || code != 0 || v["history"] != "unavailable" || v["outcome"] != "succeeded" {
			t.Fatalf("check %d: exit %d %v\n%s%s", i, code, err, out, errs)
		}
	}
	if got := f.hooks.types(); strings.Count(got, "keepgoing.degraded") != 1 {
		t.Fatalf("reports: %s", got)
	}
	if b, _ := os.ReadFile(journal); string(b) != "{not a journal" {
		t.Fatal("the damaged journal was rewritten")
	}
	if _, out, _ := f.run("status"); !strings.Contains(out, "HISTORY:   incidents.json cannot be written since") {
		t.Errorf("status:\n%s", out)
	}
	if _, doc, _ := f.run("doctor"); !strings.Contains(doc, "supervisor.history_unavailable") {
		t.Errorf("doctor:\n%s", doc)
	}

	// Moved aside: the next check starts a new history and reports recovery.
	if err := os.Rename(journal, journal+".damaged"); err != nil {
		t.Fatal(err)
	}
	code, out, _ := f.run("tick")
	if code != 0 || strings.Contains(out, `"history"`) {
		t.Fatalf("after moving it aside: %d %s", code, out)
	}
	if got := f.hooks.types(); !strings.HasSuffix(got, "keepgoing.recovered") || strings.Count(got, "keepgoing.degraded") != 1 {
		t.Fatalf("reports: %s", got)
	}
	if _, out, _ := f.run("status"); strings.Contains(out, "HISTORY:") {
		t.Errorf("status still warns:\n%s", out)
	}
}

// A damaged state.json still fails the check: it cannot know what it may do.
func TestADamagedStateRecordStillFailsTheCheck(t *testing.T) {
	f := newFixture(t)
	f.run("enable")
	f.tick()
	os.WriteFile(filepath.Join(f.dir, "state", "state.json"), []byte("{"), 0o600)
	if code, _, _ := f.run("tick"); code != exitFailed {
		t.Fatalf("exit %d", code)
	}
}
