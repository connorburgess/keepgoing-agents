package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/incident"
	"github.com/connorburgess/keepgoing-agents/internal/probe"
	"github.com/connorburgess/keepgoing-agents/internal/registry"
	"github.com/connorburgess/keepgoing-agents/internal/report"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

var dockNow = time.Date(2030, 3, 4, 12, 0, 0, 0, time.UTC)

func dockFixtureReader(t *testing.T) (*dockReader, string) {
	t.Helper()
	root := t.TempDir()
	dir := dockScratch(t, root, dockNow)
	runsClock(t, dockNow)
	return &dockReader{directory: dir, refresh: dockDefaultRefresh, staleAfter: defaultStaleAfter, query: inspectDaemon, now: func() time.Time { return dockNow }}, root
}

func dockLoopNamed(t *testing.T, r dockReport, name string) dockLoop {
	t.Helper()
	for _, l := range r.Loops {
		if l.Name == name {
			return l
		}
	}
	t.Fatalf("no loop %s in %+v", name, r.Loops)
	return dockLoop{}
}

// treeDigest fingerprints every file and directory under root.
func treeDigest(t *testing.T, root string) string {
	t.Helper()
	h := sha256.New()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s %v %d %d\n", path, info.Mode(), info.Size(), info.ModTime().UnixNano())
		if info.Mode().IsRegular() {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			h.Write(b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func TestDockReportCoversEveryStateFromFixtures(t *testing.T) {
	d, _ := dockFixtureReader(t)
	r := d.read(context.Background(), false)
	if r.SchemaVersion != 1 || !r.ReadOnly || !r.Complete || r.AllClear || r.Unread != 0 || !r.ReadAt.Equal(dockNow) || r.RefreshSeconds != 15 ||
		r.Registry.Availability != "available" || r.Registry.Count != len(dockLoops) || len(r.Loops) != len(dockLoops) {
		t.Fatalf("%+v", r)
	}
	want := map[string]string{
		"docs-site-refresh": "supervising", "billing-api-migration": "waiting_for_you", "mobile-onboarding": "paused", "search-indexer": "waiting_on_limit",
		"release-notes-bot": "finished", "nightly-dependency-bumps": "not_checked_recently", "data-export-cleanup": "off", "payments-reconciler": "supervising",
	}
	for name, state := range want {
		if l := dockLoopNamed(t, r, name); l.State != state || l.Phrase == "" || l.Detail == "" || l.Evidence != "available" {
			t.Errorf("%s: state %q phrase %q evidence %q", name, l.State, l.Phrase, l.Evidence)
		}
	}
	// Loops that want attention come first.
	if r.Loops[0].Name != "billing-api-migration" || r.Loops[1].Name != "nightly-dependency-bumps" {
		t.Errorf("order: %s, %s", r.Loops[0].Name, r.Loops[1].Name)
	}

	// Needs you: the hold's recorded cause and how long it has waited, from
	// the retained history record of the hold. The note is withheld.
	if len(r.NeedsYou) != 1 {
		t.Fatalf("%+v", r.NeedsYou)
	}
	n := r.NeedsYou[0]
	if n.Kind != "held" || n.Loop != "billing-api-migration" || n.Reason != "A lane is waiting for an answer." || n.Note != "" || !n.NoteWithheld ||
		n.SinceEvidence != "hold_recorded" || n.WaitedSeconds == nil || *n.WaitedSeconds != int64((2*time.Hour+14*time.Minute)/time.Second) || !strings.Contains(n.Action, "resume") {
		t.Errorf("%+v", n)
	}

	docs := dockLoopNamed(t, r, "docs-site-refresh")
	if c := docs.Children; c.Availability != "current" || !c.Holding || c.Working != 3 || c.Stuck != 0 || c.Failed != 1 || c.Phrase != "3 working, 1 failed" {
		t.Errorf("lanes %+v", c)
	}
	if docs.Detail != "On. Its last check sent nothing because lanes are working or waiting." {
		t.Errorf("%q", docs.Detail)
	}
	if c := docs.LastCheck; c.Availability != "recent" || c.AgeSeconds == nil || *c.AgeSeconds != 180 || c.Phrase != "Checked; sent nothing" {
		t.Errorf("last check %+v", c)
	}
	if docs.CurrentRoute != (dockTarget{Availability: "known", Role: "main", Name: "primary", Tool: "BB thread", Provider: "claude-code", Model: "claude-opus-5-5"}) {
		t.Errorf("%+v", docs.CurrentRoute)
	}
	if docs.NextCheck.Availability != "unknown" || !strings.Contains(docs.NextCheck.Detail, "no keepgoing daemon endpoint is recorded") {
		t.Errorf("next check must be unknown without a daemon: %+v", docs.NextCheck)
	}
	if f := docs.Fallbacks; f.Availability != "known" || f.Ready != 1 || f.Eligible != 2 || !f.Enough {
		t.Errorf("%+v", f)
	}
	// Its routes: the first route, then the others in configured order.
	ch := docs.Chain
	if ch.Availability != "known" || len(ch.HandoffOn) != 1 || ch.HandoffOn[0] != "rate_limit" || len(ch.Entries) != 4 {
		t.Fatalf("%+v", ch)
	}
	for i, w := range []struct{ id, readiness string }{{"primary", "main"}, {"claude-second-account", "ready"}, {"codex-thread", "not_ready"}, {"example-cli", "disabled"}} {
		if e := ch.Entries[i]; e.Position != i || e.ID != w.id || e.Readiness != w.readiness || e.Current != (i == 0) || e.InUse != (i == 0) {
			t.Errorf("route %d: %+v", i, e)
		}
	}
	if ch.Entries[0].Use != "In use now" {
		t.Errorf("%q", ch.Entries[0].Use)
	}
	if u := ch.Entries[0].Usage; u.Availability != "available" || len(u.Windows) != 2 || u.Windows[0].Name != "5-hour" || u.Windows[0].UsedPercent != 38 || u.AgeSeconds == nil || *u.AgeSeconds != 720 {
		t.Errorf("%+v", u)
	}
	if e := ch.Entries[2]; e.Phrase != "Sign-in failed at its last probe" || e.Usage.Availability != "unavailable" || e.ProbeAge == nil {
		t.Errorf("%+v", e)
	}

	// "In use" needs an enabled loop with no hold and a recent check that
	// recorded the target. Anything else says what is recorded.
	for name, use := range map[string]string{
		"billing-api-migration": "Recorded route; loop is held", "mobile-onboarding": "Recorded route; loop is paused", "release-notes-bot": "Recorded route; loop is finished",
		"data-export-cleanup": "Recorded route; loop is off", "nightly-dependency-bumps": "Recorded route; no recent check",
	} {
		if e := dockLoopNamed(t, r, name).Chain.Entries[0]; !e.Current || e.InUse || e.Use != use {
			t.Errorf("%s: %+v", name, e)
		}
	}

	// The same data by route: one entry per configured model on an account.
	if e := ch.Entries[1]; e.Route == "" || e.Route == ch.Entries[0].Route || e.Account != "second-account" {
		t.Errorf("%+v", e)
	}
	routes := map[string]*dockRoute{}
	for i := range r.Routes {
		routes[r.Routes[i].Model+"/"+r.Routes[i].Account] = &r.Routes[i]
	}
	opus, sonnet, haiku := routes["claude-opus-5-5/claude-code"], routes["claude-sonnet-5-5/second-account"], routes["claude-haiku-5-5/claude-code"]
	if len(r.Routes) != 7 || opus == nil || sonnet == nil || haiku == nil || opus.Key != ch.Entries[0].Route || opus.Lanes != "unknown" {
		t.Fatalf("routes %+v", r.Routes)
	}
	names := func(list []dockRouteLoop) string {
		out := []string{}
		for _, x := range list {
			out = append(out, x.Loop)
		}
		return strings.Join(out, ",")
	}
	// Only the loop that is in use counts as running on the route; held,
	// paused and stale loops are recorded for it, and off and finished loops
	// are never "running".
	if names(opus.RunningNow) != "docs-site-refresh" || names(opus.RecordedFor) != "billing-api-migration,nightly-dependency-bumps,mobile-onboarding" ||
		names(opus.StandbyFor) != "payments-reconciler" || len(opus.UnknownFor) != 0 || opus.RunningNow[0].Working != 3 {
		t.Errorf("opus: running %q recorded %q standby %q", names(opus.RunningNow), names(opus.RecordedFor), names(opus.StandbyFor))
	}
	if len(haiku.RunningNow) != 0 || names(haiku.RecordedFor) != "data-export-cleanup,release-notes-bot" {
		t.Errorf("haiku: running %q recorded %q", names(haiku.RunningNow), names(haiku.RecordedFor))
	}
	// The newest evidence any loop holds wins: a 12-minute-old reading over
	// nightly-dependency-bumps' stale one, a 47-minute-old passing probe over
	// the 30- and 32-hour-old ones.
	if opus.Usage.Availability != "available" || opus.Usage.Windows[0].UsedPercent != 38 || sonnet.Readiness != "ready" || *sonnet.ProbeAge != 47*60 ||
		len(sonnet.StandbyFor) != 4 || names(sonnet.RunningNow) != "payments-reconciler" || sonnet.StandbyFor[0].Position != 1 {
		t.Errorf("opus %+v sonnet %+v", opus.Usage, sonnet)
	}

	// A loop running on a later route whose last probe is old: the current
	// target is not probed, so it gets no readiness verdict, only the age.
	pay := dockLoopNamed(t, r, "payments-reconciler")
	if pay.CurrentRoute.Role != "fallback" || pay.CurrentRoute.Name != "claude-second-account" || pay.Chain.Entries[0].Current {
		t.Errorf("%+v", pay.CurrentRoute)
	}
	if e := pay.Chain.Entries[1]; !e.Current || !e.InUse || e.Readiness != "current" || e.Phrase != "In use now" || e.ProbeAge == nil || *e.ProbeAge != int64(32*time.Hour/time.Second) {
		t.Errorf("%+v", e)
	}

	// Held: the lane listing is the one verified by the check that held it.
	if c := dockLoopNamed(t, r, "billing-api-migration").Children; c.Availability != "held" || c.Working != 1 || c.Stuck != 1 {
		t.Errorf("%+v", c)
	}

	// A limit with its reset, and a send allowance.
	search := dockLoopNamed(t, r, "search-indexer")
	if w := search.LimitWait; w == nil || w.Passed || w.ResetsAt == nil || !w.ResetsAt.Equal(dockNow.Add(2*time.Hour+20*time.Minute)) {
		t.Errorf("%+v", w)
	}
	if len(search.Limits) != 1 || search.Limits[0].Phrase != "41 of 60" || search.Limits[0].Reached {
		t.Errorf("%+v", search.Limits)
	}
	if search.Fallbacks.Ready != 0 || search.Chain.Entries[1].Phrase != "Hit a usage limit at its last probe" || search.Chain.Entries[0].Usage.Windows[0].UsedPercent != 100 || !search.Chain.Entries[0].InUse {
		t.Errorf("%+v %+v", search.Fallbacks, search.Chain.Entries)
	}

	// Stale evidence is labelled, never shown as current.
	nightly := dockLoopNamed(t, r, "nightly-dependency-bumps")
	if nightly.LastCheck.Availability != "stale" || nightly.Chain.Entries[0].Usage.Availability != "stale" || !nightly.Chain.Entries[0].Usage.Windows[0].Stale || nightly.Chain.Entries[1].Readiness != "not_ready" {
		t.Errorf("%+v %+v", nightly.LastCheck, nightly.Chain.Entries)
	}

	// Missing evidence is explicit.
	off := dockLoopNamed(t, r, "data-export-cleanup")
	if off.LastCheck.Availability != "never" || off.Children.Availability != "none_recorded" || off.Fallbacks.Availability != "none_configured" || off.Chain.Availability != "none_configured" || len(off.Chain.Entries) != 1 {
		t.Errorf("%+v", off)
	}
}

// dockEvidence builds one loop's inputs for the phrase tables.
type dockEvidence struct {
	controls *clientControls
	noSnap   bool
	issues   []string
	status   func(*statusOut) // nil status when nil and noStatus
	noStatus bool
	blocker  string
	hold     *dockHold
}

func (ev dockEvidence) input() dockInput {
	in := dockInput{Entry: registry.Entry{ID: "loop_0123456789abcdef0123456789abcdef", Loop: "example-loop"}, Hold: ev.hold}
	in.Client = clientStatusReport{Outcome: "available", Issues: append([]string{}, ev.issues...)}
	if !ev.noSnap {
		in.Client.Snapshot = &clientSnapshot{Loop: "example-loop", Controls: ev.controls, Daemon: daemonStatus{State: "not_recorded"}}
	}
	if !ev.noStatus {
		at := dockNow.Add(-time.Minute)
		st := &statusOut{Target: "primary", TargetConfigured: true, InWindow: true, LimitWait: limitWaitStatus{State: "none"},
			State: state.File{CheckedAt: at, LastCheck: &state.Check{At: at, Target: "primary", Action: "none", Outcome: "succeeded"}}}
		if ev.status != nil {
			ev.status(st)
		}
		in.Status = st
	}
	if ev.blocker != "" {
		in.RunsKnown, in.Runs = true, runsLoop{Blocker: runsBlocker{Availability: ev.blocker, Children: []runsChild{{Status: "active"}}}}
	}
	return in
}

// TestDockPhrasesFollowEvidence maps each record the view can be given to the
// exact words it shows. A phrase that says more than its record does has to
// be written into this table to pass, where a reviewer sees it.
func TestDockPhrasesFollowEvidence(t *testing.T) {
	on := &clientControls{Enabled: true}
	reset := dockNow.Add(time.Hour)
	for _, c := range []struct {
		name                  string
		ev                    dockEvidence
		state, phrase, detail string
		need, use             string
	}{
		{"no snapshot from registered status", dockEvidence{noSnap: true, noStatus: true, issues: []string{"registration_unavailable"}},
			"unknown", "Can't be read", "keepgoing could not read this loop, so nothing about it is shown.",
			"This registration is no longer in the registry. Whether it needs you is unknown.", ""},
		{"a pending transfer returns no snapshot", dockEvidence{noSnap: true, noStatus: true, issues: []string{"transfer_pending"}},
			"unknown", "Can't be read", "keepgoing could not read this loop, so nothing about it is shown.",
			"A move to another conversation is in progress, which holds it.", ""},
		{"a read cut off returns no snapshot", dockEvidence{noSnap: true, noStatus: true, issues: []string{"cancelled"}},
			"unknown", "Can't be read", "keepgoing could not read this loop, so nothing about it is shown.",
			"The read was cut off before this loop was read. Whether it needs you is unknown.", ""},
		{"control.json unreadable", dockEvidence{},
			"unknown", "Unknown", "Its control record could not be read, so whether it is on, held or stopped is unknown.",
			"Its control record could not be read. Whether it needs you is unknown.", "Recorded route"},
		{"stop flag", dockEvidence{controls: &clientControls{Enabled: true, Stop: true, Paused: true, AwaitingUser: true}},
			"stopped", "Stopped", "A stop is requested. Nothing is sent until it is resumed.", "", "Recorded route; loop is stopped"},
		{"completed flag", dockEvidence{controls: &clientControls{Enabled: true, Completed: true}},
			"finished", "Finished", "Marked finished, which is a declaration and is not verified. Nothing is sent until it is resumed.", "", "Recorded route; loop is finished"},
		{"awaiting-user flag, cause not retained", dockEvidence{controls: &clientControls{Enabled: true, AwaitingUser: true}},
			"waiting_for_you", "Waiting for you", "It is held for a person.", "It is held for a person.", "Recorded route; loop is held"},
		{"awaiting-user flag set by the await-user command", dockEvidence{controls: &clientControls{Enabled: true, AwaitingUser: true}, hold: &dockHold{At: dockNow, Marked: true}},
			"waiting_for_you", "Waiting for you", "It was marked as waiting for a person.", "It was marked as waiting for a person.", "Recorded route; loop is held"},
		{"awaiting-user flag set by another control operation", dockEvidence{controls: &clientControls{Enabled: true, AwaitingUser: true}, hold: &dockHold{At: dockNow}},
			"waiting_for_you", "Waiting for you", "It is held for a person.", "It is held for a person.", "Recorded route; loop is held"},
		{"awaiting-user flag set by a check on lane input", dockEvidence{controls: &clientControls{Enabled: true, AwaitingUser: true}, hold: &dockHold{At: dockNow, Cause: "child_input"}},
			"waiting_for_you", "Waiting for you", "A lane is waiting for an answer.", "A lane is waiting for an answer.", "Recorded route; loop is held"},
		{"paused flag", dockEvidence{controls: &clientControls{Enabled: true, Paused: true}},
			"paused", "Paused", "Paused. Nothing is sent until it is resumed.", "", "Recorded route; loop is paused"},
		{"enabled is false", dockEvidence{controls: &clientControls{}},
			"off", "Off", "Supervision is off for this loop.", "", "Recorded route; loop is off"},
		{"on, status unreadable", dockEvidence{controls: on, noStatus: true},
			"unknown", "Unknown", "It is on, but its check history could not be read.", "Its check history could not be read. Whether it needs you is unknown.", ""},
		{"on, outside the operating window", dockEvidence{controls: on, status: func(s *statusOut) { s.InWindow = false }},
			"outside_hours", "Outside its hours", "It is on, and the time is outside its configured operating hours.", "", "Recorded route; outside its hours"},
		{"on, no check recorded", dockEvidence{controls: on, status: func(s *statusOut) { s.State = state.File{} }},
			"not_checked_recently", "Not checked yet", "It is on, and no check is recorded.", "", "Recorded route; no recent check"},
		{"on, last check older than stale-after", dockEvidence{controls: on, status: func(s *statusOut) { s.State.CheckedAt = dockNow.Add(-2 * time.Hour) }},
			"not_checked_recently", "Not checked recently", "It is on, and its last recorded check is older than 30 min.", "", "Recorded route; no recent check"},
		{"on, recorded limit wait, reset in the future", dockEvidence{controls: on, status: func(s *statusOut) {
			s.LimitWait = limitWaitStatus{State: "reported", Target: "primary", ResetsAt: &reset}
		}},
			"waiting_on_limit", "Waiting for a limit to reset", "A check recorded a provider usage limit with no other route able to take over. The loop is not held and keeps checking.", "", "In use now"},
		{"on, failed checks in a row", dockEvidence{controls: on, status: func(s *statusOut) { s.State.FailedChecks = 2 }},
			"checks_failing", "Checks are failing", "Checks are running but could not observe or act.", "", "In use now"},
		{"on, recent check", dockEvidence{controls: on},
			"supervising", "Supervising", "On, with a recent check.", "", "In use now"},
		{"on, recent check that recorded lane work as its reason", dockEvidence{controls: on, blocker: blockerBlocked},
			"supervising", "Supervising", "On. Its last check sent nothing because lanes are working or waiting.", "", "In use now"},
		{"on, lanes listed but not the check's reason", dockEvidence{controls: on, blocker: blockerListed},
			"supervising", "Supervising", "On, with a recent check.", "", "In use now"},
		{"on, recent dry-run check", dockEvidence{controls: on, status: func(s *statusOut) { s.State.LastCheck.DryRun = true }},
			"supervising", "Supervising", "On, in dry-run mode: its last check decided and sent nothing.", "", "In use now"},
		{"on, recent check that recorded another target", dockEvidence{controls: on, status: func(s *statusOut) { s.State.LastCheck.Target = "example-fallback" }},
			"supervising", "Supervising", "On, with a recent check.", "", "Recorded route; the last check did not record it"},
		{"on, unconfirmed new conversation", dockEvidence{controls: on, status: func(s *statusOut) { s.NewThreads.Pending = true }},
			"supervising", "Supervising", "On, with a recent check.", "A new conversation it tried to create for a move to another route could not be confirmed.", "In use now"},
	} {
		in := c.ev.input()
		in.Chain = &dockChainInput{}
		in.Chain.Primary.ID = "primary"
		l, need := projectDockLoop(in, dockNow, defaultStaleAfter, false)
		got := ""
		if need != nil {
			got = need.Reason
		}
		use := ""
		if len(l.Chain.Entries) > 0 {
			use = l.Chain.Entries[0].Use
		}
		if l.State != c.state || l.Phrase != c.phrase || l.Detail != c.detail || got != c.need || use != c.use {
			t.Errorf("%s:\n got  %q | %q | %q | need %q | use %q\n want %q | %q | %q | need %q | use %q", c.name, l.State, l.Phrase, l.Detail, got, use, c.state, c.phrase, c.detail, c.need, c.use)
		}
		// Only a loop that is on, unheld, recently checked on this target is in use.
		if inUse := len(l.Chain.Entries) > 0 && l.Chain.Entries[0].InUse; inUse != (c.use == "In use now") {
			t.Errorf("%s: inUse %t", c.name, inUse)
		}
		if l.Identifiers != nil {
			t.Errorf("%s: identifiers in the default view", c.name)
		}
		if c.ev.noStatus && (l.CurrentRoute.Availability != "unknown" || l.LastCheck.Availability != "unknown" || l.Children.Availability != "unknown") {
			t.Errorf("%s: missing evidence must be unknown: %+v", c.name, l)
		}
	}

	// The recorded action and outcome of the last check.
	for _, c := range []struct {
		check state.Check
		want  string
	}{
		{state.Check{Action: "none", Outcome: "succeeded"}, "Checked; sent nothing"},
		{state.Check{Action: "wake", Outcome: "succeeded"}, "Sent a wake"},
		{state.Check{Action: "nudge", Outcome: "succeeded"}, "Sent a nudge"},
		{state.Check{Action: "reprompt", Outcome: "succeeded"}, "Sent a re-prompt or retried the turn"},
		{state.Check{Action: "await_user", Outcome: "succeeded"}, "Held the loop for a person"},
		{state.Check{Action: "stop", Outcome: "succeeded"}, "Stopped the session"},
		{state.Check{Action: "handoff", Outcome: "succeeded"}, "Moved to another route"},
		{state.Check{Action: "switch_back", Outcome: "succeeded"}, "Returned to the first route"},
		{state.Check{Action: "none", Outcome: "failed"}, "The check failed"},
		{state.Check{Action: "wake", Outcome: "failed"}, "Sent a wake, which failed"},
		{state.Check{Action: "wake", Outcome: "dry_run"}, "Sent a wake (dry run: nothing was sent)"},
		{state.Check{Action: "something-new", Outcome: "succeeded"}, "Checked"},
	} {
		if got := dockAction(&c.check); got != c.want {
			t.Errorf("%+v: %q", c.check, got)
		}
	}

	// The last probe of a route that is not ready.
	for _, c := range []struct {
		ok    bool
		class string
		want  string
	}{
		{true, "", "Last probe passed, but too long ago to rely on"}, {false, "rate_limit", "Hit a usage limit at its last probe"},
		{false, "auth", "Sign-in failed at its last probe"}, {false, "network", "Could not be reached at its last probe"},
		{false, "overloaded", "Provider overloaded at its last probe"}, {false, "unknown", "Last probe failed"},
	} {
		if got := dockProbePhrase(c.ok, c.class); got != c.want {
			t.Errorf("%t %s: %q", c.ok, c.class, got)
		}
	}

	// The next check: only a responsive daemon gives a time.
	next := dockNow.Add(time.Minute)
	for _, c := range []struct {
		daemon               daemonStatus
		availability, detail string
	}{
		{daemonStatus{State: "not_recorded"}, "unknown", "no keepgoing daemon endpoint is recorded for this loop; a scheduler outside keepgoing is not visible"},
		{daemonStatus{State: "unavailable"}, "unknown", "a keepgoing daemon endpoint is recorded for this loop but could not be verified"},
		{daemonStatus{State: "skipped"}, "unknown", "the local daemon was not asked"},
		{daemonStatus{State: "responsive", Phase: "waiting", NextCheckAt: &next}, "known", ""},
		{daemonStatus{State: "responsive", Phase: "checking"}, "in_progress", ""},
	} {
		if n := dockNextCheck(&clientSnapshot{Daemon: c.daemon}); n.Availability != c.availability || n.Detail != c.detail {
			t.Errorf("%+v: %+v", c.daemon, n)
		}
	}

	// Lanes, from the runs blocker.
	for _, c := range []struct {
		blocker              runsBlocker
		availability, phrase string
		holding              bool
	}{
		{runsBlocker{Availability: blockerBlocked, Children: []runsChild{{Status: "active"}, {Status: "error"}}}, "current", "1 working, 1 failed", true},
		{runsBlocker{Availability: blockerListed, Children: []runsChild{{Status: "awaiting_input"}}}, "current", "1 waiting for an answer", false},
		{runsBlocker{Availability: blockerNone}, "current", "None at the last check", false},
		{runsBlocker{Availability: blockerHeld, Children: []runsChild{{Status: "awaiting_input"}}}, "held", "1 waiting for an answer (as of the check that held it)", false},
		{runsBlocker{Availability: blockerUnknown, Reason: "no_open_run"}, "none_recorded", "None recorded", false},
		{runsBlocker{Availability: blockerUnknown, Reason: "last_check_older_than_stale_after"}, "stale", "Stale: the last listing is too old to show as current", false},
		{runsBlocker{Availability: blockerUnknown, Reason: "child_listing_incomplete"}, "unknown", "Unknown", false},
		{runsBlocker{Availability: blockerUnknown, Reason: "not_refreshed_by_the_last_check"}, "unknown", "Unknown", false},
	} {
		if got := dockChildWork(runsLoop{Blocker: c.blocker}, true); got.Availability != c.availability || got.Phrase != c.phrase || got.Holding != c.holding {
			t.Errorf("%+v: %+v", c.blocker, got)
		}
	}
	if got := dockChildWork(runsLoop{}, false); got.Availability != "unknown" || got.Phrase != "Unknown" {
		t.Errorf("%+v", got)
	}
}

func TestDockNothingNeedsYouOnlyWhenEveryLoopWasRead(t *testing.T) {
	on := &clientControls{Enabled: true}
	quiet := dockEvidence{controls: on}.input()
	// Every loop read, none held: the only case that is all clear.
	if r := buildDockReport([]dockInput{quiet, quiet}, dockNow, time.Minute, defaultStaleAfter, false, true, 0, true); !r.AllClear || len(r.NeedsYou) != 0 || r.Unread != 0 {
		t.Errorf("%+v", r)
	}
	for name, in := range map[string]dockInput{
		"control unreadable": dockEvidence{}.input(),
		"pending transfer":   dockEvidence{noSnap: true, noStatus: true, issues: []string{"transfer_pending"}}.input(),
		"binding gone":       dockEvidence{noSnap: true, noStatus: true, issues: []string{"binding_unavailable"}}.input(),
		"status unreadable":  dockEvidence{controls: on, noStatus: true}.input(),
		"read cut off":       dockEvidence{noSnap: true, noStatus: true, issues: []string{"cancelled"}}.input(),
	} {
		r := buildDockReport([]dockInput{quiet, in}, dockNow, time.Minute, defaultStaleAfter, false, true, 0, true)
		if r.AllClear || r.Unread != 1 || len(r.NeedsYou) != 1 || r.NeedsYou[0].Kind != "unreadable" || r.NeedsYou[0].Reason == "" || r.NeedsYou[0].Action == "" {
			t.Errorf("%s: allClear %t unread %d needs %+v", name, r.AllClear, r.Unread, r.NeedsYou)
		}
	}
	// A held loop, a read that was cut off, loops left out, and an
	// unreadable registry are never all clear either.
	held := dockEvidence{controls: &clientControls{Enabled: true, AwaitingUser: true}}.input()
	if r := buildDockReport([]dockInput{held}, dockNow, time.Minute, defaultStaleAfter, false, true, 0, true); r.AllClear || r.Unread != 0 || r.NeedsYou[0].Kind != "held" {
		t.Errorf("%+v", r)
	}
	if r := buildDockReport([]dockInput{quiet}, dockNow, time.Minute, defaultStaleAfter, false, true, 0, false); r.AllClear || r.Complete {
		t.Errorf("incomplete: %+v", r)
	}
	if r := buildDockReport([]dockInput{quiet}, dockNow, time.Minute, defaultStaleAfter, false, true, 3, true); r.AllClear {
		t.Errorf("omitted: %+v", r)
	}
	if r := buildDockReport(nil, dockNow, time.Minute, defaultStaleAfter, false, false, 0, true); r.AllClear || r.Registry.Availability != "unavailable" || len(r.Loops) != 0 {
		t.Errorf("registry: %+v", r)
	}
	// A loop whose current route could not be read is not "on standby" for
	// its own first route: it is listed as unknown.
	unread := dockEvidence{controls: on, noStatus: true}.input()
	unread.Chain = &dockChainInput{}
	unread.Chain.Primary.ID = "primary"
	r := buildDockReport([]dockInput{unread}, dockNow, time.Minute, defaultStaleAfter, false, true, 0, true)
	if len(r.Routes) != 1 || len(r.Routes[0].UnknownFor) != 1 || len(r.Routes[0].StandbyFor)+len(r.Routes[0].RunningNow)+len(r.Routes[0].RecordedFor) != 0 {
		t.Errorf("%+v", r.Routes)
	}
}

func TestDockHoldWaitAndNoteWithholding(t *testing.T) {
	in := dockEvidence{controls: &clientControls{Enabled: true, AwaitingUser: true}}.input()
	in.Status.Control = control.File{UpdatedAt: dockNow.Add(-time.Hour), Note: "  see thr_private0001 in /private/example  "}
	// Without a history record of the hold starting, the wait is a lower
	// bound. The note is withheld, as registered status withholds it.
	_, need := projectDockLoop(in, dockNow, defaultStaleAfter, false)
	if need.SinceEvidence != "control_changed" || *need.WaitedSeconds != 3600 || need.Note != "" || !need.NoteWithheld {
		t.Errorf("%+v", need)
	}
	if _, need = projectDockLoop(in, dockNow, defaultStaleAfter, true); need.Note != "see thr_private0001 in /private/example" || need.NoteWithheld {
		t.Errorf("%+v", need)
	}
	in.Hold = &dockHold{At: dockNow.Add(-2 * time.Hour), Marked: true}
	if _, need = projectDockLoop(in, dockNow, defaultStaleAfter, false); need.SinceEvidence != "hold_recorded" || *need.WaitedSeconds != 7200 {
		t.Errorf("%+v", need)
	}
}

var dockPrivate = regexp.MustCompile(`thr_[A-Za-z0-9]+|loop_[0-9a-f]{32}`)

func TestDockWithholdsIdentifiersNotesAndPathsAndIsBounded(t *testing.T) {
	d, root := dockFixtureReader(t)
	// A note carrying a thread ID and a path, far over the size cap.
	f, _ := registry.Load(d.directory)
	var billing registry.Entry
	for _, e := range f.Entries {
		if e.Loop == "billing-api-migration" {
			billing = e
		}
	}
	if _, err := control.Update(billing.StateDir, func(c *control.File) {
		c.Note = "ask in thr_private0001, see " + filepath.Join(root, "notes") + " " + strings.Repeat("long note ", 5000)
	}); err != nil {
		t.Fatal(err)
	}
	body, err := d.json(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) > 64<<10 {
		t.Errorf("report is %d bytes for eight loops", len(body))
	}
	if m := dockPrivate.Find(body); m != nil {
		t.Errorf("identifier %s in the default report", m)
	}
	for _, private := range []string{root, "keepgoing.yaml", "nonexistent-provider-fixture", "state_dir", "nonexistent-probe", "long note"} {
		if bytes.Contains(body, []byte(private)) {
			t.Errorf("%q in the default report", private)
		}
	}
	var r dockReport
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatal(err)
	}
	if r.NeedsYou[0].Note != "" || !r.NeedsYou[0].NoteWithheld {
		t.Errorf("%+v", r.NeedsYou[0])
	}
	// On request: the registration, the configured thread and the note, cut
	// to its cap. Still no configuration or state path of keepgoing's own.
	d.cache = nil
	withIDs, err := d.json(true)
	if err != nil {
		t.Fatal(err)
	}
	// thr_example0002 is the thread payments-reconciler is on, which its
	// registered status prints; a thread that is only on standby
	// (thr_example0003) and lane threads are never included.
	for _, want := range []string{billing.ID, dockLoopThread("billing-api-migration"), "thr_example0002", "ask in thr_private0001"} {
		if !bytes.Contains(withIDs, []byte(want)) {
			t.Errorf("identifier view lacks %s", want)
		}
	}
	for _, private := range []string{"keepgoing.yaml", "thr_child", "thr_example0003", filepath.Join(root, "loops")} {
		if bytes.Contains(withIDs, []byte(private)) {
			t.Errorf("identifier view contains %s", private)
		}
	}
	if err := json.Unmarshal(withIDs, &r); err != nil {
		t.Fatal(err)
	}
	if n := []rune(r.NeedsYou[0].Note); len(n) != dockMaxNote || n[len(n)-1] != '…' {
		t.Errorf("note is %d runes", len(n))
	}
}

func TestDockRoutesAreCappedAndKeyedByAccount(t *testing.T) {
	in := &dockChainInput{}
	in.Primary.ID = "primary"
	in.Candidates = make([]config.Candidate, dockMaxChain+9)
	for i := range in.Candidates {
		in.Candidates[i].ID = fmt.Sprintf("fallback-%d", i)
	}
	c := dockHandoffChain(in, dockRouteUse{Target: "primary"}, dockNow)
	if len(c.Entries) != dockMaxChain+1 || c.Omitted != 9 || c.Availability != "unknown" {
		t.Errorf("%d entries, %d omitted, %s", len(c.Entries), c.Omitted, c.Availability)
	}
	// Two accounts under one group and model are two routes: the key takes
	// in what selects the account a probe reads, so they never share a reading.
	a := config.Candidate{ID: "account-a"}
	a.Provider, a.Group, a.Adapter = "example", "shared-group", "claude"
	a.Probe.Env = map[string]string{"CLAUDE_CONFIG_DIR": "/fictional/accounts/a"}
	b := a
	b.ID, b.Probe.Env = "account-b", map[string]string{"CLAUDE_CONFIG_DIR": "/fictional/accounts/b"}
	same := a
	same.ID = "account-a-again"
	two := dockHandoffChain(&dockChainInput{Primary: in.Primary, Candidates: []config.Candidate{a, b, same}, Summary: &fallbacksOut{}}, dockRouteUse{}, dockNow)
	if two.Entries[1].Route == two.Entries[2].Route || two.Entries[1].Route != two.Entries[3].Route {
		t.Errorf("%s %s %s", two.Entries[1].Route, two.Entries[2].Route, two.Entries[3].Route)
	}
	// A recorded failure on the current route is still said.
	failing := &dockChainInput{Primary: in.Primary, Candidates: []config.Candidate{{ID: "example-fallback"}}}
	failing.Summary = &fallbacksOut{Candidates: []candidateRow{{ID: "example-fallback", Last: &probe.Result{At: dockNow.Add(-time.Hour), ErrorClass: "auth"}}}}
	if e := dockHandoffChain(failing, dockRouteUse{Target: "example-fallback", InUse: true, Phrase: "In use now"}, dockNow).Entries[1]; !e.Current || e.Readiness != "not_ready" || e.Phrase != "Sign-in failed at its last probe" {
		t.Errorf("%+v", e)
	}
	// Durations read the same on both sides of a limit.
	changed := dockNow.Add(-(3*time.Hour + 12*time.Minute))
	limits := dockLimits(&statusOut{ProgressAllowance: &progressAllowanceStatus{Timeout: "24h0m0s", ChangedAt: &changed}}, dockNow)
	if len(limits) != 1 || limits[0].Phrase != "3 h 12 min of 24 h" {
		t.Errorf("%+v", limits)
	}
	for d, want := range map[time.Duration]string{40 * time.Second: "1 min", 90 * time.Minute: "1 h 30 min", 54 * time.Hour: "2 days 6 h", 48 * time.Hour: "2 days", -time.Hour: "0 min"} {
		if got := dockDuration(d); got != want {
			t.Errorf("%v: %q", d, got)
		}
	}
}

// A read is never cut short by the request that happened to start it, and a
// read that is cut off by the time limit is never cached or shown as clear.
func TestDockReadIsNotTiedToARequestAndIncompleteReadsAreNotCached(t *testing.T) {
	d, _ := dockFixtureReader(t)
	release := make(chan struct{})
	d.query = func(ctx context.Context, dir, current string, offline bool) daemonStatus {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return daemonStatus{State: "not_recorded"}
	}
	// The time limit passes while loops are still being read.
	d.timeout = 50 * time.Millisecond
	body, err := d.json(false)
	if err != nil {
		t.Fatal(err)
	}
	var r dockReport
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatal(err)
	}
	if r.Complete || r.AllClear || r.Unread == 0 || len(r.NeedsYou) < r.Unread || len(r.Loops) != len(dockLoops) {
		t.Fatalf("complete %t allClear %t unread %d needs %d", r.Complete, r.AllClear, r.Unread, len(r.NeedsYou))
	}
	if len(d.cache) != 0 {
		t.Fatal("an incomplete read was cached")
	}
	// The next read, allowed to finish, is complete and is what gets cached.
	close(release)
	d.timeout = 0
	if body, err = d.json(false); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatal(err)
	}
	if !r.Complete || r.Unread != 0 || len(d.cache) != 1 {
		t.Fatalf("complete %t unread %d cached %d", r.Complete, r.Unread, len(d.cache))
	}
	// When the command itself is exiting, a read is an error, not a report.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d.ctx, d.cache = ctx, nil
	if _, err := d.json(false); err == nil || len(d.cache) != 0 {
		t.Fatal("a read during exit returned a report")
	}
}

// The loop page beyond its routes: last decision, hold, device, each lane and
// recent history, from the fixture loops.
func TestDockLoopPageDecisionHoldDeviceLanesAndHistory(t *testing.T) {
	d, _ := dockFixtureReader(t)
	r := d.read(context.Background(), false)
	docs := dockLoopNamed(t, r, "docs-site-refresh")
	// The last decision, and when the unbroken run of that result began.
	if dec := docs.Decision; dec.Availability != "known" || dec.Phrase != "Sent nothing: lanes are working" || dec.Cause != "child_busy" || *dec.AgeSeconds != 180 ||
		dec.Since == nil || !dec.Since.Equal(dockNow.Add(-57*time.Minute)) {
		t.Errorf("%+v", dec)
	}
	if docs.Hold != (dockHoldState{State: "none", Phrase: "None"}) {
		t.Errorf("%+v", docs.Hold)
	}
	// One device, named only as this computer by default.
	if docs.Device != (dockDevice{Name: "This computer", Role: "Runs this loop's checks"}) {
		t.Errorf("%+v", docs.Device)
	}
	// Each lane, the ones that need a person first; a lane that is itself a
	// registered loop is named and linked. No title is recorded, and no key
	// is shown by default.
	lanes := docs.Lanes
	if lanes.Availability != "current" || len(lanes.Lanes) != 4 || lanes.Omitted != 0 {
		t.Fatalf("%+v", lanes)
	}
	named := 0
	for i, want := range []string{"failed", "working", "working", "working"} {
		l := lanes.Lanes[i]
		if l.Number != i+1 || l.State != want || l.Title != "" || l.TitleEvidence != "not_recorded" || l.Key != "" {
			t.Errorf("lane %d: %+v", i, l)
		}
		if l.Loop != "" {
			named++
			if l.Loop != "mobile-onboarding" || l.LoopKey != dockLoopNamed(t, r, "mobile-onboarding").Key {
				t.Errorf("%+v", l)
			}
		}
	}
	if named != 1 {
		t.Errorf("%d lanes named as registered loops", named)
	}
	// Effort comes from the configured reasoning value, and is unrecorded
	// where none is configured.
	if e := docs.Chain.Entries; e[0].Effort != "medium" || e[1].Effort != "" {
		t.Errorf("%q %q", e[0].Effort, e[1].Effort)
	}
	// Recent history, newest first, with a run of identical checks as one line.
	ev := docs.Events
	if ev.Availability != "known" || len(ev.Events) != 5 || ev.Pruned {
		t.Fatalf("%+v", ev)
	}
	for i, want := range []struct{ tone, phrase, detail string }{
		{"routine", "Sent nothing: lanes are working", ""},
		{"routine", "Sent a wake: the session was idle", "The send returned; receipt is not verified."},
		{"key", "Returned to the first route", "to claude-opus-5-5 on claude-code"},
		{"key", "Moved to another route", "to claude-sonnet-5-5 on second-account"},
		{"key", "Hold for a person cleared by a control command", ""},
	} {
		if e := ev.Events[i]; e.Tone != want.tone || e.Phrase != want.phrase || e.Detail != want.detail {
			t.Errorf("event %d: %+v", i, e)
		}
	}
	if e := ev.Events[0]; e.Folded != 9 || e.FoldedSince == nil || !e.FoldedSince.Equal(dockNow.Add(-57*time.Minute)) || e.AgeSeconds != 180 {
		t.Errorf("%+v", e)
	}

	// A held loop: the hold in force, since the record of it starting.
	billing := dockLoopNamed(t, r, "billing-api-migration")
	if h := billing.Hold; h.State != "held" || h.Phrase != "Held for a person" || h.SinceEvidence != "hold_recorded" || *h.SinceSeconds != 8040 {
		t.Errorf("%+v", h)
	}
	if billing.Decision.Phrase != "Sent nothing: the loop is held for a person" || billing.Decision.Since != nil {
		t.Errorf("%+v", billing.Decision)
	}
	if e := billing.Events.Events; len(e) != 1 || e[0].Tone != "hold" || e[0].Phrase != "Held the loop for a person: a lane is waiting for an answer" {
		t.Errorf("%+v", e)
	}
	if l := billing.Lanes; l.Availability != "held" || len(l.Lanes) != 2 || l.Lanes[0].State != "waiting" {
		t.Errorf("%+v", l)
	}
	// Paused without a history record of the pause: the wait is a lower bound.
	if h := dockLoopNamed(t, r, "mobile-onboarding").Hold; h.State != "held" || h.Phrase != "Paused" || h.SinceEvidence != "control_changed" {
		t.Errorf("%+v", h)
	}
	// Nothing recorded is said, not filled in.
	off := dockLoopNamed(t, r, "data-export-cleanup")
	if off.Decision.Availability != "unknown" || off.Decision.Phrase != "No check recorded" || off.Events.Availability != "none_recorded" || len(off.Lanes.Lanes) != 0 ||
		off.Device.Role != "Holds this loop's records; no check is recorded here" {
		t.Errorf("%+v %+v %+v", off.Decision, off.Events, off.Device)
	}

	// The hostname and lane keys are identifiers: in the JSON only on request.
	host, _ := os.Hostname()
	body, err := d.json(false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), `"hostname"`) || strings.Contains(string(body), `"key":"`+childKeyOf("thr_child0001")) || (len(host) > 3 && bytes.Contains(body, []byte(host))) {
		t.Error("hostname or a lane key in the default report")
	}
	d.cache = nil
	withIDs := d.read(context.Background(), true)
	docs = dockLoopNamed(t, withIDs, "docs-site-refresh")
	if docs.Device.Hostname != clip(host, dockMaxText) || docs.Lanes.Lanes[1].Key == "" || len(docs.Lanes.Lanes[1].Key) != 24 {
		t.Errorf("%+v %+v", docs.Device, docs.Lanes.Lanes[1])
	}
}

// Each record of the loop page's new parts, mapped to the exact words shown.
func TestDockLoopPagePhrasesFollowEvidence(t *testing.T) {
	// Every cause the history can record has words, except the two that
	// mean there is no cause to give.
	for _, cause := range []string{"disabled", "paused", "awaiting_user", "completed", "retrying", "retry_unverified", "retry_silent", "handoff_deferred", "deadline", "send_limit",
		"git_limit", "stall_limit", "stall", "idle", "active", "child_input", "child_busy", "child_error", "child_unverified", "input", "stop", "limit_wait", "limit_no_fallback", "limit_failover_disabled"} {
		if dockReason(cause) == "" {
			t.Errorf("no words for cause %s", cause)
		}
	}
	for _, c := range []struct {
		action, outcome, cause, want string
		dryRun                       bool
	}{
		{"none", "succeeded", "child_busy", "Sent nothing: lanes are working", false},
		{"none", "succeeded", "other", "Sent nothing", false},
		{"none", "succeeded", "unavailable", "Sent nothing", false},
		{"none", "failed", "active", "The check failed: the session was working", false},
		{"wake", "succeeded", "idle", "Sent a wake: the session was idle", false},
		{"nudge", "succeeded", "stall", "Sent a nudge: no progress for a while", false},
		{"await_user", "succeeded", "input", "Held the loop for a person: the agent is waiting on a person", false},
		{"none", "succeeded", "limit_no_fallback", "Sent nothing: a usage limit with no other route able to take over", false},
		{"wake", "dry_run", "idle", "Sent a wake (dry run: nothing was sent): the session was idle", true},
	} {
		if got := dockDecisionPhrase(c.action, c.outcome, c.dryRun, c.cause); got != c.want {
			t.Errorf("%+v: %q", c, got)
		}
	}
	// A control change is worded from the flags before and after it.
	on := incident.Controls{Enabled: true}
	for _, c := range []struct {
		before, after incident.Controls
		want          string
	}{
		{incident.Controls{}, on, "Turned on by a control command"},
		{on, incident.Controls{}, "Turned off by a control command"},
		{on, incident.Controls{Enabled: true, Paused: true}, "Paused by a control command"},
		{incident.Controls{Enabled: true, Paused: true, AwaitingUser: true}, on, "Pause cleared, hold for a person cleared by a control command"},
		{on, incident.Controls{Enabled: true, Stop: true}, "Stop requested by a control command"},
		{on, incident.Controls{Enabled: true, Completed: true}, "Marked finished by a control command"},
		{on, incident.Controls{Enabled: true, AwaitingUser: true}, "Held for a person by a control command"},
		{on, on, "A control change was saved; no hold changed"},
	} {
		b, a := c.before, c.after
		if got := dockControlPhrase(incident.Record{Kind: "control", ControlBefore: &b, ControlAfter: &a}); got != c.want {
			t.Errorf("%+v -> %+v: %q", c.before, c.after, got)
		}
	}
	if got := dockControlPhrase(incident.Record{Kind: "control"}); got != "A control change was saved" {
		t.Errorf("%q", got)
	}
	// A report's recorded outcome; acceptance is not a person seeing it.
	for _, c := range []struct{ outcome, tone, phrase string }{
		{"http_accepted", "routine", "Report sent: limit wait"}, {"not_sent", "routine", "Report not sent for lack of time: limit wait"}, {"http_failed", "problem", "Report not confirmed: limit wait"},
	} {
		ev := dockEvent{Tone: "routine"}
		dockWordDelivery(&ev, report.Delivery{Event: "limit_wait", Outcome: c.outcome})
		if ev.Tone != c.tone || ev.Phrase != c.phrase {
			t.Errorf("%s: %+v", c.outcome, ev)
		}
	}
	// The hold in force, from the control flags.
	for _, c := range []struct {
		controls *clientControls
		state    string
		phrase   string
	}{
		{nil, "unknown", "Unknown"}, {&clientControls{Enabled: true}, "none", "None"}, {&clientControls{}, "none", "None"},
		{&clientControls{Enabled: true, Paused: true}, "held", "Paused"}, {&clientControls{Enabled: true, Stop: true, Paused: true}, "held", "Stop requested, paused"},
		{&clientControls{Enabled: true, Completed: true}, "held", "Finished"}, {&clientControls{Enabled: true, AwaitingUser: true}, "held", "Held for a person"},
	} {
		if h := dockCurrentHold(c.controls, time.Time{}, nil, dockNow); h.State != c.state || h.Phrase != c.phrase || h.Since != nil {
			t.Errorf("%+v: %+v", c.controls, h)
		}
	}
	// The device's role rests on a recorded check, or says there is none.
	if d := dockDeviceOf(nil, "example-host", false); d.Role != "Holds this loop's registration; its records could not be read" || d.Hostname != "" {
		t.Errorf("%+v", d)
	}
	// An unreadable history is unknown, not empty.
	if ev := dockRecentEvents(nil, nil, dockNow); ev.Availability != "unknown" {
		t.Errorf("%+v", ev)
	}
	unreadable := &timelineReport{Sources: []timelineSource{{Source: incident.FileName, Availability: "unavailable"}}}
	if ev := dockRecentEvents(unreadable, nil, dockNow); ev.Availability != "unknown" || len(ev.Events) != 0 {
		t.Errorf("%+v", ev)
	}
}

func TestDockReadWritesNothing(t *testing.T) {
	d, root := dockFixtureReader(t)
	// A registered loop whose runtime directory does not exist yet.
	cfg := filepath.Join(root, "loops", "never-started", "keepgoing.yaml")
	dockWrite(t, cfg, dockConfigYAML("never-started", false, 0))
	var out, errs bytes.Buffer
	if code := run(context.Background(), []string{"registry", "add", "-c", cfg, "--directory", d.directory, "--json"}, &out, &errs); code != 0 {
		t.Fatal(errs.String())
	}
	before := treeDigest(t, root)
	for _, identifiers := range []bool{false, true, false} {
		d.cache = nil
		if _, err := d.json(identifiers); err != nil {
			t.Fatal(err)
		}
	}
	if after := treeDigest(t, root); after != before {
		t.Fatal("a read changed the registry, a configuration or a state directory")
	}
	if _, err := os.Stat(filepath.Join(root, "loops", "never-started", "state")); !os.IsNotExist(err) {
		t.Fatal("a read created a runtime directory")
	}
	r := d.read(context.Background(), false)
	if l := dockLoopNamed(t, r, "never-started"); l.State != "off" || l.LastCheck.Availability != "never" {
		t.Errorf("%+v", l)
	}
}

// dockServe starts the server on a loopback listener and returns its base URL
// (with token) and the address.
func dockServe(t *testing.T) (string, string, *dockReader) {
	t.Helper()
	d, _ := dockFixtureReader(t)
	ln, err := dockListen(0)
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().(*net.TCPAddr)
	if !addr.IP.IsLoopback() || !addr.IP.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Fatalf("listening on %s", addr)
	}
	token, err := dockNewToken()
	if err != nil || len(token) != 64 {
		t.Fatal(token, err)
	}
	srv := newDockServer(addr.Port, dockTokenAuth{token: token}, d)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() { done <- srv.serve(ctx, ln, io.Discard) }()
	t.Cleanup(func() {
		cancel()
		if code := <-done; code != exitOK {
			t.Errorf("serve exit %d", code)
		}
	})
	return srv.url(token), addr.String(), d
}

func dockRequest(t *testing.T, method, url string, edit func(*http.Request)) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	if edit != nil {
		edit(req)
	}
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return resp, string(b)
}

func TestDockServerRefusesWithoutTokenCrossOriginAndWrites(t *testing.T) {
	base, addr, _ := dockServe(t)
	api := base + dockAPIPath

	for _, path := range []string{"", "dock.css", "dock.js", dockAPIPath} {
		resp, body := dockRequest(t, http.MethodGet, base+path, nil)
		if resp.StatusCode != 200 || body == "" {
			t.Errorf("GET %q: %d", path, resp.StatusCode)
		}
		if resp.Header.Get("Access-Control-Allow-Origin") != "" || resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("X-Content-Type-Options") != "nosniff" ||
			!strings.Contains(resp.Header.Get("Content-Security-Policy"), "default-src 'none'") || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
			t.Errorf("GET %q headers: %v", path, resp.Header)
		}
	}
	resp, body := dockRequest(t, http.MethodGet, api, nil)
	var r dockReport
	if resp.Header.Get("Content-Type") != "application/json" || json.Unmarshal([]byte(body), &r) != nil || r.SchemaVersion != 1 || len(r.Loops) != len(dockLoops) {
		t.Fatalf("%s", body)
	}

	wrong := strings.Repeat("0", 64)
	refused := []struct {
		name, method, url string
		edit              func(*http.Request)
	}{
		{"no token", "GET", "http://" + addr + "/", nil},
		{"no token api", "GET", "http://" + addr + "/" + dockAPIPath, nil},
		{"wrong token", "GET", "http://" + addr + "/" + wrong + "/" + dockAPIPath, nil},
		{"token prefix", "GET", base[:len(base)-2] + "/" + dockAPIPath, nil},
		{"token without slash", "GET", strings.TrimSuffix(base, "/"), nil},
		{"token in query only", "GET", "http://" + addr + "/?token=" + strings.Split(base, "/")[3], nil},
		{"foreign origin", "GET", api, func(q *http.Request) { q.Header.Set("Origin", "https://example.com") }},
		{"null origin", "GET", api, func(q *http.Request) { q.Header.Set("Origin", "null") }},
		{"other port origin", "GET", api, func(q *http.Request) { q.Header.Set("Origin", "http://127.0.0.1:1") }},
		{"cross-site fetch", "GET", api, func(q *http.Request) { q.Header.Set("Sec-Fetch-Site", "cross-site") }},
		{"same-site fetch", "GET", api, func(q *http.Request) { q.Header.Set("Sec-Fetch-Site", "same-site") }},
		{"rebound host", "GET", api, func(q *http.Request) { q.Host = "dock.example.com" }},
		{"rebound host with port", "GET", api, func(q *http.Request) { q.Host = "dock.example.com:" + strings.Split(addr, ":")[1] }},
		{"post", "POST", api, nil},
		{"put", "PUT", api, nil},
		{"patch", "PATCH", api, nil},
		{"delete", "DELETE", api, nil},
		{"options preflight", "OPTIONS", api, func(q *http.Request) {
			q.Header.Set("Origin", "https://example.com")
			q.Header.Set("Access-Control-Request-Method", "GET")
		}},
		{"post page", "POST", base, nil},
	}
	for _, c := range refused {
		resp, body := dockRequest(t, c.method, c.url, c.edit)
		if resp.StatusCode != http.StatusForbidden || strings.Contains(body, "schemaVersion") || resp.Header.Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("%s: status %d, %d bytes", c.name, resp.StatusCode, len(body))
		}
	}
	// The page's own same-origin requests are accepted.
	resp, _ = dockRequest(t, http.MethodGet, api, func(q *http.Request) {
		q.Header.Set("Origin", "http://"+addr)
		q.Header.Set("Sec-Fetch-Site", "same-origin")
	})
	if resp.StatusCode != 200 {
		t.Errorf("same origin: %d", resp.StatusCode)
	}
	// Nothing but the page, its two assets and the one endpoint exists.
	for _, path := range []string{"api/v1/pause", "api/v1/dock/", "api/v2/dock", "../", "dockui/index.html", "control"} {
		if resp, _ := dockRequest(t, http.MethodGet, base+path, nil); resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %q: %d", path, resp.StatusCode)
		}
	}
}

func TestDockPageHasNoExternalReferences(t *testing.T) {
	for _, name := range []string{"dockui/index.html", "dockui/dock.css", "dockui/dock.js"} {
		b, err := dockUI.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		// The SVG namespace is an identifier, not a request.
		text := strings.ReplaceAll(string(b), "http://www.w3.org/2000/svg", "")
		for _, banned := range []string{"http://", "https://", "//cdn", "@import", "url(", "innerHTML = d", "eval(", "localStorage", "document.cookie"} {
			if strings.Contains(text, banned) {
				t.Errorf("%s contains %q", name, banned)
			}
		}
	}
}

// dockBuffer is written by the command's goroutine and read by the test.
type dockBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (d *dockBuffer) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.b.Write(p)
}

func (d *dockBuffer) String() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.b.String()
}

func TestDockCommandServesUntilCancelledAndValidatesFlags(t *testing.T) {
	d, _ := dockFixtureReader(t)
	for _, args := range [][]string{{"extra"}, {"--port", "70000"}, {"--refresh", "1s"}, {"--refresh", "1h"}, {"--stale-after", "0"}, {"--unknown"}} {
		var out, errs bytes.Buffer
		if code := run(context.Background(), append([]string{"dock"}, args...), &out, &errs); code != exitUsage || out.Len() != 0 {
			t.Errorf("%v: exit %d %s", args, code, out.String())
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	out := &dockBuffer{}
	done := make(chan int, 1)
	go func() { done <- run(ctx, []string{"dock", "--directory", d.directory, "--offline"}, out, io.Discard) }()
	address := regexp.MustCompile(`http://127\.0\.0\.1:\d+/[0-9a-f]{64}/`)
	for deadline := time.Now().Add(30 * time.Second); !address.MatchString(out.String()); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("no address printed")
		}
	}
	url := address.FindString(out.String())
	resp, body := dockRequest(t, http.MethodGet, url+dockAPIPath, nil)
	if resp.StatusCode != 200 || !strings.Contains(body, `"readOnly":true`) {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	cancel()
	select {
	case code := <-done:
		if code != exitOK {
			t.Fatalf("exit %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("dock did not exit when asked")
	}
	if _, err := net.DialTimeout("tcp", strings.Split(strings.TrimPrefix(url, "http://"), "/")[0], time.Second); err == nil {
		t.Fatal("still listening after exit")
	}
}
