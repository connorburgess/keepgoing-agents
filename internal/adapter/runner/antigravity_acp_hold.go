package runner

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
)

// Each fact is its own file, created once and never replaced or removed. A
// reader therefore cannot block a writer, and no update can lose another.
const (
	antigravityACPReservedName  = "acp-prompt-reserved.json"
	antigravityACPNeedsUserName = "acp-needs-user.json"
	antigravityACPAdoptedName   = "acp-hold-adopted.json"
	// Two 256-byte identifiers at JSON's worst-case six-byte escaping, plus keys.
	antigravityACPHoldLimit = 4096
)

var errAntigravityACPHold = errors.New("acp: interaction hold record unavailable")

// AntigravityACPHold is the saved evidence of one ACP exchange for one target
// directory. It holds identities and three facts, never prompt text, permission
// details, tool arguments or provider output.
//
// PromptAttempted is a reservation made before the prompt write, not proof of
// delivery. NeedsUser means a permission request was cancelled and deferred to
// a person; nothing was approved. Adopted means the supervisor recorded that it
// saved the awaiting-user hold for exactly this turn.
type AntigravityACPHold struct {
	Turn            string
	SessionID       string
	PromptAttempted bool
	NeedsUser       bool
	Adopted         bool
}

// Deferred reports human-input evidence the supervisor has not yet adopted as
// an awaiting-user hold. It stays true across restarts until adoption.
func (h AntigravityACPHold) Deferred() bool { return h.NeedsUser && !h.Adopted }

// acpHoldID is the content of every record: the turn and session it belongs to.
type acpHoldID struct{ turn, session string }

// AntigravityACPHold reads the saved evidence. No files is no evidence (false,
// nil). Anything unreadable, oversized, malformed, duplicated, of an unknown
// version, or inconsistent between files fails closed with a fixed error that
// echoes no file content, path or identity.
func (d Dir) AntigravityACPHold() (AntigravityACPHold, bool, error) {
	reserved, hasReserved, err := d.readAntigravityACPRecord(antigravityACPReservedName)
	if err != nil {
		return AntigravityACPHold{}, false, err
	}
	needs, hasNeeds, err := d.readAntigravityACPRecord(antigravityACPNeedsUserName)
	if err != nil {
		return AntigravityACPHold{}, false, err
	}
	adopted, hasAdopted, err := d.readAntigravityACPRecord(antigravityACPAdoptedName)
	if err != nil {
		return AntigravityACPHold{}, false, err
	}
	// Adoption is only trusted beside the evidence it names. Anything else could
	// hide a hold, so it is unreadable rather than ignored.
	if (hasReserved && hasNeeds && reserved != needs) || (hasAdopted && (!hasNeeds || adopted != needs)) {
		return AntigravityACPHold{}, false, errAntigravityACPHold
	}
	if !hasReserved && !hasNeeds {
		return AntigravityACPHold{}, false, nil
	}
	id := reserved
	if hasNeeds {
		id = needs
	}
	return AntigravityACPHold{Turn: id.turn, SessionID: id.session, PromptAttempted: hasReserved, NeedsUser: hasNeeds, Adopted: hasAdopted}, true, nil
}

// AntigravityACPHoldCheckpoint returns a checkpoint callback for
// ExchangeAntigravityACP bound to this target directory and the caller's
// opaque turn identity. A nil return means the evidence is written, synced and
// published under its final name. An error means the call added nothing, except
// that a call carrying both facts may have saved the reservation before failing.
//
// Evidence only accumulates: nothing here replaces or removes a file. The
// prompt reservation is published with an exclusive create, so of any number of
// exchanges, restarted or concurrent, at most one is told it may write a
// prompt; the rest are refused, as is any prompt after human-input evidence. A
// different turn or session, and new evidence beside a leftover adoption, are
// refused. There is deliberately no rollover: starting a later turn needs the
// send/session reconciliation that no runner implements yet.
//
// Human-input evidence is saved once per turn. Later permission requests in the
// same cancelled turn add nothing, including after adoption.
//
// A synced file and exclusive hard link survive process exit and restart; this
// is not a power-loss or storage guarantee, and it needs a filesystem with
// hard links. Unsupported operations fail closed.
func (d Dir) AntigravityACPHoldCheckpoint(turn string) func(context.Context, AntigravityACPCheckpoint) error {
	return func(ctx context.Context, c AntigravityACPCheckpoint) error {
		if ctx == nil || ctx.Err() != nil || !acpIdentifier(turn) || !acpIdentifier(c.SessionID) || (!c.BeforePrompt && !c.NeedsUser) {
			return errAntigravityACPHold
		}
		id := acpHoldID{turn, c.SessionID}
		reserved, hasReserved, err := d.readAntigravityACPRecord(antigravityACPReservedName)
		if err != nil {
			return err
		}
		needs, hasNeeds, err := d.readAntigravityACPRecord(antigravityACPNeedsUserName)
		if err != nil {
			return err
		}
		_, hasAdopted, err := d.readAntigravityACPRecord(antigravityACPAdoptedName)
		if err != nil {
			return err
		}
		if (hasReserved && reserved != id) || (hasNeeds && needs != id) || (hasAdopted && !hasNeeds) {
			return errAntigravityACPHold
		}
		if c.BeforePrompt {
			if hasReserved || hasNeeds {
				return errAntigravityACPHold
			}
			// Losing the exclusive create means another exchange holds the reservation.
			if created, err := d.publishAntigravityACPRecord(antigravityACPReservedName, id); err != nil || !created {
				return errAntigravityACPHold
			}
		}
		if c.NeedsUser && !hasNeeds {
			created, err := d.publishAntigravityACPRecord(antigravityACPNeedsUserName, id)
			if err != nil {
				return errAntigravityACPHold
			}
			if !created {
				// Saved concurrently: accept only the same turn and session.
				if needs, ok, err := d.readAntigravityACPRecord(antigravityACPNeedsUserName); err != nil || !ok || needs != id {
					return errAntigravityACPHold
				}
			}
		}
		return nil
	}
}

// AdoptAntigravityACPHold records that the supervisor saved its awaiting-user
// hold for this turn's human-input evidence. Call it only after that hold is
// durable. It is idempotent and never changes the evidence. Without matching
// evidence it refuses, so it cannot pre-clear a later interaction.
//
// Adoption is what distinguishes an operator's explicit resume from an
// automatic restart: unadopted evidence raises the hold again, adopted evidence
// does not. It approves nothing and does not make the turn eligible to continue.
func (d Dir) AdoptAntigravityACPHold(turn string) error {
	h, ok, err := d.AntigravityACPHold()
	if err != nil {
		return err
	}
	if !ok || !h.NeedsUser || h.Turn != turn {
		return errAntigravityACPHold
	}
	if h.Adopted {
		return nil
	}
	if _, err := d.publishAntigravityACPRecord(antigravityACPAdoptedName, acpHoldID{h.Turn, h.SessionID}); err != nil {
		return errAntigravityACPHold
	}
	// Whoever created it, it must now read back as this turn's adoption.
	if h, ok, err := d.AntigravityACPHold(); err != nil || !ok || !h.Adopted {
		return errAntigravityACPHold
	}
	return nil
}

// readAntigravityACPRecord requires a bounded regular file holding one version 1
// object with exactly the version, turn and sessionId keys.
func (d Dir) readAntigravityACPRecord(name string) (acpHoldID, bool, error) {
	p := d.path(name)
	info, err := os.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		return acpHoldID{}, false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > antigravityACPHoldLimit {
		return acpHoldID{}, false, errAntigravityACPHold
	}
	f, err := os.Open(p)
	if err != nil {
		return acpHoldID{}, false, errAntigravityACPHold
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, antigravityACPHoldLimit+1))
	if err != nil || len(b) > antigravityACPHoldLimit {
		return acpHoldID{}, false, errAntigravityACPHold
	}
	fields, err := cursorObject(b)
	var id acpHoldID
	if err != nil || len(fields) != 3 || string(fields["version"]) != "1" ||
		!acpString(fields, "turn", &id.turn) || !acpIdentifier(id.turn) ||
		!acpString(fields, "sessionId", &id.session) || !acpIdentifier(id.session) {
		return acpHoldID{}, false, errAntigravityACPHold
	}
	return id, true, nil
}

// publishAntigravityACPRecord syncs a private temporary file and links it to
// its final name, which fails if that name exists. The file is complete before
// it is visible and is never replaced. It reports false when the name was
// already taken.
func (d Dir) publishAntigravityACPRecord(name string, id acpHoldID) (bool, error) {
	b, err := json.Marshal(struct {
		Version   int    `json:"version"`
		Turn      string `json:"turn"`
		SessionID string `json:"sessionId"`
	}{1, id.turn, id.session})
	if err != nil || len(b)+1 > antigravityACPHoldLimit {
		return false, errAntigravityACPHold
	}
	if err := os.MkdirAll(string(d), 0o700); err != nil {
		return false, err
	}
	f, err := os.CreateTemp(string(d), ".acp-hold-*")
	if err != nil {
		return false, err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(b, '\n')); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return false, err
	}
	if closeErr != nil {
		return false, closeErr
	}
	if err := os.Link(f.Name(), d.path(name)); err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
