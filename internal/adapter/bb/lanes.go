package bb

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/snapshot"
)

// A lane is a direct child thread of the supervised thread. This file is
// everything the adapter can do about one, and it is used only when the loop
// has set bb.children.retry_errored:
//
//   - InspectLane reads a lane's status, its pending interactions and the
//     typed fields of its log events, through the same reader as the
//     supervised thread's own log.
//   - RetryLane runs `bb thread retry` for it.
//
// Nothing here sends a message, stops, creates, archives or re-parents a
// thread. No message text is decoded: the log reader unmarshals each event's
// data only into structs of codes, flags, numbers and identifiers, and what
// it keeps between checks holds no identifier of the lane.

// lanePages bounds how much of a lane's log one check reads. A long log is
// read across several checks; until it has been read to the end the lane's
// failure is unknown and it is not retried.
const lanePages = 4

// maxLaneSummaries bounds the stored summaries. It matches the limit on
// children inspected in one listing.
const maxLaneSummaries = 200

// laneSummary is a lane's log summary between checks.
type laneSummary struct {
	Cursor Cursor `json:"cursor"`
	// Complete: the log has been read to its end at least once.
	Complete bool `json:"complete,omitempty"`
}

// lanesFile is bb-lanes.json: the summaries, filed under a keyed hash of each
// lane's ID, and the key. The key is random, made once per target and kept
// only here, in the loop's private state, so the same lane has a different
// name in every loop and a hash cannot be looked up in a table made
// elsewhere. It is not protection from someone who can read this file.
type lanesFile struct {
	Key   string                 `json:"key"`
	Lanes map[string]laneSummary `json:"lanes"`
}

// laneHash is the name a lane, or its provider reference, is filed under.
func (a *Adapter) laneHash(id string) string {
	a.loadLanes()
	mac := hmac.New(sha256.New, a.laneKey)
	mac.Write([]byte(id))
	return hex.EncodeToString(mac.Sum(nil)[:12])
}

func (a *Adapter) lanesPath() string {
	if a.stateDir == "" {
		return ""
	}
	return filepath.Join(a.stateDir, "bb-lanes.json")
}

func (a *Adapter) loadLanes() map[string]laneSummary {
	if a.lanes != nil {
		return a.lanes
	}
	lanes := map[string]laneSummary{}
	if p := a.lanesPath(); p != "" {
		if b, err := snapshot.ReadRecord(p); err == nil {
			var stored lanesFile
			if json.Unmarshal(b, &stored) == nil && stored.Lanes != nil {
				if key, err := hex.DecodeString(stored.Key); err == nil && len(key) == laneKeyBytes {
					lanes, a.laneKey = stored.Lanes, key
				}
			}
		}
	}
	if len(a.laneKey) != laneKeyBytes {
		// No usable file: a new key, and nothing filed under an old one.
		a.laneKey = make([]byte, laneKeyBytes)
		if _, err := rand.Read(a.laneKey); err != nil {
			panic("bb: no random source for the lane key: " + err.Error())
		}
		lanes = map[string]laneSummary{}
	}
	a.lanes = lanes
	return lanes
}

const laneKeyBytes = 32

func (a *Adapter) saveLanes(lanes map[string]laneSummary) bool {
	// Keep the most recently scanned summaries.
	if len(lanes) > maxLaneSummaries {
		keys := make([]string, 0, len(lanes))
		for k := range lanes {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return lanes[keys[i]].Cursor.ScannedAt.After(lanes[keys[j]].Cursor.ScannedAt) })
		for _, k := range keys[maxLaneSummaries:] {
			delete(lanes, k)
		}
	}
	a.lanes = lanes
	p := a.lanesPath()
	if p == "" {
		return true
	}
	if err := os.MkdirAll(a.stateDir, 0o700); err != nil {
		return false
	}
	b, _ := json.Marshal(lanesFile{Key: hex.EncodeToString(a.laneKey), Lanes: lanes})
	tmp := p + ".tmp"
	if os.WriteFile(tmp, append(b, '\n'), 0o600) != nil {
		return false
	}
	return snapshot.Replace(tmp, p) == nil
}

// ForgetLanes implements adapter.LaneRecoverer: the summaries and their key
// are removed, so nothing about a lane outlives the option being switched off.
func (a *Adapter) ForgetLanes() {
	a.lanes, a.laneKey = nil, nil
	if p := a.lanesPath(); p != "" {
		os.Remove(p)
		os.Remove(p + ".tmp")
	}
}

// scanLane reads the lane's log events after its summary, at most lanePages
// pages, and saves the progress. ok is true only when the log was read to its
// end, so the summary describes the lane's latest turn.
func (a *Adapter) scanLane(ctx context.Context, id string) (Cursor, bool) {
	lanes := a.loadLanes()
	key := a.laneHash(id)
	sum := lanes[key]
	c := sum.Cursor
	if c.RetrySchema != summarySchema {
		// A summary derived by an earlier build keeps the error class as that
		// build read it: a refusal or restriction may be cached as unknown
		// or auth, and would be retried. Read the lane's log again from the
		// start, as the supervised thread's own summary is.
		c = Cursor{}
	}
	// The summary holds no thread ID, hashes the provider reference, and
	// keeps no interaction IDs.
	c.Thread, c.HashRefs, c.RetrySchema = "", true, summarySchema
	c.hashRef = a.laneHash
	seenAt := a.now().UTC()
	for pages := 0; pages < lanePages; pages++ {
		out, err := a.run(ctx, "", "thread", "log", id, "--json", "--after-seq", strconv.FormatInt(c.LastSeq, 10), "--limit", strconv.Itoa(pageSize))
		if err != nil || ctx.Err() != nil {
			return c, false
		}
		var events []event
		if json.Unmarshal(out, &events) != nil {
			return c, false
		}
		progressed := false
		for _, e := range events {
			if e.Seq > c.LastSeq {
				c.apply(e, seenAt)
				progressed = true
			}
		}
		c.Pending, c.ChildOutcomeSeq = nil, 0
		if len(events) < pageSize || !progressed {
			c.ScannedAt = seenAt
			lanes[key] = laneSummary{Cursor: c, Complete: true}
			return c, a.saveLanes(lanes)
		}
	}
	// More remains. Keep the progress; the failure is not known yet.
	lanes[key] = laneSummary{Cursor: c}
	a.saveLanes(lanes)
	return c, false
}

// InspectLane implements adapter.LaneRecoverer. It establishes each fact
// from a fresh read and leaves it false when the read failed or did not
// match, so the caller withholds the retry.
func (a *Adapter) InspectLane(ctx context.Context, id string) decide.LaneFacts {
	var f decide.LaneFacts
	if !childIDPattern.MatchString(id) || id == a.cfg.Thread {
		return f
	}
	out, err := a.run(ctx, "", "thread", "show", id, "--json")
	if err != nil || ctx.Err() != nil {
		return f
	}
	if _, bad := envelopeError(out); bad {
		return f
	}
	var resp struct {
		Thread *struct {
			ID         string          `json:"id"`
			Parent     json.RawMessage `json:"parentThreadId"`
			Status     string          `json:"status"`
			Queued     int             `json:"queuedMessageCount"`
			ArchivedAt *int64          `json:"archivedAt"`
			DeletedAt  *int64          `json:"deletedAt"`
		} `json:"thread"`
	}
	if json.Unmarshal(out, &resp) != nil || resp.Thread == nil || resp.Thread.ID != id {
		return f
	}
	t := resp.Thread
	var parent *string
	if len(t.Parent) == 0 || json.Unmarshal(t.Parent, &parent) != nil || parent == nil || *parent != a.cfg.Thread || t.ArchivedAt != nil || t.DeletedAt != nil {
		return f
	}
	f.Verified = true
	f.Errored = t.Status == "error"
	f.Queued = max(0, t.Queued)
	if !f.Errored || f.Queued > 0 {
		return f
	}
	pending, unknown := a.childPending(ctx, id)
	f.InputKnown, f.WaitingOnPerson = !unknown, pending
	if !f.InputKnown || f.WaitingOnPerson {
		return f
	}
	c, ok := a.scanLane(ctx, id)
	if !ok {
		return f
	}
	f.ClassKnown, f.Class = true, decide.ErrUnknown
	sig := signals{class: decide.ErrUnknown, ok: true, blocked: c.Blocked, resetsAt: c.ResetsAt, lastEventAt: c.LastEventAt, turnStartedMs: c.TurnStartedMs}
	if c.Error != nil {
		sig.hasError, sig.class, sig.willRetry, sig.retryAt = true, c.Error.Class, c.Error.WillRetry, c.Error.At
		f.Class = c.Error.Class
	}
	// A blocked subscription window is a rate limit whatever the last error said.
	if sig.blocked && sig.resetsAt.After(a.now()) && (f.Class == decide.ErrUnknown || f.Class == decide.ErrNone || f.Class == decide.ErrNetwork) {
		f.Class = decide.ErrRateLimit
	}
	// The same rules as for the supervised thread: a notice counts while it
	// is fresh and BB's retry plugin has not declined the failure.
	f.Retrying = sig.willRetry && !a.retryExpired(decide.Error, sig) && !decide.RetryNoticeDead(a.retryService(ctx, id, sig.turnStartedMs))
	return f
}

// RetryLane implements adapter.LaneRecoverer via `bb thread retry <lane>`.
func (a *Adapter) RetryLane(ctx context.Context, id string) (adapter.TurnRetry, error) {
	if !childIDPattern.MatchString(id) || id == a.cfg.Thread {
		return adapter.TurnRetry{}, adapter.ErrUnsupported{Op: "lane retry"}
	}
	return a.retryThread(ctx, id)
}
