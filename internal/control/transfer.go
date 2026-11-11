package control

import (
	"errors"
	"regexp"
)

// PendingBBTransfer is a roll-forward intent. Digests bind config bytes without
// copying prompts. They are private and must be omitted from diagnostic output.
type PendingBBTransfer struct {
	Version int    `json:"version"`
	ID      string `json:"id"`
	From    string `json:"from"`
	To      string `json:"to"`
	Before  string `json:"before,omitempty"`
	After   string `json:"after,omitempty"`
}

var transferID = regexp.MustCompile(`^move_[0-9a-f]{32}$`)
var digest = regexp.MustCompile(`^[0-9a-f]{64}$`)
var ErrTransferPending = errors.New("BB transfer is pending; use transfer recover before enabling, resuming or changing goals")

func (p *PendingBBTransfer) Valid() bool {
	return p != nil && p.Version == 1 && transferID.MatchString(p.ID) && ownerID.MatchString(p.From) && ownerID.MatchString(p.To) && p.From != p.To && digest.MatchString(p.Before) && digest.MatchString(p.After) && p.Before != p.After
}
func CheckTransferReady(f File) error {
	if f.PendingTransfer != nil {
		return ErrTransferPending
	}
	return nil
}

// BindBBTarget permanently rejects stale config copies after a move. Ownership
// generation changes even if the destination had already claimed this loop.
func BindBBTarget(f *File, to string) error {
	if !ownerID.MatchString(to) {
		return errors.New("invalid BB transfer target")
	}
	same := f.Ownership != nil && f.Ownership.ThreadID == to
	if same && f.Ownership.Generation == ^uint64(0) {
		return errors.New("loop ownership generation exhausted")
	}
	if err := ClaimOwner(f, to, true); err != nil {
		return err
	}
	if same {
		f.Ownership.Generation++
	}
	f.BBTarget = to
	return nil
}
