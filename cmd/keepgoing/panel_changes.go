package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/registry"
)

// Changes from the panel: pause, stop and resume of one loop at a time. They
// are offered only by the background panel server, and only when it was
// started with keepgoing panel --allow-changes; keepgoing dock never offers
// them. Every change is two requests, prepare then confirm, and runs the same
// in-process functions as client pause|stop and client prepare-resume and
// resume, so it takes the same locks, makes the same checks and returns the
// same issue codes. The page acts as the person at this computer with no BB
// thread identity, as a terminal outside BB does. Nothing is retried by
// itself: a new attempt is a new prepare and a new review.

const (
	panelFormHeader     = "X-Keepgoing-Form"
	panelMaxBody        = 4 << 10
	panelMaxForms       = 32
	panelHoldFormTTL    = 2 * time.Minute
	panelChangeTimeout  = 20 * time.Second
	panelChangesPrefix  = "api/v1/loops/"
	panelChangeOrigin   = "panel"
	panelPrepareContext = "keepgoing-panel-prepare\x00"
	// panelExposure is said wherever changes are turned on.
	panelExposure = "While changes are allowed, anyone who can use this browser window can pause, stop and resume loops, and replace the file that runs a loop's checks with this panel's build."
)

// panelChanges is the change half of a panel server started with changes
// allowed. A server without it routes no write request at all.
type panelChanges struct {
	ctx      context.Context // the server's lifetime; changes never run on a request's
	registry string          // the --directory value, as given
	reader   *dockReader
	since    time.Time
	now      func() time.Time
	secret   []byte // keys the per-session prepare value; never leaves the process

	mu    sync.Mutex
	forms map[string]panelForm
	order []string // form values, oldest first
}

// panelForm is one prepared change waiting for its confirmation. The value
// that names it is single-use and bound to the session, the loop and the
// action. A resume ticket stays here; it is never sent to the page.
type panelForm struct {
	session, key, action, id, loop string
	digest                         string // the controlDigest of the record reviewed
	expires                        time.Time
	stop                           *stopChoices // the stop's options the review offered
	// group: for a change to every loop, each loop it changes and the
	// record reviewed for it; names, for the result.
	group []panelGroupTarget
	names map[string]string
}

func newPanelChanges(ctx context.Context, registryDir string, reader *dockReader, since time.Time, now func() time.Time) (*panelChanges, error) {
	secret, err := panelRandom(32)
	if err != nil {
		return nil, err
	}
	return &panelChanges{ctx: ctx, registry: registryDir, reader: reader, since: since, now: now, secret: []byte(secret), forms: map[string]panelForm{}}, nil
}

// prepareValue is the anti-forgery value a session sends with a prepare
// request. It is derived from the session, so nothing is stored for it, and
// is handed to the page only by api/v1/server, which needs the session.
func (c *panelChanges) prepareValue(session string) string {
	m := hmac.New(sha256.New, c.secret)
	m.Write([]byte(panelPrepareContext + session))
	return hex.EncodeToString(m.Sum(nil))
}

// allowPost is the guard for a change request, stricter than the guard for
// reads: the Host and Origin of the page exactly (127.0.0.1 and this port,
// the only address the session cookie is sent to), fetch metadata that says
// the page itself sent it, a JSON body of bounded size and the anti-forgery
// header. A browser that sends no fetch metadata can read the page but cannot
// change anything.
func panelAllowPost(r *http.Request, port int) bool {
	host := "127.0.0.1:" + strconv.Itoa(port)
	if r.Method != http.MethodPost || r.Host != host || r.Header.Get("Origin") != "http://"+host {
		return false
	}
	if r.Header.Get("Sec-Fetch-Site") != "same-origin" || r.Header.Get("Sec-Fetch-Dest") != "empty" {
		return false
	}
	if m := r.Header.Get("Sec-Fetch-Mode"); m != "cors" && m != "same-origin" {
		return false
	}
	if t, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || t != "application/json" {
		return false
	}
	return r.ContentLength >= 0 && r.ContentLength <= panelMaxBody && r.Header.Get(panelFormHeader) != ""
}

// panelReadAction reads the only body a change request may carry,
// {"action": "..."}: one object with exactly that key, spelled exactly so
// (JSON field matching in Go ignores case), once, and nothing after it.
// panelChangeRequest is a prepare or confirm request's body. Lanes and
// Immediately are a stop's options, and are taken only on a confirmation.
type panelChangeRequest struct {
	Action      string `json:"action"`
	Lanes       bool   `json:"lanes,omitempty"`
	Immediately bool   `json:"immediately,omitempty"`
	DropQueued  bool   `json:"dropQueued,omitempty"`
}

func panelReadAction(body io.Reader) (panelChangeRequest, bool) {
	var req panelChangeRequest
	d := json.NewDecoder(body)
	if t, err := d.Token(); err != nil || t != json.Delim('{') {
		return req, false
	}
	seen := map[string]bool{}
	for d.More() {
		t, err := d.Token()
		key, ok := t.(string)
		// Each key once, spelled exactly: no other key, case or repeat.
		if err != nil || !ok || seen[key] {
			return req, false
		}
		seen[key] = true
		v, err := d.Token()
		if err != nil {
			return req, false
		}
		switch key {
		case "action":
			s, ok := v.(string)
			if !ok {
				return req, false
			}
			req.Action = s
		case "lanes", "immediately", "dropQueued":
			b, ok := v.(bool)
			if !ok {
				return req, false
			}
			switch key {
			case "lanes":
				req.Lanes = b
			case "immediately":
				req.Immediately = b
			default:
				req.DropQueued = b
			}
		default:
			return req, false
		}
	}
	if t, err := d.Token(); err != nil || t != json.Delim('}') || !seen["action"] {
		return req, false
	}
	if _, err := d.Token(); err != io.EOF {
		return req, false
	}
	if (req.Action != "stop" && (req.Lanes || req.Immediately || req.DropQueued)) || (req.DropQueued && !req.Lanes) {
		return req, false
	}
	return req, panelActions[req.Action] != "" || panelBuildActions[req.Action] != ""
}

// panelPreview is what the page shows before the person confirms. Every
// sentence is built here from a fresh read.
type panelPreview struct {
	Action    string     `json:"action"`
	Key       string     `json:"key"`
	Title     string     `json:"title"`   // "Pause payments-reconciler?"
	Lines     []dockText `json:"lines"`   // what is recorded now and what the change does
	Confirm   string     `json:"confirm"` // the confirm button: "Pause payments-reconciler"
	Form      string     `json:"form"`    // the single-use value for the confirmation
	ExpiresAt time.Time  `json:"expiresAt"`
	// Lost is what the page says if the reply to the confirmation never
	// arrives: whether the change was saved is then unknown. Refused is what
	// it says if the panel answers the confirmation with a refusal (a 4xx),
	// which it gives before doing anything.
	Lost    string `json:"lost"`
	Refused string `json:"refused"`
	// Stop is a stop's options, when the CLI can stop the lanes and stop at
	// once: one button per option, in these words.
	Stop *stopChoices `json:"stop,omitempty"`
}

// panelResult is the outcome of a change, or of a prepare that was refused.
// Outcome is done, refused (nothing was changed), review_again (nothing was
// changed; prepare and review again) or uncertain (whether it was saved is
// not known: look at the loop before anything else).
type panelResult struct {
	Outcome string `json:"outcome"`
	Text    string `json:"text"`
	// Retry: the page may offer to prepare the same change again. It never
	// does so by itself.
	Retry bool `json:"retry,omitempty"`
	// Lines: for a change to every loop, each loop's outcome, the loops that
	// were not changed first. Outcome is then also partial when some were.
	Lines []string `json:"lines,omitempty"`
}

type panelChangeReply struct {
	SchemaVersion int           `json:"schemaVersion"`
	Preview       *panelPreview `json:"preview,omitempty"`
	Result        *panelResult  `json:"result,omitempty"`
}

var panelActions = map[string]string{"pause": "Pause", "stop": "Stop", "resume": "Resume"}
var panelDone = map[string]string{"pause": "paused", "stop": "stopped", "resume": "resumed"}

// serve answers one change request. rest is the path under the session.
func (c *panelChanges) serve(w http.ResponseWriter, r *http.Request, rest, session string) {
	key, step, ok := strings.Cut(strings.TrimPrefix(rest, panelChangesPrefix), "/")
	if !strings.HasPrefix(rest, panelChangesPrefix) || !ok || !(panelKeyPattern.MatchString(key) || key == panelGroupKey) || (step != "prepare" && step != "confirm") {
		http.NotFound(w, r)
		return
	}
	req, ok := panelReadAction(http.MaxBytesReader(w, r.Body, panelMaxBody))
	// A build change is for one loop only, never for every loop at once.
	if !ok || (step == "prepare" && (req.Lanes || req.Immediately || req.DropQueued)) || (key == panelGroupKey && panelBuildActions[req.Action] != "") {
		http.Error(w, "refused", http.StatusBadRequest)
		return
	}
	action := req.Action
	value := r.Header.Get(panelFormHeader)
	var reply panelChangeReply
	if step == "prepare" {
		want := c.prepareValue(session)
		if len(value) != len(want) || subtle.ConstantTimeCompare([]byte(value), []byte(want)) != 1 {
			http.Error(w, "refused", http.StatusForbidden)
			return
		}
		reply = c.prepare(session, key, action)
	} else {
		reply = c.confirm(session, key, action, value, holdOptions{Lanes: req.Lanes, Now: req.Immediately, DropQueued: req.DropQueued})
	}
	reply.SchemaVersion = 1
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(reply)
}

// entry finds the registration a loop key names. The page never sends a
// registration ID, ticket or path; only the key, which the server maps.
func panelEntry(registryDir, key string) (registry.Entry, []registry.Entry, string, bool) {
	dir, err := registryDirectory(registryDir)
	if err != nil {
		return registry.Entry{}, nil, "", false
	}
	f, err := registry.Load(dir)
	if err != nil {
		return registry.Entry{}, nil, dir, false
	}
	for _, e := range f.Entries {
		if dockKey(e.ID) == key {
			return e, f.Entries, dir, true
		}
	}
	return registry.Entry{}, nil, dir, false
}

func (c *panelChanges) prepare(session, key string, action string) panelChangeReply {
	ctx, cancel := context.WithTimeout(c.ctx, panelChangeTimeout)
	defer cancel()
	if key == panelGroupKey {
		return c.prepareGroup(ctx, session, action)
	}
	if panelBuildActions[action] != "" {
		return c.prepareBuild(ctx, session, key, action)
	}
	r, refused := panelReview(ctx, c.registry, c.reader, key, action, c.now().UTC())
	if refused != nil {
		return panelChangeReply{Result: refused}
	}
	form := panelForm{session: session, key: key, action: action, id: r.entry.ID, loop: r.loop, digest: r.digest, expires: r.preview.ExpiresAt, stop: r.preview.Stop}
	value, err := panelRandom(32)
	if err != nil {
		return panelChangeReply{Result: &panelResult{Outcome: "refused", Text: "A review could not be prepared. Nothing was changed."}}
	}
	c.keep(value, form)
	p := r.preview
	p.Form = value
	return panelChangeReply{Preview: &p}
}

// prepareBuild prepares a build change for one loop.
func (c *panelChanges) prepareBuild(ctx context.Context, session, key, action string) panelChangeReply {
	entry, all, dir, ok := panelEntry(c.registry, key)
	if !ok {
		return panelChangeReply{Result: &panelResult{Outcome: "refused", Text: "This loop is no longer registered. Nothing was changed."}}
	}
	ctl, why := panelReadControl(entry.StateDir)
	if why != "" {
		return panelChangeReply{Result: &panelResult{Outcome: "refused", Text: why, Retry: true}}
	}
	now := c.now().UTC()
	loop := panelLoopNow(ctx, c.reader, dir, entry, all, now)
	daemon := c.reader.query(ctx, entry.StateDir, version, false)
	r, refused := c.reader.builds.review(ctx, entry, loop, ctl, action, daemon, now)
	if refused != nil {
		return panelChangeReply{Result: refused}
	}
	value, err := panelRandom(32)
	if err != nil {
		return panelChangeReply{Result: &panelResult{Outcome: "refused", Text: "A review could not be prepared. Nothing was changed."}}
	}
	c.keep(value, panelForm{session: session, key: key, action: action, id: entry.ID, loop: loop.Name, digest: r.digest, expires: r.preview.ExpiresAt})
	p := r.preview
	p.Form = value
	return panelChangeReply{Preview: &p}
}

// confirmBuild makes a confirmed build change, bound to what was reviewed.
func (c *panelChanges) confirmBuild(f panelForm) panelChangeReply {
	ctx, cancel := context.WithTimeout(c.ctx, buildChangeTimeout)
	defer cancel()
	entry, _, _, ok := panelEntry(c.registry, f.key)
	if !ok || entry.ID != f.id {
		return panelChangeReply{Result: &panelResult{Outcome: "refused", Text: "This loop is no longer registered. Nothing was changed."}}
	}
	daemon := c.reader.query(ctx, entry.StateDir, version, false)
	res := c.reader.builds.apply(ctx, entry, f.action, f.digest, daemon, c.now)
	c.reader.forget()
	return panelChangeReply{Result: &res}
}

// prepareGroup prepares a change to every loop: one review, one form.
func (c *panelChanges) prepareGroup(ctx context.Context, session, action string) panelChangeReply {
	g, refused := panelGroupReview(ctx, c.registry, c.reader, action, c.now().UTC())
	if refused != nil {
		return panelChangeReply{Result: refused}
	}
	form := panelForm{session: session, key: panelGroupKey, action: action, expires: g.preview.ExpiresAt, names: map[string]string{}, stop: g.preview.Stop}
	for _, it := range g.items {
		if it.Fits {
			form.group = append(form.group, panelGroupTarget{Key: it.Key, Digest: it.digest, Lanes: it.lanes(), Now: it.now()})
			form.names[it.Key] = it.Name
		}
	}
	value, err := panelRandom(32)
	if err != nil {
		return panelChangeReply{Result: &panelResult{Outcome: "refused", Text: "A review could not be prepared. Nothing was changed."}}
	}
	c.keep(value, form)
	p := g.preview
	p.Form = value
	return panelChangeReply{Preview: &p}
}

// panelReviewed is one review and what its confirmation is bound to.
type panelReviewed struct {
	preview panelPreview
	entry   registry.Entry
	loop    string
	digest  string // the controlDigest of the record reviewed
}

// panelReview reads one loop and words the review of one change. The page's
// prepare and client review (the menu's review) both call it, so the two can
// never say different things. refused is set, in the page's words, when no
// review can be given now.
func panelReview(ctx context.Context, registryDir string, reader *dockReader, key, action string, now time.Time) (panelReviewed, *panelResult) {
	entry, all, dir, ok := panelEntry(registryDir, key)
	if !ok {
		return panelReviewed{}, &panelResult{Outcome: "refused", Text: "This loop is no longer registered. Nothing was changed."}
	}
	// The control record, read once under its lock: every fact the review
	// states about it comes from this one record, and the confirmation is
	// bound to it, so a change made since is refused as "review again".
	ctl, why := panelReadControl(entry.StateDir)
	if why != "" {
		return panelReviewed{}, &panelResult{Outcome: "refused", Text: why, Retry: true}
	}
	// The rest of the loop as it is now, read the way the page reads it.
	loop := panelLoopNow(ctx, reader, dir, entry, all, now)
	ttl := panelHoldFormTTL
	if action == "resume" {
		// A resume's own ticket is prepared when the person confirms, so
		// opening a review never cancels one another client prepared.
		ttl = clientResumeTTL
	}
	expires := now.Add(ttl)
	p := panelPreviewOf(action, loop, ctl, expires, now)
	p.Key, p.ExpiresAt = key, expires
	if action == "stop" {
		if cfg, ok := clientConfig(entry); ok {
			p.Stop, p.Lines = stopChoicesFor(ctx, cfg, p.Lines)
		}
	}
	return panelReviewed{preview: p, entry: entry, loop: loop.Name, digest: controlDigest(ctl)}, nil
}

// keep stores a prepared change, dropping expired ones and, past the cap, the
// oldest.
func (c *panelChanges) keep(value string, f panelForm) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	kept := c.order[:0]
	for _, v := range c.order {
		if g, ok := c.forms[v]; ok && now.Before(g.expires) {
			kept = append(kept, v)
		} else {
			delete(c.forms, v)
		}
	}
	c.order = kept
	for len(c.order) >= panelMaxForms {
		delete(c.forms, c.order[0])
		c.order = c.order[1:]
	}
	c.forms[value] = f
	c.order = append(c.order, value)
}

// take spends a form value: it is removed whether or not it matches.
func (c *panelChanges) take(value string) (panelForm, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var found panelForm
	ok := false
	for v, f := range c.forms {
		if len(v) == len(value) && subtle.ConstantTimeCompare([]byte(v), []byte(value)) == 1 {
			found, ok = f, true
			delete(c.forms, v)
			c.order = slices.DeleteFunc(c.order, func(x string) bool { return x == v })
		}
	}
	return found, ok
}

func (c *panelChanges) confirm(session, key, action, value string, opts holdOptions) panelChangeReply {
	f, ok := c.take(value)
	now := c.now()
	switch {
	case !ok, f.session != session, f.key != key, f.action != action, !f.stop.offers(opts):
		return panelChangeReply{Result: &panelResult{Outcome: "review_again", Text: "This review was already used, or is not this page's. Nothing was changed. Review it again.", Retry: true}}
	case !now.Before(f.expires):
		return panelChangeReply{Result: &panelResult{Outcome: "review_again", Text: "This review expired. Nothing was changed. Review it again.", Retry: true}}
	}
	if panelBuildActions[action] != "" {
		return c.confirmBuild(f)
	}
	ctx, cancel := context.WithTimeout(c.ctx, panelChangeTimeout)
	defer cancel()
	if key == panelGroupKey {
		g := panelGroupApply(ctx, c.registry, action, panelChangeOrigin, "", f.group, f.names, opts.Lanes)
		c.reader.forget()
		return panelChangeReply{Result: &panelResult{Outcome: g.Outcome, Text: g.Text, Retry: g.Retry, Lines: g.Lines}}
	}
	// The page acts as no BB thread, as a terminal outside BB does.
	r := panelApply(ctx, c.registry, f.id, action, panelChangeOrigin, "", f.digest, opts)
	// The next read shows the change, not a cached picture from before it.
	c.reader.forget()
	res := panelOutcome(action, r)
	return panelChangeReply{Result: &res}
}

// panelApply makes one reviewed change. Each save is refused, inside the
// control lock, if the control record is not the one reviewed (expect). A
// resume is refused, as client resume refuses it, when caller (a BB thread,
// or "" for none) may not resume a loop another thread owns. Pause and stop
// never check the owner. client apply calls it too, with no origin.
func panelApply(ctx context.Context, registryDir, id, action, origin, caller, expect string, opts holdOptions) clientHoldReport {
	if action == "resume" {
		// The same preparation and resume as client prepare-resume and
		// resume, with the expect check before the ticket is prepared.
		prepared, _ := clientResume(ctx, registryDir, id, "prepare-resume", "", false, caller, origin, expect, time.Now, nil)
		if prepared.Ticket == "" {
			return prepared.clientHoldReport
		}
		done, _ := clientResume(ctx, registryDir, id, "resume", prepared.Ticket, false, caller, origin, expect, time.Now, nil)
		return done.clientHoldReport
	}
	if opts.Now {
		// The thread a stop at once runs from: the BB thread of client apply,
		// none for the page.
		opts.CallerThread = caller
	}
	r, _ := applyClientHoldWith(ctx, registryDir, id, action, origin, expect, opts)
	return r
}

// panelReadControl reads a loop's control record under its lock. why is
// set, in the page's words, when it cannot be read now.
func panelReadControl(stateDir string) (control.File, string) {
	var f control.File
	lk, err := lock.AcquireNamed(stateDir, lock.Control)
	if err != nil {
		if errors.Is(err, lock.ErrHeld) {
			return f, "keepgoing is in the middle of a check on this loop. Nothing was changed."
		}
		return f, "Its control record could not be read. Nothing was changed."
	}
	defer lk.Release()
	if readTransferRecord(stateDir, control.FileName, &f) != nil {
		return f, "Its control record could not be read. Nothing was changed."
	}
	return f, ""
}

// panelLoop is what a preview needs of the loop: its projection.
type panelLoop struct {
	dockLoop
}

func panelLoopNow(ctx context.Context, reader *dockReader, dir string, entry registry.Entry, all []registry.Entry, now time.Time) panelLoop {
	validated := map[string]*config.Config{}
	if cfg, ok := clientConfig(entry); ok {
		validated[entry.ID] = cfg
	}
	in := reader.readLoop(ctx, dir, entry, all, validated, now)
	l, _ := projectDockLoop(in, now, reader.staleAfter, false)
	return panelLoop{dockLoop: l}
}

// panelPreviewOf words what is recorded now and what the change does. It
// claims nothing the records do not show: a saved hold is not the provider
// acting, and a resume does not turn supervision on or start a turn. What a
// resume clears comes from clearForResume, the function the resume itself
// saves with, run on a copy of the record reviewed.
func panelPreviewOf(action string, l panelLoop, ctl control.File, expires, now time.Time) panelPreview {
	p := panelPreview{Action: action, Title: panelActions[action] + " " + l.Name + "?", Confirm: panelActions[action] + " " + l.Name, Lines: []dockText{},
		Lost:    "keepgoing could not confirm whether " + l.Name + " was " + panelDone[action] + ". Check its state before doing anything else.",
		Refused: "The panel refused this request before doing anything. Nothing was changed."}
	add := func(a ...any) { p.Lines = append(p.Lines, txt(a...)) }
	owned := ctl.Ownership != nil
	add("Now: " + l.Phrase + ". Last check: " + strings.ToLower(l.LastCheck.Text[:1]) + l.LastCheck.Text[1:] + ".")
	switch action {
	case "pause":
		add("keepgoing sends it nothing until it is resumed. A check already running finishes. Its route, note, other holds and ownership do not change.")
	case "stop":
		add("The loop's next check carries out the stop; this review cannot confirm that the session stopped. Nothing is sent until it is resumed.")
		add("This stops the conversation only: its lanes, if it has any, keep running.")
		if owned {
			add("This loop is supervised for a conversation. Stopping here holds it for that conversation too; it does not tell the conversation why.")
		}
	case "resume":
		after := ctl
		e := clearForResume(&after)
		holds := []string{}
		for _, x := range []struct {
			on   bool
			word string
		}{{e.Paused, "paused"}, {e.Stop, "stop requested"}, {e.AwaitingUser, "waiting for you"}, {e.Completed, "finished"}, {e.Note, "the note left with the hold"}} {
			if x.on {
				holds = append(holds, x.word)
			}
		}
		if len(holds) == 0 {
			add("No hold is recorded, so resume clears none.")
		} else {
			add("Resume clears: " + panelJoin(holds) + ".")
		}
		if e.OpenQuestion {
			add("It also removes the record of the question that was open for a person when the loop was marked finished.")
		}
		if e.NewEpisode {
			add("The hold was raised because re-prompts or nudges were spent, so resume starts a new error episode: those counts start over. Loop-wide limits are unchanged.")
		}
		add("Resume does not turn supervision on or start a turn; the next check decides what to send.")
		if !ctl.Enabled {
			add("Supervision is off for this loop, and resume does not turn it on.")
		}
		if owned {
			add("This loop is supervised for a conversation. Resuming here clears the holds that conversation or keepgoing set; it does not answer any question the conversation asked.")
		}
		if l.CurrentRoute.Availability == "known" {
			for _, entry := range l.Chain.Entries {
				if entry.Current {
					route := entry.Title
					if entry.TitleAccount != "" {
						route += " on " + entry.TitleAccount
					}
					add("Its current route: " + route + ".")
				}
			}
		}
		if n := l.NextCheck; n.Availability == "known" {
			add("Next check: ", n.Text, ".")
		} else if n.Detail != "" {
			add("Next check: unknown; " + n.Detail + ".")
		}
		add("Confirming prepares this resume's own ticket, so a resume another client prepared and has not used yet can no longer be used.")
	}
	add("If the loop's control record changes before you confirm, nothing is changed and you are asked to review again.")
	add("This review expires ", dockUntil(now, expires), ".")
	return p
}

func panelJoin(words []string) string {
	if len(words) < 2 {
		return strings.Join(words, "")
	}
	return strings.Join(words[:len(words)-1], ", ") + " and " + words[len(words)-1]
}

// panelOutcome words a change's result from its save evidence alone. A saved
// hold is not the provider acting; a lost or unconfirmed save is uncertain and
// is never retried.
func panelOutcome(action string, r clientHoldReport) panelResult {
	has := func(code string) bool { return slices.Contains(r.Issues, code) }
	switch {
	case r.ControlSave == "confirmed" && action == "stop" && r.CarriedOut == "now":
		text := "Stopped now, as a check would stop it."
		if r.Lanes != nil && r.Lanes.Result != nil {
			text += " " + strings.ToUpper(describeLaneStop(*r.Lanes.Result)[:1]) + describeLaneStop(*r.Lanes.Result)[1:] + "."
		}
		if has("incident_history_unavailable") || has("run_history_unavailable") {
			text += " Its history could not record this change."
		}
		return panelResult{Outcome: "done", Text: text}
	case r.ControlSave == "confirmed" && action == "stop" && r.LeftBecause != "":
		text := "Stop requested: " + map[string]string{
			"check_running":   "a check of this loop was running, so that check or the next one carries it out.",
			"session_claimed": "another loop's check is acting on this session, so the next check carries it out.",
			"bb_not_found":    "bb could not be found from here, so the next check carries it out.",
		}[r.LeftBecause]
		if strings.HasSuffix(text, ": ") {
			text = "Stop requested, but it could not be carried out now: the next check carries it out."
		}
		if r.Lanes != nil {
			text += " That check stops the lanes too."
		}
		return panelResult{Outcome: "done", Text: text}
	case r.ControlSave == "confirmed":
		text := map[string]string{
			"pause":  "Paused: keepgoing will send nothing; a check already running finishes.",
			"stop":   "Stop requested: the next check carries it out.",
			"resume": "Resumed: holds cleared; the next check decides what to send.",
		}[action]
		if has("incident_history_unavailable") || has("run_history_unavailable") {
			text += " Its history could not record this change."
		}
		return panelResult{Outcome: "done", Text: text}
	case r.ControlSave == "unconfirmed", has("ticket_consumption_unconfirmed"), has("interrupted_after_consumption"):
		return panelResult{Outcome: "uncertain", Text: "keepgoing could not confirm whether this loop was " + panelDone[action] + ". Check its state before doing anything else."}
	case has("busy"):
		return panelResult{Outcome: "refused", Text: "keepgoing is in the middle of a check on this loop. Nothing was changed.", Retry: true}
	case has("observation_changed"), has("ticket_unavailable_or_expired"):
		return panelResult{Outcome: "review_again", Text: "The loop changed since you reviewed it, or the review expired. Nothing was changed. Review it again.", Retry: true}
	}
	why := "It could not be done."
	if len(r.Issues) > 0 {
		// The first issue is the reason; later ones are diagnostics.
		switch code := r.Issues[0]; code {
		case "resume_evidence_unavailable":
			why = "Its records could not be read and checked the way a resume requires."
		case "ticket_unavailable":
			why = "A review could not be prepared."
		case "cancelled":
			why = "The request was cut off before anything was saved."
		case "control_or_registration_unavailable":
			why = "Its control record or registration could not be read."
		default:
			if s, ok := dockIssues[code]; ok {
				why = s
			}
		}
	}
	return panelResult{Outcome: "refused", Text: why + " Nothing was changed."}
}

// forget drops the cached report, so the read after a change shows it.
func (d *dockReader) forget() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cache = nil
}
