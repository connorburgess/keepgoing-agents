//go:build unix

package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter/runner"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// A standing refusal to start turns is said by status and by doctor, with the
// adapter's fixed reason, so it is not only in the run log.
func TestStatusAndDoctorSayWhyNoTurnCanBeStarted(t *testing.T) {
	f := newFixture(t)
	schedulerConfig(t, f, "")
	f.run("enable")
	if code, out, errs := f.run("tick"); code != exitOK {
		t.Fatalf("%d %s %s", code, out, errs)
	}
	dir := filepath.Join(f.dir, "state")
	if _, status, _ := f.run("status"); strings.Contains(status, "turns:") {
		t.Fatalf("status reports a refusal that is not recorded:\n%s", status)
	}
	if _, doctor, _ := f.run("doctor", "--json"); strings.Contains(doctor, `"runner.wrapper"`) {
		t.Fatalf("doctor reports a refusal that is not recorded:\n%s", doctor)
	}
	st, err := state.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	since := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	st.StartRefused = &state.StartRefused{Since: since, Reason: runner.ReasonWrapperDiffers}
	if err := state.Save(dir, st); err != nil {
		t.Fatal(err)
	}
	_, status, _ := f.run("status")
	if !strings.Contains(status, "turns:     none can be started, since 2026-11-09T12:00:00Z: "+runner.ReasonWrapperDiffers) || !strings.Contains(status, "Nothing is sent or counted") {
		t.Fatalf("status does not say why no turn can be started:\n%s", status)
	}
	_, doctor, _ := f.run("doctor", "--json")
	if !strings.Contains(doctor, `"runner.wrapper"`) || !strings.Contains(doctor, runner.ReasonWrapperDiffers) || !strings.Contains(doctor, "start the daemon again") {
		t.Fatalf("doctor does not say why no turn can be started:\n%s", doctor)
	}
}
