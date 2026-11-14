package incident

import (
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/decide"
)

const MaxEvaluations = 8

// Decisions captures calls to the pure engine, not commands attempted or facts
// independently verified. Empty captured history means the engine was bypassed.
type Decisions struct {
	Availability string       `json:"availability"`
	Evaluations  []Evaluation `json:"evaluations"`
	Dropped      int          `json:"dropped"`
}

type Evaluation struct {
	Stage  string        `json:"stage"`
	Action string        `json:"action"`
	Cause  string        `json:"cause"`
	Input  DecisionInput `json:"input"`
}

// DecisionInput deliberately projects policy inputs instead of serializing
// decide.Input: observations can contain private child IDs and provider text.
// Durations use integer nanoseconds, preserving negative clock deltas exactly.
type DecisionInput struct {
	Control            Controls      `json:"control"`
	State              string        `json:"state"`
	ErrorClass         string        `json:"errorClass"`
	Queued             int           `json:"queued"`
	Background         int           `json:"background"`
	Gone               bool          `json:"gone"`
	Retrying           bool          `json:"retrying"`
	RetryUnverified    bool          `json:"retryUnverified"`
	AwaitingInput      bool          `json:"awaitingInput"`
	ActiveChildren     int           `json:"activeChildren"`
	ChildrenUnverified bool          `json:"childrenUnverified"`
	AwaitingChildren   int           `json:"awaitingChildren"`
	InWindow           bool          `json:"inWindow"`
	NoProgressReached  bool          `json:"noProgressReached"`
	SendLimitReached   bool          `json:"sendLimitReached"`
	DeadlineReached    bool          `json:"deadlineReached"`
	UnchangedFor       time.Duration `json:"unchangedForNs"`
	SinceWake          time.Duration `json:"sinceWakeNs"`
	StallAfter         time.Duration `json:"stallAfterNs"`
	MinWakeInterval    time.Duration `json:"minWakeIntervalNs"`
	FailoverEligible   bool          `json:"failoverEligible"`
	FailoverAvailable  bool          `json:"failoverAvailable"`
	ResetIn            time.Duration `json:"resetInNs"`
	WaitIfResetWithin  time.Duration `json:"waitIfResetWithinNs"`
	SwitchBackReady    bool          `json:"switchBackReady"`
	Nudges             int           `json:"nudges"`
	MaxNudges          int           `json:"maxNudges"`
	Reprompts          int           `json:"reprompts"`
	MaxReprompts       int           `json:"maxReprompts"`
	SinceLastReprompt  time.Duration `json:"sinceLastRepromptNs"`
	Backoff            time.Duration `json:"backoffNs"`
}

func (h *Decisions) Add(stage string, in decide.Input, out decide.Decision) {
	o := in.Obs
	e := Evaluation{Stage: stage, Action: string(out.Action), Cause: DecisionCause(out), Input: DecisionInput{
		Control: *Gate(in.Control), State: string(o.State), ErrorClass: string(o.ErrorClass),
		Queued: o.Queued, Background: o.Background, Gone: o.Gone, Retrying: o.Retrying,
		RetryUnverified: o.RetryUnverified, AwaitingInput: o.AwaitingInput,
		ActiveChildren: o.ActiveChildren, ChildrenUnverified: o.ChildrenUnverified, AwaitingChildren: len(o.AwaitingChildren),
		InWindow: in.InWindow, NoProgressReached: in.NoProgressReached, SendLimitReached: in.SendLimitReached, DeadlineReached: in.DeadlineReached,
		UnchangedFor: in.UnchangedFor, SinceWake: in.SinceWake, StallAfter: in.StallAfter, MinWakeInterval: in.MinWakeInterval,
		FailoverEligible: in.Failover.Eligible, FailoverAvailable: in.Failover.Available, ResetIn: in.Failover.ResetIn, WaitIfResetWithin: in.Failover.WaitIfResetWithin,
		SwitchBackReady: in.SwitchBackReady, Nudges: in.Nudges, MaxNudges: in.MaxNudges,
		Reprompts: in.Reprompts, MaxReprompts: in.MaxReprompts, SinceLastReprompt: in.SinceLastReprompt, Backoff: in.Backoff,
	}}
	h.Availability = "captured"
	h.Evaluations = append(h.Evaluations, e)
	*h = cleanDecisions(*h)
}

func cleanDecisions(h Decisions) Decisions {
	if h.Availability != "captured" {
		return Decisions{Availability: "unavailable"}
	}
	h.Dropped = max(0, h.Dropped)
	if n := len(h.Evaluations) - MaxEvaluations; n > 0 {
		h.Dropped += n
		h.Evaluations = h.Evaluations[n:]
	}
	// Copy so cleaning caller-owned records does not mutate their slices.
	evals := make([]Evaluation, len(h.Evaluations))
	copy(evals, h.Evaluations)
	h.Evaluations = evals
	for i := range h.Evaluations {
		e := &h.Evaluations[i]
		e.Stage = oneOf(e.Stage, "unavailable", "normal", "deadline", "progress_limit", "send_limit", "child_input", "stop", "switch_back_failed", "handoff_failed", "handoff_deferred", "network_down",
			"timing_gate", "timing_return_at_reset", "timing_step_back_early", "timing_step_down", "timing_step_up", "timing_repair")
		e.Action = cleanAction(e.Action)
		e.Cause = cleanCause(e.Cause)
		e.Input.State = oneOf(e.Input.State, "unavailable", "active", "idle", "error", "stopping", "pending", "unknown")
		e.Input.ErrorClass = oneOf(e.Input.ErrorClass, "unavailable", "none", "rate_limit", "auth", "restricted", "refused", "overloaded", "network", "unknown")
	}
	return h
}

// Causes of a check in which usage-aware timing changed the thread: a step
// up, a step down, a return to the first route, or a repair setting the
// thread to the route on record. A record with one of these carries a timing
// evaluation (AddTiming) with the decision's stable rule and reason codes.
const (
	TimingStepUp   = "timing_step_up"
	TimingStepDown = "timing_step_down"
	TimingReturn   = "timing_return"
	TimingRepair   = "timing_repair"
)

// AddTiming records usage-aware timing's decision beside the engine's: the
// stage is "timing_" and the rule, the action the timing cause, and the cause
// the reason code. All are fixed vocabularies; nothing else is recorded.
func (h *Decisions) AddTiming(rule, cause, reason string) {
	if h.Availability != "captured" {
		h.Availability, h.Evaluations, h.Dropped = "captured", nil, 0
	}
	h.Evaluations = append(h.Evaluations, Evaluation{Stage: "timing_" + rule, Action: cause, Cause: reason})
	*h = cleanDecisions(*h)
}
