package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/snapshot"
)

// SendsName is separate from diagnostic state so a failed check or stale record
// cannot refund a send. It holds two things. Used counts attempts made with
// max_sends enabled, for that limit. Seq and Pending are kept for every
// automatic send, limit or not: before an adapter is asked to send, the
// intended send is saved here, so a send whose outcome cannot be recorded in
// state.json is never lost. See Reserve.
const SendsName = "sends.json"

var ErrSendLimit = errors.New("loop send limit reached")
var ErrSendLedger = errors.New("send allowance unavailable; inspect private sends.json and runtime access")

// ErrSendRecord is returned when the record of an intended send cannot be
// saved. Nothing is sent then.
var ErrSendRecord = errors.New("could not save the record of the send before making it, so nothing was sent")

// SendIntent is one automatic send keepgoing was about to make. Seq orders
// them. Action is the decision it carried out (wake, nudge, reprompt,
// handoff, switch_back, child_notice, new_thread) and Target the target it
// was sent to.
type SendIntent struct {
	Seq    int64     `json:"seq"`
	At     time.Time `json:"at"`
	Action string    `json:"action"`
	Target string    `json:"target"`
	// Key, for a child-error notice, is the set of children it announced.
	Key string `json:"key,omitempty"`
}

// Ledger is the whole of sends.json.
type Ledger struct {
	Version int          `json:"version"`
	Used    *int64       `json:"used"`
	Seq     int64        `json:"seq,omitempty"`
	Pending []SendIntent `json:"pending,omitempty"`
	// Settled is the last record whose outcome a check recorded, kept here
	// beside the records as well as in state.json, so an older binary that
	// rewrites state.json cannot make settled records look unsettled.
	Settled int64 `json:"settled,omitempty"`
}

// maxPending bounds the intents kept. Each check makes at most a few sends,
// and every check that saves its state lets the next reservation drop the
// ones it recorded, so the list stays short unless no check can save.
const maxPending = 64

// LoadLedger reads sends.json. Missing means a new ledger. Malformed,
// unknown-version or incomplete records fail closed without echoing file
// contents. The caller serializes access with the loop's tick lock.
func LoadLedger(dir string) (Ledger, error) {
	zero := int64(0)
	b, err := snapshot.ReadFile(filepath.Join(dir, SendsName))
	if errors.Is(err, os.ErrNotExist) {
		return Ledger{Version: 1, Used: &zero}, nil
	}
	var l Ledger
	if err != nil || json.Unmarshal(b, &l) != nil || l.Version != 1 || l.Used == nil || *l.Used < 0 || l.Seq < 0 || l.Settled < 0 || len(l.Pending) > maxPending {
		return Ledger{}, ErrSendLedger
	}
	return l, nil
}

// LoadSends reads the reserved attempt count.
func LoadSends(dir string) (int64, error) {
	l, err := LoadLedger(dir)
	if err != nil {
		return 0, err
	}
	return *l.Used, nil
}

// Unrecorded returns the intended sends after applied, the last one whose
// outcome state.json has recorded. Each was handed to the adapter, or may
// have been: the check that made it could not save what happened.
func (l Ledger) Unrecorded(applied int64) []SendIntent {
	var out []SendIntent
	for _, p := range l.Pending {
		if p.Seq > applied {
			out = append(out, p)
		}
	}
	return out
}

// Reserve saves the record of an intended send BEFORE an adapter is asked
// to make it, whether or not a send limit is configured. With max_sends set
// it also counts the attempt against the limit. recorded is the last intent
// whose outcome state.json holds; older intents are dropped. It returns the
// new intent's sequence. If the record cannot be saved the caller sends
// nothing: a send that cannot be recorded first could not be counted, and
// the next check would make it again.
//
// Failed or uncertain sends are never refunded. A synced temporary file and
// atomic rename preserve the record across process restarts; this is not a
// power-loss/storage guarantee.
func Reserve(dir string, limit, recorded int64, intent SendIntent) (int64, error) {
	l, err := LoadLedger(dir)
	if err != nil {
		return 0, err
	}
	if limit > 0 && *l.Used >= limit {
		return 0, ErrSendLimit
	}
	used := *l.Used
	if limit > 0 {
		used++
	}
	l.Used = &used
	// Numbering never goes back below what is settled, even after the file
	// was deleted, so a new record cannot be mistaken for a settled one.
	l.Seq = max(l.Seq, recorded, l.Settled) + 1
	intent.Seq = l.Seq
	kept := l.Pending[:0:0]
	for _, p := range l.Pending {
		if p.Seq > recorded {
			kept = append(kept, p)
		}
	}
	kept = trimPending(kept, maxPending-1)
	l.Pending = append(kept, intent)
	l.Settled = max(l.Settled, recorded)
	b, _ := json.Marshal(l)
	if err := saveAllowance(dir, SendsName, b); err != nil {
		return 0, fmt.Errorf("%w: %w", ErrSendRecord, err)
	}
	return intent.Seq, nil
}

// Settle records, beside the records, that every record up to seq has had
// its outcome recorded. It only moves forward. Records up to seq are dropped.
func Settle(dir string, seq int64) error {
	l, err := LoadLedger(dir)
	if err != nil {
		return err
	}
	if seq <= l.Settled {
		return nil
	}
	l.Settled = seq
	kept := l.Pending[:0:0]
	for _, p := range l.Pending {
		if p.Seq > seq {
			kept = append(kept, p)
		}
	}
	l.Pending = kept
	b, _ := json.Marshal(l)
	if err := saveAllowance(dir, SendsName, b); err != nil {
		return ErrSendLedger
	}
	return nil
}

// SettleIntoEpisode settles, in f, the records a check has not yet read,
// before a resume starts a new error episode, so they count in the episode
// that is ending and not in the new one. Each sets the time of the last send
// to the agent, as a check would. It stops at the first brief that may have
// put the task on a second conversation (a handoff or switch-back): that
// record, and those after it, are left for the next check, which holds for a
// person. f.SendsRecorded is moved forward; the caller saves f.
func SettleIntoEpisode(dir string, f *File) error {
	l, err := LoadLedger(dir)
	if err != nil {
		return err
	}
	for _, p := range l.Unrecorded(max(f.SendsRecorded, l.Settled)) {
		if p.Action == "handoff" || p.Action == "switch_back" {
			break
		}
		if p.Action == LaneRetryAction {
			// A lane's failed turn retried by reference: counted for that
			// lane as a made retry, as a check would count it. It is not a
			// send to the supervised session.
			if f.Lanes != nil {
				r := f.Lanes.Retries[p.Key]
				if r.DayStart.IsZero() {
					r.DayStart = p.At
				}
				r.Attempts, r.DayRetries, r.LastAt, r.NoOps = r.Attempts+1, r.DayRetries+1, p.At, 0
				if f.Lanes.Retries == nil {
					f.Lanes.Retries = map[string]LaneRetry{}
				}
				f.Lanes.Retries[p.Key] = r
				f.Lanes.EpisodeRetries++
			}
			f.SendsRecorded = max(f.SendsRecorded, p.Seq)
			continue
		}
		agentSend := p.Action != "stop" && !strings.HasPrefix(p.Action, "stop:") && p.Action != "new_thread" && !strings.HasPrefix(p.Action, "report:")
		if agentSend && p.At.After(f.LastWakeAt) {
			f.LastWakeAt = p.At
		}
		f.SendsRecorded = max(f.SendsRecorded, p.Seq)
	}
	return nil
}

// LaneRetryAction is a lane retry's record (lane recovery, keyed by the
// lane's hash).
const LaneRetryAction = "child_retry"

// trimPending keeps at most limit records, dropping the oldest first. A
// brief to another conversation (handoff, switch_back) is never dropped: the
// next check must hold for a person over it. A lane stop goes before
// anything else, since stopping a lane again is harmless.
func trimPending(p []SendIntent, limit int) []SendIntent {
	drop := func(match func(SendIntent) bool) {
		for i := 0; i < len(p) && len(p) > limit; {
			if match(p[i]) {
				p = append(p[:i:i], p[i+1:]...)
				continue
			}
			i++
		}
	}
	drop(func(s SendIntent) bool { return strings.HasPrefix(s.Action, "stop:") })
	drop(func(s SendIntent) bool { return s.Action != "handoff" && s.Action != "switch_back" })
	return p
}

// RefundSend gives back the reservation seq. It is only for an attempt the
// provider answered, in a structured result, that it did not act on at all,
// or that the adapter refused on this machine before anything could reach
// the agent: nothing was sent and nothing was started. The intent is dropped, and with
// max_sends set the attempt is no longer counted. A failed or uncertain send
// is never refunded.
func RefundSend(dir string, max, seq int64) error {
	l, err := LoadLedger(dir)
	if err != nil {
		return err
	}
	used := *l.Used
	if max > 0 && used > 0 {
		used--
	}
	l.Used = &used
	kept := l.Pending[:0:0]
	for _, p := range l.Pending {
		if p.Seq != seq {
			kept = append(kept, p)
		}
	}
	l.Pending = kept
	b, _ := json.Marshal(l)
	if err := saveAllowance(dir, SendsName, b); err != nil {
		return ErrSendLedger
	}
	return nil
}

// saveAllowance syncs the replacement before publishing it. Callers hold tick.lock.
// As with other runtime records, this is not a power-loss/storage guarantee.
func saveAllowance(dir, name string, b []byte) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".allowance-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(b, '\n')); err == nil {
		err = snapshot.Sync(f)
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := snapshot.Replace(f.Name(), filepath.Join(dir, name)); err != nil {
		return err
	}
	return nil
}
