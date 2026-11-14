package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/registry"
	"github.com/connorburgess/keepgoing-agents/internal/session"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// unpaused are the fixture's loops that are not paused, by name, sorted.
func unpaused(t *testing.T, byName map[string]registry.Entry) []string {
	t.Helper()
	var out []string
	for name, e := range byName {
		if !controlOf(t, e).Paused {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// The review names each loop with a one-line effect and gives a total; it
// never repeats a loop's whole review, and stops naming loops after eight.
func TestPauseAllReviewNamesEachLoopOnceAndTotals(t *testing.T) {
	d, byName := clientReviewWorld(t)
	f, err := registry.Load(d.directory)
	if err != nil {
		t.Fatal(err)
	}
	open := unpaused(t, byName)
	if len(open) < 3 {
		t.Fatalf("the fixture needs three loops that are not paused: %q", open)
	}
	paused := byName[open[0]]
	if r, code := applyClientHold(context.Background(), d.directory, paused.ID, "pause"); code != 0 || r.ControlSave != "confirmed" {
		t.Fatalf("%+v", r)
	}
	fitting := len(open) - 1
	before := map[string]any{}
	for _, e := range f.Entries {
		before[e.ID] = controlOf(t, e)
	}
	g, refused := panelGroupReview(context.Background(), d.directory, d, "pause", dockNow)
	if refused != nil {
		t.Fatalf("%+v", refused)
	}
	lines := []string{}
	for _, l := range g.preview.Lines {
		lines = append(lines, l.renderLocal(dockNow, time.UTC))
	}
	n := len(f.Entries)
	fits := 0
	for _, it := range g.items {
		if it.Fits {
			fits++
		}
	}
	already := n - fitting
	summary := fmt.Sprintf("Pauses %d of %d loops; %d %s already paused.", fitting, n, already, plural(already, "is", "are"))
	if fits != fitting || g.preview.Title != "Pause all loops?" || g.preview.Confirm != fmt.Sprintf("Pause %d Loops", fitting) {
		t.Fatalf("%d %q %q", fits, g.preview.Title, g.preview.Confirm)
	}
	text := strings.Join(lines, "\n")
	for _, want := range []string{summary, "This review expires in 2 min"} {
		if !strings.Contains(text, want) {
			t.Errorf("lacks %q:\n%s", want, text)
		}
	}
	// One line per loop, not each loop's whole review.
	if strings.Contains(text, "Last check:") || strings.Count(text, "will be paused") != min(fitting, panelGroupShown) || (n <= panelGroupShown && !strings.Contains(text, open[0]+" is already paused.")) {
		t.Errorf("not one line per loop:\n%s", text)
	}
	for _, l := range lines {
		if utf8.RuneCountInString(l) > clientReviewMenuCap {
			t.Errorf("longer than the menu shows: %q", l)
		}
	}
	// A review changes nothing.
	for _, e := range f.Entries {
		if !reflect.DeepEqual(before[e.ID], controlOf(t, e)) {
			t.Errorf("%s changed", e.Loop)
		}
	}
	// Past eight loops: "And N more."
	var many []panelGroupItem
	for i := 0; i < 11; i++ {
		many = append(many, panelGroupItem{Key: fmt.Sprintf("l%012x", i), Name: fmt.Sprintf("example-%02d", i), Fits: true})
	}
	got := panelGroupItemLines(many, map[string]string{}, "pause")
	if len(got) != panelGroupShown+1 || got[panelGroupShown] != "And 3 more." || got[0] != "example-00 will be paused." {
		t.Errorf("%q", got)
	}
	// Nothing to pause: refused, with why.
	for _, e := range f.Entries {
		applyClientHold(context.Background(), d.directory, e.ID, "pause")
	}
	if _, r := panelGroupReview(context.Background(), d.directory, d, "pause", dockNow); r == nil || r.Text != fmt.Sprintf("No loop can be paused now: %d are already paused. Nothing was changed.", n) {
		t.Errorf("%+v", r)
	}
	// Resume is not offered for every loop at once.
	if _, r := panelGroupReview(context.Background(), d.directory, d, "resume", dockNow); r == nil || r.Outcome != "refused" {
		t.Errorf("%+v", r)
	}
}

// A partial result never reads as success, and names the loops that were not
// changed first.
func TestPauseAllResultIsNeverPartialSuccess(t *testing.T) {
	loops := func(outcomes ...string) panelGroupResult {
		var r panelGroupResult
		for i, o := range outcomes {
			name := fmt.Sprintf("example-%02d", i)
			r.Loops = append(r.Loops, panelLoopTo{Key: name, Name: name, Outcome: o, Text: panelGroupLoopText(name, "pause", panelResult{Outcome: o, Text: "keepgoing is in the middle of a check on this loop. Nothing was changed."}, panelLoopTo{})})
		}
		return panelGroupTotal("pause", r)
	}
	for _, x := range []struct {
		outcomes []string
		outcome  string
		text     string
	}{
		{[]string{"done", "done", "done"}, "done", "Paused 3 loops."},
		{[]string{"done"}, "done", "Paused 1 loop."},
		{[]string{"done", "review_again", "done"}, "partial", "Paused 2 of 3; 1 needs a new review."},
		{[]string{"done", "refused", "uncertain", "review_again", "review_again"}, "partial", "Paused 1 of 5; 2 need a new review, 1 was refused and 1 could not be confirmed."},
		{[]string{"review_again", "review_again"}, "review_again", "Paused 0 of 2; 2 need a new review."},
		{[]string{"refused", "uncertain"}, "uncertain", "Paused 0 of 2; 1 was refused and 1 could not be confirmed."},
	} {
		r := loops(x.outcomes...)
		if r.Outcome != x.outcome || r.Text != x.text {
			t.Errorf("%v: %q %q", x.outcomes, r.Outcome, r.Text)
		}
		if x.outcome != "done" && strings.HasPrefix(r.Lines[0], "example-") && strings.HasSuffix(r.Lines[0], ": paused.") {
			t.Errorf("%v: a loop that was changed is named before one that was not: %q", x.outcomes, r.Lines)
		}
	}
	// Past eight loops the result stops naming them, after those not changed.
	var many []string
	for i := 0; i < 10; i++ {
		many = append(many, "done")
	}
	many[9] = "review_again"
	r := loops(many...)
	if len(r.Lines) != panelGroupShown+1 || r.Lines[panelGroupShown] != "And 2 more." || !strings.Contains(r.Lines[0], "review it again") {
		t.Errorf("%q", r.Lines)
	}
}

// The page: one review for every loop, one confirmation, each loop changed
// on its own; a loop changed in between needs a new review and the rest are
// paused.
func TestPauseAllFromThePage(t *testing.T) {
	d, byName := clientReviewWorld(t)
	now := time.Now()
	c, err := newPanelChanges(context.Background(), d.directory, d, now, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	p := c.prepare("session-a", panelGroupKey, "pause")
	if p.Preview == nil || p.Preview.Key != panelGroupKey || p.Preview.Title != "Pause all loops?" {
		t.Fatalf("%+v", p.Result)
	}
	open := unpaused(t, byName)
	changedName := open[0]
	changed := byName[changedName]
	if r, code := applyClientHold(context.Background(), d.directory, changed.ID, "stop"); code != 0 || r.ControlSave != "confirmed" {
		t.Fatalf("%+v", r)
	}
	// Another session's confirmation, or another action, is refused.
	if r := c.confirm("session-b", panelGroupKey, "pause", p.Preview.Form, holdOptions{}); r.Result == nil || r.Result.Outcome != "review_again" {
		t.Fatalf("%+v", r.Result)
	}
	p = c.prepare("session-a", panelGroupKey, "pause")
	if r := c.confirm("session-a", panelGroupKey, "stop", p.Preview.Form, holdOptions{}); r.Result == nil || r.Result.Outcome != "review_again" || controlOf(t, byName[open[1]]).Paused {
		t.Fatalf("%+v", r.Result)
	}
	p = c.prepare("session-a", panelGroupKey, "pause")
	if r, code := applyClientHold(context.Background(), d.directory, changed.ID, "stop"); code != 0 || r.ControlSave != "confirmed" {
		t.Fatalf("%+v", r)
	}
	// Stop was already set, so the record is unchanged: it is still paused
	// with the rest. Change it for real with a pause from a terminal.
	if r, code := applyClientHold(context.Background(), d.directory, changed.ID, "pause"); code != 0 || r.ControlSave != "confirmed" {
		t.Fatalf("%+v", r)
	}
	r := c.confirm("session-a", panelGroupKey, "pause", p.Preview.Form, holdOptions{})
	if r.Result == nil || r.Result.Outcome != "partial" || !strings.HasSuffix(r.Result.Text, "; 1 needs a new review.") || !strings.Contains(r.Result.Lines[0], changedName+": changed since you reviewed it") {
		t.Fatalf("%+v", r.Result)
	}
	for _, name := range open {
		e := byName[name]
		if !controlOf(t, e).Paused {
			t.Errorf("%s is not paused", name)
		}
		if name != changedName && lastOperation(t, e) != "panel_pause" {
			t.Errorf("%s: %q", name, lastOperation(t, e))
		}
	}
	// The form was spent.
	if again := c.confirm("session-a", panelGroupKey, "pause", p.Preview.Form, holdOptions{}); again.Result == nil || again.Result.Outcome != "review_again" {
		t.Fatalf("%+v", again.Result)
	}
}

// The command: client review pause --all gives each loop its own expect
// value; client apply pause --all takes them back, and refuses a loop alone
// when its value is for another loop or has expired.
func TestPauseAllFromTheCommand(t *testing.T) {
	d, byName := clientReviewWorld(t)
	var out, errs bytes.Buffer
	run := func(args ...string) (int, clientGroupReply) {
		t.Helper()
		out.Reset()
		errs.Reset()
		code := clientCommand(env{context.Background(), &out, &errs}, append(args, "--directory", d.directory, "--json"))
		var r clientGroupReply
		if out.Len() > 0 {
			if err := json.Unmarshal(out.Bytes(), &r); err != nil || r.SchemaVersion != 1 {
				t.Fatalf("%v %s", err, out.String())
			}
		}
		return code, r
	}
	open := unpaused(t, byName)
	code, review := run("review", "pause", "--all")
	if code != exitOK || review.Group == nil || review.Group.Title != "Pause all loops?" || len(review.Group.Loops) != len(byName) {
		t.Fatalf("%d %+v %s", code, review.Result, errs.String())
	}
	var args []string
	keys := []string{}
	for _, l := range review.Group.Loops {
		if l.Expect == "" {
			continue
		}
		if _, _, ok := parseClientExpect(l.Expect, l.Key, "pause+all"); !ok {
			t.Fatalf("%s: %q", l.Name, l.Expect)
		}
		args = append(args, "--expect", l.Key+"="+l.Expect)
		keys = append(keys, l.Key)
	}
	// One value given for another loop is refused for that loop alone.
	swapped := append([]string(nil), args...)
	swapped[1] = keys[0] + "=" + strings.SplitN(args[3], "=", 2)[1]
	code, r := run(append([]string{"apply", "pause", "--all"}, swapped...)...)
	if code != exitFailed || r.Result == nil || r.Result.Outcome != "partial" || r.Result.Text != fmt.Sprintf("Paused %d of %d; 1 needs a new review.", len(keys)-1, len(keys)) {
		t.Fatalf("%d %+v", code, r.Result)
	}
	paused := 0
	for _, name := range open {
		if controlOf(t, byName[name]).Paused {
			paused++
		}
	}
	if paused != len(keys)-1 || len(keys) != len(open) {
		t.Fatalf("%d paused", paused)
	}
	// Reviewed again, the rest is paused, and the total says all of them.
	_, review = run("review", "pause", "--all")
	args = nil
	for _, l := range review.Group.Loops {
		if l.Expect != "" {
			args = append(args, "--expect", l.Key+"="+l.Expect)
		}
	}
	if code, r := run(append([]string{"apply", "pause", "--all"}, args...)...); code != exitOK || r.Result.Outcome != "done" || r.Result.Text != "Paused 1 loop." {
		t.Fatalf("%d %+v", code, r.Result)
	}
	// Expired: refused for every loop, and nothing changes.
	key := keys[0]
	digest := controlDigest(controlOf(t, byName[func() string {
		for n, e := range byName {
			if dockKey(e.ID) == key {
				return n
			}
		}
		return ""
	}()]))
	old := clientExpect(key, "pause+all", time.Now().Add(-time.Second), digest)
	if code, r := run("apply", "pause", "--all", "--expect", key+"="+old); code != exitFailed || r.Result.Outcome != "review_again" || !strings.Contains(r.Result.Lines[0], "the review expired") {
		t.Fatalf("%d %+v", code, r.Result)
	}
	// The arguments are exact.
	for _, bad := range [][]string{
		{"review", "resume", "--all"}, {"review", "pause", "--all", "--loop", key}, {"apply", "pause", "--all", "--lanes", "--expect", key + "=" + old},
		{"review", "stop", "--all", "--lanes"}, {"apply", "stop", "--all", "--immediately", "--expect", key + "=" + old}, {"apply", "stop", "--all", "--lanes", "--drop-queued", "--expect", key + "=" + old},
		{"review", "pause", "--all", "--expect", key + "=" + old}, {"apply", "pause", "--all"},
		{"apply", "pause", "--all", "--expect", old}, {"apply", "pause", "--all", "--expect", key + "=" + old, "--expect", key + "=" + old},
		{"apply", "pause", "--all", "--expect", "example=" + old},
	} {
		if code, _ := run(bad...); code != exitUsage {
			t.Errorf("%q: %d", bad, code)
		}
	}
	// panel list says pause can be made to every loop while one can be paused.
	list := buildPanelList(d.read(context.Background(), false))
	if !reflect.DeepEqual(list.CanAll, []string{"stop"}) {
		t.Errorf("every loop is paused and none stopped, yet canAll is %q", list.CanAll)
	}
}

// Stop all words what it does from each loop's own stop options (#209): at
// once only where a loop's own review said so, its lanes only where it has
// them, and never deleting queued messages for every loop at once.
func TestStopAllReviewUsesEachLoopsOwnOptions(t *testing.T) {
	two, three := 2, 3
	withLanes := func(now bool, lanes *int, queued *int) *stopChoices {
		c := &stopChoices{Immediately: now, Lanes: lanes, Queued: queued, Options: []stopOption{{Label: "Stop the Conversation"}, {Label: "Stop It and Its Lanes", Lanes: true}}}
		if queued != nil && *queued > 0 {
			c.Options = append(c.Options, stopOption{Label: "Stop It, Its Lanes and Their Queued Messages", Lanes: true, DropQueued: true})
		}
		return c
	}
	items := []panelGroupItem{
		{Name: "example-api", Fits: true, stop: withLanes(true, &two, &three)},
		{Name: "example-docs", Fits: true},                                   // no lanes: the plain stop
		{Name: "example-site", Fits: true, stop: withLanes(false, nil, nil)}, // bb not found from here
		{Name: "example-old", why: "already"},
	}
	c, lines := panelGroupStopReview(items)
	if c == nil || !c.Immediately || len(c.Options) != 2 || c.Options[0] != (stopOption{Label: "Stop the Conversations"}) || c.Options[1] != (stopOption{Label: "Stop Them and Their Lanes", Lanes: true}) {
		t.Fatalf("%+v", c)
	}
	text := strings.Join(lines, "\n")
	for _, want := range []string{
		"1 of the stops are carried out when you confirm, as a check does it; the other 2 at their next check.",
		"Stop Them and Their Lanes also stops the lanes of the 2 loops that have them: 2 running now in the 1 whose lanes could be counted. The others stop their conversation alone.",
		"3 messages are queued in lanes that have not started. Stopping the lanes keeps them, and BB may run them; to delete them, stop each loop on its own.",
		"Nothing is sent to a stopped loop until it is resumed.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("lacks %q:\n%s", want, text)
		}
	}
	for _, l := range lines {
		if utf8.RuneCountInString(l) > clientReviewMenuCap {
			t.Errorf("longer than the menu shows: %q", l)
		}
	}
	// Each loop's own offer: lanes only where it has them, at once only
	// where its review said so.
	if !items[0].lanes() || !items[0].now() || items[1].lanes() || items[1].now() || !items[2].lanes() || items[2].now() {
		t.Errorf("%v %v %v %v %v %v", items[0].lanes(), items[0].now(), items[1].lanes(), items[1].now(), items[2].lanes(), items[2].now())
	}
	if got := panelGroupItemLine(items[0], "Supervising", "stop"); got != "example-api (Supervising) will be stopped; 2 of its lanes are running." {
		t.Errorf("%q", got)
	}
	// No loop with lanes and none at once: no options, the plain stop.
	c, lines = panelGroupStopReview([]panelGroupItem{{Name: "example-docs", Fits: true}})
	if c != nil || lines[0] != "Each loop's next check carries out its stop; this review cannot confirm that the sessions stopped." || lines[1] != "This stops each conversation only: no loop here has lanes keepgoing can stop." {
		t.Errorf("%+v %q", c, lines)
	}
	// Every loop with lanes, none counted.
	_, lines = panelGroupStopReview([]panelGroupItem{{Name: "a", Fits: true, stop: withLanes(false, nil, nil)}, {Name: "b", Fits: true, stop: withLanes(false, nil, nil)}})
	if !strings.Contains(strings.Join(lines, "\n"), "Stop Them and Their Lanes also stops each loop's lanes; how many are running could not be counted from here.") {
		t.Errorf("%q", lines)
	}
}

// Stop all through the command: each loop's expect value binds what its own
// review offered, so a single loop's stop review cannot stand in for it; with
// --lanes each loop that has lanes asks for them too, and the total says
// which stops wait for a check.
func TestStopAllFromTheCommand(t *testing.T) {
	d, byName := clientReviewWorld(t)
	var out, errs bytes.Buffer
	run := func(args ...string) (int, clientGroupReply) {
		t.Helper()
		out.Reset()
		errs.Reset()
		code := clientCommand(env{context.Background(), &out, &errs}, append(args, "--directory", d.directory, "--json"))
		var r clientGroupReply
		if out.Len() > 0 {
			if err := json.Unmarshal(out.Bytes(), &r); err != nil || r.SchemaVersion != 1 {
				t.Fatalf("%v %s", err, out.String())
			}
		}
		return code, r
	}
	code, review := run("review", "stop", "--all")
	if code != exitOK || review.Group == nil || review.Group.Title != "Stop all loops?" || review.Group.Stop == nil || len(review.Group.Stop.Options) != 2 || review.Group.Stop.Options[1].Label != "Stop Them and Their Lanes" {
		t.Fatalf("%d %+v %s", code, review, errs.String())
	}
	for _, o := range review.Group.Stop.Options {
		if o.DropQueued {
			t.Fatal("queued messages offered for deletion for every loop at once")
		}
	}
	var args []string
	var first clientGroupLoop
	for _, l := range review.Group.Loops {
		if l.Expect == "" {
			continue
		}
		if first.Key == "" {
			first = l
		}
		if _, _, ok := parseClientExpect(l.Expect, l.Key, "stop"); ok {
			t.Fatalf("%s: a group value works as a single review's", l.Name)
		}
		args = append(args, "--expect", l.Key+"="+l.Expect)
	}
	// A single loop's own stop review does not stand in for the group's.
	_, single, _ := runClientReview(t, "review", "stop", "--loop", first.Key, "--directory", d.directory, "--json")
	if code, r := run("apply", "stop", "--all", "--lanes", "--expect", first.Key+"="+single.Preview.Expect); code != exitFailed || r.Result.Outcome != "review_again" {
		t.Fatalf("%d %+v", code, r.Result)
	}
	code, r := run(append([]string{"apply", "stop", "--all", "--lanes"}, args...)...)
	n := len(args) / 2
	if code != exitOK || r.Result.Outcome != "done" || r.Result.Text != fmt.Sprintf("Stop requested for %d loops; each loop's next check carries it out.", n) {
		t.Fatalf("%d %+v", code, r.Result)
	}
	if !strings.HasSuffix(r.Result.Lines[0], ": stop requested, with its lanes; its next check carries it out.") {
		t.Errorf("%q", r.Result.Lines)
	}
	for name, e := range byName {
		c := controlOf(t, e)
		if !c.Stop || c.StopLanesAt == nil || c.StopLanesDropQueued || lastOperation(t, e) != "stop" {
			t.Errorf("%s: %+v %q", name, c, lastOperation(t, e))
		}
	}
	// panel list: stop can no longer be made to every loop.
	if list := buildPanelList(d.read(context.Background(), false)); slices.Contains(list.CanAll, "stop") {
		t.Errorf("%q", list.CanAll)
	}
}

// Stop all from the page: the group's options are the review's, a lanes
// choice asks each loop with lanes for them, and the form is bound to it.
func TestStopAllFromThePage(t *testing.T) {
	d, byName := clientReviewWorld(t)
	now := time.Now()
	c, err := newPanelChanges(context.Background(), d.directory, d, now, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	p := c.prepare("session-a", panelGroupKey, "stop")
	if p.Preview == nil || p.Preview.Stop == nil || p.Preview.Title != "Stop all loops?" {
		t.Fatalf("%+v", p.Result)
	}
	// Deleting queued messages was not offered, so it is refused.
	if r := c.confirm("session-a", panelGroupKey, "stop", p.Preview.Form, holdOptions{Lanes: true, DropQueued: true}); r.Result == nil || r.Result.Outcome != "review_again" {
		t.Fatalf("%+v", r.Result)
	}
	p = c.prepare("session-a", panelGroupKey, "stop")
	r := c.confirm("session-a", panelGroupKey, "stop", p.Preview.Form, holdOptions{})
	if r.Result == nil || r.Result.Outcome != "done" || !strings.HasPrefix(r.Result.Text, "Stop requested for ") {
		t.Fatalf("%+v", r.Result)
	}
	for name, e := range byName {
		if c := controlOf(t, e); !c.Stop || c.StopLanesAt != nil {
			t.Errorf("%s: conversations only, yet %+v", name, c)
		}
	}
}

// The total of a stop says what happened: stopped now, or requested.
func TestStopAllTotalSaysWhatHappened(t *testing.T) {
	total := func(now ...bool) string {
		var r panelGroupResult
		for i, n := range now {
			name := fmt.Sprintf("example-%02d", i)
			r.Loops = append(r.Loops, panelLoopTo{Key: name, Name: name, Outcome: "done", now: n, Text: panelGroupLoopText(name, "stop", panelResult{Outcome: "done"}, panelLoopTo{now: n})})
		}
		return panelGroupTotal("stop", r).Text
	}
	for _, x := range []struct {
		now  []bool
		want string
	}{
		{[]bool{true, true}, "Stopped 2 loops now."},
		{[]bool{false}, "Stop requested for 1 loop; its next check carries it out."},
		{[]bool{false, false, false}, "Stop requested for 3 loops; each loop's next check carries it out."},
		{[]bool{true, true, false}, "Stopped 2 loops now; stop requested for 1, which its next check carries out."},
	} {
		if got := total(x.now...); got != x.want {
			t.Errorf("%v: %q", x.now, got)
		}
	}
}

// The reviewer's journey: ten loops, each to be stopped at once, each with
// its check lock held by a running check. Every stop is saved before any is
// carried out, so the time a change is given only leaves stops to the next
// check, and never leaves a loop unsaved or "refused".
func TestStopAllSavesEveryLoopBeforeCarryingAnyOut(t *testing.T) {
	d, byName := clientReviewWorld(t)
	var targets []panelGroupTarget
	names := map[string]string{}
	for name, e := range byName {
		held, err := lock.Acquire(e.StateDir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { held.Release() })
		targets = append(targets, panelGroupTarget{Key: dockKey(e.ID), Digest: controlDigest(controlOf(t, e)), Lanes: true, Now: true})
		names[dockKey(e.ID)] = name
	}
	if len(targets) < 10 {
		t.Fatalf("the fixture has %d loops", len(targets))
	}
	// Less time than one loop's wait for its running check.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	g := panelGroupApply(ctx, d.directory, "stop", "", "", targets, names, true)
	if time.Since(start) > 12*time.Second {
		t.Errorf("took %v", time.Since(start))
	}
	n := len(targets)
	if g.Outcome != "done" || g.Retry || g.Text != fmt.Sprintf("Stop requested for %d loops; each loop's next check carries it out.", n) {
		t.Fatalf("%+v", g)
	}
	for _, l := range g.Lines {
		if strings.HasPrefix(l, "And ") {
			continue
		}
		if !strings.Contains(l, ": stop requested, with its lanes; ") || strings.Contains(l, "refused") || strings.Contains(l, "cut off") {
			t.Errorf("%q", l)
		}
	}
	for name, e := range byName {
		if c := controlOf(t, e); !c.Stop || c.StopLanesAt == nil {
			t.Errorf("%s was not saved: %+v", name, c)
		}
	}
}

// Run from a BB thread, the loop of that conversation is carried out last,
// and said first.
func TestStopAllCarriesOutTheCallersOwnLoopLast(t *testing.T) {
	d, byName := clientReviewWorld(t)
	var targets []panelGroupTarget
	names := map[string]string{}
	caller, ownName := "", ""
	for name, e := range byName {
		cfg, ok := clientConfig(e)
		if !ok {
			continue
		}
		if id, ok := session.Of(cfg.Loop.Agent); ok && id.Kind == "bb" && caller == "" {
			caller, ownName = id.ID, name
		}
		held, err := lock.Acquire(e.StateDir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { held.Release() })
		targets = append(targets, panelGroupTarget{Key: dockKey(e.ID), Digest: controlDigest(controlOf(t, e)), Now: true})
		names[dockKey(e.ID)] = name
	}
	if caller == "" {
		t.Skip("no BB loop in the fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	g := panelGroupApply(ctx, d.directory, "stop", "", caller, targets, names, false)
	if !strings.HasPrefix(g.Lines[0], ownName+" (the loop of this conversation, stopped last): stop requested") {
		t.Errorf("%q", g.Lines)
	}
}

// What a stop carried out now did to its lanes is said per loop, and a lane
// stop that did not reach every lane is never a plain "Stopped N loops now".
func TestStopAllSaysWhatHappenedToTheLanes(t *testing.T) {
	loop := func(name string, lanes *state.LaneStop) panelLoopTo {
		l := panelLoopTo{Key: name, Name: name, Outcome: "done", now: true, lanes: lanes != nil, laneR: lanes}
		l.Text = panelGroupLoopText(name, "stop", panelResult{Outcome: "done"}, l)
		return l
	}
	done := &state.LaneStop{Outcome: "done", Stopped: 3}
	short := &state.LaneStop{Outcome: "incomplete", Stopped: 2, Failed: map[string]int{"transport": 1}}
	none := &state.LaneStop{Outcome: "done"}
	if got := loop("example-api", done).Text; got != "example-api: stopped now; 3 running lanes stopped." {
		t.Errorf("%q", got)
	}
	if got := loop("example-api", short).Text; got != "example-api: stopped now; 2 of 3 lanes stopped, lanes not all reached, and its next check goes on." {
		t.Errorf("%q", got)
	}
	if got := loop("example-api", none).Text; got != "example-api: stopped now; none of its lanes was running." {
		t.Errorf("%q", got)
	}
	g := panelGroupTotal("stop", panelGroupResult{Loops: []panelLoopTo{loop("a", done), loop("b", short), loop("c", nil)}})
	if g.Outcome != "partial" || g.Text != "Stopped 3 loops now. Not every lane was reached for 1 of them; its next check goes on." || !strings.HasPrefix(g.Lines[0], "b:") {
		t.Errorf("%+v", g)
	}
	// A partial result words stopped now and requested apart, and a cut-off
	// is a stop left to a check, not a refusal.
	cut := panelLoopTo{Key: "d", Name: "d", Outcome: "done", why: "cancelled"}
	cut.Text = panelGroupLoopText("d", "stop", panelResult{Outcome: "done"}, cut)
	again := panelLoopTo{Key: "e", Name: "e", Outcome: "review_again"}
	again.Text = panelGroupLoopText("e", "stop", panelResult{Outcome: "review_again"}, again)
	g = panelGroupTotal("stop", panelGroupResult{Loops: []panelLoopTo{loop("a", done), cut, again}})
	if g.Outcome != "partial" || !g.Retry || g.Text != "Of 3 loops: 1 stopped now, 1 with a stop requested for its next check and 1 needs a new review." {
		t.Errorf("%+v", g)
	}
	if cut.Text != "d: stop requested; there was no time left to carry it out here, so its next check does." {
		t.Errorf("%q", cut.Text)
	}
}

// A single loop's stop value binds the options its review offered, as the
// page does: an option it did not offer is refused.
func TestClientApplyStopRefusesAnOptionTheReviewDidNotOffer(t *testing.T) {
	d, byName := clientReviewWorld(t)
	e := byName["docs-site-refresh"]
	key := dockKey(e.ID)
	_, review, _ := runClientReview(t, "review", "stop", "--loop", key, "--directory", d.directory, "--json")
	if review.Preview == nil || review.Preview.Stop == nil || review.Preview.Stop.Immediately {
		t.Fatalf("the fixture's stop review: %+v", review.Preview)
	}
	before := controlOf(t, e)
	for _, extra := range [][]string{{"--lanes", "--drop-queued"}, {"--immediately"}, {"--lanes", "--immediately"}} {
		code, r, _ := runClientReview(t, append([]string{"apply", "stop", "--loop", key, "--expect", review.Preview.Expect, "--directory", d.directory, "--json"}, extra...)...)
		if code != exitFailed || r.Result == nil || r.Result.Text != "This review did not offer that stop. Nothing was changed. Review it again." {
			t.Errorf("%q: %d %+v", extra, code, r.Result)
		}
	}
	if !reflect.DeepEqual(controlOf(t, e), before) {
		t.Fatal("a stop the review did not offer changed the loop")
	}
	code, r, _ := runClientReview(t, "apply", "stop", "--loop", key, "--expect", review.Preview.Expect, "--lanes", "--directory", d.directory, "--json")
	if code != exitOK || r.Result.Outcome != "done" || controlOf(t, e).StopLanesAt == nil {
		t.Fatalf("%d %+v", code, r.Result)
	}
	// Single-loop and every-loop pause values are not each other's.
	other := byName["inventory-sync"]
	_, single, _ := runClientReview(t, "review", "pause", "--loop", dockKey(other.ID), "--directory", d.directory, "--json")
	var out, errs bytes.Buffer
	clientCommand(env{context.Background(), &out, &errs}, []string{"apply", "pause", "--all", "--expect", dockKey(other.ID) + "=" + single.Preview.Expect, "--directory", d.directory, "--json"})
	if !strings.Contains(out.String(), "not part of this review") || controlOf(t, other).Paused {
		t.Errorf("%s", out.String())
	}
}
