package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
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
	session, key, action, id, loop, ticket string
	expires                                time.Time
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
// reads: a loopback Host, an Origin that is exactly this page's, fetch
// metadata that says the page itself sent it, a JSON body of bounded size and
// the anti-forgery header. A browser that sends no fetch metadata can read the
// page but cannot change anything.
func panelAllowPost(r *http.Request, guard dockGuard) bool {
	host := strings.ToLower(r.Host)
	if r.Method != http.MethodPost || !guard.hosts[host] || strings.ToLower(r.Header.Get("Origin")) != "http://"+host {
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

// panelChangeRequest is the only body a change request may carry.
type panelChangeRequest struct {
	Action string `json:"action"`
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
	// arrives: whether the change was saved is then unknown.
	Lost string `json:"lost"`
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
	if !strings.HasPrefix(rest, panelChangesPrefix) || !ok || !panelKeyPattern.MatchString(key) || (step != "prepare" && step != "confirm") {
		http.NotFound(w, r)
		return
	}
	var req panelChangeRequest
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, panelMaxBody))
	d.DisallowUnknownFields()
	if d.Decode(&req) != nil || d.Decode(new(any)) != io.EOF || panelActions[req.Action] == "" {
		http.Error(w, "refused", http.StatusBadRequest)
		return
	}
	value := r.Header.Get(panelFormHeader)
	var reply panelChangeReply
	if step == "prepare" {
		want := c.prepareValue(session)
		if len(value) != len(want) || subtle.ConstantTimeCompare([]byte(value), []byte(want)) != 1 {
			http.Error(w, "refused", http.StatusForbidden)
			return
		}
		reply = c.prepare(session, key, req.Action)
	} else {
		reply = c.confirm(session, key, req.Action, value)
	}
	reply.SchemaVersion = 1
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(reply)
}

// entry finds the registration a loop key names. The page never sends a
// registration ID, ticket or path; only the key, which the server maps.
func (c *panelChanges) entry(key string) (registry.Entry, []registry.Entry, string, bool) {
	dir, err := registryDirectory(c.registry)
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

func (c *panelChanges) prepare(session, key, action string) panelChangeReply {
	entry, all, dir, ok := c.entry(key)
	if !ok {
		return panelChangeReply{Result: &panelResult{Outcome: "refused", Text: "This loop is no longer registered. Nothing was changed."}}
	}
	ctx, cancel := context.WithTimeout(c.ctx, panelChangeTimeout)
	defer cancel()
	now := c.now().UTC()
	// The loop as it is now, read the way the page reads it.
	loop := c.loopNow(ctx, dir, entry, all, now)
	var ctl control.File
	owned, note := false, false
	if readTransferRecord(entry.StateDir, control.FileName, &ctl) == nil {
		owned, note = ctl.Ownership != nil, strings.TrimSpace(ctl.Note) != ""
	}
	form := panelForm{session: session, key: key, action: action, id: entry.ID, loop: loop.Name, expires: now.Add(panelHoldFormTTL)}
	controls := loop.controls
	if action == "resume" {
		// The same preparation as client prepare-resume, as no BB thread.
		r, _ := clientResume(ctx, c.registry, entry.ID, "prepare-resume", "", false, "", panelChangeOrigin, time.Now, nil)
		if r.Ticket == "" || r.ExpiresAt == nil {
			res := panelOutcome(action, r.clientHoldReport)
			return panelChangeReply{Result: &res}
		}
		form.ticket, form.expires = r.Ticket, *r.ExpiresAt
		if r.Snapshot != nil && r.Snapshot.Controls != nil {
			controls = r.Snapshot.Controls
		}
	}
	value, err := panelRandom(32)
	if err != nil {
		return panelChangeReply{Result: &panelResult{Outcome: "refused", Text: "A review could not be prepared. Nothing was changed."}}
	}
	c.keep(value, form)
	p := panelPreviewOf(action, loop, controls, owned, note, form.expires, now)
	p.Key, p.Form, p.ExpiresAt = key, value, form.expires
	return panelChangeReply{Preview: &p}
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

func (c *panelChanges) confirm(session, key, action, value string) panelChangeReply {
	f, ok := c.take(value)
	now := c.now()
	switch {
	case !ok, f.session != session, f.key != key, f.action != action:
		return panelChangeReply{Result: &panelResult{Outcome: "review_again", Text: "This review was already used, or is not this page's. Nothing was changed. Review it again.", Retry: true}}
	case !now.Before(f.expires):
		return panelChangeReply{Result: &panelResult{Outcome: "review_again", Text: "This review expired. Nothing was changed. Review it again.", Retry: true}}
	}
	ctx, cancel := context.WithTimeout(c.ctx, panelChangeTimeout)
	defer cancel()
	var r clientHoldReport
	if action == "resume" {
		rr, _ := clientResume(ctx, c.registry, f.id, "resume", f.ticket, false, "", panelChangeOrigin, time.Now, nil)
		r = rr.clientHoldReport
	} else {
		r, _ = applyClientHoldFrom(ctx, c.registry, f.id, action, panelChangeOrigin)
	}
	// The next read shows the change, not a cached picture from before it.
	c.reader.forget()
	res := panelOutcome(action, r)
	return panelChangeReply{Result: &res}
}

// panelLoop is what a preview needs of the loop: its projection and its
// controls as just read.
type panelLoop struct {
	dockLoop
	controls *clientControls
}

func (c *panelChanges) loopNow(ctx context.Context, dir string, entry registry.Entry, all []registry.Entry, now time.Time) panelLoop {
	validated := map[string]*config.Config{}
	if cfg, ok := clientConfig(entry); ok {
		validated[entry.ID] = cfg
	}
	in := c.reader.readLoop(ctx, dir, entry, all, validated, now)
	l, _ := projectDockLoop(in, now, c.reader.staleAfter, false)
	p := panelLoop{dockLoop: l}
	if s := in.Client.Snapshot; s != nil {
		p.controls = s.Controls
	}
	return p
}

// panelPreviewOf words what is recorded now and what the change does. It
// claims nothing the records do not show: a saved hold is not the provider
// acting, and a resume does not turn supervision on or start a turn.
func panelPreviewOf(action string, l panelLoop, controls *clientControls, owned, note bool, expires, now time.Time) panelPreview {
	p := panelPreview{Action: action, Title: panelActions[action] + " " + l.Name + "?", Confirm: panelActions[action] + " " + l.Name, Lines: []dockText{},
		Lost: "keepgoing could not confirm whether " + l.Name + " was " + panelDone[action] + ". Check its state below before doing anything else."}
	add := func(a ...any) { p.Lines = append(p.Lines, txt(a...)) }
	add("Now: " + l.Phrase + ". Last check: " + strings.ToLower(l.LastCheck.Text[:1]) + l.LastCheck.Text[1:] + ".")
	switch action {
	case "pause":
		add("keepgoing sends it nothing until it is resumed. A check already running finishes. Its route, note, other holds and ownership do not change.")
	case "stop":
		add("The loop's next check carries out the stop; this page cannot confirm that the session stopped. Nothing is sent until it is resumed.")
		if owned {
			add("This loop is supervised for a conversation. Stopping here holds it for that conversation too; it does not tell the conversation why.")
		}
	case "resume":
		holds := []string{}
		if controls != nil {
			for _, x := range []struct {
				on   bool
				word string
			}{{controls.Paused, "paused"}, {controls.Stop, "stop requested"}, {controls.AwaitingUser, "waiting for you"}, {controls.Completed, "finished"}} {
				if x.on {
					holds = append(holds, x.word)
				}
			}
		}
		if note {
			holds = append(holds, "the note left with the hold")
		}
		switch {
		case controls == nil:
			add("Resume clears every hold: paused, stop requested, waiting for you, finished, and any note left with a hold.")
		case len(holds) == 0:
			add("No hold is recorded, so resume clears nothing.")
		default:
			add("Resume clears: " + panelJoin(holds) + ".")
		}
		add("Resume does not turn supervision on or start a turn; the next check decides what to send.")
		if controls != nil && !controls.Enabled {
			add("Supervision is off for this loop, and resume does not turn it on.")
		}
		if owned {
			add("This loop is supervised for a conversation. Resuming here clears the holds that conversation or keepgoing set; it does not answer any question the conversation asked.")
		}
		if l.CurrentRoute.Availability == "known" {
			for _, e := range l.Chain.Entries {
				if e.Current {
					route := e.Title
					if e.TitleAccount != "" {
						route += " on " + e.TitleAccount
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
	}
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
		return panelResult{Outcome: "uncertain", Text: "keepgoing could not confirm whether this loop was " + panelDone[action] + ". Check its state below before doing anything else."}
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
