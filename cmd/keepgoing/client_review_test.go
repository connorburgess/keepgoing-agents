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
		got, code := clientReview(context.Background(), d.directory, dockKey(e.ID), x.action, now)
		if page.Preview == nil || got.Preview == nil || code != exitOK {
			t.Fatalf("%s %s: %+v %+v", x.loop, x.action, page.Result, got.Result)
		}
		want := []string{}
		for _, l := range page.Preview.Lines {
			want = append(want, l.render(now))
		}
		p := got.Preview
		if p.Title != page.Preview.Title || p.Confirm != page.Preview.Confirm || !reflect.DeepEqual(p.Lines, want) || p.Lost != page.Preview.Lost ||
			p.Refused != page.Preview.Refused || !p.ExpiresAt.Equal(page.Preview.ExpiresAt) || p.Key != dockKey(e.ID) || p.Action != x.action {
			t.Errorf("%s %s: the menu's review differs from the page's:\n%+v\n%q", x.loop, x.action, p, want)
		}
		if p.Expect != controlDigest(before) {
			t.Errorf("%s: expect is not the digest of the record reviewed", x.loop)
		}
		if after := controlOf(t, e); !reflect.DeepEqual(after, before) || incidentCount(t, e) != records {
			t.Errorf("%s: a review changed the loop: %+v -> %+v", x.loop, before, after)
		}
		// Words for one surface only are not in either.
		for _, l := range p.Lines {
			if strings.Contains(l, "this page") || strings.Contains(l, "the panel asks") {
				t.Errorf("%s: %q", x.loop, l)
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
	key, digest := dockKey(e.ID), controlDigest(controlOf(t, e))
	before := controlOf(t, e)
	for _, args := range [][]string{
		{"review"}, {"review", "enable", "--loop", key}, {"review", "stop", "--loop", e.ID}, {"review", "stop", "--loop", key, "--expect", digest},
		{"apply", "stop", "--loop", key}, {"apply", "stop", "--loop", key, "--expect", "abc"}, {"apply", "stop", "--loop", key, "--expect", digest, "extra"},
		{"apply", "stop", "--id", e.ID, "--expect", digest},
	} {
		code, _, errs := runClientReview(t, append(args, "--directory", d.directory, "--json")...)
		if code != exitUsage || !strings.Contains(errs, "usage") {
			t.Errorf("%q: %d %s", args, code, errs)
		}
	}
	if after := controlOf(t, e); !reflect.DeepEqual(after, before) {
		t.Fatal("a refused command changed the loop")
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
