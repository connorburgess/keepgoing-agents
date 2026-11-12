package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"reflect"

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
}

func clientHoldCommand(e env, args []string) int {
	fs := flag.NewFlagSet("client "+args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	id := fs.String("id", "", "registration ID to hold")
	directory := fs.String("directory", "", "private registry directory")
	asJSON := fs.Bool("json", false, "print versioned save evidence")
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
	r, code := applyClientHold(e.ctx, *directory, *id, args[0])
	if *asJSON {
		if json.NewEncoder(e.stdout).Encode(r) != nil {
			return exitFailed
		}
	} else {
		fmt.Fprintf(e.stdout, "Registration %s: %s control save %s\n", r.RegistrationID, r.Operation, r.ControlSave)
		for _, issue := range r.Issues {
			fmt.Fprintf(e.stdout, "issue: %s\n", issue)
		}
		if r.ControlSave == "confirmed" {
			fmt.Fprintln(e.stdout, "Hold saved; provider action is not confirmed. Inspect status before another action.")
		}
	}
	return code
}

func applyClientHold(ctx context.Context, directory, id, operation string) (clientHoldReport, int) {
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
		f, err := updateControlWithHistory(entry.StateDir, operation, func(f *control.File) error {
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
			// Like ordinary pause/stop, this never claims ownership, rejects another
			// caller merely for its identity, clears holds, or repairs transfer state.
			if operation == "pause" {
				f.Paused = true
			} else {
				f.Stop = true
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
	if len(r.Issues) > 0 {
		return r, exitFailed
	}
	return r, exitOK
}
