package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"reflect"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/registry"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

type clientHoldReport struct {
	SchemaVersion  int             `json:"schemaVersion"`
	RegistrationID string          `json:"registrationId"`
	Operation      string          `json:"operation"`
	ControlSave    string          `json:"controlSave"`
	Issues         []string        `json:"issues"`
	Controls       *clientControls `json:"controls,omitempty"`
	// Stop only. Lanes: the stop was asked to cover the lanes, and what it
	// did to them when it ran now. CarriedOut: whether this command ran the
	// stop ("now") or left it to the loop's checks ("left_to_check").
	Lanes      *clientLanes `json:"lanes,omitempty"`
	CarriedOut string       `json:"carriedOut,omitempty"`
	// LeftBecause says why a stop asked to run now was left to a check.
	LeftBecause string `json:"leftBecause,omitempty"`
	// Warnings are the stop check's, for a stop carried out now.
	Warnings []string `json:"warnings,omitempty"`
}

// clientLanes is the lanes part of a client stop.
type clientLanes struct {
	Requested bool `json:"requested"`
	// Result is what the stop did to them, when it ran in this command.
	Result *state.LaneStop `json:"result,omitempty"`
}

// holdOptions are the stop's options: cover the lanes, carry it out now.
// CallerThread is the BB thread the command runs in, for a stop carried out
// now; the panel and the menu leave it empty, acting as no BB thread.
type holdOptions struct {
	Lanes, Now   bool
	CallerThread string
}

func clientHoldCommand(e env, args []string) int {
	fs := flag.NewFlagSet("client "+args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	id := fs.String("id", "", "registration ID to hold")
	directory := fs.String("directory", "", "private registry directory")
	asJSON := fs.Bool("json", false, "print versioned save evidence")
	var opts holdOptions
	if args[0] == "stop" {
		fs.BoolVar(&opts.Lanes, "lanes", false, "also stop the lanes: the conversations the supervised one started, and theirs")
		fs.BoolVar(&opts.Now, "now", false, "carry the stop out in this command instead of leaving it to the next check")
	}
	if err := fs.Parse(args[1:]); err != nil {
		if err == flag.ErrHelp {
			fs.SetOutput(e.stdout)
			fs.PrintDefaults()
			return exitOK
		}
		e.errf("client: invalid hold arguments; see client pause --help or client stop --help")
		return exitUsage
	}
	if fs.NArg() != 0 || !clientRegistrationID.MatchString(*id) {
		e.errf("client: provide a registration ID; no positional arguments")
		return exitUsage
	}
	if opts.Now {
		opts.CallerThread = os.Getenv("BB_THREAD_ID")
	}
	r, code := applyClientHoldWith(e.ctx, *directory, *id, args[0], "", "", opts)
	if *asJSON {
		if json.NewEncoder(e.stdout).Encode(r) != nil {
			return exitFailed
		}
	} else {
		fmt.Fprintf(e.stdout, "Registration %s: %s control save %s\n", r.RegistrationID, r.Operation, r.ControlSave)
		for _, issue := range r.Issues {
			fmt.Fprintf(e.stdout, "issue: %s\n", issue)
		}
		switch {
		case r.CarriedOut == "now":
			fmt.Fprintln(e.stdout, "Stop carried out in this command.")
			if r.Lanes != nil && r.Lanes.Result != nil {
				fmt.Fprintln(e.stdout, describeLaneStop(*r.Lanes.Result))
			}
		case r.ControlSave == "confirmed" && r.LeftBecause != "":
			fmt.Fprintf(e.stdout, "Stop saved and left to the loop's checks: %s.\n", r.LeftBecause)
		case r.ControlSave == "confirmed":
			fmt.Fprintln(e.stdout, "Hold saved; provider action is not confirmed. Inspect status before another action.")
		}
		for _, w := range r.Warnings {
			fmt.Fprintf(e.stdout, "warning: %s\n", w)
		}
	}
	return code
}

func applyClientHold(ctx context.Context, directory, id, operation string) (clientHoldReport, int) {
	return applyClientHoldFrom(ctx, directory, id, operation, "", "")
}

// applyClientHoldFrom is applyClientHold for a change from origin ("" for a
// command, "panel" for the keepgoing panel), which the history records.
// expect, when set, is the controlDigest of the control record a person
// reviewed: the hold is refused as observation_changed, inside the control
// lock, when the record is no longer that one.
func applyClientHoldFrom(ctx context.Context, directory, id, operation, origin, expect string) (clientHoldReport, int) {
	return applyClientHoldWith(ctx, directory, id, operation, origin, expect, holdOptions{})
}

// applyClientHoldWith is applyClientHoldFrom with the stop's options. A
// stop with Now is carried out in the call, under the loop's check lock, as
// keepgoing stop --now does; the panel and the menu act as no BB thread.
func applyClientHoldWith(ctx context.Context, directory, id, operation, origin, expect string, opts holdOptions) (clientHoldReport, int) {
	if operation != "stop" {
		opts = holdOptions{}
	}
	var stopCfg *config.Config
	r := clientHoldReport{SchemaVersion: 1, RegistrationID: id, Operation: operation, ControlSave: "not_attempted", Issues: []string{}}
	reject := errors.New("client hold rejected")
	fail := func(issue string) error { r.Issues = append(r.Issues, issue); return reject }
	if operation != "pause" && operation != "stop" {
		return r, exitUsage
	}
	dir, err := registryDirectory(directory)
	if err != nil {
		r.Issues = append(r.Issues, "registry_unavailable")
		return r, exitFailed
	}
	err = registry.WithEntry(dir, id, func(entry registry.Entry) error {
		if ctx.Err() != nil {
			return fail("cancelled")
		}
		cfg, ok := clientConfig(entry)
		if !ok {
			return fail("binding_unavailable")
		}
		// Reject corrupt/oversized/non-object records before UpdateChecked's load.
		var initial control.File
		if readTransferRecord(entry.StateDir, control.FileName, &initial) != nil {
			return fail("control_unavailable")
		}
		f, err := updateControlFrom(entry.StateDir, operation, origin, func(f *control.File) error {
			if ctx.Err() != nil {
				return fail("cancelled")
			}
			// Check the binding again inside the short control lock. No tick lock or
			// daemon/provider query is required to add a restrictive hold.
			current, ok := clientEntry(dir, id)
			if !ok || current != entry {
				return fail("registration_changed")
			}
			fresh, ok := clientConfig(entry)
			if !ok || cfg.CheckSources() != nil || fresh.CheckSources() != nil {
				return fail("binding_changed")
			}
			var checked control.File
			if readTransferRecord(entry.StateDir, control.FileName, &checked) != nil || !reflect.DeepEqual(checked, *f) {
				return fail("control_unavailable")
			}
			if expect != "" && controlDigest(checked) != expect {
				return fail("observation_changed")
			}
			// Like ordinary pause/stop, this never claims ownership, rejects another
			// caller merely for its identity, clears holds, or repairs transfer state.
			if operation == "pause" {
				f.Paused = true
			} else {
				f.Stop = true
				if opts.Lanes {
					at := time.Now().UTC()
					f.StopLanesAt = &at
				}
			}
			r.ControlSave = "unconfirmed" // A save error/lost reply is not permission to replay.
			return nil
		})
		if err == nil || errors.Is(err, errControlHistory) {
			recordRegistration(entry.StateDir, entry.ID)
		}
		if err != nil && !errors.Is(err, errControlHistory) {
			return err
		}
		r.ControlSave = "confirmed"
		r.Controls = &clientControls{f.Enabled, f.Paused, f.Stop, f.AwaitingUser, f.Completed}
		stopCfg = cfg
		if errors.Is(err, errControlHistory) {
			r.Issues = append(r.Issues, "incident_history_unavailable")
		}
		if state.Append(entry.StateDir, state.Event{Loop: entry.Loop, Event: "control", Action: operation, Reason: f.Summary()}) != nil {
			r.Issues = append(r.Issues, "run_history_unavailable")
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, lock.ErrHeld) {
			r.Issues = append(r.Issues, "busy")
			return r, exitSkipped
		}
		if !errors.Is(err, reject) {
			r.Issues = append(r.Issues, "control_or_registration_unavailable")
		}
		return r, exitFailed
	}
	if operation == "stop" {
		r.CarriedOut = "left_to_check"
		if opts.Lanes {
			r.Lanes = &clientLanes{Requested: true}
		}
	}
	if operation == "stop" && opts.Now && stopCfg != nil && r.ControlSave == "confirmed" {
		// Outside the registry entry's lock: the stop check takes the loop's
		// own check lock, and may wait a few seconds for it.
		now, err := stopNow(ctx, stopCfg, stopNowWait, opts.CallerThread)
		r.CarriedOut, r.LeftBecause, r.Warnings = now.CarriedOut, now.Why, now.Warnings
		if r.Lanes != nil {
			r.Lanes.Result = now.Lanes
		}
		if now.failed(err) {
			r.Issues = append(r.Issues, "stop_now_failed")
		}
	}
	if len(r.Issues) > 0 {
		return r, exitFailed
	}
	return r, exitOK
}
