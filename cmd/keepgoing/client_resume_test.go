package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/incident"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/registry"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

type resumeFixture struct {
	dir   string
	entry registry.Entry
	at    time.Time
}

func newResumeFixture(t *testing.T) resumeFixture {
	t.Helper()
	t.Setenv("BB_THREAD_ID", "thr_example_owner")
	dir, entry := clientFixture(t)
	ctl := control.File{Paused: true, Stop: true, AwaitingUser: true, Completed: true, Note: "PRIVATE-NOTE", Ownership: &control.Ownership{ThreadID: "thr_example_owner", Generation: 1}}
	if err := control.Save(entry.StateDir, ctl); err != nil {
		t.Fatal(err)
	}
	if _, err := control.WriteGoal(entry.StateDir, entry.Loop, "thr_example_owner", control.GoalSpec{Title: "PRIVATE-TITLE", ObjectiveRefs: []string{"PRIVATE-OBJECTIVE"}, AcceptanceRefs: []string{"PRIVATE-ACCEPTANCE"}}, "", 0); err != nil {
		t.Fatal(err)
	}
	return resumeFixture{dir, entry, time.Now().UTC()}
}
func (f resumeFixture) call(op, ticket string, private bool, hook func() error) (clientResumeReport, int) {
	return clientResume(context.Background(), f.dir, f.entry.ID, op, ticket, private, os.Getenv("BB_THREAD_ID"), "", func() time.Time { return f.at }, hook)
}
func (f resumeFixture) prepare(t *testing.T) clientResumeReport {
	t.Helper()
	r, c := f.call("prepare-resume", "", false, nil)
	if c != 0 || r.Ticket == "" || r.Snapshot == nil {
		t.Fatal(c, r)
	}
	return r
}
func resumeBytes(t *testing.T, p string) []byte {
	t.Helper()
	b, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func resumeWrite(t *testing.T, p string, b []byte) {
	t.Helper()
	if e := os.WriteFile(p, b, 0600); e != nil {
		t.Fatal(e)
	}
}

func TestClientResumePrepareAndSingleUse(t *testing.T) {
	f := newResumeFixture(t)
	path := filepath.Join(f.entry.StateDir, control.FileName)
	before := resumeBytes(t, path)
	p := f.prepare(t)
	if p.ControlSave != "not_attempted" || p.Snapshot.Controls.Enabled || !p.Snapshot.Controls.Completed || p.Snapshot.Daemon.State != "skipped" {
		t.Fatal(p)
	}
	raw, _ := json.Marshal(p)
	for _, secret := range []string{"PRIVATE", f.entry.ConfigPath, f.entry.StateDir, "thr_example_owner", "binding"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("private evidence escaped", string(raw))
		}
	}
	if !bytes.Equal(before, resumeBytes(t, path)) {
		t.Fatal("preparation changed holds")
	}
	ticket, err := loadClientTicket(f.entry.StateDir)
	if err != nil || ticket.Consumed || ticket.Binding == "" {
		t.Fatal(ticket, err)
	}
	r, c := f.call("resume", p.Ticket, false, nil)
	if c != 0 || r.ControlSave != "confirmed" || r.Controls.Enabled || r.Controls.Paused || r.Controls.Stop || r.Controls.Completed || r.Controls.AwaitingUser {
		t.Fatal(c, r)
	}
	ctl, _ := control.Load(f.entry.StateDir)
	if ctl.Note != "" || ctl.Ownership.ThreadID != "thr_example_owner" || ctl.Ownership.Generation != 1 {
		t.Fatal(ctl)
	}
	ticket, _ = loadClientTicket(f.entry.StateDir)
	if !ticket.Consumed {
		t.Fatal("ticket not consumed")
	}
	saved := resumeBytes(t, path)
	r, c = f.call("resume", p.Ticket, false, nil)
	if c != exitFailed || r.ControlSave != "not_attempted" || !bytes.Equal(saved, resumeBytes(t, path)) {
		t.Fatal("replay accepted", c, r)
	}
	j, err := incident.Load(f.entry.StateDir)
	if err != nil || len(j.Records) != 1 || j.Records[0].ControlChange.Operation != "resume" {
		t.Fatal(j, err)
	}
}

func TestClientResumeRejectsChangedEvidence(t *testing.T) {
	for _, change := range []string{"config", "config_comment", "control", "owner", "caller", "goal", "state", "target", "removed", "pending", "bound_target", "expired", "clock_rollback", "corrupt_ticket", "superseded", "repeated_pause"} {
		t.Run(change, func(t *testing.T) {
			f := newResumeFixture(t)
			if change == "repeated_pause" {
				// Model a clock rollback without sleeping or relying on platform
				// clock resolution. A repeated pause must advance this loaded time.
				ctl, err := control.Load(f.entry.StateDir)
				if err != nil {
					t.Fatal(err)
				}
				ctl.UpdatedAt = time.Now().UTC().Add(time.Hour)
				b, err := json.Marshal(ctl)
				if err != nil {
					t.Fatal(err)
				}
				resumeWrite(t, filepath.Join(f.entry.StateDir, control.FileName), b)
			}
			p := f.prepare(t)
			path := filepath.Join(f.entry.StateDir, control.FileName)
			switch change {
			case "config", "config_comment":
				b := resumeBytes(t, f.entry.ConfigPath)
				if change == "config" {
					b = []byte(strings.Replace(string(b), "thr_example", "thr_changed", 1))
				} else {
					b = append(b, []byte("# changed\n")...)
				}
				resumeWrite(t, f.entry.ConfigPath, b)
			case "control", "owner", "pending", "bound_target":
				ctl, _ := control.Load(f.entry.StateDir)
				switch change {
				case "control":
					ctl.Note = "different"
				case "owner":
					ctl.Ownership.Generation++
				case "pending":
					ctl.PendingTransfer = &control.PendingBBTransfer{}
				case "bound_target":
					ctl.BBTarget = "thr_changed"
				}
				if err := control.Save(f.entry.StateDir, ctl); err != nil {
					t.Fatal(err)
				}
			case "caller":
				t.Setenv("BB_THREAD_ID", "")
			case "goal":
				g, _ := control.LoadGoal(f.entry.StateDir)
				if _, err := control.WriteGoal(f.entry.StateDir, f.entry.Loop, "thr_example_owner", g.Revisions[0].Spec, g.ID, 1); err != nil {
					t.Fatal(err)
				}
			case "state", "target":
				st := state.File{CheckedAt: f.at}
				if change == "target" {
					st.ActiveTarget = "unknown"
				}
				if err := state.Save(f.entry.StateDir, st); err != nil {
					t.Fatal(err)
				}
			case "removed":
				if err := registry.Remove(f.dir, f.entry.ID); err != nil {
					t.Fatal(err)
				}
			case "expired":
				f.at = f.at.Add(clientResumeTTL)
			case "clock_rollback":
				f.at = f.at.Add(-time.Second)
			case "corrupt_ticket":
				resumeWrite(t, filepath.Join(f.entry.StateDir, clientResumeFile), []byte("null"))
			case "superseded":
				f.prepare(t)
			case "repeated_pause":
				previous, _ := control.Load(f.entry.StateDir)
				if r, c := applyClientHold(context.Background(), f.dir, f.entry.ID, "pause"); c != 0 {
					t.Fatal(c, r)
				}
				current, err := control.Load(f.entry.StateDir)
				if err != nil || !current.UpdatedAt.After(previous.UpdatedAt) {
					t.Fatalf("repeated pause did not advance control evidence: %v", err)
				}
			}
			before := resumeBytes(t, path)
			r, c := f.call("resume", p.Ticket, false, nil)
			if c != exitFailed || r.ControlSave != "not_attempted" || !bytes.Equal(before, resumeBytes(t, path)) {
				t.Fatal("stale resume changed holds", c, r)
			}
		})
	}
}

func TestClientResumePrepareRefusesUnavailableEvidenceAndLocks(t *testing.T) {
	for _, kind := range []string{"state", "goal", "control", "pending", "owner", "tick_lock", "control_lock", "registry_lock"} {
		t.Run(kind, func(t *testing.T) {
			f := newResumeFixture(t)
			want := exitFailed
			switch kind {
			case "state", "goal", "control":
				resumeWrite(t, filepath.Join(f.entry.StateDir, kind+".json"), []byte("null"))
			case "pending":
				ctl, _ := control.Load(f.entry.StateDir)
				ctl.PendingTransfer = &control.PendingBBTransfer{}
				control.Save(f.entry.StateDir, ctl)
			case "owner":
				t.Setenv("BB_THREAD_ID", "thr_different")
			default:
				dir, name := f.entry.StateDir, strings.TrimSuffix(kind, "_lock")
				if kind == "registry_lock" {
					dir = f.dir
				}
				held, err := lock.AcquireNamed(dir, name)
				if err != nil {
					t.Fatal(err)
				}
				defer held.Release()
				want = exitSkipped
			}
			r, c := f.call("prepare-resume", "", false, nil)
			if c != want || r.Ticket != "" || r.ControlSave != "not_attempted" {
				t.Fatal(c, r)
			}
			if _, err := os.Stat(filepath.Join(f.entry.StateDir, clientResumeFile)); !os.IsNotExist(err) {
				t.Fatal("rejected preparation wrote ticket")
			}
		})
	}
}

func TestClientResumeInterruptedConsumptionKeepsHolds(t *testing.T) {
	for _, change := range []bool{false, true} {
		t.Run(map[bool]string{false: "exit", true: "new_hold"}[change], func(t *testing.T) {
			f := newResumeFixture(t)
			p := f.prepare(t)
			path := filepath.Join(f.entry.StateDir, control.FileName)
			before := resumeBytes(t, path)
			hook := func() error {
				if change {
					ctl, _ := control.Load(f.entry.StateDir)
					ctl.Note = "new hold"
					control.Save(f.entry.StateDir, ctl)
					before = resumeBytes(t, path)
					return nil
				}
				return errors.New("simulated exit")
			}
			r, c := f.call("resume", p.Ticket, false, hook)
			if c != exitFailed || r.ControlSave != "not_attempted" || !bytes.Equal(before, resumeBytes(t, path)) {
				t.Fatal(c, r)
			}
			ticket, _ := loadClientTicket(f.entry.StateDir)
			if !ticket.Consumed {
				t.Fatal("ticket resurrected")
			}
			r, c = f.call("resume", p.Ticket, false, nil)
			if c != exitFailed || !bytes.Equal(before, resumeBytes(t, path)) {
				t.Fatal("replayed after interruption", c, r)
			}
			fresh := f.prepare(t)
			if _, c := f.call("resume", fresh.Ticket, false, nil); c != 0 {
				t.Fatal("fresh explicit preparation failed", c)
			}
		})
	}
}

func TestClientResumeHistoryFailureAndLostOutput(t *testing.T) {
	f := newResumeFixture(t)
	p := f.prepare(t)
	resumeWrite(t, filepath.Join(f.entry.StateDir, incident.FileName), []byte("null"))
	r, c := f.call("resume", p.Ticket, false, nil)
	if c != exitFailed || r.ControlSave != "confirmed" || r.Controls.Paused {
		t.Fatal(c, r)
	}
	if _, c = f.call("resume", p.Ticket, false, nil); c != exitFailed {
		t.Fatal("history failure replayed")
	}
	f = newResumeFixture(t)
	p = f.prepare(t)
	var errs bytes.Buffer
	c = run(context.Background(), []string{"client", "resume", "--directory", f.dir, "--id", f.entry.ID, "--ticket", p.Ticket, "--json"}, lostClientHoldOutput{}, &errs)
	ctl, _ := control.Load(f.entry.StateDir)
	if c != exitFailed || errs.Len() != 0 || ctl.Paused {
		t.Fatal(c, errs.String(), ctl)
	}
	if _, c = f.call("resume", p.Ticket, false, nil); c != exitFailed {
		t.Fatal("lost response replayed")
	}
}

func TestClientResumePrivatePrepareAndCLI(t *testing.T) {
	f := newResumeFixture(t)
	var out, errs bytes.Buffer
	c := run(context.Background(), []string{"client", "prepare-resume", "--directory", f.dir, "--id", f.entry.ID, "--json", "--private"}, &out, &errs)
	var r clientResumeReport
	if c != 0 || errs.Len() != 0 || json.Unmarshal(out.Bytes(), &r) != nil || r.Snapshot.PrivateGoal.Title != "PRIVATE-TITLE" {
		t.Fatal(c, out.String(), errs.String())
	}
	for _, secret := range []string{"PRIVATE-OBJECTIVE", "PRIVATE-ACCEPTANCE", "PRIVATE-NOTE", f.entry.ConfigPath} {
		if strings.Contains(out.String(), secret) {
			t.Fatal("private references escaped")
		}
	}
	before, _ := control.Load(f.entry.StateDir)
	for _, args := range [][]string{{"client", "resume"}, {"client", "resume", "--id", f.entry.ID, "--ticket", "PRIVATE"}, {"client", "prepare-resume", "--id", f.entry.ID, "--ticket", r.Ticket}, {"client", "resume", "--id", f.entry.ID, "--ticket", r.Ticket, "--private"}, {"client", "resume", "--id", f.entry.ID, "--ticket", r.Ticket, "--take-over"}} {
		out.Reset()
		errs.Reset()
		if code := run(context.Background(), args, &out, &errs); code != exitUsage || strings.Contains(errs.String(), "PRIVATE") {
			t.Fatal(code, errs.String())
		}
	}
	after, _ := control.Load(f.entry.StateDir)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("invalid invocation changed holds")
	}
}

func TestClientResumeBusyApplyLeavesTicketUsable(t *testing.T) {
	for _, name := range []string{lock.Tick, lock.Control, registry.LockName} {
		t.Run(name, func(t *testing.T) {
			f := newResumeFixture(t)
			p := f.prepare(t)
			dir := f.entry.StateDir
			if name == registry.LockName {
				dir = f.dir
			}
			held, err := lock.AcquireNamed(dir, name)
			if err != nil {
				t.Fatal(err)
			}
			r, c := f.call("resume", p.Ticket, false, nil)
			held.Release()
			if c != exitSkipped || r.ControlSave != "not_attempted" {
				t.Fatal(c, r)
			}
			ticket, err := loadClientTicket(f.entry.StateDir)
			if err != nil || ticket.Consumed {
				t.Fatal(ticket, err)
			}
			if r, c = f.call("resume", p.Ticket, false, nil); c != 0 {
				t.Fatal(c, r)
			}
		})
	}
}

func TestClientResumeWithoutBBAndFallbackChanges(t *testing.T) {
	for _, change := range []bool{false, true} {
		t.Run(map[bool]string{false: "unchanged", true: "fallback_changed"}[change], func(t *testing.T) {
			f := newResumeFixture(t)
			t.Setenv("BB_THREAD_ID", "")
			body := `version: 1
loop:
  name: example-loop
  state_dir: state
  adapter: process
  provider: example
  process: {status_command: [nonexistent-status], resume_command: [nonexistent-resume]}
  prompt_file: private-missing-task.txt
  fallbacks: fallbacks.yaml
`
			fallback := `version: 1
candidates:
  - id: alternative
    adapter: process
    provider: example
    process: {status_command: [nonexistent-status], resume_command: [nonexistent-resume]}
    probe: {kind: command, command: [nonexistent-probe]}
`
			resumeWrite(t, f.entry.ConfigPath, []byte(body))
			fb := filepath.Join(filepath.Dir(f.entry.ConfigPath), "fallbacks.yaml")
			resumeWrite(t, fb, []byte(fallback))
			p := f.prepare(t)
			if change {
				resumeWrite(t, fb, []byte(fallback+"# changed\n"))
			}
			r, c := f.call("resume", p.Ticket, false, nil)
			if change {
				if c != exitFailed || r.ControlSave != "not_attempted" {
					t.Fatal(c, r)
				}
			} else if c != 0 || r.Controls.Enabled {
				t.Fatal(c, r)
			}
		})
	}
}

func TestClientResumeExpiryDuringConsumption(t *testing.T) {
	f := newResumeFixture(t)
	p := f.prepare(t)
	path := filepath.Join(f.entry.StateDir, control.FileName)
	before := resumeBytes(t, path)
	at := f.at
	r, c := clientResume(context.Background(), f.dir, f.entry.ID, "resume", p.Ticket, false, os.Getenv("BB_THREAD_ID"), "", func() time.Time { return at }, func() error { at = at.Add(clientResumeTTL); return nil })
	if c != exitFailed || r.ControlSave != "not_attempted" || !bytes.Equal(before, resumeBytes(t, path)) {
		t.Fatal(c, r)
	}
	ticket, _ := loadClientTicket(f.entry.StateDir)
	if !ticket.Consumed {
		t.Fatal("consumption lost")
	}
}

// The command acts as the BB thread in its environment, exactly as before the
// caller became an argument: a ticket it prepares is bound to that thread, a
// request as anyone else is refused, and the same thread resumes.
func TestClientResumeCommandActsAsItsEnvironmentsThread(t *testing.T) {
	f := newResumeFixture(t)
	var out, errs bytes.Buffer
	if code := run(context.Background(), []string{"client", "prepare-resume", "--id", f.entry.ID, "--directory", f.dir, "--json"}, &out, &errs); code != 0 {
		t.Fatalf("prepare: %d %s", code, errs.String())
	}
	var p clientResumeReport
	if err := json.Unmarshal(out.Bytes(), &p); err != nil || p.Ticket == "" {
		t.Fatal(err, out.String())
	}
	if r, code := clientResume(context.Background(), f.dir, f.entry.ID, "resume", p.Ticket, false, "", "", time.Now, nil); code == 0 || r.ControlSave != "not_attempted" {
		t.Fatalf("a ticket prepared for thr_example_owner was used with no caller: %d %+v", code, r)
	}
	if r, code := clientResume(context.Background(), f.dir, f.entry.ID, "resume", p.Ticket, false, "thr_different", "", time.Now, nil); code == 0 || r.ControlSave != "not_attempted" {
		t.Fatalf("a ticket prepared for thr_example_owner was used by thr_different: %d %+v", code, r)
	}
	out.Reset()
	if code := run(context.Background(), []string{"client", "resume", "--id", f.entry.ID, "--directory", f.dir, "--ticket", p.Ticket, "--json"}, &out, &errs); code != 0 {
		t.Fatalf("resume: %d %s %s", code, out.String(), errs.String())
	}
	var r clientResumeReport
	if err := json.Unmarshal(out.Bytes(), &r); err != nil || r.ControlSave != "confirmed" {
		t.Fatal(err, out.String())
	}
}

// The registered resume, used by local clients, is the same explicit act:
// it starts a new episode for a hold keepgoing raised for a spent allowance,
// and for no other.
func TestClientResumeStartsANewErrorEpisode(t *testing.T) {
	for _, episodeHold := range []bool{true, false} {
		f := newResumeFixture(t)
		if _, err := control.Update(f.entry.StateDir, func(c *control.File) { c.EpisodeHold = episodeHold }); err != nil {
			t.Fatal(err)
		}
		if err := state.Save(f.entry.StateDir, state.File{Reprompts: 3, Nudges: 2, HandoffDeferrals: 1, NewThreadsCreated: 1}); err != nil {
			t.Fatal(err)
		}
		p := f.prepare(t)
		if r, c := f.call("resume", p.Ticket, false, nil); c != 0 || r.ControlSave != "confirmed" || len(r.Issues) != 0 {
			t.Fatal(c, r)
		}
		st, err := state.Load(f.entry.StateDir)
		want := state.File{Reprompts: 3, Nudges: 2}
		if episodeHold {
			want = state.File{ResumeEpisodes: 1}
		}
		if err != nil || st.Reprompts != want.Reprompts || st.Nudges != want.Nudges || st.ResumeEpisodes != want.ResumeEpisodes || st.HandoffDeferrals != 0 || st.NewThreadsCreated != 1 {
			t.Fatalf("episode hold %t: %+v %v", episodeHold, st, err)
		}
		j, err := incident.Load(f.entry.StateDir)
		if c := j.Records[len(j.Records)-1].ControlChange; err != nil || c.Caller != "client" || c.NewEpisode != episodeHold {
			t.Fatalf("episode hold %t: %+v %v", episodeHold, c, err)
		}
		if ctl, _ := control.Load(f.entry.StateDir); ctl.EpisodeHold {
			t.Fatal("the episode hold survived the resume")
		}
	}
}

// If the counts cannot be saved the registered resume fails with
// new_episode_unconfirmed and the holds intact. The ticket is spent, as after
// any failure once it was consumed.
func TestClientResumeFailsWithHoldsIntactWhenTheEpisodeCannotBeSaved(t *testing.T) {
	f := newResumeFixture(t)
	if _, err := control.Update(f.entry.StateDir, func(c *control.File) { c.EpisodeHold = true }); err != nil {
		t.Fatal(err)
	}
	if err := state.Save(f.entry.StateDir, state.File{Reprompts: 3}); err != nil {
		t.Fatal(err)
	}
	p := f.prepare(t)
	original := saveEpisodeState
	t.Cleanup(func() { saveEpisodeState = original })
	saveEpisodeState = func(string, state.File) error { return errors.New("fictional write failure") }
	r, c := f.call("resume", p.Ticket, false, nil)
	if c != exitFailed || r.ControlSave != "not_attempted" || len(r.Issues) != 1 || r.Issues[0] != "new_episode_unconfirmed" {
		t.Fatal(c, r)
	}
	if ctl, _ := control.Load(f.entry.StateDir); !ctl.AwaitingUser || !ctl.Paused || !ctl.EpisodeHold {
		t.Fatalf("holds changed: %+v", ctl)
	}
	if st, _ := state.Load(f.entry.StateDir); st.Reprompts != 3 || st.ResumeEpisodes != 0 {
		t.Fatalf("%+v", st)
	}
}
