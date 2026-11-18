package main

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/looptender/looptender/internal/accounts"
	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/lock"
	"github.com/looptender/looptender/internal/registry"
)

const panelRoutesPrefix = "api/v1/routes/"

// Route forms use the panel's session, anti-forgery guard, bounded expiring
// reviews and immutable startup caller. They call the CLI's in-process
// planners/writers, never a shell or a command assembled by the browser.
type panelRouteInput struct {
	Op              string   `json:"op"`
	ID              string   `json:"id,omitempty"`
	To              int      `json:"to,omitempty"`
	Account         string   `json:"account,omitempty"`
	Model           string   `json:"model,omitempty"`
	Effort          string   `json:"effort,omitempty"`
	Limit           string   `json:"limit,omitempty"`
	Behavior        string   `json:"behavior,omitempty"`
	NewConversation bool     `json:"newConversation,omitempty"`
	Loops           []string `json:"loops,omitempty"`
	Provider        string   `json:"provider,omitempty"`
}
type panelRouteAccount struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Provider  string `json:"provider"`
	Selection string `json:"selection"`
}
type panelRouteLoop struct {
	Key  string         `json:"key"`
	View loopRoutesView `json:"view"`
}
type panelRouteEditor struct {
	AccountLabels map[string]string   `json:"accountLabels"`
	Standard      standardListView    `json:"standard"`
	Accounts      []panelRouteAccount `json:"accounts"`
	Loops         []panelRouteLoop    `json:"loops"`
	Reason        string              `json:"reason,omitempty"`
}
type panelRouteReview struct {
	lines    []string
	requests []routeChangeRequest
	keys     []string
	account  *accounts.Entry
}
type panelRouteReply struct {
	Lines         []string          `json:"lines,omitempty"`
	SchemaVersion int               `json:"schemaVersion"`
	Editor        *panelRouteEditor `json:"editor,omitempty"`
	Changes       []routeChangeOut  `json:"changes,omitempty"`
	Models        []bbModel         `json:"models,omitempty"`
	Form          string            `json:"form,omitempty"`
	Text          string            `json:"text,omitempty"`
	Outcome       string            `json:"outcome,omitempty"`
}

func (c *panelChanges) routeAccountDirectory() (string, error) {
	return accountsDirectory(c.routeCatalog)
}

func (c *panelChanges) routeAccounts() ([]accounts.Entry, error) {
	dir, err := c.routeAccountDirectory()
	if err != nil {
		return nil, err
	}
	f, err := accounts.Load(dir)
	return f.Entries, err
}
func (c *panelChanges) routeEditor() panelRouteEditor {
	out := panelRouteEditor{AccountLabels: map[string]string{}, Standard: standardList(), Accounts: []panelRouteAccount{}, Loops: []panelRouteLoop{}}
	if c.caller != "" {
		out.Standard.Can = []string{}
		out.Reason = "This panel was started by a BB thread. Only a person can change the standard route list. Open an owner panel outside BB; reopening this window does not change its caller."
	}
	if all, err := c.routeAccounts(); err == nil {
		for _, a := range all {
			if a.Provider == "codex" || a.Provider == "claude-code" {
				out.Accounts = append(out.Accounts, panelRouteAccount{a.ID, a.Label, a.Provider, a.Mode})
			}
		}
	}

	// These labels come only from an explicitly stated catalog ID, never
	// provider/model/directory resemblance. An absent association stays unknown.
	if path, err := config.StandardRoutesPath(); err == nil {
		if routes, err := config.LoadRoutes(path); err == nil {
			for _, rt := range routes.Routes {
				for _, a := range out.Accounts {
					if rt.Account == a.ID && rt.Provider == a.Provider {
						out.AccountLabels[rt.ID] = a.Label
					}
				}
			}
		}
	}
	dir, err := registryDirectory(c.registry)
	if err != nil {
		out.Reason = "The loop registry cannot be read. Nothing can be edited here."
		out.Standard.Can = []string{}
		return out
	}
	f, err := registry.Load(dir)
	if err != nil {
		out.Reason = "The loop registry cannot be read. Nothing can be edited here."
		out.Standard.Can = []string{}
		return out
	}
	for _, e := range f.Entries {
		out.Loops = append(out.Loops, panelRouteLoop{dockKey(e.ID), loopRoutes(routeLoopRef{e.ConfigPath, e.Loop}, c.now())})
	}
	return out
}

// Strict decoding rejects duplicate/case-folded/unknown fields as well as
// values of the wrong type. IDs and paths are never accepted as authority.
func panelReadRoute(body io.Reader) (panelRouteInput, bool) {
	var in panelRouteInput
	raw, err := io.ReadAll(body)
	if err != nil {
		return in, false
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	if t, err := d.Token(); err != nil || t != json.Delim('{') {
		return in, false
	}
	allowed := []string{"op", "id", "to", "account", "model", "effort", "limit", "behavior", "newConversation", "loops", "provider"}
	seen := map[string]bool{}
	for d.More() {
		t, err := d.Token()
		key, ok := t.(string)
		if err != nil || !ok || seen[key] || !slices.Contains(allowed, key) {
			return in, false
		}
		seen[key] = true
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return in, false
		}
	}
	if t, err := d.Token(); err != nil || t != json.Delim('}') {
		return in, false
	}
	if _, err := d.Token(); err != io.EOF {
		return in, false
	}
	if json.Unmarshal(raw, &in) != nil || !seen["op"] {
		return in, false
	}
	return in, true
}

func (c *panelChanges) serveRoutes(w http.ResponseWriter, r *http.Request, rest, session string) {
	step := strings.TrimPrefix(rest, panelRoutesPrefix)
	if !slices.Contains([]string{"read", "models", "prepare", "confirm"}, step) {
		http.NotFound(w, r)
		return
	}
	in, ok := panelReadRoute(http.MaxBytesReader(w, r.Body, panelMaxBody))
	if !ok {
		http.Error(w, "refused", http.StatusBadRequest)
		return
	}
	value := r.Header.Get(panelFormHeader)
	if step != "confirm" {
		want := c.prepareValue(session)
		if subtle.ConstantTimeCompare([]byte(value), []byte(want)) != 1 {
			http.Error(w, "refused", http.StatusForbidden)
			return
		}
	}
	reply := panelRouteReply{SchemaVersion: 1}
	switch step {
	case "read":
		if in.Op != "read" {
			http.Error(w, "refused", http.StatusBadRequest)
			return
		}
		v := c.routeEditor()
		reply.Editor = &v
	case "models":
		if in.Op != "models" || (in.Provider != "codex" && in.Provider != "claude-code") {
			http.Error(w, "refused", http.StatusBadRequest)
			return
		}
		// Explicit read-only listing, only after a person presses Load models.
		// Fixed executable/arguments use the existing bounded structured reader.
		models, err := bbModels(c.ctx, runBBLookup, "bb", in.Provider)
		if err != nil {
			reply.Text = "The model list is unavailable. Enter a model and effort from your provider's supported settings; access has not been checked."
		} else {
			if len(models) > 128 {
				models = models[:128]
			}
			for i := range models {
				models[i].DisplayName = bbLabel(models[i].DisplayName, 100)
				levels := models[i].Efforts[:0]
				for _, e := range models[i].Efforts {
					if len(levels) < 16 && len(e.Level) <= 64 && fallbackAddModelName("effort", e.Level) == nil {
						levels = append(levels, e)
					}
				}
				models[i].Efforts = levels
			}
			reply.Models = models
			reply.Text = "Models reported by BB. This does not check this account's model access or quota."
		}
	case "prepare":
		reply = c.prepareRoutes(session, in)
	case "confirm":
		reply = c.confirmRoutes(session, value, in.Op)
	}
	reply.SchemaVersion = 1
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(reply)
}

func panelRouteFailure(text string) panelRouteReply {
	return panelRouteReply{Outcome: "refused", Text: text}
}
func (c *panelChanges) prepareRoutes(session string, in panelRouteInput) panelRouteReply {
	review := &panelRouteReview{}
	base := routeChangeRequest{op: in.Op, id: in.ID, to: in.To, caller: c.caller, source: "panel", registry: c.registry, now: c.now().UTC()}
	switch in.Op {
	case "add":
		if c.caller != "" {
			return panelRouteFailure(agentIdentityRefusal().Text)
		}
		all, err := c.routeAccounts()
		if err != nil {
			return panelRouteFailure("The selected account catalog cannot be read. Nothing was changed.")
		}
		for _, a := range all {
			if a.ID == in.Account {
				copy := a
				review.account = &copy
				break
			}
		}
		a := review.account
		if a == nil || (a.Provider != "claude-code" && a.Provider != "codex") {
			return panelRouteFailure("Choose an existing Claude Code or Codex account selection.")
		}
		if in.Model == "" || len(in.Model) > 128 || fallbackAddModelName("model", in.Model) != nil || (in.Effort != "" && fallbackAddModelName("effort", in.Effort) != nil) {
			return panelRouteFailure("Enter a plain model name and, if used, a plain effort name.")
		}
		if in.Limit == "" || len(in.Limit) > 128 || fallbackAddGroup(&config.Config{}, in.Limit) != nil {
			return panelRouteFailure("Choose a limit relationship and a label using letters, digits, dots, underscores or hyphens.")
		}
		if !slices.Contains([]string{config.EnterSameConversation, config.EnterNewConversation, config.EnterHeadless}, in.Behavior) || (in.Behavior == config.EnterNewConversation && !in.NewConversation) {
			return panelRouteFailure("Choose how the route runs. Starting a conversation requires explicit permission.")
		}
		if in.Behavior == config.EnterHeadless && a.Provider == "codex" && in.Effort != "" {
			return panelRouteFailure("Codex CLI routes have no effort override. Leave effort empty.")
		}
		if in.Behavior != config.EnterHeadless && a.Mode != "externally_managed" {
			return panelRouteFailure("Conversation routes require an externally managed account selection. A catalog selection does not change the account used by a BB thread.")
		}
		if in.Behavior == config.EnterHeadless && a.Mode == "externally_managed" {
			return panelRouteFailure("Choose an inherited or existing-directory account selection for a headless route.")
		}
		id, err := panelRandom(12)
		if err != nil {
			return panelRouteFailure("A route could not be prepared. Nothing was changed.")
		}
		base.id = "route-" + strings.ToLower(id)
		// panelRandom is hexadecimal; IDs are opaque and never derived identities.
		base.route = config.Route{ID: base.id, Provider: a.Provider, Model: in.Model, Effort: in.Effort, Group: in.Limit, Account: a.ID, Enter: in.Behavior}
		if in.Behavior == config.EnterHeadless && a.Mode == "explicit_directory" {
			base.route.AccountDir = a.Directory
		}
		review.lines = []string{"Account selection you state: " + a.Label + " (" + a.Provider + "). This does not verify a live thread's login.", "Model: " + in.Model + ". Effort: " + in.Effort + " (empty uses provider default).", "Recorded limit label: " + in.Limit + ". Sharing or independence is your configuration statement, not verified quota.", "Conversation behavior: " + map[string]string{config.EnterSameConversation: "continue the loop's existing BB conversation", config.EnterNewConversation: "may create a BB conversation at handoff, only for loops already opted in", config.EnterHeadless: "run the loop's CLI with inherited login or the selected existing directory"}[in.Behavior] + "."}
		review.requests = []routeChangeRequest{base}
	case "move", "remove", "off", "on":
		review.requests = []routeChangeRequest{base}
	case "follow_standard":
		if len(in.Loops) == 0 || len(in.Loops) > 64 {
			return panelRouteFailure("Select between one and 64 loops per review.")
		}
		seen := map[string]bool{}
		for _, key := range in.Loops {
			if seen[key] {
				return panelRouteFailure("A loop was selected twice.")
			}
			seen[key] = true
			e, _, _, ok := panelEntry(c.registry, key)
			if !ok {
				return panelRouteFailure("A selected loop is no longer registered. Read the list again.")
			}
			req := base
			req.loop = e.Loop
			review.requests = append(review.requests, req)
			review.keys = append(review.keys, key)
		}
	default:
		return panelRouteFailure("This route action is not supported.")
	}
	reply := panelRouteReply{Outcome: "preview", Changes: []routeChangeOut{}}
	for i, req := range review.requests {
		out, _ := panelRouteChange(req, false)
		if out.Outcome != "preview" && out.Outcome != "unchanged" {
			return panelRouteReply{Outcome: out.Outcome, Changes: []routeChangeOut{panelSafeRouteOut(out)}, Text: "The change could not be prepared. Nothing was changed."}
		}
		review.requests[i].expect = out.Expect
		reply.Changes = append(reply.Changes, panelSafeRouteOut(out))
	}
	value, err := panelRandom(32)
	if err != nil {
		return panelRouteFailure("A review could not be created. Nothing was changed.")
	}
	c.keep(value, panelForm{session: session, key: "routes", action: in.Op, expires: c.now().Add(panelHoldFormTTL), route: review})
	reply.Lines = review.lines
	reply.Form = value
	reply.Text = "Review before saving. Holds, budgets, ownership and conversation-creation limits stay in place. This does not run a model or check account access."
	if in.Op == "follow_standard" {
		reply.Text += " Each loop saves separately. A refusal stops the remaining saves; earlier successful saves remain. Handoff settings and permission to create conversations do not change."
	}
	return reply
}
func panelRouteChange(req routeChangeRequest, apply bool) (routeChangeOut, int) {
	if req.op == "follow_standard" {
		return routeSourceChange(req, "", "", apply)
	}
	return routeChange(req, apply)
}

// Refusal details from CLI planners can name private paths. The browser gets
// the stable code and safe recovery text, never filesystem error strings.
func panelSafeRouteOut(out routeChangeOut) routeChangeOut {
	if out.Refusal != nil {
		out.Refusal = &routeRefusal{Code: out.Refusal.Code, Text: "The route change was refused (" + out.Refusal.Code + "). Review the current list and account selection before trying again."}
	}
	return out
}
func (c *panelChanges) confirmRoutes(session, value, op string) panelRouteReply {
	f, ok := c.take(value)
	if !ok || f.session != session || f.key != "routes" || f.action != op || f.route == nil || !c.now().Before(f.expires) {
		return panelRouteReply{Outcome: "review_again", Text: "This review expired, was already used, or belongs to another page. Review again; nothing was changed."}
	}
	review := f.route
	// Bind the account selection to the review, including its exact revision.
	// Hold the catalog writer lock until the route writer finishes.
	if a := review.account; a != nil {
		dir, err := c.routeAccountDirectory()
		if err != nil {
			return panelRouteFailure("The catalog is unavailable; nothing was changed.")
		}
		held, err := lock.AcquireNamed(dir, "accounts")
		if err != nil {
			return panelRouteFailure("The catalog is changing; review again. Nothing was changed.")
		}
		defer held.Release()
		all, err := c.routeAccounts()
		found := false
		if err == nil {
			for _, current := range all {
				if current.ID == a.ID && current.Revision == a.Revision && current.Context == a.Context {
					found = true
				}
			}
		}
		if !found {
			return panelRouteReply{Outcome: "review_again", Text: "The selected account changed since review. Nothing was changed. Review again."}
		}
	}
	// Preflight all selected loops before any writes. This reduces partial
	// completion but is not atomic: each core writer rechecks under its lock.
	for i, req := range review.requests {
		if req.op == "follow_standard" {
			e, _, _, exists := panelEntry(c.registry, review.keys[i])
			if !exists || e.Loop != req.loop {
				return panelRouteReply{Outcome: "review_again", Text: "A selected registration changed. Nothing was changed. Review again."}
			}
		}
		out, _ := panelRouteChange(req, false)
		if (out.Outcome != "preview" && out.Outcome != "unchanged") || out.Expect != req.expect {
			return panelRouteReply{Outcome: "review_again", Text: "The route list or a selected loop changed. Nothing was changed. Review again."}
		}
	}
	reply := panelApplyReviewedRoutes(review.requests, func(i int, req routeChangeRequest) routeChangeOut {
		req.now = c.now().UTC()
		if req.op == "follow_standard" {
			e, _, _, exists := panelEntry(c.registry, review.keys[i])
			if !exists || e.Loop != req.loop {
				out := newRouteChangeOut(req)
				out.Outcome = "review_again"
				return out
			}
		}
		out, _ := panelRouteChange(req, true)
		return out
	})
	reply.Lines = review.lines
	c.reader.forget()
	return reply
}

// Each request commits under its own core-writer lock. A later refusal must
// never be described as rollback, nor hide the unattempted loop results.
func panelApplyReviewedRoutes(requests []routeChangeRequest, apply func(int, routeChangeRequest) routeChangeOut) panelRouteReply {
	reply := panelRouteReply{Outcome: "changed", Text: "Saved the reviewed change.", Changes: []routeChangeOut{}}
	for i, req := range requests {
		out := apply(i, req)
		reply.Changes = append(reply.Changes, panelSafeRouteOut(out))
		if out.Outcome != "changed" && out.Outcome != "unchanged" {
			reply.Outcome = out.Outcome
			if i > 0 {
				reply.Outcome = "partial"
			}
			reply.Text = "Saving stopped. Earlier successful saves remain; remaining loops were not changed. Review again."
			break
		}
	}
	for _, req := range requests[len(reply.Changes):] {
		out := newRouteChangeOut(req)
		out.Outcome = "not_attempted"
		reply.Changes = append(reply.Changes, out)
	}
	return reply
}
