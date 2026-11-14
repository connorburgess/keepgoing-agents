package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/supervise"
)

// stopChoices are a stop's options in a review (docs/local-clients.md): the
// page and the menu offer one button per option, in these words, and pass
// --immediately with every one, and --lanes with an option that has lanes.
type stopChoices struct {
	// Lanes is how many lanes the stop would stop now, from a fresh read with
	// the stop's own bounds and rules. It is absent when they could not be
	// counted.
	Lanes *int `json:"lanes,omitempty"`
	// LanesAtLeast: Lanes is a lower bound (a listing failed, or something
	// lay past the bounds).
	LanesAtLeast bool `json:"lanesAtLeast,omitempty"`
	// Queued counts the messages queued in lanes that have not started or
	// are idle: those the option with dropQueued would delete. Absent when
	// nothing could be counted.
	Queued *int `json:"queued,omitempty"`
	// Immediately: client apply stop takes --immediately, and is given it with
	// every option.
	Immediately bool         `json:"immediately"`
	Options     []stopOption `json:"options"`
}

type stopOption struct {
	Label string `json:"label"`
	Lanes bool   `json:"lanes"`
	// DropQueued: this option passes --drop-queued. Their text is not kept.
	DropQueued bool `json:"dropQueued,omitempty"`
}

// stopCountTimeout bounds the lane count of one review.
const stopCountTimeout = 8 * time.Second

// stopChoicesFor counts the loop's lanes, read-only, and words the stop's
// options. It changes the review's lines in place: the line that says the
// next check carries out the stop, when the stop is carried out at once,
// and the line that says lanes keep running, by what the options do. A loop
// whose agent has no lanes gets no options: its stop is the plain one.
func stopChoicesFor(ctx context.Context, cfg *config.Config, lines []dockText) (*stopChoices, []dockText) {
	ctx, cancel := context.WithTimeout(ctx, stopCountTimeout)
	defer cancel()
	n := supervise.CountLanes(ctx, cfg, supervise.Options{})
	if !n.Applicable {
		return nil, lines
	}
	c := &stopChoices{Immediately: true, Options: []stopOption{{Label: "Stop the Conversation", Lanes: false}}}
	var lanes []dockText
	if n.Unreachable {
		// Nothing can be carried out from here: the stop is saved, and the
		// next check, which finds bb, carries it out.
		c.Immediately = false
		c.Options = append(c.Options, stopOption{Label: "Stop It and Its Lanes", Lanes: true})
		lanes = append(lanes, txt("bb could not be found from here; the next check will stop the lanes."))
		return c, replaceStopLines(lines, false, lanes)
	}
	queued := n.Queued
	c.Queued = &queued
	switch {
	case n.Complete && n.Running == 0:
		zero := 0
		c.Lanes = &zero
		lanes = append(lanes, txt("None of its lanes is running now, so there is nothing more to stop."))
	default:
		label := "Stop It and Its Lanes"
		if n.Running > 0 || n.Complete {
			running := n.Running
			c.Lanes, c.LanesAtLeast = &running, !n.Complete
			label = fmt.Sprintf("Stop It and Its %d %s", running, map[bool]string{true: "Lane", false: "Lanes"}[running == 1])
			if !n.Complete {
				label = fmt.Sprintf("Stop It and Its %d or More Lanes", running)
			}
		}
		c.Options = append(c.Options, stopOption{Label: label, Lanes: true})
		lanes = append(lanes, txt(lanesLine(n)), txt("Stopping its lanes too reaches 3 levels down and 50 threads in all, leaves another loop's lanes to it, and sends a lane nothing but a stop."))
	}
	if n.Queued > 0 {
		// Deleting what someone wrote is a choice of its own: the text is
		// not kept.
		noun := map[bool]string{true: "Message", false: "Messages"}[n.Queued == 1]
		c.Options = append(c.Options, stopOption{Label: fmt.Sprintf("Stop It, Its Lanes and Their %d Queued %s", n.Queued, noun), Lanes: true, DropQueued: true})
		lanes = append(lanes, txt(fmt.Sprintf("%d message(s) are queued in lanes that have not started. Stopping the lanes keeps them, and BB may run them.", n.Queued)),
			txt("The last choice deletes those messages. What was written in them is not kept anywhere."))
	}
	return c, replaceStopLines(lines, true, lanes)
}

// The stop review's own lines that the options change.
const (
	stopNextCheckLine = "The loop's next check carries out the stop"
	stopOnlyLine      = "This stops the conversation only"
)

// replaceStopLines puts what the options do where the review said the
// next check carries out the stop and that lanes keep running, so the other
// lines keep their place (the time the review expires stays last).
func replaceStopLines(lines []dockText, immediately bool, lanes []dockText) []dockText {
	out := make([]dockText, 0, len(lines)+len(lanes))
	for _, l := range lines {
		switch plain := l.render(time.Time{}); {
		case immediately && strings.HasPrefix(plain, stopNextCheckLine):
			out = append(out, txt("The stop is carried out when you confirm, as a check does it; this review cannot confirm that the session stopped."),
				txt("If a check of this loop is running then, that check or the next one does it."))
		case strings.HasPrefix(plain, stopOnlyLine):
			out = append(out, lanes...)
			lanes = nil
		default:
			out = append(out, l)
		}
	}
	return append(out, lanes...)
}

func lanesLine(n supervise.LaneCount) string {
	what := fmt.Sprintf("%d of its lanes are running now", n.Running)
	if n.Running == 1 {
		what = "1 of its lanes is running now"
	}
	switch {
	case !n.Complete && n.Running == 0:
		what = "Its lanes could not all be read now"
	case !n.Complete:
		what = fmt.Sprintf("At least %d of its lanes are running now", n.Running)
	}
	return what + "."
}

// offers reports whether the review offered what a confirmation asks for: a
// confirmation with no options is always one, and an option is one only if
// the review listed it, with the same lanes and dropQueued.
func (c *stopChoices) offers(o holdOptions) bool {
	if !o.Lanes && !o.Now && !o.DropQueued {
		return true
	}
	if c == nil || (o.Now && !c.Immediately) {
		return false
	}
	for _, opt := range c.Options {
		if opt.Lanes == o.Lanes && opt.DropQueued == o.DropQueued {
			return true
		}
	}
	return false
}
