//go:build unix

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
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

// A damaged marker beside a damaged control record does not stand in for
// it: the check fails and sends nothing, as with no marker. Beside a readable
// record it is a stop (see TestAMarkerThatHoldsNothingIsAStop).
func TestADamagedHoldMarkerDoesNotStandInForTheRecord(t *testing.T) {
	f := newFixture(t)
	f.run("enable")
	f.tick()
	write(t, filepath.Join(f.bbDir, "thr_primary.show"), `{"thread":{"status":"active"}}`, 0o600)
	damageControl(t, f, "")
	write(t, filepath.Join(f.dir, "state", control.HoldFileName), "{", 0o600)
	before := f.read("calls")
	if code, _, _ := f.run("tick"); code != exitFailed || strings.Contains(strings.TrimPrefix(f.read("calls"), before), "thread") {
		t.Fatalf("exit %d, calls %q", code, strings.TrimPrefix(f.read("calls"), before))
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

// The marker may only make keepgoing do less. One that parses but holds
// nothing, or holds anything other than exactly a version 1 stop or pause,
// is a stop. With control.json damaged it does not let a check past the
// owner and transfer checks: the check sends nothing, probes nothing and
// fails as it would with no marker at all.
func TestAMarkerThatHoldsNothingIsAStop(t *testing.T) {
	for name, body := range map[string]string{
		"empty object": `{}`, "null": `null`, "another version": `{"version":99}`,
		"enabled and no hold":   `{"stop":false,"paused":false,"enabled":true}`,
		"version 1 and no hold": `{"version":1,"stop":false,"paused":false}`,
		"an unknown field":      `{"version":1,"paused":true,"enabled":true}`,
		"two values":            `{"version":1,"paused":true} {"version":1}`,
		"oversized":             strings.Repeat(" ", 5000) + `{"version":1,"paused":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.run("enable")
			f.tick()
			damageControl(t, f, "{")
			write(t, filepath.Join(f.dir, "state", control.HoldFileName), body, 0o600)
			told, probes := f.read("thr_primary.told"), f.read("probes")
			code, out, _ := f.run("tick")
			if code != exitFailed || f.read("thr_primary.told") != told || f.read("probes") != probes {
				t.Fatalf("a check with an unusable marker did work: exit %d %s\nprobes %q", code, out, f.read("probes"))
			}
			if _, out, _ := f.run("status"); !strings.Contains(out, "control:   stopped (invalid hold marker)") || strings.Contains(out, "since never") {
				t.Fatalf("status:\n%s", out)
			}
		})
	}
	// With control.json readable, such a marker stops a running session.
	f := newFixture(t)
	f.run("enable")
	f.tick()
	write(t, filepath.Join(f.bbDir, "thr_primary.show"), `{"thread":{"status":"active"}}`, 0o600)
	write(t, filepath.Join(f.dir, "state", control.HoldFileName), `{"stop":false,"enabled":true}`, 0o600)
	if v := f.tick(); v["action"] != "stop" {
		t.Fatalf("a readable control record with an unusable marker: %v", v)
	}
}

// A marker that is not a small regular file is a stop, and is not opened.
func TestAMarkerThatIsNotARegularFileIsAStop(t *testing.T) {
	for _, kind := range []string{"named pipe", "link to a device", "link to a valid marker"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			f.run("enable")
			f.tick()
			write(t, filepath.Join(f.bbDir, "thr_primary.show"), `{"thread":{"status":"active"}}`, 0o600)
			marker := filepath.Join(f.dir, "state", control.HoldFileName)
			switch kind {
			case "named pipe":
				if err := mkfifo(marker); err != nil {
					t.Skip("not run: named pipes are not available here:", err)
				}
			case "link to a device":
				if err := os.Symlink("/dev/zero", marker); err != nil {
					t.Fatal(err)
				}
			case "link to a valid marker":
				elsewhere := filepath.Join(f.dir, "elsewhere.json")
				write(t, elsewhere, `{"version":1,"paused":true}`, 0o600)
				if err := os.Symlink(elsewhere, marker); err != nil {
					t.Fatal(err)
				}
			}
			start := time.Now()
			v := f.tick()
			if v["action"] != "stop" || time.Since(start) > 5*time.Second {
				t.Fatalf("%s: %v after %s", kind, v, time.Since(start))
			}
		})
	}
}

// The daemon's early-wake poll queries nothing while a marker holds the loop.
func TestTheWakePollQueriesNothingUnderAMarker(t *testing.T) {
	f := newFixture(t)
	f.run("enable")
	f.tick()
	if _, err := control.SaveHold(filepath.Join(f.dir, "state"), false, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	cfg := loadFixture(t, f.cfg)
	before := f.read("calls")
	if pollWake(context.Background(), cfg) {
		t.Fatal("the poll asked for an early check")
	}
	if got := strings.TrimPrefix(f.read("calls"), before); got != "" {
		t.Fatalf("the poll queried the provider under a pause marker:\n%s", got)
	}
}

// Stops racing each other on a damaged control record either save the hold
// or say to try again (75); none reports a failure.
func TestConcurrentStopsWithADamagedControlRecord(t *testing.T) {
	f := newFixture(t)
	f.run("enable")
	damageControl(t, f, "{")
	codes := make(chan int, 16)
	for i := 0; i < 16; i++ {
		go func() { c, _, _ := f.run("stop"); codes <- c }()
	}
	for i := 0; i < 16; i++ {
		if c := <-codes; c != 0 && c != exitSkipped {
			t.Errorf("a stop exited %d", c)
		}
	}
	if h, err := control.LoadHold(filepath.Join(f.dir, "state")); err != nil || h == nil || !h.Stop {
		t.Fatalf("marker: %+v %v", h, err)
	}
}

// Both records damaged: the check fails, nothing is overwritten, and no
// degraded report is sent, because the check's own work was not saved.
func TestDamagedStateAndJournalTogetherFailTheCheck(t *testing.T) {
	f := newFixture(t)
	f.run("enable")
	f.tick()
	state, journal := filepath.Join(f.dir, "state", "state.json"), filepath.Join(f.dir, "state", "incidents.json")
	os.WriteFile(state, []byte("{"), 0o600)
	os.WriteFile(journal, []byte("{"), 0o600)
	if code, _, _ := f.run("tick"); code != exitFailed {
		t.Fatalf("exit %d", code)
	}
	for _, p := range []string{state, journal} {
		if b, _ := os.ReadFile(p); string(b) != "{" {
			t.Errorf("%s was overwritten", filepath.Base(p))
		}
	}
	if strings.Contains(f.hooks.types(), "degraded") {
		t.Fatalf("reports: %s", f.hooks.types())
	}
}

// A control record written by the current main build keeps every field
// through a stop saved in the marker while it was unreadable, an ordinary
// stop beside the marker, and the resume that releases it. Only the holds
// and the note resume clears may change. A field added to control.File
// without being set here fails the test, so a build that drops it on its
// next control write cannot pass.
func TestEveryControlFieldSurvivesAMarkerStopAndResume(t *testing.T) {
	f := newFixture(t)
	dir := filepath.Join(f.dir, "state")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	full := control.File{
		// A pending transfer refuses resume; it is left out here, and
		// TestClientHoldPreservesStateDuringTickAndTransfer shows a stop keeps it.
		BBTarget:     "thr_primary",
		Ownership:    &control.Ownership{ThreadID: "thr_example_owner", Generation: 4, Previous: &control.Handoff{ThreadID: "thr_example_before", Status: "archived", At: at}},
		Enabled:      true,
		Paused:       true,
		Stop:         true,
		AwaitingUser: true,
		Completed:    true,
		Note:         "Example note",
		EpisodeHold:  true,
		OpenQuestion: &control.OpenQuestion{Note: "Example question", At: at},
		UpdatedAt:    at,
	}
	v := reflect.ValueOf(full)
	for i := 0; i < v.NumField(); i++ {
		if name := v.Type().Field(i).Name; name != "PendingTransfer" && v.Field(i).IsZero() {
			t.Fatalf("control.File.%s is not set by this test; set it so it is checked", name)
		}
	}
	if err := control.Save(dir, full); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, control.FileName)
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// Damaged: the stop goes to the marker and the record is left alone.
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out, errs := f.run("stop"); code != 0 {
		t.Fatalf("stop: %d %s%s", code, out, errs)
	}
	// Restored from a copy: an ordinary stop beside the marker keeps every field.
	if err := os.WriteFile(path, written, 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out, errs := f.run("stop"); code != 0 {
		t.Fatalf("stop: %d %s%s", code, out, errs)
	}
	got, err := control.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	got.UpdatedAt = full.UpdatedAt
	if !reflect.DeepEqual(got, full) {
		t.Fatalf("stop changed the record:\n got %+v\nwant %+v", got, full)
	}

	if code, out, errs := f.run("resume"); code != 0 {
		t.Fatalf("resume: %d %s%s", code, out, errs)
	}
	if h, err := control.LoadHold(dir); h != nil || err != nil {
		t.Fatal("resume left the marker", h, err)
	}
	got, err = control.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := full
	want.Paused, want.Stop, want.AwaitingUser, want.Completed = false, false, false, false
	want.EpisodeHold, want.OpenQuestion, want.Note = false, nil, ""
	want.UpdatedAt = got.UpdatedAt
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resume changed more than the holds:\n got %+v\nwant %+v", got, want)
	}
}

// A named pipe in place of control.json is refused at once, not waited on,
// so stop still saves its hold in the marker instead of hanging.
func TestAPipeInPlaceOfControlJSONDoesNotHangStop(t *testing.T) {
	f := newFixture(t)
	f.run("enable")
	dir := filepath.Join(f.dir, "state")
	path := filepath.Join(dir, control.FileName)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := mkfifo(path); err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 1)
	go func() {
		code, _, _ := f.run("stop")
		done <- code
	}()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("stop exited %d", code)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("stop blocked on a pipe at control.json")
	}
	if h, err := control.LoadHold(dir); err != nil || h == nil || !h.Stop {
		t.Fatal(h, err)
	}
}

// Something under the marker's name that is not a valid hold reads as a
// stop, and status says so instead of showing it as a valid stop; output
// after enable never calls a held loop enabled.
func TestStatusAndEnableShowTheMarker(t *testing.T) {
	f := newFixture(t)
	f.run("enable")
	dir := filepath.Join(f.dir, "state")
	if _, err := control.SaveHold(dir, false, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := f.run("enable"); code != 0 || strings.Contains(out, ": enabled") || !strings.Contains(out, "paused") {
		t.Fatalf("enable with a pause marker: %d %s", code, out)
	}
	if err := os.WriteFile(filepath.Join(dir, control.HoldFileName), []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, out, _ := f.run("status"); !strings.Contains(out, "control:   stopped (invalid hold marker)") || !strings.Contains(out, "is not a valid hold") {
		t.Fatalf("status:\n%s", out)
	}
	_, out, _ := f.run("status", "--json")
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil || v["controlState"] != "stopped (invalid hold marker)" || v["controlHoldInvalid"] != true {
		t.Fatalf("status --json: %v %s", err, out)
	}
	if code, out, _ := f.run("enable"); code != 0 || !strings.Contains(out, "stopped (invalid hold marker)") {
		t.Fatalf("enable with an invalid marker: %d %s", code, out)
	}
	// With control.json damaged as well, the invalid marker is still shown.
	if err := os.WriteFile(filepath.Join(dir, control.FileName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, out, _ := f.run("status"); !strings.Contains(out, "control:   stopped (invalid hold marker)") {
		t.Fatalf("status:\n%s", out)
	}
}
