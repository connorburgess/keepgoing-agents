package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/looptender/looptender/internal/accounts"
	"github.com/looptender/looptender/internal/lock"
	"github.com/looptender/looptender/internal/snapshot"
)

const panelAccountSelectionPrefix = "api/v1/account-selections/"
const panelAccountSelectionAction = "review_selection"

type panelAccountSelectionReview struct {
	selection accountQuotaSelection
	caller    string
}

// Capture fixed catalog fields and native identities without accepting its old
// environment binding. Only the explicit reviewed revision below may replace it.
func accountSelectionForReview(dir, id string, env []string) (accountQuotaSelection, error) {
	f, err := accounts.Load(dir)
	if err != nil {
		return accountQuotaSelection{}, err
	}
	e := accounts.Find(&f, id)
	if e == nil || e.Provider != "codex" {
		return accountQuotaSelection{}, accounts.ErrChanged
	}
	s := accountQuotaSelection{Entry: *e, Bound: accounts.Context(f, *e, env)}
	capture := func(path string, wantDir bool) (os.FileInfo, error) {
		real, err := filepath.EvalSymlinks(path)
		if err != nil || real != path {
			return nil, accounts.ErrChanged
		}
		info, err := snapshot.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || info.IsDir() != wantDir || (!wantDir && !info.Mode().IsRegular()) {
			return nil, accounts.ErrChanged
		}
		return info, nil
	}
	if s.CLI, err = capture(e.CLI, false); err != nil {
		return s, err
	}
	if s.Workdir, err = capture(e.Workdir, true); err != nil {
		return s, err
	}
	if e.Mode == "explicit_directory" {
		if s.Directory, err = capture(e.Directory, true); err != nil {
			return s, err
		}
	}
	return s, nil
}

func (c *panelChanges) serveAccountSelection(w http.ResponseWriter, r *http.Request, rest, session string) {
	id, step, ok := strings.Cut(strings.TrimPrefix(rest, panelAccountSelectionPrefix), "/")
	if !ok || !panelAccountID.MatchString(id) || (step != "prepare" && step != "confirm") {
		http.NotFound(w, r)
		return
	}
	req, ok := panelReadAction(http.MaxBytesReader(w, r.Body, panelMaxBody))
	if !ok || req.Action != panelAccountSelectionAction || req.Lanes || req.Immediately || req.DropQueued {
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
		reply = c.prepareAccountSelection(session, id)
	} else {
		reply = c.confirmAccountSelection(session, id, r.Header.Get(panelFormHeader))
	}
	reply.SchemaVersion = 1
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(reply)
}

func (c *panelChanges) prepareAccountSelection(session, id string) panelChangeReply {
	refused := func() panelChangeReply {
		return panelChangeReply{Result: &panelResult{Outcome: "refused", Text: "This selection or one of its existing paths is unavailable. Nothing was updated."}}
	}
	dir, err := accountsDirectory(c.reader.accountsDir)
	if err != nil {
		return refused()
	}
	s, err := accountSelectionForReview(dir, id, c.quotaEnv)
	if err != nil {
		return refused()
	}
	value, err := panelRandom(32)
	if err != nil {
		return refused()
	}
	expires := c.now().UTC().Add(panelHoldFormTTL)
	c.keep(value, panelForm{session: session, key: id, action: panelAccountSelectionAction, expires: expires, selection: &panelAccountSelectionReview{selection: s, caller: c.caller}})
	lines := []dockText{txt("Rebinds this unchanged selection to this panel's startup environment. That environment may authenticate differently from another terminal; account identity is not verified."),
		txt("Provider and label stay: Codex · " + s.Entry.Label), txt("Executable stays: " + s.Entry.CLI), txt("Working directory stays: " + s.Entry.Workdir)}
	if s.Entry.Mode == "explicit_directory" {
		lines = append(lines, txt("Explicit Codex directory stays: "+s.Entry.Directory))
	} else {
		lines = append(lines, txt("Authentication remains inherited in this panel's environment. No login or default is changed."))
	}
	lines = append(lines, txt("Creates a new selection revision and clears previous login/plan observations. Earlier quota no longer applies. No provider is queried and quota is not refreshed; review that read separately afterward."))
	return panelChangeReply{Preview: &panelPreview{Action: panelAccountSelectionAction, Key: id, Title: "Review " + s.Entry.Label + " for this panel?", Confirm: "Use this unchanged selection", Form: value, ExpiresAt: expires, Lines: lines,
		Lost: "The selection update reply was lost. Read the saved state before another review; nothing is retried automatically.", Refused: "The selection update was refused before publication. Nothing was updated."}}
}

func (c *panelChanges) confirmAccountSelection(session, id, value string) panelChangeReply {
	f, ok := c.take(value)
	if c.ctx.Err() != nil || !ok || f.session != session || f.key != id || f.action != panelAccountSelectionAction || f.selection == nil || f.selection.caller != c.caller || !c.now().Before(f.expires) {
		return panelChangeReply{Result: &panelResult{Outcome: "review_again", Text: "This selection review expired, changed, or was already used. Review again."}}
	}
	result := panelResult{Outcome: "refused", Text: "The selection, environment or existing paths changed. Read the saved state and review again. Nothing was updated."}
	dir, err := accountsDirectory(c.reader.accountsDir)
	if err != nil {
		return panelChangeReply{Result: &result}
	}
	guard, err := accounts.CheckGuard(dir, id)
	if err != nil {
		if errors.Is(err, lock.ErrHeld) {
			result.Text = "A check or quota refresh is running for this selection. Wait, then review again."
		}
		return panelChangeReply{Result: &result}
	}
	defer guard.Release()
	reviewed := f.selection.selection
	err = accounts.Update(dir, func(catalog *accounts.File) error {
		if c.ctx.Err() != nil {
			return c.ctx.Err()
		}
		selected := accounts.Find(catalog, id)
		if selected == nil || !accounts.SameSelection(reviewed.Entry, *selected) {
			return accounts.ErrChanged
		}
		fresh, err := accountSelectionForReview(dir, id, c.quotaEnv)
		if err != nil || !reviewed.same(fresh) {
			return accounts.ErrChanged
		}
		*selected = accounts.Revise(*catalog, *selected, *selected, c.caller, c.now().UTC(), c.quotaEnv)
		return nil
	})
	if err == nil {
		c.reader.forget()
		result = panelResult{Outcome: "done", Text: "This unchanged selection is now bound to this panel's environment. Earlier quota no longer applies. Review a quota refresh separately."}
	}
	return panelChangeReply{Result: &result}
}
