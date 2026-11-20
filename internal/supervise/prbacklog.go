package supervise

import (
	"context"
	"errors"
	"time"

	"github.com/looptender/looptender/internal/adapter"
	"github.com/looptender/looptender/internal/prbacklog"
	"github.com/looptender/looptender/internal/prompt"
)

type backlogDelivery struct {
	digest, target, conversation string
	newConversation              bool
}

func (t *tick) withBacklog(a adapter.Adapter, m prompt.Message) (prompt.Message, *backlogDelivery, bool) {
	p := t.loop.PRBacklog.Policy()
	c := t.st.PRBacklog
	if c.Due(p, t.now) {
		r := prbacklog.Report{Failure: "budget_unavailable"}
		// Do not borrow any of the send/report reserve or start an optional query
		// with less than two seconds (including bounded process cleanup). A skipped attempt is throttled too.
		if left := t.left(); left >= 2*time.Second {
			timeout := min(left-time.Second, prbacklog.Timeout)
			ctx, cancel := context.WithTimeout(t.ctx, timeout)
			run := t.opts.BacklogQuery
			if run == nil {
				run = prbacklog.ExecGH("gh")
			}
			started := t.opts.Clock()
			r = prbacklog.Collect(ctx, run, p, func() time.Time { return t.now.Add(t.opts.Clock().Sub(started)) })
			cancel()
		}
		c = prbacklog.Update(c, p, r, t.now)
		t.st.PRBacklog = c
	}
	// Optional work must not put a wake past a new stop, hold or retirement.
	// The control writer and tick lock still define the existing concurrency
	// boundary; this is no cancellation guarantee for an already sent wake.
	if t.ctx.Err() != nil || !t.routeChangeAllowed() {
		return m, nil, false
	}
	text, digest := prbacklog.Notice(p, c, t.now)
	conversation, _ := conversationKey(a)
	if len(conversation) > 256 {
		conversation = ""
	}
	fresh := t.newConversation(t.res.Target)
	if !c.WantsNotice(digest, t.res.Target, conversation, fresh) {
		return m, nil, true
	}
	m.Text += text
	return m, &backlogDelivery{digest, t.res.Target, conversation, fresh}, true
}
func (t *tick) noteBacklog(n *backlogDelivery, err error) {
	if n == nil {
		return
	}
	var refused adapter.ErrNotStarted
	delivered := err == nil && !errors.As(err, &refused)
	t.st.PRBacklog.Note(n.digest, n.target, n.conversation, n.newConversation, delivered)
}
