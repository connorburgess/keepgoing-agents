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
// turn. It is tied to the current failure by time: observedAt, BB's clock when
// the turn failed, must not be earlier than BB's createdAt of the latest
// turn/started event. Both are BB's clock, so skew against this host does not
// matter. Only kinds and numbers are read; the reason text is not.
//
// The plugin is optional. A missing command, a failed call, another thread's
// answer or an unrecognised shape leaves the facts false, which keeps the
// notice until it ages out. The status call is made only when the decision
// alone would drop the notice.
func (a *Adapter) retryService(ctx context.Context, turnStartedMs int64) decide.RetryService {
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
	out, err := a.runBounded(ctx, "provider-retry", "explain", a.cfg.Thread, "--json")
	if err != nil || json.Unmarshal(out, &explain) != nil || explain.Thread != a.cfg.Thread {
		return s
	}
	d := explain.Diagnostic
	if d == nil || d.Decision == nil || d.ObservedAt == nil || (d.Decision.Kind != "decline" && d.Decision.Kind != "retry") {
		return s
	}
	s.DecisionKnown = true
	s.Declined = d.Decision.Kind == "decline"
	s.ForCurrentTurn = turnStartedMs > 0 && *d.ObservedAt >= turnStartedMs
	if !s.Declined || !s.ForCurrentTurn {
		return s
	}
	var status struct {
		Retries *[]json.RawMessage `json:"retries"`
	}
	out, err = a.runBounded(ctx, "provider-retry", "status", a.cfg.Thread, "--json")
	if err != nil || json.Unmarshal(out, &status) != nil || status.Retries == nil {
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
