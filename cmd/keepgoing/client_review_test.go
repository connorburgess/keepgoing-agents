package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/incident"
	"github.com/connorburgess/keepgoing-agents/internal/registry"
)

func clientReviewWorld(t *testing.T) (*dockReader, map[string]registry.Entry) {
	t.Helper()
	d, _ := dockFixtureReader(t)
	f, err := registry.Load(d.directory)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]registry.Entry{}
	for _, e := range f.Entries {
		byName[e.Loop] = e
	}
	return d, byName
}

func runClientReview(t *testing.T, args ...string) (int, clientReviewReply, string) {
	t.Helper()
	var out, errs bytes.Buffer
	code := clientCommand(env{context.Background(), &out, &errs}, args)
	var reply clientReviewReply
	if out.Len() > 0 {
		if err := json.Unmarshal(out.Bytes(), &reply); err != nil || reply.SchemaVersion != 1 {
			t.Fatalf("not a schema-1 reply: %v %s", err, out.String())
		}
	}
	return code, reply, errs.String()
}

func incidentCount(t *testing.T, e registry.Entry) int {
	t.Helper()
	j, err := incident.Load(e.StateDir)
	if err != nil {
		return 0
	}
	return len(j.Records)
}

// client review prints the page's own review, word for word, and changes
// nothing: the menu and the page can never say different things.
func TestClientReviewIsThePanelsReview(t *testing.T) {
	d, byName := clientReviewWorld(t)
	now := dockNow
	c, err := newPanelChanges(context.Background(), d.directory, d, now, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range []struct{ loop, action string }{
		{"docs-site-refresh", "pause"}, {"docs-site-refresh", "stop"}, {"mobile-onboarding", "resume"},
	} {
		e := byName[x.loop]
		before, records := controlOf(t, e), incidentCount(t, e)
		page := c.prepare("session-a", dockKey(e.ID), x.action)
		got, code := clientReview(context.Background(), d.directory, dockKey(e.ID), x.action, now, time.UTC)
		if page.Preview == nil || got.Preview == nil || code != exitOK {
			t.Fatalf("%s %s: %+v %+v", x.loop, x.action, page.Result, got.Result)
		}
		want := []string{}
		// The page's sentences, with each time in the zone given and named.
		for _, l := range page.Preview.Lines {
			want = append(want, l.renderLocal(now, time.UTC))
		}
		p := got.Preview
		if p.Title != page.Preview.Title || p.Confirm != page.Preview.Confirm || !reflect.DeepEqual(p.Lines, want) || p.Lost != page.Preview.Lost ||
			p.Refused != page.Preview.Refused || !p.ExpiresAt.Equal(page.Preview.ExpiresAt) || p.Key != dockKey(e.ID) || p.Action != x.action {
			t.Errorf("%s %s: the menu's review differs from the page's:\n%+v\n%q", x.loop, x.action, p, want)
		}
		if digest, expires, _, ok := parseClientStopExpect(p.Expect, dockKey(e.ID), x.action); !ok || digest != controlDigest(before) || expires.Unix() != p.ExpiresAt.Unix() {
			t.Errorf("%s: expect does not bind the record reviewed, the loop, the action and the expiry", x.loop)
		}
		if after := controlOf(t, e); !reflect.DeepEqual(after, before) || incidentCount(t, e) != records {
			t.Errorf("%s: a review changed the loop: %+v -> %+v", x.loop, before, after)
		}
		// Words for one surface only are not in either, and no line is cut
		// short where the menu shows it (Contract.text keeps 180 characters).
		for _, l := range append([]string{p.Title, p.Confirm, p.Lost, p.Refused, p.Expired}, p.Lines...) {
			if strings.Contains(l, "this page") || strings.Contains(l, "the panel asks") || utf8.RuneCountInString(l) > clientReviewMenuCap {
				t.Errorf("%s: %d %q", x.loop, utf8.RuneCountInString(l), l)
			}
		}
	}
	// Through the command, with its exit code.
	e := byName["docs-site-refresh"]
	code, reply, _ := runClientReview(t, "review", "stop", "--loop", dockKey(e.ID), "--directory", d.directory, "--json")
	if code != exitOK || reply.Preview == nil || reply.Preview.Title != "Stop docs-site-refresh?" || reply.Result != nil {
		t.Fatalf("%d %+v", code, reply)
	}
	// A loop that is not registered is refused, in the page's words.
	code, reply, _ = runClientReview(t, "review", "stop", "--loop", "l000000000000", "--directory", d.directory, "--json")
	if code != exitFailed || reply.Result == nil || reply.Result.Text != "This loop is no longer registered. Nothing was changed." {
		t.Fatalf("%d %+v", code, reply)
	}
}

// client apply acts only on the control record that was reviewed, says what
// happened in the page's words, and acts as no BB thread.
func TestClientApplyActsOnlyOnTheRecordReviewed(t *testing.T) {
	d, byName := clientReviewWorld(t)
	t.Setenv("BB_THREAD_ID", "thr_example0001")
	e := byName["docs-site-refresh"]
	key := dockKey(e.ID)
	_, review, _ := runClientReview(t, "review", "stop", "--loop", key, "--directory", d.directory, "--json")
	if review.Preview == nil {
		t.Fatalf("%+v", review.Result)
	}
	// From a terminal, between the review and the confirmation.
	if r, code := applyClientHold(context.Background(), d.directory, e.ID, "pause"); code != 0 || r.ControlSave != "confirmed" {
		t.Fatalf("%+v", r)
	}
	before := controlOf(t, e)
	code, reply, _ := runClientReview(t, "apply", "stop", "--loop", key, "--expect", review.Preview.Expect, "--directory", d.directory, "--json")
	if code != exitFailed || reply.Result == nil || reply.Result.Outcome != "review_again" || !reply.Result.Retry {
		t.Fatalf("%d %+v", code, reply.Result)
	}
	if after := controlOf(t, e); !reflect.DeepEqual(after, before) {
		t.Fatalf("applied to a record that was not reviewed: %+v -> %+v", before, after)
	}
	// Reviewed again, it is done, as a command and not from the panel.
	_, review, _ = runClientReview(t, "review", "stop", "--loop", key, "--directory", d.directory, "--json")
	code, reply, _ = runClientReview(t, "apply", "stop", "--loop", key, "--expect", review.Preview.Expect, "--directory", d.directory, "--json")
	if code != exitOK || reply.Result == nil || reply.Result.Outcome != "done" || reply.Result.Text != "Stop requested: the next check carries it out." {
		t.Fatalf("%d %+v", code, reply.Result)
	}
	if !controlOf(t, e).Stop || lastOperation(t, e) != "stop" {
		t.Fatalf("%+v %q", controlOf(t, e), lastOperation(t, e))
	}
	if controlOf(t, e).Ownership != nil {
		t.Fatal("apply took the caller's thread as owner")
	}
	// Resume: prepared and used in one apply; the ticket is never printed.
	_, review, _ = runClientReview(t, "review", "resume", "--loop", key, "--directory", d.directory, "--json")
	if review.Preview == nil || !strings.Contains(strings.Join(review.Preview.Lines, "\n"), "Resume clears: paused and stop requested.") {
		t.Fatalf("%+v", review)
	}
	var out, errs bytes.Buffer
	code = clientCommand(env{context.Background(), &out, &errs}, []string{"apply", "resume", "--loop", key, "--expect", review.Preview.Expect, "--directory", d.directory, "--json"})
	if code != exitOK || strings.Contains(out.String(), "resume_") || controlOf(t, e).Paused || controlOf(t, e).Stop || lastOperation(t, e) != "resume" {
		t.Fatalf("%d %s %+v", code, out.String(), controlOf(t, e))
	}
}

// The arguments are exactly these: an action, a page key, and for apply a
// review's expect value. Anything else is a usage error and touches nothing.
func TestClientReviewArgumentsAreExact(t *testing.T) {
	d, byName := clientReviewWorld(t)
	e := byName["docs-site-refresh"]
	key, digest := dockKey(e.ID), clientExpect(dockKey(e.ID), "stop", time.Now().Add(time.Minute), controlDigest(controlOf(t, e)))
	before := controlOf(t, e)
	for _, args := range [][]string{
		{"review"}, {"review", "enable", "--loop", key}, {"review", "stop", "--loop", e.ID}, {"review", "stop", "--loop", key, "--expect", digest},
		{"apply", "stop", "--loop", key}, {"apply", "stop", "--loop", key, "--expect", "abc"}, {"apply", "stop", "--loop", key, "--expect", digest, "extra"},
		{"apply", "stop", "--id", e.ID, "--expect", digest}, {"apply", "stop", "--loop", key, "--expect", controlDigest(controlOf(t, e))},
	} {
		code, _, errs := runClientReview(t, append(args, "--directory", d.directory, "--json")...)
		if code != exitUsage || !strings.Contains(errs, "usage") {
			t.Errorf("%q: %d %s", args, code, errs)
		}
	}
	if after := controlOf(t, e); !reflect.DeepEqual(after, before) {
		t.Fatal("a refused command changed the loop")
	}
	// Help is help, as for the other client commands.
	for _, args := range [][]string{{"review", "--help"}, {"apply", "-h"}, {"review", "stop", "--help"}} {
		var out, errs bytes.Buffer
		if code := clientCommand(env{context.Background(), &out, &errs}, args); code != exitOK || !strings.Contains(out.String(), "--expect") {
			t.Errorf("%q: %d %s %s", args, code, out.String(), errs.String())
		}
	}
}

// One review is for one action on one loop, until it expires: its expect
// value applied to another loop, as another action or too late is refused,
// and nothing changes.
func TestClientApplyRefusesAReviewForAnythingElse(t *testing.T) {
	d, byName := clientReviewWorld(t)
	alpha, beta := byName["docs-site-refresh"], byName["inventory-sync"]
	_, review, _ := runClientReview(t, "review", "pause", "--loop", dockKey(alpha.ID), "--directory", d.directory, "--json")
	if review.Preview == nil {
		t.Fatalf("%+v", review.Result)
	}
	beforeA, beforeB := controlOf(t, alpha), controlOf(t, beta)
	for _, x := range []struct{ key, action, want string }{
		{dockKey(beta.ID), "pause", clientReviewOther}, {dockKey(beta.ID), "stop", clientReviewOther}, {dockKey(alpha.ID), "stop", clientReviewOther},
	} {
		code, reply, _ := runClientReview(t, "apply", x.action, "--loop", x.key, "--expect", review.Preview.Expect, "--directory", d.directory, "--json")
		if code != exitFailed || reply.Result == nil || reply.Result.Outcome != "review_again" || reply.Result.Text != x.want {
			t.Errorf("%s %s: %d %+v", x.key, x.action, code, reply.Result)
		}
	}
	// Past its time, even for the loop and action reviewed.
	digest, expires, _ := parseClientExpect(review.Preview.Expect, dockKey(alpha.ID), "pause")
	if reply, code := clientApply(context.Background(), d.directory, dockKey(alpha.ID), "pause", review.Preview.Expect, "", expires.Add(11*time.Second), holdOptions{}); code != exitFailed || reply.Result.Text != clientReviewExpired {
		t.Errorf("an expired review was applied: %d %+v", code, reply.Result)
	}
	// A value whose expiry was moved does not check.
	moved := clientExpectPattern.ReplaceAllString(review.Preview.Expect, "r1.9999999999.${2}.${3}")
	if reply, code := clientApply(context.Background(), d.directory, dockKey(alpha.ID), "pause", moved, "", time.Now(), holdOptions{}); code != exitFailed || reply.Result.Text != clientReviewOther {
		t.Errorf("a changed expiry was accepted: %d %+v", code, reply.Result)
	}
	if !reflect.DeepEqual(controlOf(t, alpha), beforeA) || !reflect.DeepEqual(controlOf(t, beta), beforeB) {
		t.Fatal("a refused apply changed a loop")
	}
	_ = digest
	// The text line names the registry directory, so it can be pasted.
	var out, errs bytes.Buffer
	clientCommand(env{context.Background(), &out, &errs}, []string{"review", "pause", "--loop", dockKey(alpha.ID), "--directory", d.directory})
	if !strings.Contains(out.String(), "To confirm before it expires: keepgoing client apply pause --loop "+dockKey(alpha.ID)+" --expect r1.") || !strings.Contains(out.String(), "--directory "+shellQuote(d.directory)) {
		t.Errorf("%s", out.String())
	}
}

// A resume acts as the BB thread it runs in, as keepgoing resume does: a loop
// another conversation owns is refused, and stays held. Outside BB it may be
// resumed. Pause and stop are anyone's.
func TestClientApplyResumeIsRefusedFromAnotherConversation(t *testing.T) {
	d, byName := clientReviewWorld(t)
	e := byName["docs-site-refresh"]
	key := dockKey(e.ID)
	if _, err := control.Update(e.StateDir, func(f *control.File) { f.Ownership = &control.Ownership{ThreadID: "thr_example_owner", Generation: 1} }); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BB_THREAD_ID", "thr_example_other")
	// Pause from the other conversation is allowed, as keepgoing pause is.
	_, review, _ := runClientReview(t, "review", "pause", "--loop", key, "--directory", d.directory, "--json")
	if code, reply, _ := runClientReview(t, "apply", "pause", "--loop", key, "--expect", review.Preview.Expect, "--directory", d.directory, "--json"); code != exitOK || reply.Result.Outcome != "done" {
		t.Fatalf("%d %+v", code, reply.Result)
	}
	_, review, _ = runClientReview(t, "review", "resume", "--loop", key, "--directory", d.directory, "--json")
	before := controlOf(t, e)
	code, reply, _ := runClientReview(t, "apply", "resume", "--loop", key, "--expect", review.Preview.Expect, "--directory", d.directory, "--json")
	if code != exitFailed || reply.Result == nil || reply.Result.Outcome != "refused" || reply.Result.Text != clientReviewOwned {
		t.Fatalf("a resume from another conversation was not refused: %d %+v", code, reply.Result)
	}
	if after := controlOf(t, e); !reflect.DeepEqual(after, before) || !after.Paused {
		t.Fatalf("the loop changed: %+v", after)
	}
	// keepgoing client prepare-resume refuses it the same way.
	if r, _ := clientResume(context.Background(), d.directory, e.ID, "prepare-resume", "", false, "thr_example_other", "", "", time.Now, nil); r.Ticket != "" {
		t.Fatal("prepare-resume allowed what client apply refused")
	}
	// The resume function itself refuses it too, past the plain check.
	if r := panelApply(context.Background(), d.directory, e.ID, "resume", "", "thr_example_other", controlDigest(before), holdOptions{}); r.ControlSave == "confirmed" {
		t.Fatalf("%+v", r)
	}
	// From outside BB, as a person at a terminal or the menu.
	t.Setenv("BB_THREAD_ID", "")
	_, review, _ = runClientReview(t, "review", "resume", "--loop", key, "--directory", d.directory, "--json")
	if code, reply, _ := runClientReview(t, "apply", "resume", "--loop", key, "--expect", review.Preview.Expect, "--directory", d.directory, "--json"); code != exitOK || reply.Result.Outcome != "done" || controlOf(t, e).Paused {
		t.Fatalf("%d %+v", code, reply.Result)
	}
}

// A stale resume is refused before it prepares a ticket, so a resume another
// client prepared can still be used.
func TestAStaleResumeLeavesAnotherClientsTicket(t *testing.T) {
	d, byName := clientReviewWorld(t)
	e := byName["mobile-onboarding"]
	key := dockKey(e.ID)
	_, review, _ := runClientReview(t, "review", "resume", "--loop", key, "--directory", d.directory, "--json")
	if review.Preview == nil {
		t.Fatalf("%+v", review.Result)
	}
	if r, code := applyClientHold(context.Background(), d.directory, e.ID, "stop"); code != 0 || r.ControlSave != "confirmed" {
		t.Fatalf("%+v", r)
	}
	other, _ := clientResume(context.Background(), d.directory, e.ID, "prepare-resume", "", false, "", "", "", time.Now, nil)
	if other.Ticket == "" {
		t.Fatalf("%+v", other)
	}
	if code, reply, _ := runClientReview(t, "apply", "resume", "--loop", key, "--expect", review.Preview.Expect, "--directory", d.directory, "--json"); code != exitFailed || reply.Result.Outcome != "review_again" {
		t.Fatalf("%d %+v", code, reply.Result)
	}
	if r, code := clientResume(context.Background(), d.directory, e.ID, "resume", other.Ticket, false, "", "", "", time.Now, nil); code != exitOK || r.ControlSave != "confirmed" {
		t.Fatalf("the other client's ticket was replaced: %d %+v", code, r)
	}
}

// Review times are in this computer's zone, named, not UTC: at 06:43 in
// +07 a review says it expires at 06:45 +07.
func TestClientReviewTimesAreLocalAndNamed(t *testing.T) {
	d, byName := clientReviewWorld(t)
	bangkok, err := time.LoadLocation("Asia/Bangkok")
	if err != nil {
		bangkok = time.FixedZone("+07", 7*3600)
	}
	now := time.Date(2030, 1, 1, 23, 43, 0, 0, time.UTC) // 06:43 in +07
	got, _ := clientReview(context.Background(), d.directory, dockKey(byName["docs-site-refresh"].ID), "pause", now, bangkok)
	if got.Preview == nil {
		t.Fatalf("%+v", got.Result)
	}
	last := got.Preview.Lines[len(got.Preview.Lines)-1]
	if last != "This review expires in 2 min (06:45 +07)." {
		t.Errorf("%q", last)
	}
	if got, _ := clientReview(context.Background(), d.directory, dockKey(byName["docs-site-refresh"].ID), "pause", now, time.UTC); got.Preview.Lines[len(got.Preview.Lines)-1] != "This review expires in 2 min (23:45 UTC)." {
		t.Errorf("%q", got.Preview.Lines)
	}
	if s := (dockText{dockPart{At: &now, F: "clock"}}).renderLocal(now, time.FixedZone("x", -(3*3600+1800))); s != "20:13 -03:30" {
		t.Errorf("%q", s)
	}
}

// panel list says which changes fit each loop, as the page offers them, and
// panel status carries the footer's exposure statement.
func TestPanelListSaysWhichChangesFitAndStatusTheExposure(t *testing.T) {
	d, _ := dockFixtureReader(t)
	r := d.read(context.Background(), false)
	list := buildPanelList(r)
	for i, l := range r.Loops {
		if !reflect.DeepEqual(list.Loops[i].Can, l.Can) {
			t.Errorf("%s: list %q, page %q", l.Name, list.Loops[i].Can, l.Can)
		}
	}
	raw, _ := json.Marshal(list)
	if !strings.Contains(string(raw), `"can":[`) {
		t.Fatalf("%s", raw)
	}
	var out, errs bytes.Buffer
	if code := panelStatusCommand(env{context.Background(), &out, &errs}, []string{"--json", "--panel-directory", filepath.Join(panelShortDir(t), "panel")}); code != exitOK {
		t.Fatalf("%d %s", code, errs.String())
	}
	if !strings.Contains(out.String(), `"exposure":"`+panelExposure+`"`) {
		t.Fatalf("%s", out.String())
	}
}
