// Package supervise runs one tick: observe, decide, act, record.
package supervise

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/state"
	"github.com/connorburgess/keepgoing-agents/internal/window"
)

// Options tune a tick.
type Options struct {
	Now    time.Time // zero means time.Now()
	DryRun bool      // decide and record, but send nothing
	// Fingerprint overrides state.Fingerprint, for tests.
	Fingerprint func([]string) (string, error)
}

// Result is what a tick did, printed as JSON by the CLI.
type Result struct {
	At           time.Time `json:"at"`
	Loop         string    `json:"loop"`
	Control      string    `json:"control"`
	State        string    `json:"state"`
	ErrorClass   string    `json:"errorClass,omitempty"`
	Action       string    `json:"action"`
	Reason       string    `json:"reason"`
	Window       string    `json:"window"`
	UnchangedFor string    `json:"unchangedFor,omitempty"`
	Reprompts    int       `json:"reprompts,omitempty"`
	DryRun       bool      `json:"dryRun,omitempty"`
	Warning      string    `json:"warning,omitempty"`
}

// Tick performs one supervision pass. The caller holds the lock.
func Tick(ctx context.Context, cfg *config.Config, a adapter.Adapter, opts Options) (Result, error) {
	loop := cfg.Loop
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()
	res := Result{At: now, Loop: loop.Name, Action: string(decide.None), DryRun: opts.DryRun}

	ctl, err := control.Load(loop.StateDir)
	if err != nil {
		return res, err
	}
	res.Control = ctl.Summary()
	st, err := state.Load(loop.StateDir)
	if err != nil {
		return res, err
	}
	if !ctl.Stop {
		st.StopRequestedSent = false
	}

	// Disabled, paused, or awaiting the user: do not even touch the provider.
	gate := ctl.Gate()
	if !gate.Enabled || gate.Paused || gate.AwaitingUser {
		if !gate.Enabled {
			res.Reason = "disabled"
		} else if gate.Paused {
			res.Reason = "paused"
		} else {
			res.Reason = "awaiting user"
		}
		res.State = "not observed"
		res.Window = "not checked"
		return res, record(loop, st, res, now, nil)
	}

	obs, err := a.Observe(ctx)
	if err != nil {
		res.State = string(decide.Unknown)
		res.Reason = "could not verify state; no action taken"
		res.Window = "not checked"
		return res, record(loop, st, res, now, err)
	}
	res.State = string(obs.State)
	if obs.State == decide.Error {
		res.ErrorClass = string(obs.ErrorClass)
	}

	inWindow, why, err := window.Inside(loop.Window, now)
	if err != nil {
		return res, err
	}
	res.Window = why

	// Progress fingerprint: only when repositories are configured. Without
	// them, stall detection is off and an active session is left alone.
	unchangedFor := time.Duration(0)
	if len(loop.Progress.Repositories) > 0 {
		fp := opts.Fingerprint
		if fp == nil {
			fp = state.Fingerprint
		}
		f, err := fp(loop.Progress.Repositories)
		if err != nil {
			res.Reason = "could not fingerprint repositories; no action taken"
			return res, record(loop, st, res, now, err)
		}
		if f != st.Fingerprint || st.LastProgressAt.IsZero() {
			st.Fingerprint = f
			st.LastProgressAt = now
		}
		unchangedFor = now.Sub(st.LastProgressAt)
		res.UnchangedFor = unchangedFor.Truncate(time.Second).String()
	}

	sinceWake := 100 * 365 * 24 * time.Hour
	if !st.LastWakeAt.IsZero() {
		sinceWake = now.Sub(st.LastWakeAt)
	}
	sinceReprompt := time.Duration(0)
	if !st.LastRepromptAt.IsZero() {
		sinceReprompt = now.Sub(st.LastRepromptAt)
	}

	// A session that is running or finished cleanly resets the error budget.
	if obs.State == decide.Active || obs.State == decide.Idle {
		st.Reprompts = 0
		st.LastRepromptAt = time.Time{}
	}

	d := decide.Decide(decide.Input{
		Obs:               obs,
		Control:           gate,
		InWindow:          inWindow,
		UnchangedFor:      unchangedFor,
		SinceWake:         sinceWake,
		StallAfter:        loop.Progress.StallAfter.Duration,
		MinWakeInterval:   loop.Progress.MinWakeInterval.Duration,
		Reprompts:         st.Reprompts,
		MaxReprompts:      loop.Errors.MaxReprompts,
		SinceLastReprompt: sinceReprompt,
		Backoff:           loop.Errors.Backoff.Duration,
	})
	res.Action, res.Reason = string(d.Action), d.Reason

	if d.Action == decide.Stop && st.StopRequestedSent {
		res.Action, res.Reason = string(decide.None), "stop already requested"
		return res, record(loop, st, res, now, nil)
	}

	var actErr error
	if !opts.DryRun {
		actErr = act(ctx, loop, a, d, &st, now, &res)
	}
	st.LastState = string(obs.State)
	res.Reprompts = st.Reprompts
	return res, record(loop, st, res, now, actErr)
}

func act(ctx context.Context, loop config.Loop, a adapter.Adapter, d decide.Decision, st *state.File, now time.Time, res *Result) error {
	switch d.Action {
	case decide.Wake, decide.Nudge, decide.Reprompt:
		prompt, err := loop.PromptText()
		if err != nil {
			return err
		}
		mode := adapter.ModeAuto
		if d.Action == decide.Nudge {
			mode = adapter.ModeQueue
		}
		if err := a.Send(ctx, prompt, mode); err != nil {
			return err
		}
		st.LastWakeAt = now
		if d.Action == decide.Reprompt {
			st.Reprompts++
			st.LastRepromptAt = now
		}
	case decide.AwaitUser:
		_, err := control.Update(loop.StateDir, func(f *control.File) {
			f.AwaitingUser = true
			f.Note = d.Reason
		})
		return err
	case decide.Stop:
		err := a.Stop(ctx)
		var unsupported adapter.ErrUnsupported
		if errors.As(err, &unsupported) {
			res.Warning = fmt.Sprintf("%s adapter cannot stop a running turn; nothing more will be sent", a.Name())
			err = nil
		}
		if err != nil {
			return err
		}
		st.StopRequestedSent = true
	}
	return nil
}

func record(loop config.Loop, st state.File, res Result, now time.Time, actErr error) error {
	st.CheckedAt = now
	if err := state.Save(loop.StateDir, st); err != nil {
		return err
	}
	ev := state.Event{At: now, Loop: loop.Name, Event: "tick", Action: res.Action, Reason: res.Reason, State: res.State}
	if actErr != nil {
		ev.Error = actErr.Error()
	}
	if res.DryRun {
		ev.Extra = map[string]any{"dryRun": true}
	}
	if err := state.Append(loop.StateDir, ev); err != nil {
		return err
	}
	return actErr
}
