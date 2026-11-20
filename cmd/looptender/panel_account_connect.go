package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/looptender/looptender/internal/accounts"
	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/lock"
	"github.com/looptender/looptender/internal/snapshot"
)

const panelAccountConnectPrefix = "api/v1/account-connect/"
const panelAccountConnectAction = "connect_account"
const panelAccountConnectAnotherAction = "connect_another_account"
const accountConnectLifetime = 10 * time.Minute
const accountConnectMaxAttempts = 16

// URLs are ephemeral provider-issued continuation links, returned only to the
// initiating authenticated panel session. Never log/persist them or provider output.
type panelAccountConnectState struct {
	ID                 string    `json:"id"`
	AccountID          string    `json:"accountId"`
	Provider           string    `json:"provider"`
	State              string    `json:"state"`
	Reason             string    `json:"reason,omitempty"`
	ExpiresAt          time.Time `json:"expiresAt"`
	ConnectedAccountID string    `json:"connectedAccountId,omitempty"`
	AuthorizationURL   string    `json:"authorizationUrl,omitempty"`
}
type panelAccountConnectReview struct {
	selection                        accountQuotaSelection
	caller, digest, label, attemptID string
}
type panelAccountConnectAttempt struct {
	view            panelAccountConnectState
	session, caller string
	cancel          context.CancelFunc
	done            chan struct{}
}
type accountConnectAdmit func(context.Context, accounts.Entry) (string, error)
type accountConnectRun func(context.Context, accounts.Entry, []string, func(string)) error

func panelConnectProvidersFor(goos, arch string) []string {
	var out []string
	if goos == "darwin" || goos == "linux" {
		out = append(out, "codex")
	}
	if goos == "darwin" && arch == "arm64" {
		out = append(out, "claude-code")
	}
	return out
}
func panelConnectSupported(provider string) bool {
	for _, p := range panelConnectProvidersFor(runtime.GOOS, runtime.GOARCH) {
		if p == provider {
			return true
		}
	}
	return false
}
func accountConnectEnvironmentAllowed(env []string) bool {
	if !accountClaudeQuotaEnvironment(env) {
		return false
	}
	for _, pair := range env {
		k, v, _ := strings.Cut(pair, "=")
		k = strings.ToUpper(k)
		if v == "" {
			continue
		}
		// Do not borrow credentials or silently remove an explicit authentication
		// choice. Cloud/gateway/descriptor authentication also defeats directory isolation.
		if (strings.HasPrefix(k, "CODEX_") && k != "CODEX_HOME" && k != "CODEX_TOOL_CALL_ID" && k != "CODEX_SANDBOX") || (strings.HasPrefix(k, "CLAUDE_") && k != "CLAUDE_CONFIG_DIR") || strings.HasPrefix(k, "AWS_") || strings.HasPrefix(k, "GOOGLE_") || strings.HasPrefix(k, "AZURE_") || strings.HasPrefix(k, "ANTHROPIC_") || strings.HasPrefix(k, "OPENAI_") || k == "CHATGPT_ACCESS_TOKEN" {
			return false
		}
	}
	return true
}

func panelReadConnect(body io.Reader) (action, attempt, label string, ok bool) {
	raw, e := io.ReadAll(body)
	if e != nil || !accounts.QuotaJSONUnique(raw) {
		return
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || m == nil || len(m) > 3 {
		return
	}
	for k, v := range m {
		if len(v) == 0 || v[0] != '"' {
			return "", "", "", false
		}
		switch k {
		case "action":
			if json.Unmarshal(v, &action) != nil {
				return "", "", "", false
			}
		case "label":
			if json.Unmarshal(v, &label) != nil || label == "" {
				return "", "", "", false
			}
		case "attemptId":
			if json.Unmarshal(v, &attempt) != nil || !panelAccountID.MatchString(attempt) {
				return "", "", "", false
			}
		default:
			return "", "", "", false
		}
	}
	return action, attempt, label, action == panelAccountConnectAction || action == panelAccountConnectAnotherAction
}
func (c *panelChanges) serveAccountConnect(w http.ResponseWriter, r *http.Request, rest, session string) {
	id, step, ok := strings.Cut(strings.TrimPrefix(rest, panelAccountConnectPrefix), "/")
	if !ok || !panelAccountID.MatchString(id) || (step != "prepare" && step != "start" && step != "status" && step != "cancel") {
		http.NotFound(w, r)
		return
	}
	action, attempt, label, ok := panelReadConnect(http.MaxBytesReader(w, r.Body, panelMaxBody))
	if !ok || (action != panelAccountConnectAction && !(step == "prepare" && action == panelAccountConnectAnotherAction)) || ((action == panelAccountConnectAnotherAction) != (label != "")) || ((step == "status" || step == "cancel") != panelAccountID.MatchString(attempt)) || ((step == "prepare" || step == "start") && attempt != "") {
		http.Error(w, "refused", 400)
		return
	}
	var reply panelChangeReply
	if step == "start" {
		reply = c.startAccountConnect(session, id, r.Header.Get(panelFormHeader))
	} else {
		value, want := r.Header.Get(panelFormHeader), c.prepareValue(session)
		if len(value) != len(want) || subtle.ConstantTimeCompare([]byte(value), []byte(want)) != 1 {
			http.Error(w, "refused", 403)
			return
		}
		switch step {
		case "prepare":
			reply = c.prepareAccountConnectLabel(session, id, label)
		case "status", "cancel":
			reply = c.readAccountConnect(session, id, attempt, step == "cancel")
		}
	}
	reply.SchemaVersion = 1
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(reply)
}
func connectRefused(reason string) panelChangeReply {
	return panelChangeReply{Result: &panelResult{Outcome: "refused", Text: reason}}
}
func (c *panelChanges) prepareAccountConnect(session, id string) panelChangeReply {
	return c.prepareAccountConnectLabel(session, id, "")
}
func (c *panelChanges) prepareAccountConnectLabel(session, id, label string) panelChangeReply {
	refuse := func() panelChangeReply {
		return connectRefused("This selection, executable, or authentication environment cannot be isolated. No sign-in was started.")
	}
	if !accountConnectEnvironmentAllowed(c.quotaEnv) {
		return refuse()
	}
	dir, err := accountsDirectory(c.reader.accountsDir)
	if err != nil {
		return refuse()
	}
	s, err := accountSelectionForReview(dir, id, c.quotaEnv)
	if err != nil || !panelConnectSupported(s.Entry.Provider) {
		return refuse()
	}
	ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
	defer cancel()
	if label != "" {
		catalog, e := accounts.Load(dir)
		if e != nil || !accountConnectLabelAvailable(catalog, label) {
			return refuse()
		}
	}
	admit := c.connectAdmit
	if admit == nil {
		admit = accountConnectDigest
	}
	digest, err := admit(ctx, s.Entry)
	if err != nil {
		return refuse()
	}
	token, err := panelRandom(32)
	if err != nil {
		return refuse()
	}
	attemptID, err := panelRandom(16)
	if err != nil {
		return refuse()
	}
	expires := c.now().UTC().Add(panelHoldFormTTL)
	c.keep(token, panelForm{session: session, key: id, action: panelAccountConnectAction, expires: expires, connect: &panelAccountConnectReview{selection: s, caller: c.caller, digest: digest, label: label, attemptID: attemptID}})
	title := "Connect " + s.Entry.Label + "?"
	publication := "Only after verified sign-in will this row select the new context. Existing credentials, other accounts, routes and defaults are retained. This does not connect a BB provider account."
	if label != "" {
		title = "Connect another account: " + label + "?"
		publication = "Only after verified sign-in will a new row named " + label + " be added. The template row " + s.Entry.Label + " and its selected context remain unchanged. Existing credentials, routes and defaults are retained. This does not connect a BB provider account."
	}
	return panelChangeReply{Preview: &panelPreview{AttemptID: attemptID, Action: panelAccountConnectAction, Key: id, Title: title, Confirm: "Start provider sign-in", Form: token, ExpiresAt: expires, Lines: []dockText{txt("Starts the provider's own browser sign-in in a new private account context. Complete sign-in personally. No model or quota request is made."), txt(publication), txt("A lost reply never restarts sign-in. Cancel stops this owned attempt; provider-written credentials may remain in its private context. Manual-code or organization-required terminal flows may need a terminal.")}, Lost: "The sign-in start reply was lost. Do not start again automatically; inspect this selection before a new review.", Refused: "Sign-in was not started."}}
}
func (c *panelChanges) startAccountConnect(session, id, form string) panelChangeReply {
	f, ok := c.take(form)
	if !ok || f.session != session || f.key != id || f.action != panelAccountConnectAction || f.connect == nil || f.connect.caller != c.caller || !c.now().Before(f.expires) {
		return panelChangeReply{Result: &panelResult{Outcome: "review_again", Text: "This sign-in review expired or was already used. Review again.", Retry: true}}
	}
	dir, err := accountsDirectory(c.reader.accountsDir)
	if err != nil || !accountConnectEnvironmentAllowed(c.quotaEnv) {
		return connectRefused("The private selection is unavailable. No sign-in was started.")
	}
	guard, err := accounts.CheckGuard(dir, id)
	if err != nil {
		return connectRefused("A check, refresh or sign-in already owns this selection. No sign-in was started.")
	}
	keepGuard := false
	defer func() {
		if !keepGuard {
			guard.Release()
		}
	}()
	current, err := accountSelectionForReview(dir, id, c.quotaEnv)
	if err != nil || !f.connect.selection.same(current) {
		return connectRefused("The reviewed selection changed. No sign-in was started.")
	}
	token := f.connect.attemptID
	c.mu.Lock()
	if c.connectAttempts == nil {
		c.connectAttempts = map[string]*panelAccountConnectAttempt{}
	}
	// Bounded session memory, including terminal records: no automatic eviction
	// that could hide a lost reply and encourage another login.
	if c.connectClosing || c.ctx.Err() != nil {
		c.mu.Unlock()
		return connectRefused("This panel is closing. No sign-in was started.")
	}
	if len(c.connectAttempts) >= accountConnectMaxAttempts {
		c.mu.Unlock()
		return connectRefused("This panel reached its sign-in attempt limit. Reopen it before another reviewed attempt.")
	}
	ctx, cancel := context.WithTimeout(c.ctx, accountConnectLifetime)
	a := &panelAccountConnectAttempt{view: panelAccountConnectState{ID: token, AccountID: id, Provider: current.Entry.Provider, State: "waiting", ExpiresAt: c.now().UTC().Add(accountConnectLifetime)}, session: session, caller: c.caller, cancel: cancel, done: make(chan struct{})}
	c.connectAttempts[token] = a
	c.mu.Unlock()
	keepGuard = true
	go c.runAccountConnect(ctx, dir, a, f.connect, guard)
	return c.readAccountConnect(session, id, token, false)
}
func (c *panelChanges) readAccountConnect(session, id, attempt string, cancel bool) panelChangeReply {
	c.mu.Lock()
	defer c.mu.Unlock()
	a := c.connectAttempts[attempt]
	if a == nil || a.session != session || a.caller != c.caller || a.view.AccountID != id {
		return connectRefused("This sign-in attempt is unavailable to this session.")
	}
	if cancel && a.view.State == "waiting" {
		a.view.State = "cancelled"
		a.view.Reason = "cancelled"
		a.view.AuthorizationURL = ""
		a.cancel()
	}
	v := a.view
	return panelChangeReply{Connect: &v}
}
func (c *panelChanges) runAccountConnect(ctx context.Context, dir string, a *panelAccountConnectAttempt, review *panelAccountConnectReview, guard *lock.Lock) {
	defer close(a.done)
	defer guard.Release()
	defer a.cancel()
	state, reason := "failed", "sign_in_unavailable"
	defer func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if a.view.State == "waiting" {
			a.view.State = state
			a.view.Reason = reason
		}
		a.view.AuthorizationURL = ""
	}()
	contextDir, identity, err := accountConnectDirectory(dir)
	if err != nil {
		return
	}
	// Failed/uncertain contexts are deliberately retained, not scanned or erased:
	// a provider may have committed credentials before its completion reply was lost.
	entry := review.selection.Entry
	entry.Mode = "explicit_directory"
	entry.Directory = contextDir
	admission, cancel := context.WithTimeout(ctx, 5*time.Second)
	image, cleanup, err := accountClaudeQuotaStage(admission, entry.CLI, review.digest)
	cancel()
	if err != nil {
		reason = "unverified_cli"
		return
	}
	defer cleanup()
	entry.CLI = image
	fresh, err := accountSelectionForReview(dir, entry.ID, c.quotaEnv)
	if err != nil || !review.selection.same(fresh) {
		state = "selection_changed"
		reason = "selection_changed"
		return
	}
	run := c.connectRun
	if run == nil {
		run = accountConnectProvider
	}
	env := accountQuotaExecutionEnvironment(entry, c.quotaEnv)
	// Both providers retain the genuine startup caller, including Codex.
	if entry.Provider == "codex" {
		for _, pair := range c.quotaEnv {
			k, _, _ := strings.Cut(pair, "=")
			if k == "BB_THREAD_ID" {
				env = append(env, pair)
			}
		}
	}
	err = run(ctx, entry, env, func(u string) {
		c.mu.Lock()
		defer c.mu.Unlock()
		if a.view.State == "waiting" {
			a.view.AuthorizationURL = u
		}
	})
	if err != nil {
		if ctx.Err() != nil {
			state = "expired"
			reason = "timeout"
			if errors.Is(ctx.Err(), context.Canceled) {
				state = "cancelled"
				reason = "cancelled"
			}
		} else if errors.Is(err, errConnectProtocol) {
			reason = "provider_protocol_unavailable"
		}
		return
	}
	if ctx.Err() != nil {
		state = "expired"
		reason = "timeout"
		return
	}
	// Cancel and publication serialize on the local attempt mutex. Catalog Update
	// separately locks/reloads and revalidates the reviewed selection before save.
	c.mu.Lock()
	defer c.mu.Unlock()
	if a.view.State != "waiting" {
		return
	}
	publishedID := ""
	err = accounts.Update(dir, func(catalog *accounts.File) error {
		fresh, e := accountSelectionForReview(dir, entry.ID, c.quotaEnv)
		if e != nil || !review.selection.same(fresh) {
			return accounts.ErrChanged
		}
		st, e := snapshot.Lstat(contextDir)
		real, re := filepath.EvalSymlinks(contextDir)
		if e != nil || re != nil || real != contextDir || !os.SameFile(identity, st) || !st.IsDir() || !config.OwnedByUser(st) || st.Mode().Perm()&0077 != 0 {
			return accounts.ErrChanged
		}
		selected := accounts.Find(catalog, entry.ID)
		if selected == nil || !accounts.SameSelection(*selected, review.selection.Entry) {
			return accounts.ErrChanged
		}
		replacement := *selected
		replacement.Mode = "explicit_directory"
		replacement.Directory = contextDir
		if review.label != "" {
			if !accountConnectLabelAvailable(*catalog, review.label) {
				return accounts.ErrChanged
			}
			replacement.Label = review.label
			replacement = accounts.NewEntry(*catalog, replacement, c.caller, c.now().UTC(), c.quotaEnv)
			catalog.Entries = append(catalog.Entries, replacement)
			publishedID = replacement.ID
		} else {
			*selected = accounts.Revise(*catalog, *selected, replacement, c.caller, c.now().UTC(), c.quotaEnv)
			publishedID = selected.ID
		}
		return nil
	})
	if err != nil {
		state = "selection_changed"
		reason = "selection_changed"
		return
	}
	a.view.ConnectedAccountID = publishedID
	a.view.State = "connected"
	a.view.Reason = ""
	a.view.AuthorizationURL = ""
	state = "connected"
	reason = ""
	c.reader.forget()
}
func accountConnectDirectory(dir string) (string, os.FileInfo, error) {
	real, err := filepath.EvalSymlinks(dir)
	if err != nil || real != dir {
		return "", nil, accounts.ErrInvalid
	}
	st, err := snapshot.Lstat(dir)
	if err != nil || !st.IsDir() || !config.OwnedByUser(st) || st.Mode().Perm()&0077 != 0 {
		return "", nil, accounts.ErrInvalid
	}
	// MkdirTemp creates an exclusive direct child, with no pre-existing path to
	// follow, rather than accepting any browser-supplied directory.
	p, err := os.MkdirTemp(dir, "connection-")
	if err != nil {
		return "", nil, err
	}
	st, err = snapshot.Lstat(p)
	return p, st, err
}

func accountConnectLabelAvailable(f accounts.File, label string) bool {
	if len(f.Entries) >= accounts.MaxEntries || !accounts.Text(label, 120) || strings.TrimSpace(label) != label {
		return false
	}
	for _, e := range f.Entries {
		if strings.EqualFold(e.Label, label) {
			return false
		}
	}
	return true
}

// Called by runPanelServer on every return after newPanelChanges succeeds,
// before its earlier resource-release defers. Close admission under the same
// mutex used to install attempts, cancel ALL tasks first, then join them all.
// Owned provider commands kill/wait on cancellation; staging also checks ctx.
// The done channel closes only after image cleanup and guard release.
func (c *panelChanges) closeAccountConnect() {
	c.mu.Lock()
	c.connectClosing = true
	attempts := make([]*panelAccountConnectAttempt, 0, len(c.connectAttempts))
	for _, a := range c.connectAttempts {
		a.cancel()
		attempts = append(attempts, a)
	}
	c.mu.Unlock()
	for _, a := range attempts {
		<-a.done
	}
}
