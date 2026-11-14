package bb

import (
	"context"
	"encoding/json"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/decide"
)

// retryPluginTimeout bounds each of the two calls to BB's retry plugin. They
// run inside the observation, at most once each per check, and only while a
// fresh retry notice sits on an errored thread.
const retryPluginTimeout = 5 * time.Second

// maxRetryPluginOutput is the most plugin output that is parsed. A decision
// is a few hundred bytes and a pending list a few rows; anything larger is
// not an answer this code recognises, and is treated as unknown.
const maxRetryPluginOutput = 256 << 10

// retryService asks BB's optional provider-retry plugin what it decided for
// this thread's last failed turn and whether it has a retry pending.
//
//	bb provider-retry explain <thread> --json
//	  {"threadId":"...","diagnostic":null}
//	  {"threadId":"...","diagnostic":{"requestId":"...","observedAt":<ms>,
//	    "availability":{...},"decision":{"kind":"decline","reason":"..."}}}
//	  {"threadId":"...","diagnostic":{...,"decision":{"kind":"retry","reason":"...","sendAt":<ms>}}}
//	bb provider-retry status <thread> --json
//	  {"retries":[...]}
//
// The plugin keeps one decision per thread, the one for its latest failed
// turn. It is tied to the current failure by time: observedAt, the plugin's
// time when the turn failed, must not be earlier than BB's createdAt of the
// latest turn/started event, and must not be later than this check. Both
// times are stamped by BB. Whether one process stamps both has not been
// verified; if a remote host stamps events from a clock behind the server's,
// a decline for an earlier turn could look current. The thread is in error
// either way, so the cost is recovering it before its notice aged out. Only
// kinds and numbers are read; the reason text is not.
//
// The plugin is optional. A missing command, a failed call, another thread's
// answer or an unrecognised shape leaves the facts false, which keeps the
// notice until it ages out. The status call is made only when the decision
// alone would drop the notice.
func (a *Adapter) retryService(ctx context.Context, thread string, turnStartedMs int64) decide.RetryService {
	var s decide.RetryService
	var explain struct {
		Thread     string `json:"threadId"`
		Diagnostic *struct {
			ObservedAt *int64 `json:"observedAt"`
			Decision   *struct {
				Kind string `json:"kind"`
			} `json:"decision"`
		} `json:"diagnostic"`
	}
	out, err := a.runBounded(ctx, "provider-retry", "explain", thread, "--json")
	if err != nil || len(out) > maxRetryPluginOutput || json.Unmarshal(out, &explain) != nil || explain.Thread != thread {
		return s
	}
	d := explain.Diagnostic
	if d == nil || d.Decision == nil || d.ObservedAt == nil || (d.Decision.Kind != "decline" && d.Decision.Kind != "retry") {
		return s
	}
	s.DecisionKnown = true
	s.Declined = d.Decision.Kind == "decline"
	// A decision stamped after this check cannot be about what it observed.
	s.ForCurrentTurn = turnStartedMs > 0 && *d.ObservedAt >= turnStartedMs && *d.ObservedAt <= a.now().UnixMilli()
	if !s.Declined || !s.ForCurrentTurn {
		return s
	}
	var status struct {
		Retries *[]json.RawMessage `json:"retries"`
	}
	out, err = a.runBounded(ctx, "provider-retry", "status", thread, "--json")
	if err != nil || len(out) > maxRetryPluginOutput || json.Unmarshal(out, &status) != nil || status.Retries == nil {
		return s
	}
	s.PendingKnown, s.Pending = true, len(*status.Retries)
	return s
}

// runBounded runs one bb command with the plugin timeout.
func (a *Adapter) runBounded(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, retryPluginTimeout)
	defer cancel()
	return a.run(ctx, "", args...)
}
