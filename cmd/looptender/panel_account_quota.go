package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"runtime"
	"strings"

	"github.com/looptender/looptender/internal/accounts"
	"github.com/looptender/looptender/internal/lock"
)

const panelAccountQuotaPrefix = "api/v1/accounts/"
const panelAccountQuotaAction = "subscription_quota"

// The inspected native Claude image is Darwin arm64 only. Capability is a
// platform prerequisite; the acquisition layer still admits each exact image.
func panelQuotaProvidersFor(goos, goarch string) []string {
	providers := []string{"codex", "antigravity"}
	if goos == "darwin" && goarch == "arm64" {
		providers = append(providers, "claude-code")
	}
	return providers
}
func panelQuotaProviderSupported(provider string) bool {
	for _, p := range panelQuotaProvidersFor(runtime.GOOS, runtime.GOARCH) {
		if p == provider {
			return true
		}
	}
	return false
}

var panelAccountID = regexp.MustCompile(`^[a-f0-9]{32}$`)

// This handler is reached only after the existing panel session, same-origin,
// fetch metadata, content-type, body bound and allow-changes guards. Reviews
// perform no RPC; confirmation spends a session-bound single-use form.
func (c *panelChanges) serveAccountQuota(w http.ResponseWriter, r *http.Request, rest, session string) {
	id, step, ok := strings.Cut(strings.TrimPrefix(rest, panelAccountQuotaPrefix), "/")
	if !ok || !panelAccountID.MatchString(id) || (step != "prepare" && step != "confirm") {
		http.NotFound(w, r)
		return
	}
	req, ok := panelReadAction(http.MaxBytesReader(w, r.Body, panelMaxBody))
	if !ok || req.Action != panelAccountQuotaAction || req.Lanes || req.Immediately || req.DropQueued {
		http.Error(w, "refused", 400)
		return
	}
	var reply panelChangeReply
	if step == "prepare" {
		value, want := r.Header.Get(panelFormHeader), c.prepareValue(session)
		if len(value) != len(want) || subtle.ConstantTimeCompare([]byte(value), []byte(want)) != 1 {
			http.Error(w, "refused", 403)
			return
		}
		reply = c.prepareAccountQuota(session, id)
	} else {
		reply = c.confirmAccountQuota(session, id, r.Header.Get(panelFormHeader))
	}
	reply.SchemaVersion = 1
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(reply)
}
func (c *panelChanges) prepareAccountQuota(session, id string) panelChangeReply {
	refuse := func() panelChangeReply {
		return panelChangeReply{Result: &panelResult{Outcome: "refused", Text: "The selected context or its private quota evidence is unavailable. Nothing was refreshed."}}
	}
	dir, err := accountsDirectory(c.reader.accountsDir)
	if err != nil {
		return refuse()
	}
	s, err := accountQuotaCapture(dir, id, c.quotaEnv)
	if err != nil || !panelQuotaProviderSupported(s.Entry.Provider) {
		return refuse()
	}
	switch s.Entry.Provider {
	case "claude-code":
		if _, err := accounts.LoadClaudeQuota(dir); err != nil {
			return refuse()
		}
	case "antigravity":
		if _, err := accounts.LoadAntigravityQuota(dir); err != nil {
			return refuse()
		}
	default:
		if _, err := accounts.LoadQuota(dir); err != nil {
			return refuse()
		}
	}
	value, err := panelRandom(32)
	if err != nil {
		return refuse()
	}
	expires := c.now().UTC().Add(panelHoldFormTTL)
	c.keep(value, panelForm{session: session, key: id, action: panelAccountQuotaAction, expires: expires, quota: &s})
	lines := []dockText{txt("Contacts the selected Codex context to read its subscription windows. No model request or login is started."), txt("This reading belongs to this selection. Route observations and token totals remain separate.")}
	title, confirm := "Read subscription quota for "+s.Entry.Label+"?", "Read subscription quota"
	if s.Entry.Provider == "claude-code" {
		title, confirm = "Retrieve usage for "+s.Entry.Label+"?", "Retrieve usage"
		lines = []dockText{txt("Retrieves usage through an inspected native Claude Code 2.1.326 image on Darwin arm64. Unverified images or unavailable isolation refuse before a usage request."), txt("The parked exchange disables hooks and sends no model request, user message, session claim or transcript scan. No login is started."), txt("Provider freshness is unknown: retrieval may return cached or seeded usage. Retrieval time is not the provider's observation time. Route observations remain separate.")}
	}
	if s.Entry.Provider == "antigravity" {
		title, confirm = "Retrieve Antigravity usage for "+s.Entry.Label+"?", "Retrieve usage"
		lines = []dockText{txt("Retrieves Antigravity /usage through the selected agy CLI. This is a model-free slash command: no ordinary turn is started."), txt("Provider freshness is unknown: retrieval may return cached usage. Retrieval time is not the provider's observation time. Route observations remain separate.")}
	}
	return panelChangeReply{Preview: &panelPreview{Action: panelAccountQuotaAction, Key: id, Title: title, Confirm: confirm, Form: value, ExpiresAt: expires,
		Lines: lines,
		Lost:  "The quota refresh reply was lost. Read the cached state before trying again; nothing is retried automatically.", Refused: "The quota request was refused before it ran. Nothing was refreshed."}}
}
func (c *panelChanges) confirmAccountQuota(session, id, value string) panelChangeReply {
	f, ok := c.take(value)
	if !ok || f.session != session || f.key != id || f.action != panelAccountQuotaAction || f.quota == nil || !c.now().Before(f.expires) {
		return panelChangeReply{Result: &panelResult{Outcome: "review_again", Text: "This quota review expired, changed, or was already used. Review again.", Retry: true}}
	}
	dir, err := accountsDirectory(c.reader.accountsDir)
	if err != nil || !panelQuotaProviderSupported(f.quota.Entry.Provider) {
		return panelChangeReply{Result: &panelResult{Outcome: "refused", Text: "The private account catalog is unavailable. Nothing was refreshed."}}
	}
	run := c.quotaRun
	if run == nil {
		run = accountQuotaRead
	}
	v, err := accountQuotaRefresh(c.ctx, dir, id, f.quota, run, c.quotaEnv, c.now)
	c.reader.forget()
	result := panelResult{Outcome: "done", Text: "Subscription quota reading saved for this selected context."}
	if err != nil {
		result = panelResult{Outcome: "refused", Text: "The selected context changed or its private evidence is unavailable. No quota reading was replaced.", Retry: true}
		if errors.Is(err, lock.ErrHeld) {
			result.Text = "A check or quota refresh is already running for this selection. Wait, then read the cached state."
		}
	} else if v.FailedRefreshAt != nil {
		result.Text = "Quota refresh unavailable. Any earlier reading retains its original timestamp."
	} else if f.quota.Entry.Provider == "claude-code" || f.quota.Entry.Provider == "antigravity" {
		result.Text = "Usage retrieved for this selected context. Provider freshness remains unknown; the reply may contain cached or seeded usage."
	}
	return panelChangeReply{Result: &result}
}
