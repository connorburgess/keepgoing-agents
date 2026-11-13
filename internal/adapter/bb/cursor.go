package bb

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/evidence"
	"github.com/connorburgess/keepgoing-agents/internal/snapshot"
)

// bb returns log events oldest first and pages forward with --after-seq, so
// the newest events are only reachable by reading everything after a known
// position. The cursor remembers that position and a running summary of the
// events seen so far, so each tick reads only what is new.

const pageSize = 500

// Cursor is the persisted scan position and summary for one thread.
type Cursor struct {
	RetrySchema     int    `json:"retrySchema,omitempty"`
	ProviderThread  string `json:"providerThread,omitempty"`
	ChildOutcomeSeq int64  `json:"childOutcomeSeq,omitempty"`
	Thread          string `json:"thread"`
	LastSeq         int64  `json:"lastSeq"`
	TurnSeq         int64  `json:"turnSeq,omitempty"` // latest turn start
	// TurnStartedMs is BB's own createdAt of the latest turn/started event, in
	// epoch milliseconds, unbounded. It is compared only with other BB times.
	TurnStartedMs int64             `json:"turnStartedMs,omitempty"`
	Error         *errorSignal      `json:"error,omitempty"`   // latest error since that turn started
	Blocked       bool              `json:"blocked,omitempty"` // latest rate-limit window blocked
	ResetsAt      time.Time         `json:"resetsAt,omitempty"`
	Pending       map[string]string `json:"pending,omitempty"` // interaction id -> "pending"
	// LastEventAt is the bounded time of the newest event that is not
	// requested input (client/...), and
	// ScannedAt the local time of the last complete scan. See eventTime.
	LastEventAt time.Time `json:"lastEventAt,omitempty"`
	ScannedAt   time.Time `json:"scannedAt,omitempty"`
}

// eventTime bounds BB's createdAt for an event by what this supervisor knows
// locally. It is never later than seenAt, the scan that read the event, so a
// source clock running ahead cannot keep evidence fresh. It is never earlier
// than the previous complete scan, which did not contain the event, so a
// source clock running behind (a remote BB host) cannot age evidence early.
// The first scan of a log has no earlier scan, so old events keep their own
// time there. A missing or unusable createdAt is the scan time.
func (c *Cursor) eventTime(e event, seenAt time.Time) time.Time {
	t := e.time()
	if t.IsZero() || t.After(seenAt) {
		return seenAt
	}
	if !c.ScannedAt.IsZero() && t.Before(c.ScannedAt) {
		t = c.ScannedAt
		if t.After(seenAt) { // the local clock moved backwards between scans
			t = seenAt
		}
	}
	return t
}

type errorSignal struct {
	Evidence   evidence.Detail   `json:"evidence"`
	Seq        int64             `json:"seq"`
	Class      decide.ErrorClass `json:"class"`
	WillRetry  bool              `json:"willRetry,omitempty"`
	RetryKnown bool              `json:"retryKnown,omitempty"`
	// At is the bounded event time (see Cursor.eventTime). For a retry notice
	// it is what the freshness interval is measured from. Zero only in a
	// summary saved before signals carried a time.
	At time.Time `json:"at,omitempty"`
}

type event struct {
	Seq  int64           `json:"seq"`
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
	// CreatedAt is Unix milliseconds in BB. It is decoded leniently so an
	// unexpected shape loses only the event time, never the whole scan.
	CreatedAt json.RawMessage `json:"createdAt"`
}

// minEventMillis and maxEventMillis reject values that are not plausible Unix
// millisecond timestamps (seconds, microseconds, zero or negative numbers).
const (
	minEventMillis = int64(1_000_000_000_000) // 2001-09-09
	maxEventMillis = int64(9_999_999_999_999) // 2286-11-20
)

func (e event) time() time.Time {
	var ms int64
	if len(e.CreatedAt) == 0 || json.Unmarshal(e.CreatedAt, &ms) != nil || ms < minEventMillis || ms > maxEventMillis {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

// apply folds one event into the summary. seenAt is the local scan time.
func (c *Cursor) apply(e event, seenAt time.Time) {
	c.LastSeq = e.Seq
	when := c.eventTime(e, seenAt)
	// Requested input (a person's message, a child notice, this supervisor's
	// own nudge) says nothing about whether the turn is alive.
	if !strings.HasPrefix(e.Type, "client/") {
		c.LastEventAt = when
	}
	if childOutcome(e, c.Thread) {
		c.ChildOutcomeSeq = e.Seq
	}
	switch {
	case e.Type == "client/turn/requested":
		// Input can be queued behind a reconnecting turn; it is not recovery.
		c.TurnSeq = e.Seq
	case e.Type == "turn/started":
		// A new turn makes earlier errors stale.
		c.TurnSeq, c.Error = e.Seq, nil
		c.TurnStartedMs = 0
		if at := e.time(); !at.IsZero() {
			c.TurnStartedMs = at.UnixMilli()
		}
		var d struct {
			ProviderThread string `json:"providerThreadId"`
		}
		_ = json.Unmarshal(e.Data, &d)
		c.ProviderThread = d.ProviderThread
	case e.Type == "provider/error":
		var d struct {
			ProviderThread string `json:"providerThreadId"`
			WillRetry      *bool  `json:"willRetry"`
			Info           struct {
				Category     string `json:"category"`
				HTTPStatus   *int   `json:"httpStatusCode"`
				ProviderCode string `json:"providerCode"`
			} `json:"errorInfo"`
		}
		if json.Unmarshal(e.Data, &d) == nil {
			if d.ProviderThread != "" && c.ProviderThread != "" && d.ProviderThread != c.ProviderThread {
				return
			}
			if c.ProviderThread == "" {
				c.ProviderThread = d.ProviderThread
			}
			c.Error = &errorSignal{Evidence: evidence.Clean(evidence.Detail{Source: "bb_provider_error", Category: d.Info.Category, Code: d.Info.ProviderCode, HTTPStatus: d.Info.HTTPStatus}), Seq: e.Seq, Class: firstKnown(d.Info.Category, httpCode(d.Info.HTTPStatus), d.Info.ProviderCode), WillRetry: d.WillRetry != nil && *d.WillRetry, RetryKnown: d.WillRetry != nil, At: when}
		}
	case e.Type == "item/agentMessage/delta" || e.Type == "item/started" || e.Type == "turn/completed":
		// Consume event metadata only. Tool output can continue independently of
		// a disconnected model stream and is deliberately not a recovery signal.
		var d struct {
			ProviderThread string `json:"providerThreadId"`
			ItemID         string `json:"itemId"`
			Status         string `json:"status"`
			Item           struct {
				ID   string `json:"id"`
				Type string `json:"type"`
			} `json:"item"`
		}
		if json.Unmarshal(e.Data, &d) != nil || c.ProviderThread == "" || d.ProviderThread != c.ProviderThread || c.Error == nil {
			return
		}
		switch {
		case e.Type == "item/agentMessage/delta" && d.ItemID != "":
			c.Error = nil
		case e.Type == "item/started" && d.Item.ID != "" && d.Item.Type == "agentMessage":
			c.Error = nil
		case e.Type == "turn/completed" && d.Status == "completed":
			c.Error = nil
		case e.Type == "turn/completed" && (d.Status == "failed" || d.Status == "interrupted"):
			// The scan may still fail on a later page. Do not mutate the cached
			// summary through its shared pointer before the scan is committed.
			ended := *c.Error
			ended.WillRetry, ended.RetryKnown = false, true
			c.Error = &ended
		}
	case e.Type == "system/error" || strings.HasSuffix(e.Type, "/failed"):
		var d struct {
			Code string `json:"code"`
		}
		if json.Unmarshal(e.Data, &d) == nil {
			source := "bb_system_error"
			if e.Type != "system/error" {
				source = "bb_failure_event"
			}
			c.Error = &errorSignal{Evidence: evidence.Clean(evidence.Detail{Source: source, Code: d.Code}), Seq: e.Seq, Class: adapter.ClassifyCode(d.Code), At: when}
		}
	case e.Type == "provider/rateLimits/updated":
		c.Blocked, c.ResetsAt = rateLimits(e.Data)
	case e.Type == "system/interaction/lifecycle":
		var d struct {
			Interaction struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"interaction"`
		}
		if json.Unmarshal(e.Data, &d) == nil && d.Interaction.ID != "" {
			if c.Pending == nil {
				c.Pending = map[string]string{}
			}
			if d.Interaction.Status == "pending" {
				c.Pending[d.Interaction.ID] = "pending"
			} else {
				delete(c.Pending, d.Interaction.ID)
			}
		}
	}
}

func (a *Adapter) cursorPath() string {
	if a.stateDir == "" {
		return ""
	}
	return filepath.Join(a.stateDir, "bb-cursor.json")
}

func (a *Adapter) loadCursor() Cursor {
	if a.mem != nil && a.mem.Thread == a.cfg.Thread {
		return *a.mem
	}
	var c Cursor
	if p := a.cursorPath(); p != "" {
		if b, err := snapshot.ReadFile(p); err == nil && json.Unmarshal(b, &c) == nil && c.Thread == a.cfg.Thread {
			return c
		}
	}
	return Cursor{Thread: a.cfg.Thread}
}

func (a *Adapter) saveCursor(c Cursor) bool {
	p := a.cursorPath()
	if p == "" {
		a.mem = &c
		return true
	}
	if err := os.MkdirAll(a.stateDir, 0o700); err != nil {
		return false
	}
	b, _ := json.Marshal(c)
	tmp := p + ".tmp"
	if os.WriteFile(tmp, append(b, '\n'), 0o600) != nil {
		return false
	}
	if snapshot.Replace(tmp, p) != nil {
		return false
	}
	a.mem = &c
	return true
}

// scan reads every event after the cursor and returns the updated summary.
// ok is false when the log could not be read; the summary is then stale.
func (a *Adapter) scan(ctx context.Context) (Cursor, bool) {
	c := a.loadCursor()
	if c.RetrySchema != 1 {
		// Rebuild old summaries once: they may have consumed recovery events or
		// cleared errors on queued input. PollWake retains the old LastSeq as
		// its delivery watermark, so this scan cannot replay old child notices.
		c = Cursor{Thread: a.cfg.Thread, RetrySchema: 1}
	}
	seenAt := a.now().UTC()
	if c.LastSeq > 0 && c.LastEventAt.IsZero() {
		// A summary saved before event times were kept: age it from this scan,
		// so it is bounded without rereading the log.
		c.LastEventAt = seenAt
	}
	if c.Error != nil && c.Error.At.IsZero() {
		// Likewise for its error signal. Copy, because the cached summary
		// shares this pointer and the scan may still fail.
		aged := *c.Error
		aged.At = seenAt
		c.Error = &aged
	}
	for pages := 0; pages < 10000; pages++ {
		out, err := a.run(ctx, "", "thread", "log", a.cfg.Thread, "--json", "--after-seq", strconv.FormatInt(c.LastSeq, 10), "--limit", strconv.Itoa(pageSize))
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
		if len(events) < pageSize || !progressed {
			c.ScannedAt = seenAt
			return c, a.saveCursor(c)
		}
	}
	return c, false // An incomplete scan must not authorize an early check.
}
