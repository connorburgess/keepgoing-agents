package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/control"
	"github.com/looptender/looptender/internal/lock"
	"github.com/looptender/looptender/internal/registry"
	"github.com/looptender/looptender/internal/snapshot"
	"github.com/looptender/looptender/internal/state"
)

// This private review contains no provider data and never grants resume.
// The panel form will bind it to one session, key and single-use confirmation.
type panelSendAllowanceReview struct {
	entry                     registry.Entry
	edit                      *config.SendAllowanceEdit
	sources, controls, ledger string
	current, proposed, used   int64
}

var errPanelSendAllowance = errors.New("send allowance could not be reviewed or changed; read the loop again")

// sendAllowanceChange holds the same tick/control locks as runtime writers.
// A non-nil expected review requests publication; nil only prepares a review.
func (c *panelChanges) sendAllowanceChange(key string, proposed int64, expected *panelSendAllowanceReview) (*panelSendAllowanceReview, error) {
	// Increasing the allowance is an operator action, not an agent's way to
	// extend its own reach. Preserve the immutable startup caller boundary.
	if c.caller != "" || proposed <= 0 {
		return nil, errPanelSendAllowance
	}
	entry, _, _, ok := panelEntry(c.registry, key)
	if !ok {
		return nil, errPanelSendAllowance
	}
	tick, err := lock.Acquire(entry.StateDir)
	if err != nil {
		return nil, errPanelSendAllowance
	}
	defer tick.Release()
	held, err := lock.AcquireNamed(entry.StateDir, lock.Control)
	if err != nil {
		return nil, errPanelSendAllowance
	}
	defer held.Release()
	cfg, ok := clientConfig(entry)
	if !ok || cfg.CheckSources() != nil {
		return nil, errPanelSendAllowance
	}
	var ctl control.File
	if readTransferRecord(entry.StateDir, control.FileName, &ctl) != nil || control.CheckTransferReady(ctl) != nil || control.CheckOwner(ctl, c.caller, false) != nil {
		return nil, errPanelSendAllowance
	}
	// A missing ledger is not presented as zero by this editor. It must have
	// actual count evidence before promising remaining attempts.
	ledgerPath := filepath.Join(entry.StateDir, state.SendsName)
	info, err := snapshot.LstatRecord(ledgerPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, errPanelSendAllowance
	}
	f, err := snapshot.OpenRecord(ledgerPath)
	if err != nil {
		return nil, errPanelSendAllowance
	}
	raw, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	closeErr := f.Close()
	if err != nil || closeErr != nil || len(raw) > 1<<20 {
		return nil, errPanelSendAllowance
	}
	used, err := state.LoadSends(entry.StateDir)
	if err != nil {
		return nil, errPanelSendAllowance
	}
	sum := sha256.Sum256(raw)
	review := &panelSendAllowanceReview{entry: entry, sources: cfg.SourcesDigest(), controls: controlDigest(ctl), ledger: hex.EncodeToString(sum[:]), current: cfg.Loop.Limits.MaxSends, proposed: proposed, used: used}
	edit, err := cfg.PrepareSendAllowance(proposed)
	if err != nil {
		return nil, errPanelSendAllowance
	}
	if expected == nil {
		review.edit = edit
		return review, nil
	}
	if review.entry.ID != expected.entry.ID || review.entry.ConfigPath != expected.entry.ConfigPath || review.entry.StateDir != expected.entry.StateDir || review.entry.Loop != expected.entry.Loop || review.sources != expected.sources || review.controls != expected.controls || review.ledger != expected.ledger || review.current != expected.current || review.proposed != expected.proposed || review.used != expected.used {
		return nil, errPanelSendAllowance
	}
	// Registration itself is not guarded by the tick lock; recheck its binding
	// immediately before publication. Uncooperative external writers remain
	// outside this operation's serialization guarantee.
	fresh, _, _, ok := panelEntry(c.registry, key)
	if !ok || fresh.ID != entry.ID || fresh.ConfigPath != entry.ConfigPath || fresh.StateDir != entry.StateDir || fresh.Loop != entry.Loop {
		return nil, errPanelSendAllowance
	}
	// Commit the originally reviewed edit, retaining its eagerly captured file
	// identity across the separate HTTP requests, even for identical bytes.
	if expected.edit == nil || expected.edit.Commit() != nil {
		return nil, errPanelSendAllowance
	}
	return review, nil
}

const panelSendAllowancePrefix = "api/v1/send-allowance/"
const panelSendAllowanceAction = "set_send_allowance"

// Exact keys and integer spelling avoid ambiguous review/confirmation bodies.
func panelReadSendAllowance(body io.Reader, prepare bool) (int64, bool) {
	d := json.NewDecoder(body)
	d.UseNumber()
	if v, err := d.Token(); err != nil || v != json.Delim('{') {
		return 0, false
	}
	seen := map[string]bool{}
	var allowance int64
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] {
			return 0, false
		}
		seen[key] = true
		value, err := d.Token()
		if err != nil {
			return 0, false
		}
		switch key {
		case "action":
			if value != panelSendAllowanceAction {
				return 0, false
			}
		case "allowance":
			number, ok := value.(json.Number)
			if !prepare || !ok {
				return 0, false
			}
			allowance, err = number.Int64()
			if err != nil || allowance <= 0 {
				return 0, false
			}
		default:
			return 0, false
		}
	}
	if v, err := d.Token(); err != nil || v != json.Delim('}') {
		return 0, false
	}
	if _, err := d.Token(); err != io.EOF {
		return 0, false
	}
	return allowance, seen["action"] && (!prepare || seen["allowance"])
}

func (c *panelChanges) serveSendAllowance(w http.ResponseWriter, r *http.Request, rest, session string) {
	key, step, ok := strings.Cut(strings.TrimPrefix(rest, panelSendAllowancePrefix), "/")
	if !ok || !panelKeyPattern.MatchString(key) || (step != "prepare" && step != "confirm") {
		http.NotFound(w, r)
		return
	}
	proposed, ok := panelReadSendAllowance(http.MaxBytesReader(w, r.Body, panelMaxBody), step == "prepare")
	if !ok {
		http.Error(w, "refused", http.StatusBadRequest)
		return
	}
	reply := panelChangeReply{SchemaVersion: 1}
	if step == "prepare" {
		want := c.prepareValue(session)
		if subtle.ConstantTimeCompare([]byte(r.Header.Get(panelFormHeader)), []byte(want)) != 1 {
			http.Error(w, "refused", http.StatusForbidden)
			return
		}
		reply = c.prepareSendAllowance(session, key, proposed)
	} else {
		reply = c.confirmSendAllowance(session, key, r.Header.Get(panelFormHeader))
	}
	reply.SchemaVersion = 1
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(reply)
}

func (c *panelChanges) prepareSendAllowance(session, key string, proposed int64) panelChangeReply {
	review, err := c.sendAllowanceChange(key, proposed, nil)
	if err != nil {
		return panelChangeReply{Result: &panelResult{Outcome: "refused", Text: "This allowance cannot be edited here. Open a controls panel outside a supervised thread and check the loop's configuration and send records. Nothing was changed."}}
	}
	value, err := panelRandom(32)
	if err != nil {
		return panelChangeReply{Result: &panelResult{Outcome: "refused", Text: "A review could not be created. Nothing was changed."}}
	}
	expires := c.now().Add(panelHoldFormTTL)
	c.keep(value, panelForm{session: session, key: key, action: panelSendAllowanceAction, expires: expires, allowance: review})
	current := strconv.FormatInt(review.current, 10)
	if review.current == 0 {
		current = "unlimited"
	}
	remaining := max(int64(0), proposed-review.used)
	return panelChangeReply{Preview: &panelPreview{Action: panelSendAllowanceAction, Key: key, Title: "Change send allowance for " + review.entry.Loop + "?", Form: value, ExpiresAt: expires, Confirm: "Save send allowance", Lines: []dockText{
		txt("Current allowance: " + current + ". Proposed allowance: " + strconv.FormatInt(proposed, 10) + "."),
		txt("Counted attempts: " + strconv.FormatInt(review.used, 10) + ". Remaining under the proposed allowance: " + strconv.FormatInt(remaining, 10) + "."),
		txt("Existing holds stay in place. This does not resume the loop or reset counted attempts. Attempts made while no allowance was configured were not counted toward this limit."),
	}, Lost: "Whether the allowance was saved is unknown. Read the loop before reviewing again; do not repeat the confirmation.", Refused: "Nothing was changed. Read the loop and review again."}}
}

func (c *panelChanges) confirmSendAllowance(session, key, value string) panelChangeReply {
	form, ok := c.take(value)
	if !ok || form.session != session || form.key != key || form.action != panelSendAllowanceAction || form.allowance == nil || !c.now().Before(form.expires) {
		return panelChangeReply{Result: &panelResult{Outcome: "review_again", Text: "This review expired or was already used. Nothing was changed."}}
	}
	if _, err := c.sendAllowanceChange(key, form.allowance.proposed, form.allowance); err != nil {
		return panelChangeReply{Result: &panelResult{Outcome: "review_again", Text: "The loop changed or its records are unavailable. Nothing was changed; read and review again."}}
	}
	return panelChangeReply{Result: &panelResult{Outcome: "done", Text: "Send allowance saved. Existing holds and counted attempts are unchanged."}}
}
