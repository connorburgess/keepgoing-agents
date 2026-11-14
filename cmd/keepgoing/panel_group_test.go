package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/connorburgess/keepgoing-agents/internal/registry"
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
	got := panelGroupItemLines(many, map[string]string{})
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
	// Only pause is offered for every loop at once, for now.
	if _, r := panelGroupReview(context.Background(), d.directory, d, "stop", dockNow); r == nil || r.Outcome != "refused" {
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
			r.Loops = append(r.Loops, panelLoopTo{Key: name, Name: name, Outcome: o, Text: panelGroupLoopText(name, "pause", panelResult{Outcome: o, Text: "keepgoing is in the middle of a check on this loop. Nothing was changed."})})
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
	if r := c.confirm("session-b", panelGroupKey, "pause", p.Preview.Form); r.Result == nil || r.Result.Outcome != "review_again" {
		t.Fatalf("%+v", r.Result)
	}
	p = c.prepare("session-a", panelGroupKey, "pause")
	if r := c.confirm("session-a", panelGroupKey, "stop", p.Preview.Form); r.Result == nil || r.Result.Outcome != "review_again" || controlOf(t, byName[open[1]]).Paused {
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
	r := c.confirm("session-a", panelGroupKey, "pause", p.Preview.Form)
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
	if again := c.confirm("session-a", panelGroupKey, "pause", p.Preview.Form); again.Result == nil || again.Result.Outcome != "review_again" {
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
		if _, _, ok := parseClientExpect(l.Expect, l.Key, "pause"); !ok {
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
	old := clientExpect(key, "pause", time.Now().Add(-time.Second), digest)
	if code, r := run("apply", "pause", "--all", "--expect", key+"="+old); code != exitFailed || r.Result.Outcome != "review_again" || !strings.Contains(r.Result.Lines[0], "the review expired") {
		t.Fatalf("%d %+v", code, r.Result)
	}
	// The arguments are exact.
	for _, bad := range [][]string{
		{"review", "resume", "--all"}, {"review", "stop", "--all"}, {"review", "pause", "--all", "--loop", key},
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
	if !reflect.DeepEqual(list.CanAll, []string{}) {
		t.Errorf("every loop is paused, yet canAll is %q", list.CanAll)
	}
}
