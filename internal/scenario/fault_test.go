package scenario

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runtimeDir is the loop's state directory.
func (w *World) runtimeDir() string { return filepath.Join(filepath.Dir(w.Config), "state") }

// killAfter makes the stand-in bb kill the keepgoing process that runs
// `thread <command>`, once that command has done its work and answered.
func (w *World) killAfter(command string) {
	w.t.Helper()
	if err := os.WriteFile(filepath.Join(w.world, "kill."+command), nil, 0o600); err != nil {
		w.t.Fatal(err)
	}
}

// failingTurns makes every turn of the thread end in an unclassified error.
func failingTurns(th *Thread) {
	fail := func(th *Thread) {
		th.Event("provider/error", map[string]any{"willRetry": false, "errorInfo": map[string]any{"category": "unknown", "httpStatusCode": nil, "providerCode": "unknown"}})
		th.Set("error")
	}
	th.OnSend = func(th *Thread, m Send) { th.StartTurn(); fail(th) }
	fail(th)
}

// killedTick runs one check that is expected to be killed partway.
func (w *World) killedTick(label string) *Step {
	w.t.Helper()
	r := w.Run("tick", "-c", w.Config, "--now", w.stamp())
	s := w.record(label, r, nil)
	if r.Code != -1 {
		w.Failf(s, "the check was not killed: exit %d\n%s%s", r.Code, r.Stdout, r.Stderr)
	}
	return s
}

func reprompts(s *Step) int {
	var st struct {
		State struct {
			Reprompts int `json:"reprompts"`
		} `json:"state"`
	}
	json.Unmarshal([]byte(s.Status.Raw), &st)
	return st.State.Reprompts
}

// TestFaultKilledCheck kills the supervisor partway through a check, right
// after it has done something outside itself and before the check is saved,
// and restarts it one minute later. A restart must not lose a hold and must
// not do again what the killed check did.
//
// The points a test can reach from outside are the ones next to an external
// effect: the stand-in bb kills its caller after a command has answered, and
// the webhook receiver kills it while a report is being delivered. Between a
// send and the end of the check keepgoing saves state.json, the run graph,
// the run log and the incident journal, in that order, so "after the send" is
// before all of them and "during the report" is after state.json.
func TestFaultKilledCheck(t *testing.T) {
	t.Parallel()

	t.Run("while observing, before anything is written", func(t *testing.T) {
		t.Parallel()
		w := NewWorld(t, scenarioStart)
		w.Init("await_user")
		w.Control("enable")
		failingTurns(w.Thread(primaryThread))
		w.killAfter("show")
		w.Advance(time.Minute)
		s := w.killedTick("killed while observing")
		w.Check(s, "nothing was sent", nothingSent(s))
		w.Advance(time.Minute)
		s = w.Tick()
		w.Check(s, "the restart does the check that was interrupted", all(
			action(s, "reprompt"), sentExactly(s, Send{Kind: "retry", Thread: primaryThread}), holds(s),
			check(reprompts(s) == 1, "%d re-prompts counted, want 1", reprompts(s)),
		))
	})

	t.Run("after a turn retry, before the check is saved", func(t *testing.T) {
		t.Parallel()
		w := NewWorld(t, scenarioStart)
		w.Init("await_user")
		w.Control("enable")
		failingTurns(w.Thread(primaryThread))
		w.killAfter("retry")
		w.Advance(time.Minute)
		s := w.killedTick("killed after the retry")
		w.Check(s, "the retry was sent", sentExactly(s, Send{Kind: "retry", Thread: primaryThread}))
		w.Advance(time.Minute)
		s = w.Tick()
		w.Check(s, "no hold is lost or invented", holds(s))
		w.Check(s, "the restart does not retry again inside the backoff", all(action(s, "none"), nothingSent(s)))
		w.Check(s, "the interrupted retry is counted", check(reprompts(s) >= 1 && len(s.Sent) == 0, "after the restart %d re-prompts are counted and it sent %s", reprompts(s), describeSends(s.Sent)))
	})

	t.Run("after a stop, before the check is saved", func(t *testing.T) {
		t.Parallel()
		w := NewWorld(t, scenarioStart)
		w.Init("await_user", "stop")
		w.Control("enable")
		th := w.Thread(primaryThread)
		w.Advance(time.Minute)
		w.Tick() // wakes it; the turn is running
		w.Control("stop")
		w.killAfter("stop")
		w.Advance(time.Minute)
		s := w.killedTick("killed after the stop")
		w.Check(s, "the stop was sent", check(bbCommands(s, "stop") == 1, "bb thread stop ran %d times", bbCommands(s, "stop")))
		w.Advance(time.Minute)
		s = w.Tick()
		w.Check(s, "the stop hold is kept", holds(s, "stop"))
		// The restart cannot tell a stop made from one recorded and then not
		// made, so it sends the stop again, once: a second stop is harmless,
		// and taking an unmade stop as made would break "stop wins".
		w.Check(s, "the stop is sent again at most once", check(bbCommands(s, "stop") <= 1, "the restart sent the stop %d times", bbCommands(s, "stop")))
		_ = th
	})

	t.Run("with a send allowance, after the send", func(t *testing.T) {
		t.Parallel()
		w := NewWorld(t, scenarioStart)
		w.Init("await_user")
		w.EditConfig(func(l map[string]any) { l["limits"] = map[string]any{"max_sends": 9} })
		w.Control("enable")
		failingTurns(w.Thread(primaryThread))
		w.killAfter("retry")
		w.Advance(time.Minute)
		w.killedTick("killed after the retry")
		used := func() int {
			var v struct {
				Used int `json:"used"`
			}
			b, _ := os.ReadFile(filepath.Join(w.runtimeDir(), "sends.json"))
			json.Unmarshal(b, &v)
			return v.Used
		}
		s := w.record("after the kill", Result{}, nil)
		w.Check(s, "the interrupted send was counted against the allowance before it was made", check(used() == 1, "allowance used %d, want 1", used()))
	})

	// killDuringReport runs one check and kills it as a report of the given
	// type arrives at the receiver.
	killDuringReport := func(w *World, typ, label string) *Step {
		cmd := exec.Command(keepgoingBin, "tick", "-c", w.Config, "--now", w.stamp())
		cmd.Dir = w.Root
		cmd.Env = append(append([]string{}, w.env...), "BB_THREAD_ID=")
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		w.Hooks.OnReport(func(got string) {
			if got == typ && cmd.Process != nil {
				cmd.Process.Kill()
			}
		})
		if err := cmd.Start(); err != nil {
			w.t.Fatal(err)
		}
		cmd.Wait()
		w.Hooks.OnReport(nil)
		s := w.record(label, Result{Code: cmd.ProcessState.ExitCode(), Stdout: out.String(), Stderr: errb.String()}, nil)
		if s.Result.Code != -1 {
			w.Failf(s, "the check was not killed: exit %d", s.Result.Code)
		}
		return s
	}

	t.Run("while the report of a hold is being delivered", func(t *testing.T) {
		t.Parallel()
		w := NewWorld(t, scenarioStart)
		w.Init("await_user")
		w.EditConfig(func(l map[string]any) {
			errs, _ := l["errors"].(map[string]any)
			errs["max_reprompts"] = 1
		})
		w.Control("enable")
		failingTurns(w.Thread(primaryThread))
		w.Advance(time.Minute)
		s := w.Tick()
		w.Check(s, "the one re-prompt allowed", all(action(s, "reprompt"), sentExactly(s, Send{Kind: "retry", Thread: primaryThread})))
		// The next check after the backoff holds for a person, saves the
		// hold, and reports it; it is killed while the report arrives.
		w.Advance(11 * time.Minute)
		s = killDuringReport(w, "keepgoing.await_user", "killed while reporting the hold")
		w.Check(s, "the hold was saved before its report", all(holds(s, "awaitingUser"), nothingSent(s), reported(s, "keepgoing.await_user")))
		w.Advance(time.Minute)
		s = w.Tick()
		w.Check(s, "the restart keeps the hold and sends nothing", all(holds(s, "awaitingUser"), action(s, "none"), nothingSent(s), noCalls(s)))
		w.Check(s, "the report is not sent again", noReports(s))
	})

	t.Run("while a report without a hold is being delivered", func(t *testing.T) {
		t.Parallel()
		w := NewWorld(t, scenarioStart)
		w.Init("await_user")
		w.Control("enable")
		// A question on the supervised thread: the check reports it and
		// sends nothing; it raises no hold of its own.
		w.writeJSON(primaryThread+".interactions", []map[string]any{{"id": "pint_example1", "status": "pending"}})
		w.Advance(time.Minute)
		s := killDuringReport(w, "keepgoing.await_user", "killed while reporting the question")
		w.Check(s, "nothing was sent", all(nothingSent(s), reported(s, "keepgoing.await_user")))
		w.Advance(time.Minute)
		s = w.Tick()
		w.Check(s, "the restart sends nothing", all(action(s, "none"), nothingSent(s)))
		w.Check(s, "the report is not sent again", noReports(s))
	})
}

// bbCommands counts the `bb thread <command>` calls in a step.
func bbCommands(s *Step, command string) int {
	n := 0
	for _, c := range s.Calls {
		if len(c) > 1 && c[1] == command {
			n++
		}
	}
	return n
}

// TestFaultDamagedRecord empties, truncates or overwrites one record at a
// time and runs a check, status and pause. A record that decides whether
// keepgoing may act fails closed: nothing is sent and the file is named. A
// diagnostic record does not stop the work. No damaged record is rewritten.
func TestFaultDamagedRecord(t *testing.T) {
	t.Parallel()
	type expect struct {
		file string
		// mayAct: the check still does its work (only diagnostic records).
		mayAct bool
		// tickExit is the check's exit code, and named what its error says.
		tickExit int
		named    string
		// statusWorks and pauseWorks describe the commands a person uses.
		statusWorks, pauseWorks bool
		// known lists assertions that fail today, with what they wait on.
		known map[string][]Waiting
	}
	cases := []expect{
		{file: "control.json", tickExit: 1, named: "control.json", known: map[string][]Waiting{
			statusNamesIt:   Waits(WaitStatusUnreadable),
			pauseStillWorks: Waits(WaitStopDamagedControl),
		}},
		{file: "state.json", tickExit: 1, named: "state.json", pauseWorks: true, known: map[string][]Waiting{
			statusNamesIt: Waits(WaitStatusUnreadable),
		}},
		{file: "sends.json", tickExit: 0, named: "sends.json", statusWorks: true, pauseWorks: true},
		{file: "progress.json", tickExit: 0, named: "progress.json", statusWorks: true, pauseWorks: true},
		{file: "health.json", tickExit: 0, named: "health.json", statusWorks: true, pauseWorks: true},
		// The incident journal is diagnostic, but a check that cannot append
		// to it exits 1 after doing its work (docs/diagnostics.md), and so does
		// a control command after saving its change.
		{file: "incidents.json", mayAct: true, tickExit: 1, named: "incident history", statusWorks: true},
		{file: "deliveries.json", mayAct: true, tickExit: 0, statusWorks: true, pauseWorks: true},
		{file: "graph.json", mayAct: true, tickExit: 0, statusWorks: true, pauseWorks: true},
	}
	damage := map[string]func([]byte) []byte{
		"empty":     func([]byte) []byte { return nil },
		"truncated": func(b []byte) []byte { return b[:len(b)/2] },
		"garbage":   func([]byte) []byte { return []byte("\x00\x01not a record{") },
	}
	for _, c := range cases {
		for _, how := range []string{"empty", "truncated", "garbage"} {
			t.Run(c.file+"/"+how, func(t *testing.T) {
				t.Parallel()
				w := NewWorld(t, scenarioStart)
				w.Init("await_user", "error")
				// Every record exists: a fallback (health), a send allowance
				// and a no-progress timeout (sends, progress), a report
				// (deliveries), and checks (state, journal, graph).
				w.Thread(altThread)
				w.Fallbacks(fallbacksFile(existingFallback), nil)
				w.EditConfig(func(l map[string]any) {
					l["limits"] = map[string]any{"max_sends": 9, "no_progress_timeout": "24h"}
				})
				primary := w.Thread(primaryThread)
				w.Control("enable")
				w.Advance(time.Minute)
				w.Tick()
				w.Control("await-user", "--note", "fictional question")
				w.Control("resume")
				primary.FinishTurn()
				primary.OnSend = nil
				path := filepath.Join(w.runtimeDir(), c.file)
				b, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("%s was not written by the setup: %v", c.file, err)
				}
				damaged := damage[how](b)
				if err := os.WriteFile(path, damaged, 0o600); err != nil {
					t.Fatal(err)
				}
				expectFor := func(s *Step, name string, err error) {
					t.Helper()
					if waiting, ok := c.known[name]; ok {
						w.Known(s, waiting, name, err)
					} else {
						w.Check(s, name, err)
					}
				}

				w.Advance(time.Hour)
				r := w.Run("tick", "-c", w.Config, "--now", w.stamp())
				s := &Step{Label: "check with " + c.file + " " + how, At: w.Now, Result: r}
				sent := w.sendsSince()
				if c.mayAct {
					w.Check(s, "a damaged diagnostic record does not stop the work", check(sent == 1, "%d sends, want the idle wake", sent))
				} else {
					w.Check(s, "a damaged record that decides fails closed", check(sent == 0, "%d sends", sent))
				}
				w.Check(s, "the check's exit code", exit(s, c.tickExit))
				if c.named != "" {
					w.Check(s, "the check names the damaged record", check(strings.Contains(r.Stderr, c.named), "stderr %q", r.Stderr))
				}
				now, _ := os.ReadFile(path)
				w.Check(s, "the damaged record is not rewritten", check(bytes.Equal(now, damaged), "%s was rewritten", c.file))

				st := w.Run("status", "-c", w.Config)
				if c.statusWorks {
					w.Check(s, "status works", check(st.Code == 0, "status: exit %d %s", st.Code, st.Stderr))
				} else {
					expectFor(s, statusNamesIt, statusExplains(st, c.file))
				}
				p := w.Run("pause", "-c", w.Config)
				if c.pauseWorks {
					w.Check(s, "pause works", check(p.Code == 0, "pause: exit %d %s", p.Code, p.Stderr))
				} else if c.file == "incidents.json" {
					w.Check(s, "pause is saved, with a warning", check(p.Code == 1 && strings.Contains(p.Stderr, "warning: control saved"), "pause: exit %d %s", p.Code, p.Stderr))
				} else {
					expectFor(s, pauseStillWorks, check(p.Code == 0, "pause: exit %d %s", p.Code, strings.TrimSpace(p.Stderr)))
				}
			})
		}
	}
}

const (
	statusNamesIt   = "status names the damaged record, says what to do, and still prints what it can"
	pauseStillWorks = "a person can still pause or stop the loop"
)

// statusExplains: status names the file and what to do about it, and still
// prints the loop. It exits 1, because a record that decides could not be
// read.
func statusExplains(r Result, file string) error {
	text := r.Stdout + r.Stderr
	return all(
		check(r.Code == 1, "status exits %d", r.Code),
		check(strings.Contains(text, file) && strings.Contains(strings.ToLower(text), "restore"), "it does not name %s with what to do: %q", file, strings.TrimSpace(text)),
		check(strings.Contains(r.Stdout, "example-loop"), "it prints nothing else"),
	)
}

// sendsSince counts what the stand-ins received since the last recorded step.
func (w *World) sendsSince() int {
	var sends []Send
	readLines(w.t, filepath.Join(w.world, "sends.jsonl"), &sends)
	return len(sends) - w.sendsSeen
}

// WaitStatusUnreadable: status stops at the first record it cannot read and
// prints only the parse error. It should name the file, say what to do, and
// print what it can. This lane (T3) takes it next.
const WaitStatusUnreadable Waiting = "status names an unreadable record, test-suites lane"

// WaitStopDamagedControl: with control.json unreadable, pause and stop fail
// too, so nothing a person can run from keepgoing holds the loop. Checks send
// nothing, so it is held in effect, but a scheduler or daemon keeps running.
// Reported from the fault-injection suite; not assigned yet.
const WaitStopDamagedControl Waiting = "stop with a damaged control.json, unassigned"
