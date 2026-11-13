package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/incident"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/registry"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// panelChangeWorld is a panel server started with --allow-changes over the
// fictional registry, with one session.
type panelChangeWorld struct {
	dir, root, page, origin, form string
	rec                           panelRecord
	cookie                        *http.Cookie
}

func newPanelChangeWorld(t *testing.T, changes bool) panelChangeWorld {
	t.Helper()
	dir, rec, root := panelServeInProcessWith(t, time.Hour, changes)
	page, c := panelSession(t, dir, rec)
	w := panelChangeWorld{dir: dir, root: root, page: page, rec: rec, cookie: c, origin: "http://127.0.0.1:" + strconv.Itoa(rec.Port)}
	_, body := dockRequest(t, http.MethodGet, page+dockServerPath, withCookie(c))
	var served dockServed
	if err := json.Unmarshal([]byte(body), &served); err != nil {
		t.Fatal(err)
	}
	w.form = served.Form
	return w
}

func (w panelChangeWorld) entry(t *testing.T, loop string) registry.Entry {
	t.Helper()
	f, err := registry.Load(filepath.Join(w.root, "registry"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range f.Entries {
		if e.Loop == loop {
			return e
		}
	}
	t.Fatalf("no loop %s", loop)
	return registry.Entry{}
}

// post sends a change request as the page does; edit may spoil it.
func (w panelChangeWorld) post(t *testing.T, key, step, form, body string, edit func(*http.Request)) (*http.Response, panelChangeReply) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, w.page+"api/v1/loops/"+key+"/"+step, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", w.origin)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(panelFormHeader, form)
	req.AddCookie(w.cookie)
	if edit != nil {
		edit(req)
	}
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var reply panelChangeReply
	_ = json.NewDecoder(resp.Body).Decode(&reply)
	return resp, reply
}

func controlOf(t *testing.T, e registry.Entry) control.File {
	t.Helper()
	f, err := control.Load(e.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func lastOperation(t *testing.T, e registry.Entry) string {
	t.Helper()
	j, err := incident.Load(e.StateDir)
	if err != nil || len(j.Records) == 0 {
		t.Fatal(err)
	}
	if c := j.Records[len(j.Records)-1].ControlChange; c != nil {
		return c.Operation
	}
	return ""
}

// Without --allow-changes, and on keepgoing dock always, no change request is
// routed: a request the page could send is refused, and nothing is saved.
func TestPanelChangesExistOnlyWhenAllowed(t *testing.T) {
	w := newPanelChangeWorld(t, false)
	e := w.entry(t, "docs-site-refresh")
	before := controlOf(t, e)
	if w.form != "" {
		t.Fatal("a read-only panel handed out an anti-forgery value")
	}
	if resp, reply := w.post(t, dockKey(e.ID), "prepare", "anything", `{"action":"pause"}`, nil); resp.StatusCode != http.StatusForbidden || reply.Preview != nil {
		t.Fatalf("read-only panel: %d", resp.StatusCode)
	}
	base, _, _ := dockServe(t)
	resp, _ := dockRequest(t, http.MethodPost, base+"api/v1/loops/"+dockKey(e.ID)+"/prepare", func(r *http.Request) {
		r.Header.Set("Origin", strings.TrimSuffix(strings.SplitAfterN(base, "/", 4)[2], "/"))
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		r.Header.Set("Content-Type", "application/json")
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("dock: %d", resp.StatusCode)
	}
	if after := controlOf(t, e); after.Paused != before.Paused || after.Stop != before.Stop {
		t.Fatal("a refused request changed the loop")
	}
}

// Every check on a change request: each spoiled request is refused, and only
// the request the page sends is answered.
func TestPanelChangeRequestsPassEveryCheck(t *testing.T) {
	w := newPanelChangeWorld(t, true)
	e := w.entry(t, "docs-site-refresh")
	key := dockKey(e.ID)
	if w.form == "" || len(w.form) != 64 {
		t.Fatalf("form %q", w.form)
	}
	before := controlOf(t, e)
	for name, c := range map[string]struct {
		form, body string
		edit       func(*http.Request)
	}{
		"no Origin":             {w.form, `{"action":"pause"}`, func(r *http.Request) { r.Header.Del("Origin") }},
		"a foreign Origin":      {w.form, `{"action":"pause"}`, func(r *http.Request) { r.Header.Set("Origin", "http://example.com") }},
		"another port's Origin": {w.form, `{"action":"pause"}`, func(r *http.Request) { r.Header.Set("Origin", "http://127.0.0.1:1") }},
		"no fetch metadata":     {w.form, `{"action":"pause"}`, func(r *http.Request) { r.Header.Del("Sec-Fetch-Site") }},
		"a cross-site fetch":    {w.form, `{"action":"pause"}`, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-site") }},
		"a navigation": {w.form, `{"action":"pause"}`, func(r *http.Request) {
			r.Header.Set("Sec-Fetch-Mode", "navigate")
			r.Header.Set("Sec-Fetch-Dest", "document")
		}},
		"a form post body":       {w.form, `action=pause`, func(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded") }},
		"plain text":             {w.form, `{"action":"pause"}`, func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }},
		"a foreign Host":         {w.form, `{"action":"pause"}`, func(r *http.Request) { r.Host = "attacker.example:" + strconv.Itoa(w.rec.Port) }},
		"no session":             {w.form, `{"action":"pause"}`, func(r *http.Request) { r.Header.Del("Cookie") }},
		"two session cookies":    {w.form, `{"action":"pause"}`, func(r *http.Request) { r.AddCookie(&http.Cookie{Name: panelCookieName, Value: "x"}) }},
		"no anti-forgery value":  {"", `{"action":"pause"}`, nil},
		"a wrong value":          {strings.Repeat("0", 64), `{"action":"pause"}`, nil},
		"an unknown action":      {w.form, `{"action":"enable"}`, nil},
		"an unknown field":       {w.form, `{"action":"pause","id":"` + e.ID + `"}`, nil},
		"a body over the cap":    {w.form, `{"action":"pause","x":"` + strings.Repeat("a", panelMaxBody) + `"}`, nil},
		"a registration ID path": {w.form, `{"action":"pause"}`, func(r *http.Request) { r.URL.Path = strings.Replace(r.URL.Path, key, e.ID, 1) }},
	} {
		resp, reply := w.post(t, key, "prepare", c.form, c.body, c.edit)
		if resp.StatusCode == http.StatusOK || reply.Preview != nil {
			t.Errorf("%s: %d %+v", name, resp.StatusCode, reply)
		}
	}
	// A GET to a change path changes nothing.
	if resp, _ := dockRequest(t, http.MethodGet, w.page+"api/v1/loops/"+key+"/confirm", withCookie(w.cookie)); resp.StatusCode == http.StatusOK {
		t.Errorf("GET confirm: %d", resp.StatusCode)
	}
	if after := controlOf(t, e); after != before {
		t.Fatal("a refused request changed the loop")
	}
	// The page's own request is answered with a review, not a change.
	resp, reply := w.post(t, key, "prepare", w.form, `{"action":"pause"}`, nil)
	if resp.StatusCode != http.StatusOK || reply.Preview == nil || reply.Preview.Form == "" || reply.Preview.Form == w.form {
		t.Fatalf("%d %+v", resp.StatusCode, reply)
	}
	if after := controlOf(t, e); after != before {
		t.Fatal("preparing a pause changed the loop")
	}
	// Its confirmation value is bound to this session: another session's
	// request carrying it is refused, and the value is then spent.
	_, otherCookie := panelSession(t, w.dir, w.rec)
	theirs := w
	theirs.cookie = otherCookie
	if _, r := theirs.post(t, key, "confirm", reply.Preview.Form, `{"action":"pause"}`, nil); r.Result == nil || r.Result.Outcome != "review_again" {
		t.Errorf("another session confirmed: %+v", r)
	}
	if _, r := w.post(t, key, "confirm", reply.Preview.Form, `{"action":"pause"}`, nil); r.Result == nil || r.Result.Outcome != "review_again" {
		t.Errorf("a spent value confirmed: %+v", r)
	}
	if controlOf(t, e).Paused {
		t.Fatal("a refused confirmation paused the loop")
	}
}

var panelTicketPattern = regexp.MustCompile(`resume_[0-9a-f]{32}`)

// Pause, then resume, as the page does them: each reviewed with sentences
// from a fresh read, saved through the client commands' own functions,
// recorded as from the panel, and never answered with a resume ticket.
func TestPanelPauseAndResumeThroughThePage(t *testing.T) {
	w := newPanelChangeWorld(t, true)
	e := w.entry(t, "docs-site-refresh")
	key := dockKey(e.ID)
	// A read just before the change, which the reader caches.
	dockRequest(t, http.MethodGet, w.page+dockAPIPath, withCookie(w.cookie))
	_, p := w.post(t, key, "prepare", w.form, `{"action":"pause"}`, nil)
	if p.Preview == nil || p.Preview.Title != "Pause docs-site-refresh?" || p.Preview.Confirm != "Pause docs-site-refresh" || len(p.Preview.Lines) < 3 {
		t.Fatalf("%+v", p)
	}
	lines := []string{}
	for _, l := range p.Preview.Lines {
		lines = append(lines, l.render(time.Now()))
	}
	if !strings.HasPrefix(lines[0], "Now: Supervising. Last check: ") || lines[1] != "keepgoing sends it nothing until it is resumed. A check already running finishes. Its route, note, other holds and ownership do not change." ||
		!strings.HasPrefix(lines[len(lines)-1], "This review expires in 2 min") {
		t.Errorf("%q", lines)
	}
	_, done := w.post(t, key, "confirm", p.Preview.Form, `{"action":"pause"}`, nil)
	if done.Result == nil || done.Result.Outcome != "done" || done.Result.Text != "Paused: keepgoing will send nothing; a check already running finishes." {
		t.Fatalf("%+v", done)
	}
	if !controlOf(t, e).Paused || lastOperation(t, e) != "panel_pause" {
		t.Fatalf("not saved as a panel pause: %+v %q", controlOf(t, e), lastOperation(t, e))
	}
	// The next read shows it at once, from the loop's own records.
	_, body := dockRequest(t, http.MethodGet, w.page+dockAPIPath, withCookie(w.cookie))
	var r dockReport
	if err := json.Unmarshal([]byte(body), &r); err != nil || dockLoopNamed(t, r, "docs-site-refresh").State != "paused" {
		t.Fatalf("read after the change: %v", err)
	}
	// Resume lists exactly what it clears and keeps its ticket on the server.
	resp, prep := w.post(t, key, "prepare", w.form, `{"action":"resume"}`, nil)
	raw, _ := json.Marshal(prep)
	if resp.StatusCode != http.StatusOK || prep.Preview == nil || panelTicketPattern.Match(raw) {
		t.Fatalf("%d %s", resp.StatusCode, raw)
	}
	text := ""
	for _, l := range prep.Preview.Lines {
		text += l.render(time.Now()) + "\n"
	}
	for _, want := range []string{"Resume clears: paused.", "Resume does not turn supervision on or start a turn; the next check decides what to send.", "Its current route: claude-opus-5-5.", "This review expires in 5 min"} {
		if !strings.Contains(text, want) {
			t.Errorf("resume review lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "supervised for a conversation") {
		t.Errorf("an owner sentence for a loop with no owner:\n%s", text)
	}
	_, done = w.post(t, key, "confirm", prep.Preview.Form, `{"action":"resume"}`, nil)
	if done.Result == nil || done.Result.Outcome != "done" || controlOf(t, e).Paused || lastOperation(t, e) != "panel_resume" {
		t.Fatalf("%+v %+v", done, controlOf(t, e))
	}
	// A confirmation for another action than the one reviewed is refused.
	_, p = w.post(t, key, "prepare", w.form, `{"action":"stop"}`, nil)
	if _, r := w.post(t, key, "confirm", p.Preview.Form, `{"action":"pause"}`, nil); r.Result == nil || r.Result.Outcome != "review_again" || controlOf(t, e).Stop || controlOf(t, e).Paused {
		t.Errorf("%+v", r)
	}
}

// A loop with an owner recorded says so in its stop and resume reviews, from
// the fact alone, never naming the owner.
func TestPanelReviewsSayWhenAConversationOwnsTheLoop(t *testing.T) {
	w := newPanelChangeWorld(t, true)
	e := w.entry(t, "mobile-onboarding")
	if _, err := control.Update(e.StateDir, func(f *control.File) { f.Ownership = &control.Ownership{ThreadID: "thr_example_owner", Generation: 1} }); err != nil {
		t.Fatal(err)
	}
	for action, want := range map[string]string{
		"resume": "This loop is supervised for a conversation. Resuming here clears the holds that conversation or keepgoing set; it does not answer any question the conversation asked.",
		"stop":   "This loop is supervised for a conversation. Stopping here holds it for that conversation too; it does not tell the conversation why.",
	} {
		_, p := w.post(t, dockKey(e.ID), "prepare", w.form, `{"action":"`+action+`"}`, nil)
		if p.Preview == nil {
			t.Fatalf("%s: %+v", action, p)
		}
		raw, _ := json.Marshal(p)
		text := string(raw)
		if !strings.Contains(text, want) || strings.Contains(text, "thr_example_owner") {
			t.Errorf("%s: %s", action, text)
		}
	}
}

// The change functions directly: the page acts as no BB thread even when the
// server's environment names one; a busy loop is refused and nothing is
// retried; an expired review is refused.
func TestPanelChangesActAsNoThreadAndRetryNothing(t *testing.T) {
	d, _ := dockFixtureReader(t)
	t.Setenv("BB_THREAD_ID", "thr_example_owner")
	now := time.Now()
	c, err := newPanelChanges(context.Background(), d.directory, d, now, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	f, err := registry.Load(d.directory)
	if err != nil {
		t.Fatal(err)
	}
	var held registry.Entry
	for _, e := range f.Entries {
		if e.Loop == "billing-api-migration" {
			held = e
		}
	}
	key := dockKey(held.ID)
	// The loop is owned by one thread and the server's environment names
	// another. Acting as the environment's thread would be refused by the
	// owner check; acting as no thread, as a terminal outside BB, is not.
	if _, err := control.Update(held.StateDir, func(f *control.File) { f.Ownership = &control.Ownership{ThreadID: "thr_example_other", Generation: 1} }); err != nil {
		t.Fatal(err)
	}
	if r, code := clientResume(context.Background(), d.directory, held.ID, "prepare-resume", "", false, "thr_example_owner", "", "", time.Now, nil); code == 0 || r.Ticket != "" {
		t.Fatalf("the environment's thread could prepare a resume of another thread's loop: %+v", r)
	}
	p := c.prepare("session-a", key, "resume")
	if p.Preview == nil {
		t.Fatalf("%+v", p.Result)
	}
	if r := c.confirm("session-a", key, "resume", p.Preview.Form); r.Result == nil || r.Result.Outcome != "done" || controlOf(t, held).AwaitingUser {
		t.Fatalf("%+v", r.Result)
	}
	// A loop in the middle of a check: refused, offered again, nothing saved.
	p = c.prepare("session-a", key, "pause")
	lk, err := lock.AcquireNamed(held.StateDir, lock.Control)
	if err != nil {
		t.Fatal(err)
	}
	r := c.confirm("session-a", key, "pause", p.Preview.Form)
	_ = lk.Release()
	if r.Result == nil || r.Result.Outcome != "refused" || !r.Result.Retry || r.Result.Text != "keepgoing is in the middle of a check on this loop. Nothing was changed." || controlOf(t, held).Paused {
		t.Fatalf("%+v", r.Result)
	}
	// The value is spent by that attempt: a second confirmation is refused.
	if r := c.confirm("session-a", key, "pause", p.Preview.Form); r.Result == nil || r.Result.Outcome != "review_again" || controlOf(t, held).Paused {
		t.Fatalf("%+v", r.Result)
	}
	// An expired review.
	p = c.prepare("session-a", key, "pause")
	now = now.Add(panelHoldFormTTL)
	if r := c.confirm("session-a", key, "pause", p.Preview.Form); r.Result == nil || r.Result.Outcome != "review_again" || r.Result.Text != "This review expired. Nothing was changed. Review it again." || controlOf(t, held).Paused {
		t.Fatalf("%+v", r.Result)
	}
	// Every save evidence mapped to its words.
	for _, c := range []struct {
		report  clientHoldReport
		outcome string
		text    string
	}{
		{clientHoldReport{ControlSave: "confirmed", Issues: []string{"incident_history_unavailable"}}, "done", "Paused: keepgoing will send nothing; a check already running finishes. Its history could not record this change."},
		{clientHoldReport{ControlSave: "unconfirmed"}, "uncertain", "keepgoing could not confirm whether this loop was paused. Check its state below before doing anything else."},
		{clientHoldReport{ControlSave: "not_attempted", Issues: []string{"ticket_consumption_unconfirmed"}}, "uncertain", "keepgoing could not confirm whether this loop was paused. Check its state below before doing anything else."},
		{clientHoldReport{ControlSave: "not_attempted", Issues: []string{"observation_changed"}}, "review_again", "The loop changed since you reviewed it, or the review expired. Nothing was changed. Review it again."},
		{clientHoldReport{ControlSave: "not_attempted", Issues: []string{"binding_changed"}}, "refused", "Its configuration changed while it was being read. Nothing was changed."},
		{clientHoldReport{ControlSave: "not_attempted", Issues: []string{"cancelled"}}, "refused", "The request was cut off before anything was saved. Nothing was changed."},
	} {
		if got := panelOutcome("pause", c.report); got.Outcome != c.outcome || got.Text != c.text {
			t.Errorf("%+v: %+v", c.report, got)
		}
	}
}

// The command never changes a running server's mode, and status says how
// long changes have been allowed and how to turn them off.
func TestPanelModeIsFixedAndStatusSaysIt(t *testing.T) {
	w := newPanelChangeWorld(t, true)
	var out, errs bytes.Buffer
	args := []string{"--no-open", "--panel-directory", w.dir, "--directory", filepath.Join(w.root, "registry")}
	if code := panelCommand(env{context.Background(), &out, &errs}, args); code != exitFailed || !strings.Contains(errs.String(), "the running panel allows changes") {
		t.Fatalf("%d %s", code, errs.String())
	}
	out.Reset()
	errs.Reset()
	if code := panelCommand(env{context.Background(), &out, &errs}, append([]string{"--allow-changes"}, args...)); code != exitOK || !strings.Contains(out.String(), "Changes are allowed from the page") {
		t.Fatalf("%d %s %s", code, out.String(), errs.String())
	}
	out.Reset()
	if code := panelStatusCommand(env{context.Background(), &out, &errs}, []string{"--panel-directory", w.dir}); code != exitOK || !strings.Contains(out.String(), "Changes allowed for") || !strings.Contains(out.String(), "keepgoing panel stop turns them off") {
		t.Fatalf("%d %s", code, out.String())
	}
	out.Reset()
	if panelStatusCommand(env{context.Background(), &out, &errs}, []string{"--json", "--panel-directory", w.dir}); !strings.Contains(out.String(), `"changesAllowed":true`) {
		t.Fatalf("%s", out.String())
	}
	_, body := dockRequest(t, http.MethodGet, w.page+dockServerPath, withCookie(w.cookie))
	var served dockServed
	if err := json.Unmarshal([]byte(body), &served); err != nil || served.Changes != "on" || served.ChangesSince == nil ||
		!strings.HasPrefix(served.ChangesText.render(time.Now()), "Changes allowed for under a minute: started with keepgoing panel --allow-changes at ") ||
		!strings.HasSuffix(served.ChangesText.render(time.Now()), ". keepgoing panel stop turns them off.") {
		t.Fatalf("%s", body)
	}
	// And a read-only server refuses to be reused as one that allows changes.
	ro := newPanelChangeWorld(t, false)
	errs.Reset()
	if code := panelCommand(env{context.Background(), &out, &errs}, []string{"--allow-changes", "--no-open", "--panel-directory", ro.dir, "--directory", filepath.Join(ro.root, "registry")}); code != exitFailed || !strings.Contains(errs.String(), "a read-only panel is already running") {
		t.Fatalf("%d %s", code, errs.String())
	}
}

// A panel resume of a hold keepgoing raised for a spent allowance starts a new
// episode exactly as client resume does, and is recorded as from the panel.
func TestPanelResumeOfASpentCapHoldStartsANewEpisode(t *testing.T) {
	d, _ := dockFixtureReader(t)
	now := time.Now()
	c, err := newPanelChanges(context.Background(), d.directory, d, now, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	f, err := registry.Load(d.directory)
	if err != nil {
		t.Fatal(err)
	}
	var e registry.Entry
	for _, x := range f.Entries {
		if x.Loop == "billing-api-migration" {
			e = x
		}
	}
	if _, err := control.Update(e.StateDir, func(f *control.File) { f.EpisodeHold = true }); err != nil {
		t.Fatal(err)
	}
	st, err := state.Load(e.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	st.Reprompts, st.Nudges = 3, 2
	if err := state.Save(e.StateDir, st); err != nil {
		t.Fatal(err)
	}
	p := c.prepare("session-a", dockKey(e.ID), "resume")
	if p.Preview == nil {
		t.Fatalf("%+v", p.Result)
	}
	if r := c.confirm("session-a", dockKey(e.ID), "resume", p.Preview.Form); r.Result == nil || r.Result.Outcome != "done" {
		t.Fatalf("%+v", r.Result)
	}
	after, err := state.Load(e.StateDir)
	if err != nil || after.Reprompts != 0 || after.Nudges != 0 || after.ResumeEpisodes != 1 {
		t.Fatalf("%+v %v", after, err)
	}
	j, err := incident.Load(e.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if ch := j.Records[len(j.Records)-1].ControlChange; ch == nil || ch.Operation != "panel_resume" || !ch.NewEpisode || ch.Caller != "client" {
		t.Fatalf("%+v", ch)
	}
	if ctl := controlOf(t, e); ctl.EpisodeHold || ctl.AwaitingUser {
		t.Fatalf("%+v", ctl)
	}
}

// A confirmation acts only on the control record the person reviewed: a
// change made from a terminal in between makes all three actions "review
// again", with nothing changed by the panel.
func TestPanelConfirmRefusesAChangedRecord(t *testing.T) {
	d, _ := dockFixtureReader(t)
	now := time.Now()
	c, err := newPanelChanges(context.Background(), d.directory, d, now, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	f, err := registry.Load(d.directory)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]registry.Entry{}
	for _, e := range f.Entries {
		byName[e.Loop] = e
	}
	for _, x := range []struct{ loop, action, between string }{
		{"docs-site-refresh", "stop", "pause"},
		{"inventory-sync", "pause", "stop"},
		{"mobile-onboarding", "resume", "stop"},
	} {
		e := byName[x.loop]
		p := c.prepare("session-a", dockKey(e.ID), x.action)
		if p.Preview == nil {
			t.Fatalf("%s: %+v", x.loop, p.Result)
		}
		// From a terminal, between the review and the confirmation.
		if r, code := applyClientHold(context.Background(), d.directory, e.ID, x.between); code != 0 || r.ControlSave != "confirmed" {
			t.Fatalf("%s: %+v", x.loop, r)
		}
		before := controlOf(t, e)
		r := c.confirm("session-a", dockKey(e.ID), x.action, p.Preview.Form)
		if r.Result == nil || r.Result.Outcome != "review_again" || !r.Result.Retry {
			t.Errorf("%s %s after a %s: %+v", x.loop, x.action, x.between, r.Result)
		}
		if after := controlOf(t, e); after != before {
			t.Errorf("%s: the panel changed a record it had not reviewed: %+v -> %+v", x.loop, before, after)
		}
	}
}

// The resume review says everything the resume does, from the same function
// the resume saves with: the holds, the note, the open question and a new
// error episode, each only when it applies.
func TestPanelResumeReviewSaysWhatResumeDoes(t *testing.T) {
	d, _ := dockFixtureReader(t)
	now := time.Now()
	c, err := newPanelChanges(context.Background(), d.directory, d, now, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	f, _ := registry.Load(d.directory)
	var e registry.Entry
	for _, x := range f.Entries {
		if x.Loop == "billing-api-migration" {
			e = x
		}
	}
	review := func() string {
		p := c.prepare("session-a", dockKey(e.ID), "resume")
		if p.Preview == nil {
			t.Fatalf("%+v", p.Result)
		}
		out := ""
		for _, l := range p.Preview.Lines {
			out += l.render(now) + "\n"
		}
		return out
	}
	episode, question := "resume starts a new error episode: those counts start over", "removes the record of the question that was open for a person"
	if text := review(); strings.Contains(text, episode) || strings.Contains(text, question) || !strings.Contains(text, "Resume clears: waiting for you and the note left with the hold.") {
		t.Errorf("plain hold:\n%s", text)
	}
	if _, err := control.Update(e.StateDir, func(f *control.File) {
		f.EpisodeHold, f.Completed, f.OpenQuestion = true, true, &control.OpenQuestion{Note: "fictional question", At: now}
	}); err != nil {
		t.Fatal(err)
	}
	text := review()
	if !strings.Contains(text, episode) || !strings.Contains(text, question) || !strings.Contains(text, "Resume clears: waiting for you, finished and the note left with the hold.") {
		t.Errorf("spent-cap hold with an open question:\n%s", text)
	}
	// What the review lists is what the resume clears.
	ctl := controlOf(t, e)
	after := ctl
	got := clearForResume(&after)
	if got != (resumeEffect{AwaitingUser: true, Completed: true, Note: true, OpenQuestion: true, NewEpisode: true}) || after.AwaitingUser || after.Completed || after.Note != "" || after.OpenQuestion != nil || after.EpisodeHold {
		t.Errorf("%+v %+v", got, after)
	}
}

// Each check on a change request on its own: a request that fails only that
// check is refused. (The table in TestPanelChangeRequestsPassEveryCheck
// varies several headers at once for some cases.)
func TestPanelChangeRequestChecksEachHoldAlone(t *testing.T) {
	w := newPanelChangeWorld(t, true)
	e := w.entry(t, "docs-site-refresh")
	key := dockKey(e.ID)
	other := w.entry(t, "inventory-sync")
	foreign := "attacker.example:" + strconv.Itoa(w.rec.Port)
	padded := `{"action":"pause"}` + strings.Repeat(" ", panelMaxBody)
	for name, c := range map[string]struct {
		body string
		edit func(*http.Request)
	}{
		// The Origin matches the foreign Host, so only the Host check stands.
		"a foreign Host with its own Origin": {`{"action":"pause"}`, func(r *http.Request) { r.Host = foreign; r.Header.Set("Origin", "http://"+foreign) }},
		"localhost, though loopback": {`{"action":"pause"}`, func(r *http.Request) {
			r.Host = "localhost:" + strconv.Itoa(w.rec.Port)
			r.Header.Set("Origin", "http://"+r.Host)
		}},
		"Sec-Fetch-Dest alone":         {`{"action":"pause"}`, func(r *http.Request) { r.Header.Set("Sec-Fetch-Dest", "document") }},
		"Sec-Fetch-Mode alone":         {`{"action":"pause"}`, func(r *http.Request) { r.Header.Set("Sec-Fetch-Mode", "navigate") }},
		"a valid body over 4 KiB":      {padded, nil},
		"a body sent in chunks":        {`{"action":"pause"}`, func(r *http.Request) { r.ContentLength = -1; r.TransferEncoding = []string{"chunked"} }},
		"the key in another case":      {`{"Action":"pause"}`, nil},
		"a second key in another case": {`{"action":"pause","Action":"stop"}`, nil},
		"the key twice":                {`{"action":"pause","action":"stop"}`, nil},
	} {
		resp, reply := w.post(t, key, "prepare", w.form, c.body, c.edit)
		if resp.StatusCode == http.StatusOK || reply.Preview != nil {
			t.Errorf("%s: %d %+v", name, resp.StatusCode, reply)
		}
	}
	// A confirmation value for one loop does not confirm on another.
	_, p := w.post(t, key, "prepare", w.form, `{"action":"pause"}`, nil)
	if p.Preview == nil {
		t.Fatal("no review")
	}
	if _, r := w.post(t, dockKey(other.ID), "confirm", p.Preview.Form, `{"action":"pause"}`, nil); r.Result == nil || r.Result.Outcome != "review_again" {
		t.Errorf("another loop's key: %+v", r)
	}
	if controlOf(t, e).Paused || controlOf(t, other).Paused {
		t.Fatal("a confirmation for one loop changed a loop")
	}
	// The exposure is said where changes are turned on.
	_, body := dockRequest(t, http.MethodGet, w.page+dockServerPath, withCookie(w.cookie))
	if !strings.Contains(body, panelExposure) {
		t.Errorf("footer: %s", body)
	}
	var out, errs bytes.Buffer
	if code := panelCommand(env{context.Background(), &out, &errs}, []string{"--allow-changes", "--no-open", "--panel-directory", w.dir, "--directory", filepath.Join(w.root, "registry")}); code != exitOK || !strings.Contains(out.String(), panelExposure) {
		t.Errorf("%d %s", code, out.String())
	}
}

// A review reads the control record under its lock: while a check or a
// command holds it, the review is refused as busy, with nothing prepared.
func TestPanelReviewReadsTheRecordUnderItsLock(t *testing.T) {
	d, _ := dockFixtureReader(t)
	now := time.Now()
	c, err := newPanelChanges(context.Background(), d.directory, d, now, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	f, _ := registry.Load(d.directory)
	e := f.Entries[0]
	lk, err := lock.AcquireNamed(e.StateDir, lock.Control)
	if err != nil {
		t.Fatal(err)
	}
	r := c.prepare("session-a", dockKey(e.ID), "pause")
	_ = lk.Release()
	if r.Preview != nil || r.Result == nil || r.Result.Outcome != "refused" || !r.Result.Retry || r.Result.Text != "keepgoing is in the middle of a check on this loop. Nothing was changed." || len(c.forms) != 0 {
		t.Fatalf("%+v %+v", r.Preview, r.Result)
	}
	if r := c.prepare("session-a", dockKey(e.ID), "pause"); r.Preview == nil {
		t.Fatalf("after the lock is released: %+v", r.Result)
	}
}
