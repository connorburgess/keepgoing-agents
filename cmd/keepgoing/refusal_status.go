package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// refusalStatus is the refusal gauge as status and the panel show it.
type refusalStatus struct {
	// Text is empty when the loop has never seen a refused turn.
	Text             string     `json:"text,omitempty"`
	LastDay          int        `json:"lastDay"`
	RecoveredLastDay int        `json:"recoveredLastDay"`
	Total            int        `json:"total"`
	Recovered        int        `json:"recovered"`
	Streak           int        `json:"streak"`
	LongestStreak    int        `json:"longestStreak"`
	LastAt           *time.Time `json:"lastAt,omitempty"`
	// Question is the open question about refusals or a restriction, in
	// words; QuestionKind its fixed kind.
	Question     string `json:"question,omitempty"`
	QuestionKind string `json:"questionKind,omitempty"`
}

// comingOption marks answers the question records but no command can carry
// out yet; they are shown, never offered as available.
var comingOption = map[string]bool{"fresh_session": true}

func describeRefusals(g state.RefusalGauge, q *control.Question, now time.Time) refusalStatus {
	day, recovered := g.InLastDay(now)
	r := refusalStatus{LastDay: day, RecoveredLastDay: recovered, Total: g.Total, Recovered: g.Recovered, Streak: g.Streak, LongestStreak: g.LongestStreak}
	if !g.LastAt.IsZero() {
		at := g.LastAt.UTC()
		r.LastAt = &at
	}
	if g.Total > 0 {
		r.Text = fmt.Sprintf("%d in the last 24h, %d recovered by continue; current streak %d (longest %d; %d in all, last %s)", day, recovered, g.Streak, g.LongestStreak, g.Total, g.LastAt.UTC().Format(time.RFC3339))
	}
	if q != nil && (q.Kind == control.QuestionRefused || q.Kind == control.QuestionRestricted) {
		r.QuestionKind = q.Kind
		ids := make([]string, 0, len(q.Options))
		for _, o := range q.Options {
			id := o.ID
			if comingOption[o.ID] {
				id += " (coming, not yet available)"
			}
			ids = append(ids, id)
		}
		what := "the model or provider declined turn after turn"
		if q.Kind == control.QuestionRestricted {
			what = "the provider denied permission for this request or account"
		}
		r.Question = fmt.Sprintf("open since %s: %s; how should this loop proceed? Answers: %s. Until it is answered nothing is sent; keepgoing resume withdraws it and goes on.", q.RaisedAt.UTC().Format(time.RFC3339), what, strings.Join(ids, ", "))
	}
	return r
}
