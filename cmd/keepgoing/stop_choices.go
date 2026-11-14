package main

import (
	"context"
	"fmt"
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
	// Immediately: client apply stop takes --immediately, and is given it with
	// every option.
	Immediately bool         `json:"immediately"`
	Options     []stopOption `json:"options"`
}

type stopOption struct {
	Label string `json:"label"`
	Lanes bool   `json:"lanes"`
}

// stopCountTimeout bounds the lane count of one review.
const stopCountTimeout = 8 * time.Second

// stopChoicesFor counts the loop's lanes, read-only, and words the stop's
// options, adding to the review's lines what the options do.
func stopChoicesFor(ctx context.Context, cfg *config.Config, lines []dockText) (*stopChoices, []dockText) {
	ctx, cancel := context.WithTimeout(ctx, stopCountTimeout)
	defer cancel()
	n := supervise.CountLanes(ctx, cfg, supervise.Options{})
	c := &stopChoices{Immediately: true, Options: []stopOption{{Label: "Stop the Conversation", Lanes: false}}}
	lines = append(lines, txt("The stop is carried out when you confirm, as a check does it. If a check of this loop is running then, that check or the next one does it."))
	switch {
	case !n.Applicable:
		lines = append(lines, txt("This loop's agent has no child conversations keepgoing can see, so there are no lanes to stop."))
	case n.Complete && n.Running == 0:
		zero := 0
		c.Lanes = &zero
		lines = append(lines, txt("None of its lanes is running now, so there is nothing more to stop."))
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
		lines = append(lines, txt(lanesLine(n)), txt("Stopping its lanes too reaches 3 levels down and 50 threads in all, leaves another loop's lanes to it, and sends a lane nothing but a stop."))
	}
	return c, lines
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
// the review listed it.
func (c *stopChoices) offers(o holdOptions) bool {
	if !o.Lanes && !o.Now {
		return true
	}
	if c == nil || (o.Now && !c.Immediately) {
		return false
	}
	for _, opt := range c.Options {
		if opt.Lanes == o.Lanes {
			return true
		}
	}
	return false
}
