package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/incident"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// Each pair of build labels, the last check's and the panel's, mapped to the
// exact words shown. Labels are compared whole; "dev" says nothing about
// which build it is; which is newer is said only for two plain releases.
func TestDockBuildComparesWholeLabels(t *testing.T) {
	for _, c := range []struct {
		recorded, panel     string
		availability, order string
		text                string
	}{
		{"", "v0.1.5", "not_recorded", "", "Which keepgoing build ran its last check is not recorded."},
		{"dev", "dev", "unknown", "", "Its last check and this panel both ran a keepgoing build with no version recorded (dev), so whether they are the same build is unknown."},
		{"dev", "v0.1.5", "unknown", "", "Its last check ran a keepgoing build with no version recorded (dev); this panel is keepgoing v0.1.5. Whether they are the same build is unknown."},
		{"v0.1.5", "dev", "unknown", "", "Its last check ran keepgoing v0.1.5; this panel is a build with no version recorded (dev). Whether they are the same build is unknown."},
		{"v0.1.5", "v0.1.5", "same", "", "Its last check ran this panel's build, keepgoing v0.1.5."},
		{"26f91db-dirty", "26f91db-dirty", "unknown", "", "Its last check and this panel both ran keepgoing 26f91db-dirty, built with uncommitted changes, so whether they are the same build is unknown."},
		{"v0.1.5-dev.26f91db", "v0.1.5-dev.26f91db", "same", "", "Its last check ran this panel's build, keepgoing v0.1.5-dev.26f91db."},
		// Two dev builds of one release are different builds, with no order.
		{"v0.1.5-dev.26f91db", "v0.1.5-dev.ab565b1", "different", "", "Its last check ran keepgoing v0.1.5-dev.26f91db; this panel is keepgoing v0.1.5-dev.ab565b1. They are different builds."},
		{"v0.1.4", "v0.1.5", "different", "older", "Its last check ran keepgoing v0.1.4, an older release than this panel's v0.1.5."},
		{"v0.1.10", "v0.1.9", "different", "newer", "Its last check ran keepgoing v0.1.10, a newer release than this panel's v0.1.9."},
		{"v1.0.0", "v0.9.9", "different", "newer", "Its last check ran keepgoing v1.0.0, a newer release than this panel's v0.9.9."},
		// A release and a dev build of it: different, and no claim of order.
		{"v0.1.5", "v0.1.5-dev.26f91db", "different", "", "Its last check ran keepgoing v0.1.5; this panel is keepgoing v0.1.5-dev.26f91db. They are different builds."},
		// A dev build of an earlier release is still not "older".
		{"v0.1.4-dev.26f91db", "v0.1.5", "different", "", "Its last check ran keepgoing v0.1.4-dev.26f91db; this panel is keepgoing v0.1.5. They are different builds."},
		{"dev-26f91db", "26f91db", "different", "", "Its last check ran keepgoing dev-26f91db; this panel is keepgoing 26f91db. They are different builds."},
		{"v0.1.5\nInjected", "v0.1.5", "different", "", "Its last check ran keepgoing unreadable; this panel is keepgoing v0.1.5. They are different builds."},
	} {
		b := dockBuildOf(c.recorded, c.panel)
		if b.Availability != c.availability || b.Order != c.order || b.Text != c.text {
			t.Errorf("%q vs %q:\n got  %s %q %q\n want %s %q %q", c.recorded, c.panel, b.Availability, b.Order, b.Text, c.availability, c.order, c.text)
		}
	}
	if b := dockBuildOf(strings.Repeat("v", 200), "v0.1.5"); len([]rune(b.Recorded)) != dockMaxBuild {
		t.Errorf("a long label is %d runes", len([]rune(b.Recorded)))
	}
	for label, want := range map[string]string{"v0.1.5": "This page is served by keepgoing v0.1.5.", "dev": "This page is served by a keepgoing build with no version recorded (dev)."} {
		if got := dockPanelVersionText(label); got != want {
			t.Errorf("%q: %q", label, got)
		}
	}
}

// The last check's label comes from state, or, when that check recorded none,
// from the newest retained check record.
func TestDockLastBuildIsTheLastChecksLabel(t *testing.T) {
	tl := &timelineReport{Events: []timelineEntry{
		{Incident: &incident.Record{Kind: "tick", Build: "v0.1.3"}},
		{Incident: &incident.Record{Kind: "tick", Build: "v0.1.4"}},
		// The newest record is a control command, run by another binary.
		{Incident: &incident.Record{Kind: "control", Build: "v0.1.9"}},
	}}
	withVersion := &statusOut{State: state.File{LastCheck: &state.Check{Version: "v0.1.5"}}}
	withoutVersion := &statusOut{State: state.File{LastCheck: &state.Check{}}}
	for name, c := range map[string]struct {
		st   *statusOut
		tl   *timelineReport
		want string
	}{
		"state's label":                        {withVersion, tl, "v0.1.5"},
		"the newest check record, not control": {withoutVersion, tl, "v0.1.4"},
		"no status":                            {nil, tl, "v0.1.4"},
		"nothing recorded":                     {withoutVersion, &timelineReport{}, ""},
		"the history's word for no label":      {withoutVersion, &timelineReport{Events: []timelineEntry{{Incident: &incident.Record{Kind: "tick", Build: "unavailable"}}}}, ""},
		"no history":                           {nil, nil, ""},
	} {
		if got := dockLastBuild(c.st, c.tl); got != c.want {
			t.Errorf("%s: %q", name, got)
		}
	}
}

// A loop whose last check ran another build is listed under "Worth knowing";
// one on the panel's build is not. Neither is ever a need. The server says
// which build serves the page.
func TestDockBuildInTheReportAndTheServerInfo(t *testing.T) {
	previous := version
	version = "v0.1.5"
	t.Cleanup(func() { version = previous })
	loop := func(build string) dockInput {
		return dockEvidence{controls: &clientControls{Enabled: true}, status: func(s *statusOut) { s.State.LastCheck.Version = build }}.input()
	}
	other, same := loop("v0.1.4"), loop("v0.1.5")
	other.Entry.ID, other.Entry.Loop = "loop_00000000000000000000000000000001", "example-other"
	same.Entry.ID, same.Entry.Loop = "loop_00000000000000000000000000000002", "example-same"
	r := buildDockReport([]dockInput{other, same}, dockNow, time.Minute, defaultStaleAfter, false, true, 0, true)
	if !r.AllClear || len(r.NeedsYou) != 0 || len(r.Worth) != 1 || r.Worth[0].Loop != "example-other" ||
		r.Worth[0].Text.render(dockNow) != "Its last check ran keepgoing v0.1.4, an older release than this panel's v0.1.5." {
		t.Fatalf("allClear %t needs %d worth %+v", r.AllClear, len(r.NeedsYou), r.Worth)
	}
	if b := dockLoopNamed(t, r, "example-same").Build; b.Availability != "same" {
		t.Errorf("%+v", b)
	}
	base, _, _ := dockServe(t)
	resp, body := dockRequest(t, http.MethodGet, base+dockServerPath, nil)
	var served dockServed
	if resp.StatusCode != http.StatusOK || json.Unmarshal([]byte(body), &served) != nil || served.Version != "v0.1.5" || served.VersionText != "This page is served by keepgoing v0.1.5." {
		t.Errorf("%d %s", resp.StatusCode, body)
	}
}
