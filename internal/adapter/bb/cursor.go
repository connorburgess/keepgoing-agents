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
)

// bb returns log events oldest first and pages forward with --after-seq, so
// the newest events are only reachable by reading everything after a known
// position. The cursor remembers that position and a running summary of the
// events seen so far, so each tick reads only what is new.

const pageSize = 500

// Cursor is the persisted scan position and summary for one thread.
type Cursor struct {
	ChildOutcomeSeq int64             `json:"childOutcomeSeq,omitempty"`
	Thread          string            `json:"thread"`
	LastSeq         int64             `json:"lastSeq"`
	TurnSeq         int64             `json:"turnSeq,omitempty"` // latest turn start
	Error           *errorSignal      `json:"error,omitempty"`   // latest error since that turn started
	Blocked         bool              `json:"blocked,omitempty"` // latest rate-limit window blocked
	ResetsAt        time.Time         `json:"resetsAt,omitempty"`
	Pending         map[string]string `json:"pending,omitempty"` // interaction id -> "pending"
}

type errorSignal struct {
	Seq       int64             `json:"seq"`
	Class     decide.ErrorClass `json:"class"`
	WillRetry bool              `json:"willRetry,omitempty"`
}

type event struct {
	Seq  int64           `json:"seq"`
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// apply folds one event into the summary.
func (c *Cursor) apply(e event) {
	c.LastSeq = e.Seq
	if childOutcome(e, c.Thread) {
		c.ChildOutcomeSeq = e.Seq
	}
	switch {
	case e.Type == "turn/started" || e.Type == "client/turn/requested":
		// A new turn makes earlier errors stale.
		c.TurnSeq, c.Error = e.Seq, nil
	case e.Type == "provider/error":
		var d struct {
			WillRetry *bool `json:"willRetry"`
			Info      struct {
				Category     string `json:"category"`
				HTTPStatus   *int   `json:"httpStatusCode"`
				ProviderCode string `json:"providerCode"`
			} `json:"errorInfo"`
		}
		if json.Unmarshal(e.Data, &d) == nil {
			c.Error = &errorSignal{Seq: e.Seq, Class: firstKnown(d.Info.Category, httpCode(d.Info.HTTPStatus), d.Info.ProviderCode), WillRetry: d.WillRetry != nil && *d.WillRetry}
		}
	case e.Type == "system/error" || strings.HasSuffix(e.Type, "/failed"):
		var d struct {
			Code string `json:"code"`
		}
		if json.Unmarshal(e.Data, &d) == nil {
			c.Error = &errorSignal{Seq: e.Seq, Class: adapter.ClassifyCode(d.Code)}
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
		if b, err := os.ReadFile(p); err == nil && json.Unmarshal(b, &c) == nil && c.Thread == a.cfg.Thread {
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
	if os.Rename(tmp, p) != nil {
		return false
	}
	a.mem = &c
	return true
}

// scan reads every event after the cursor and returns the updated summary.
// ok is false when the log could not be read; the summary is then stale.
func (a *Adapter) scan(ctx context.Context) (Cursor, bool) {
	c := a.loadCursor()
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
				c.apply(e)
				progressed = true
			}
		}
		if len(events) < pageSize || !progressed {
			return c, a.saveCursor(c)
		}
	}
	return c, false // An incomplete scan must not authorize an early check.
}
