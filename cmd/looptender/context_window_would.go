package main

import (
	"fmt"
	"io"
	"time"

	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/contextwindow"
	"github.com/looptender/looptender/internal/control"
	"github.com/looptender/looptender/internal/rungraph"
	"github.com/looptender/looptender/internal/state"
)

// contextWindowWouldVersion is the layout of the contextWindowReset object.
const contextWindowWouldVersion = 1

// contextWindowWould is what the context-threshold reset would do at the
// loop's next turn boundary, from the records status reads. It is a preview:
// this build never resets, compacts, sends or asks for a checkpoint because
// of it. A loop without loop.context_window.reset_at has Configured false,
// rule configuration, reason not_configured and text "not configured".
type contextWindowWould struct {
	Version    int      `json:"version"`
	Preview    bool     `json:"preview"`
	Configured bool     `json:"configured"`
	WouldReset bool     `json:"wouldReset"`
	Rule       string   `json:"rule"`
	Reason     string   `json:"reason"`
	Threshold  *float64 `json:"threshold,omitempty"`
	// Estimated is BB's flag on the reading the decision used, when it got
	// that far.
	Estimated            *bool  `json:"estimated,omitempty"`
	MaxReadingAgeSeconds int64  `json:"maxReadingAgeSeconds,omitempty"`
	MinIntervalSeconds   int64  `json:"minIntervalSeconds,omitempty"`
	DailyCap             int    `json:"dailyCap,omitempty"`
	Text                 string `json:"text"`
}

// contextWindowPreviewNote marks every configured line as a preview.
const contextWindowPreviewNote = "preview only: this build never resets, compacts, sends or asks for a checkpoint"

// contextWindowWouldOf decides as contextwindow.Decide would at the next turn
// boundary, from recorded state only: the latest context reading, the last
// check's observation, the lane listing the run graph recorded at that
// check, and the controls. It runs no command and reads no message text.
// This build makes no resets, so none is recorded and spacing never applies.
func contextWindowWouldOf(cfg *config.Config, st state.File, ctl control.File, controlUnreadable bool, graph *rungraph.Graph, now time.Time) contextWindowWould {
	target := st.Target()
	in := contextwindow.Input{
		Now:       now,
		ResetAt:   cfg.Loop.ContextWindowResetAt(),
		Supported: activeBB(cfg, target) != nil,
		Controls: contextwindow.Controls{
			Stop: ctl.Stop, Paused: ctl.Paused, Unreadable: controlUnreadable,
			Held: !ctl.Enabled || ctl.AwaitingUser || ctl.Completed,
		},
	}
	if r := st.Context; r != nil && r.Target == target {
		l := r.Latest
		// Layout 1 always records the flag: the adapter refuses a reading
		// without it. Another layout is not read.
		estimated := l.Estimated
		in.Reading = &contextwindow.Reading{UsedTokens: l.UsedTokens, WindowTokens: l.WindowTokens, Estimated: &estimated, At: l.At,
			Malformed: r.Version != state.ContextVersion}
	}
	c := st.LastCheck
	if c != nil && checkedTarget(c) == target {
		in.Session = contextwindow.Session{
			Observed:      true,
			State:         c.ObservedState,
			AwaitingInput: c.AwaitingInput,
			LimitWait:     st.LimitWaitReport != nil || st.LimitResetsAt.After(now),
			Retrying:      c.RetrySignal != "" || st.LastTurnRetry != nil && st.LastTurnRetry.CheckAt.Equal(c.At),
			Refusal:       st.Refusals.Streak > 0 || st.DeclineHeldTarget != "",
		}
	}
	in.Lanes = lanesAtCheck(graph, target, c)
	d := contextwindow.Decide(in)
	out := contextWindowWould{Version: contextWindowWouldVersion, Preview: true, Configured: in.ResetAt != nil,
		WouldReset: d.WouldReset, Rule: d.Rule, Reason: d.Reason, Text: d.Text(usageSpan)}
	if !out.Configured {
		return out
	}
	threshold := d.Threshold
	out.Threshold, out.Estimated = &threshold, d.Estimated
	out.MaxReadingAgeSeconds, out.MinIntervalSeconds, out.DailyCap = int64(d.MaxReadingAge/time.Second), int64(d.MinInterval/time.Second), d.DailyCap
	out.Text = "context window reset at the next turn boundary: " + out.Text + "; " + contextWindowPreviewNote
	return out
}

func checkedTarget(c *state.Check) string {
	if c.ObservedTarget != "" {
		return c.ObservedTarget
	}
	return c.Target
}

// lanesAtCheck counts the lanes working in the listing the run graph
// recorded at the last check. A listing that is missing, incomplete, of
// another target or older than that check is unverified.
func lanesAtCheck(g *rungraph.Graph, target string, c *state.Check) contextwindow.Lanes {
	if g == nil || g.Waiting == nil || c == nil {
		return contextwindow.Lanes{Unverified: true}
	}
	w := g.Waiting
	if w.Target != target || w.Listing != rungraph.ListingComplete || w.CheckedAt.Before(c.At) {
		return contextwindow.Lanes{Unverified: true}
	}
	n := 0
	for _, child := range w.Children {
		if rungraph.Holds(child.Status) {
			n++
		}
	}
	return contextwindow.Lanes{Working: n}
}

// printContextWindowWould prints the preview line for a configured loop and
// nothing for one without the key.
func printContextWindowWould(w io.Writer, c contextWindowWould) {
	if c.Configured {
		fmt.Fprintln(w, c.Text)
	}
}
