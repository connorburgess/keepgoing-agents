package bb

import (
	"context"
	"encoding/json"
)

// PollWake consumes new child outcome notices. Normal observations also advance
// this same cursor, so already-observed notices cannot cause extra early checks.
// Persist before returning true: an interrupted early check may be lost, but it
// cannot replay after restart. Ordinary interval checks remain the backstop.
func (a *Adapter) PollWake(ctx context.Context) bool {
	before := a.loadCursor().LastSeq
	c, ok := a.scan(ctx)
	return ok && ctx.Err() == nil && c.ChildOutcomeSeq > before && c.ChildOutcomeSeq >= c.TurnSeq
}

// BB 0.48.1 attaches this taxonomy to client/turn/requested. Decode only its
// structured metadata, never input, thread names, or terminal output. A later
// turn start supersedes the notice. Retries are not new child completions.
func childOutcome(e event, parent string) bool {
	if e.Type != "client/turn/requested" {
		return false
	}
	var d struct {
		Initiator string `json:"initiator"`
		Kind      string `json:"systemMessageKind"`
		RetryOf   string `json:"retryOfRequestId"`
		Subject   struct {
			Kind     string `json:"kind"`
			Thread   string `json:"threadId"`
			Count    int    `json:"count"`
			Outcomes []struct {
				Thread string `json:"threadId"`
				Status string `json:"status"`
			} `json:"outcomes"`
		} `json:"systemMessageSubject"`
	}
	if json.Unmarshal(e.Data, &d) != nil || d.Initiator != "system" || d.RetryOf != "" {
		return false
	}
	s := d.Subject
	if len(s.Outcomes) == 0 || len(s.Outcomes) > 200 {
		return false
	}
	seen := map[string]bool{}
	for _, o := range s.Outcomes {
		if !childIDPattern.MatchString(o.Thread) || o.Thread == parent || seen[o.Thread] {
			return false
		}
		seen[o.Thread] = true
		if o.Status != "completed" && o.Status != "failed" && o.Status != "interrupted" {
			return false
		}
	}
	if d.Kind == "child-outcome-batch" {
		return s.Kind == "thread-batch" && s.Count == len(s.Outcomes)
	}
	return s.Kind == "thread" && len(s.Outcomes) == 1 && s.Thread == s.Outcomes[0].Thread && d.Kind == "child-"+s.Outcomes[0].Status
}
