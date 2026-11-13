package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/incident"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/registry"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

const clientResumeFile = "client-resume.json"
const clientResumeTTL = 5 * time.Minute

var clientTicketID = regexp.MustCompile(`^resume_[0-9a-f]{32}$`)
var clientBindingHash = regexp.MustCompile(`^[0-9a-f]{64}$`)
var errClientResume = errors.New("resume evidence unavailable")

// Private comparison metadata only; neither the digest nor source material is
// sent to the client. One slot bounds storage and supersedes earlier previews.
type clientResumeTicket struct {
	Version   int       `json:"version"`
	ID        string    `json:"id"`
	Binding   string    `json:"binding"`
	IssuedAt  time.Time `json:"issuedAt"`
	ExpiresAt time.Time `json:"expiresAt"`
	Consumed  bool      `json:"consumed"`
}
type clientResumeReport struct {
	clientHoldReport
	Ticket    string          `json:"ticket,omitempty"`
	ExpiresAt *time.Time      `json:"expiresAt,omitempty"`
	Snapshot  *clientSnapshot `json:"snapshot,omitempty"`
}

func clientResumeCommand(e env, args []string) int {
	fs := flag.NewFlagSet("client "+args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	id := fs.String("id", "", "registration ID")
	dir := fs.String("directory", "", "private registry directory")
	ticket := fs.String("ticket", "", "resume: single-use prepared ticket")
	private := fs.Bool("private", false, "prepare-resume: include current goal title and next action")
	asJSON := fs.Bool("json", false, "print versioned evidence")
	if err := fs.Parse(args[1:]); err != nil {
		if err == flag.ErrHelp {
			fs.SetOutput(e.stdout)
			fs.PrintDefaults()
			return exitOK
		}
		e.errf("client: invalid resume arguments; see client prepare-resume --help or client resume --help")
		return exitUsage
	}
	if fs.NArg() != 0 || !clientRegistrationID.MatchString(*id) || (args[0] == "prepare-resume" && *ticket != "") || (args[0] == "resume" && (!clientTicketID.MatchString(*ticket) || *private)) {
		e.errf("client: prepare-resume requires --id; resume requires --id and --ticket; --private is for preparation only")
		return exitUsage
	}
	r, code := clientResume(e.ctx, *dir, *id, args[0], *ticket, *private, time.Now, nil)
	if *asJSON {
		if json.NewEncoder(e.stdout).Encode(r) != nil {
			return exitFailed
		}
	} else {
		fmt.Fprintf(e.stdout, "Registration %s: %s; control save %s\n", r.RegistrationID, r.Operation, r.ControlSave)
		if r.Ticket != "" {
			fmt.Fprintf(e.stdout, "Prepared ticket: %s (expires %s)\n", r.Ticket, r.ExpiresAt.Format(time.RFC3339))
			// Preserve the same complete structured projection for review in text mode.
			b, _ := json.MarshalIndent(r.Snapshot, "", "  ")
			fmt.Fprintf(e.stdout, "Snapshot to review:\n%s\n", b)
			fmt.Fprintln(e.stdout, "Resume explicitly clears pause, stop, awaiting-user and completion, and when the hold was raised because re-prompts or nudges were spent it starts a new error episode: those counts start over. Loop-wide limits are unchanged. It does not enable or start supervision.")
		}
		for _, issue := range r.Issues {
			fmt.Fprintf(e.stdout, "issue: %s\n", issue)
		}
	}
	return code
}

func loadClientTicket(dir string) (clientResumeTicket, error) {
	var t clientResumeTicket
	if registry.CheckDirectory(dir) != nil {
		return t, errClientResume
	}
	info, err := os.Lstat(filepath.Join(dir, clientResumeFile))
	if errors.Is(err, os.ErrNotExist) {
		return t, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return t, errClientResume
	}
	if readTransferRecord(dir, clientResumeFile, &t) != nil || t.Version != 1 || !clientTicketID.MatchString(t.ID) || !clientBindingHash.MatchString(t.Binding) || t.IssuedAt.IsZero() || t.ExpiresAt.Sub(t.IssuedAt) != clientResumeTTL {
		return t, errClientResume
	}
	return t, nil
}
func saveClientTicket(dir string, t clientResumeTicket) error {
	if _, err := loadClientTicket(dir); err != nil {
		return err
	}
	b, err := json.Marshal(t)
	if err != nil {
		return errClientResume
	}
	f, err := os.CreateTemp(dir, ".client-resume-*")
	if err != nil {
		return errClientResume
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return errClientResume
	}
	if os.Rename(f.Name(), filepath.Join(dir, clientResumeFile)) != nil {
		return errClientResume
	}
	return nil
}

// Caller holds tick + control locks. Bind full parsed records deliberately:
// even ordinary tick history changes require a new preview. This favors stale
// action rejection over keeping a preview valid across background checks.
func clientResumeBinding(entry registry.Entry, cfg *config.Config, caller string) (string, control.File, error) {
	var ctl control.File
	var st state.File
	if readTransferRecord(entry.StateDir, control.FileName, &ctl) != nil || readTransferRecord(entry.StateDir, state.FileName, &st) != nil || control.CheckOwner(ctl, caller, false) != nil || checkTransferSources(cfg, false) != nil {
		return "", ctl, errClientResume
	}
	if clientAlias(cfg, st.Target()) == "unavailable" {
		return "", ctl, errClientResume
	}
	g, err := control.LoadGoal(entry.StateDir)
	if err != nil || (g != nil && g.OwningLoop != entry.Loop) {
		return "", ctl, errClientResume
	}
	sources := cfg.SourcesDigest()
	if sources == "" {
		return "", ctl, errClientResume
	}
	b, err := json.Marshal(struct {
		Entry           registry.Entry
		Sources, Caller string
		Control         control.File
		State           state.File
		Goal            *control.Goal
	}{entry, sources, caller, ctl, st, g})
	if err != nil {
		return "", ctl, errClientResume
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), ctl, nil
}

// afterConsume is nil in production; tests simulate process exit at the durable
// consume-before-save boundary. There is deliberately no automatic recovery.
func clientResume(ctx context.Context, directory, id, operation, ticket string, private bool, now func() time.Time, afterConsume func() error) (clientResumeReport, int) {
	r := clientResumeReport{clientHoldReport: clientHoldReport{SchemaVersion: 1, RegistrationID: id, Operation: operation, ControlSave: "not_attempted", Issues: []string{}}}
	reject := errors.New("client resume rejected")
	fail := func(code string) error { r.Issues = append(r.Issues, code); return reject }
	dir, err := registryDirectory(directory)
	if err != nil {
		r.Issues = append(r.Issues, "registry_unavailable")
		return r, exitFailed
	}
	if operation != "prepare-resume" && operation != "resume" {
		return r, exitUsage
	}
	caller := os.Getenv("BB_THREAD_ID")
	err = registry.WithEntry(dir, id, func(entry registry.Entry) error {
		if ctx.Err() != nil {
			return fail("cancelled")
		}
		cfg, ok := clientConfig(entry)
		if !ok {
			return fail("binding_unavailable")
		}
		tick, err := lock.Acquire(entry.StateDir)
		if err != nil {
			return err
		}
		defer tick.Release()
		// This check happens under both locks for each branch below.
		binding := func() (string, control.File, error) {
			current, ok := clientEntry(dir, id)
			if !ok || current != entry {
				return "", control.File{}, errClientResume
			}
			fresh, ok := clientConfig(entry)
			if !ok || cfg.CheckSources() != nil || fresh.CheckSources() != nil || ctx.Err() != nil {
				return "", control.File{}, errClientResume
			}
			return clientResumeBinding(entry, cfg, caller)
		}
		if operation == "prepare-resume" {
			held, err := lock.AcquireNamed(entry.StateDir, lock.Control)
			if err != nil {
				return err
			}
			defer held.Release()
			before, _, err := binding()
			if err != nil {
				return fail("resume_evidence_unavailable")
			}
			snapshot := readClientStatus(ctx, dir, id, private, true, defaultStaleAfter, now().UTC(), inspectDaemon)
			if snapshot.Outcome != "available" {
				return fail("resume_evidence_unavailable")
			}
			after, _, err := binding()
			if err != nil || before != after {
				return fail("observation_changed")
			}
			var random [16]byte
			if _, err := rand.Read(random[:]); err != nil {
				return fail("ticket_unavailable")
			}
			at := now().UTC()
			t := clientResumeTicket{Version: 1, ID: "resume_" + hex.EncodeToString(random[:]), Binding: after, IssuedAt: at, ExpiresAt: at.Add(clientResumeTTL)}
			if saveClientTicket(entry.StateDir, t) != nil {
				return fail("ticket_unavailable")
			}
			r.Ticket = t.ID
			r.ExpiresAt = &t.ExpiresAt
			r.Snapshot = snapshot.Snapshot
			return nil
		}
		// Bound and reject malformed controls before UpdateChecked's ordinary load.
		var initial control.File
		if readTransferRecord(entry.StateDir, control.FileName, &initial) != nil {
			return fail("resume_evidence_unavailable")
		}
		newEpisode := false
		f, err := updateControlAnnotated(entry.StateDir, "resume", func(f *control.File) error {
			newEpisode = false
			current, checked, err := binding()
			if err != nil || !reflect.DeepEqual(checked, *f) {
				return fail("resume_evidence_unavailable")
			}
			t, err := loadClientTicket(entry.StateDir)
			at := now().UTC()
			if err != nil || t.ID != ticket || t.Consumed || at.Before(t.IssuedAt) || !at.Before(t.ExpiresAt) {
				return fail("ticket_unavailable_or_expired")
			}
			if current != t.Binding {
				return fail("observation_changed")
			}
			// A crash after this save sacrifices the ticket, never resumes implicitly.
			t.Consumed = true
			if saveClientTicket(entry.StateDir, t) != nil {
				return fail("ticket_consumption_unconfirmed")
			}
			if afterConsume != nil {
				if err := afterConsume(); err != nil {
					return fail("interrupted_after_consumption")
				}
			}
			// Recheck external changes/cancellation even after consumption. A failure
			// leaves holds intact and requires a new preparation.
			again, _, err := binding()
			if err != nil || again != current {
				return fail("observation_changed")
			}
			at = now().UTC()
			if at.Before(t.IssuedAt) || !at.Before(t.ExpiresAt) {
				return fail("ticket_unavailable_or_expired")
			}
			// Before the holds are cleared: if the counts cannot be reset the
			// resume fails with the holds intact, like any failure after the
			// ticket was consumed.
			if resumeEpisode(entry.StateDir, f.EpisodeHold, at, cfg.Loop.Errors.MaxReprompts, f.BriefHold...) != nil {
				return fail("new_episode_unconfirmed")
			}
			newEpisode = f.EpisodeHold
			f.Paused, f.Stop, f.AwaitingUser, f.Completed = false, false, false, false
			f.Note, f.EpisodeHold, f.OpenQuestion, f.BriefHold = "", false, nil, nil
			r.ControlSave = "unconfirmed"
			return nil
		}, func(_ control.File, c *incident.ControlChange) {
			c.Caller, c.NewEpisode = "client", newEpisode
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
		if state.Append(entry.StateDir, state.Event{Loop: entry.Loop, Event: "control", Action: "resume", Reason: f.Summary()}) != nil {
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
