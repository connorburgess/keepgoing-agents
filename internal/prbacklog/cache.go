package prbacklog

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

// Cache is bounded aggregate evidence, bound to the complete policy. No PR
// names, text, labels, branches, command output or credentials are persisted.
type Cache struct {
	Version     int       `json:"version"`
	Binding     string    `json:"binding"`
	AttemptedAt time.Time `json:"attemptedAt"`
	GoodAt      time.Time `json:"goodAt"`
	Good        *Summary  `json:"good"`
	Failure     string    `json:"failure"`
	Notice      *Delivery `json:"notice"`
	Invalid     bool      `json:"invalid,omitempty"`
}

type Delivery struct {
	Digest       string `json:"digest"`
	Target       string `json:"target"`
	Conversation string `json:"conversation"`
	Attempts     int    `json:"attempts"`
	Delivered    bool   `json:"delivered"`
}

func (c *Cache) UnmarshalJSON(b []byte) error {
	if len(b) > 8192 {
		*c = Cache{Invalid: true}
		return nil
	}
	type plain Cache
	var v plain
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*c = Cache(v)
	var fields map[string]json.RawMessage
	if json.Unmarshal(b, &fields) != nil {
		c.Invalid = true
		return nil
	}
	for _, k := range []string{"version", "binding", "attemptedAt", "goodAt", "good", "failure", "notice"} {
		if v, ok := fields[k]; !ok || (strings.TrimSpace(string(v)) == "null" && k != "good" && k != "notice") {
			c.Invalid = true
		}
	}
	if c.Good != nil {
		var good map[string]json.RawMessage
		_ = json.Unmarshal(fields["good"], &good)
		for _, k := range []string{"open", "draft", "held", "superseded", "blocked", "unknown", "branchDependencies", "additions", "deletions", "files"} {
			if v, ok := good[k]; !ok || strings.TrimSpace(string(v)) == "null" {
				c.Invalid = true
			}
		}
	}
	if c.Notice != nil {
		var notice map[string]json.RawMessage
		_ = json.Unmarshal(fields["notice"], &notice)
		for _, k := range []string{"digest", "target", "conversation", "attempts", "delivered"} {
			if v, ok := notice[k]; !ok || strings.TrimSpace(string(v)) == "null" {
				c.Invalid = true
			}
		}
	}
	return nil
}

func (c *Cache) Valid(p Policy, now time.Time) bool {
	if c == nil || c.Invalid || c.Version != 1 || c.Binding != p.Binding() || c.AttemptedAt.IsZero() || c.AttemptedAt.After(now) {
		return false
	}
	switch c.Failure {
	case "", "query_failed", "invalid_response", "output_limit", "inventory_changed", "partial_labels", "inventory_capped", "budget_unavailable":
	default:
		return false
	}
	if c.Good == nil {
		if !c.GoodAt.IsZero() || c.Failure == "" {
			return false
		}
	} else if !c.Good.Valid() || c.GoodAt.IsZero() || c.GoodAt.After(c.AttemptedAt) || c.GoodAt.After(now) || c.Good.Oldest.After(c.GoodAt) {
		return false
	}
	if n := c.Notice; n != nil {
		if _, err := hex.DecodeString(n.Digest); err != nil {
			return false
		}
		if len(n.Digest) != 64 || n.Target == "" || len(n.Target) > 256 || len(n.Conversation) > 256 || n.Attempts < 1 || n.Attempts > 3 {
			return false
		}
	}
	return true
}

func (c *Cache) Due(p Policy, now time.Time) bool {
	return !c.Valid(p, now) || now.Sub(c.AttemptedAt) >= p.Interval
}

// Update retains the last good aggregate after a failed or partial refresh.
func Update(c *Cache, p Policy, r Report, now time.Time) *Cache {
	if !c.Valid(p, now) {
		c = &Cache{Version: 1, Binding: p.Binding()}
	}
	c.AttemptedAt = now
	c.Failure = r.Failure
	if r.Complete && r.Failure == "" && r.Summary != nil && r.Summary.Valid() {
		v := *r.Summary
		c.Good = &v
		c.GoodAt = now
	} else if c.Failure == "" {
		c.Failure = "invalid_response"
	}
	return c
}

func (c *Cache) WantsNotice(digest, target, conversation string, newConversation bool) bool {
	n := c.Notice
	return n == nil || n.Digest != digest || n.Target != target || n.Conversation != conversation || newConversation || (!n.Delivered && n.Attempts < 3)
}

func (c *Cache) Note(digest, target, conversation string, newConversation, delivered bool) {
	if c.Notice == nil || c.Notice.Digest != digest || c.Notice.Target != target || c.Notice.Conversation != conversation || newConversation {
		c.Notice = &Delivery{Digest: digest, Target: target, Conversation: conversation}
	}
	if c.Notice.Attempts < 3 {
		c.Notice.Attempts++
	}
	c.Notice.Delivered = delivered
}
