//go:build unix

package scenario

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stateDir is the loop's state directory.
func (w *World) stateDir() string { return filepath.Join(filepath.Dir(w.Config), "state") }

// noRoom runs keepgoing with a zero file-size limit: creating a file works,
// writing data into one fails with "file too large", as on a full disk. The
// stand-ins lift the limit for themselves.
func (w *World) noRoom(args ...string) Result {
	w.t.Helper()
	cmd := exec.Command("/bin/sh", append([]string{"-c", `ulimit -S -f 0 && exec "$@"`, "sh", keepgoingBin}, args...)...)
	cmd.Dir = w.Root
	cmd.Env = append(append([]string{}, w.env...), "BB_THREAD_ID=")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if x, ok := err.(*exec.ExitError); ok {
		code = x.ExitCode()
	} else if err != nil {
		w.t.Fatal(err)
	}
	return Result{Args: args, Code: code, Stdout: out.String(), Stderr: errb.String()}
}

// snapshotRecords reads the files a check and a control command replace.
func (w *World) snapshotRecords() map[string]string {
	w.t.Helper()
	got := map[string]string{}
	for _, name := range []string{"state.json", "control.json", "incidents.json", "graph.json"} {
		if b, err := os.ReadFile(filepath.Join(w.stateDir(), name)); err == nil {
			got[name] = string(b)
		}
	}
	return got
}

// recordsIntact: the records are byte for byte what they were, each still
// parses, and no temporary file was left in the state directory.
func (w *World) recordsIntact(before map[string]string) error {
	var errs []error
	after := w.snapshotRecords()
	for name, b := range before {
		if after[name] != b {
			errs = append(errs, fmt.Errorf("%s changed", name))
		}
		var v any
		if json.Unmarshal([]byte(after[name]), &v) != nil {
			errs = append(errs, fmt.Errorf("%s no longer parses", name))
		}
	}
	entries, err := os.ReadDir(w.stateDir())
	if err != nil {
		return err
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			errs = append(errs, fmt.Errorf("temporary file %s left behind", e.Name()))
		}
	}
	return all(errs...)
}

// unknownError makes every turn of the thread fail with an unclassified
// error, so a session in error stays in error whatever it is sent.
func unknownError(th *Thread) {
	th.Event("provider/error", map[string]any{"willRetry": false, "errorInfo": map[string]any{"category": "unknown", "httpStatusCode": nil, "providerCode": "unknown"}})
	th.Set("error")
}

// TestScenarioUnwritableStateDirectory: the loop's state directory stops
// being writable (a permission change, a read-only mount) while its session
// is in error. Checks run and exit 1, which says they could not be recorded.
// A check that cannot save what it did must not do it: otherwise the
// re-prompt it sends is never counted, the next check sends another, and the
// cap that should end in a hold is never reached. With a send allowance
// configured the allowance cannot be reserved and nothing is sent; without
// one, which is the default, every check re-prompts.
func TestScenarioUnwritableStateDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Fatal("this scenario needs an ordinary user: root can write a read-only directory")
	}
	t.Parallel()
	for _, tc := range []struct {
		name      string
		allowance bool
	}{{"no send allowance (the default)", false}, {"with a send allowance", true}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := NewWorld(t, scenarioStart)
			w.Init("await_user", "error")
			if tc.allowance {
				w.EditConfig(func(l map[string]any) { l["limits"] = map[string]any{"max_sends": 20} })
			}
			primary := w.Thread(primaryThread)
			w.Control("enable")
			w.Advance(time.Minute)
			s := w.Tick()
			w.Check(s, "first wake", action(s, "wake"))
			// From now on every turn fails with an unclassified error.
			primary.OnSend = func(th *Thread, m Send) { th.StartTurn(); unknownError(th) }
			unknownError(primary)

			if err := os.Chmod(w.stateDir(), 0o500); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Chmod(w.stateDir(), 0o700) })
			w.sendsUnrecordable = true
			before := w.snapshotRecords()
			var sent []error
			for i := 1; i <= 6; i++ {
				w.Advance(15 * time.Minute)
				r := w.Run("tick", "-c", w.Config, "--now", w.stamp())
				s = w.record("check with an unwritable state directory", r, nil)
				w.Check(s, fmt.Sprintf("check %d says it could not be recorded", i), all(
					exit(s, 1), check(strings.Contains(r.Stderr, "permission denied"), "stderr does not name the cause: %q", r.Stderr),
				))
				sent = append(sent, stepErr(s, nothingSent(s)))
			}
			if tc.allowance {
				w.Check(s, "a check that cannot reserve its send sends nothing", all(sent...))
			} else {
				w.Known(s, Waits(WaitUnsavedSend), "a check that cannot save its state sends nothing", all(sent...))
			}

			// What a person can do: status still reads the loop; a pause fails
			// loudly and leaves the records as they were.
			st := w.Run("status", "-c", w.Config)
			w.Check(s, "status still works", check(st.Code == 0, "status: exit %d %s", st.Code, st.Stderr))
			p := w.Run("pause", "-c", w.Config)
			s = w.record("pause", p, nil)
			w.Check(s, "pause fails loudly", all(exit(s, 1), check(strings.Contains(p.Stderr, "permission denied"), "stderr %q", p.Stderr), holds(s)))
			w.Check(s, "records untouched", w.recordsIntact(before))

			// The directory is writable again. The next check works, and its
			// re-prompt is counted.
			if err := os.Chmod(w.stateDir(), 0o700); err != nil {
				t.Fatal(err)
			}
			// Sends made while nothing could be saved stay missing from the
			// run history, so its count of attempts stays short.
			sentBefore := w.sendsTotal
			w.Advance(15 * time.Minute)
			s = w.Tick()
			w.Check(s, "recovers when the directory is writable", all(exit(s, 0), action(s, "reprompt"), sentExactly(s, Send{Kind: "retry", Thread: primaryThread})))
			var counted struct {
				State struct {
					Reprompts int `json:"reprompts"`
				} `json:"state"`
			}
			json.Unmarshal([]byte(s.Status.Raw), &counted)
			w.Check(s, "the re-prompt is counted", check(counted.State.Reprompts == 1, "status counts %d re-prompts", counted.State.Reprompts))
			t.Logf("sends while the state directory was unwritable: %d", sentBefore-1)
		})
	}
}

// TestScenarioNoRoomToWrite: the disk holding the state directory is full.
// Every write fails. Checks exit 1 and send nothing, a pause fails loudly,
// no record is left half-written or truncated and no temporary file is left
// behind. When there is room again, the loop carries on.
func TestScenarioNoRoomToWrite(t *testing.T) {
	t.Parallel()
	for _, session := range []string{"idle", "error"} {
		t.Run("session "+session, func(t *testing.T) {
			t.Parallel()
			w := NewWorld(t, scenarioStart)
			w.Init("await_user", "error")
			primary := w.Thread(primaryThread)
			w.Control("enable")
			w.Advance(time.Minute)
			w.Tick()
			if session == "error" {
				primary.OnSend = func(th *Thread, m Send) { th.StartTurn(); unknownError(th) }
				unknownError(primary)
			} else {
				primary.FinishTurn()
			}
			w.sendsUnrecordable = true
			before := w.snapshotRecords()
			for i := 1; i <= 3; i++ {
				w.Advance(time.Hour)
				r := w.noRoom("tick", "-c", w.Config, "--now", w.stamp())
				s := w.record("check with no room to write", r, nil)
				w.Check(s, fmt.Sprintf("check %d", i), all(
					exit(s, 1), check(strings.Contains(r.Stderr, "file too large"), "stderr does not name the cause: %q", r.Stderr),
					nothingSent(s),
				))
			}
			p := w.noRoom("pause", "-c", w.Config)
			s := w.record("pause with no room", p, nil)
			w.Check(s, "pause fails loudly", all(exit(s, 1), check(strings.Contains(p.Stderr, "file too large"), "stderr %q", p.Stderr), holds(s)))
			st := w.noRoom("status", "-c", w.Config)
			w.Check(s, "status still works", check(st.Code == 0, "status: exit %d %s", st.Code, st.Stderr))
			w.Check(s, "records untouched", w.recordsIntact(before))

			// Room again. An idle session is woken; one in error has its
			// failed turn retried.
			w.sendsUnrecordable = false
			w.Advance(time.Hour)
			s = w.Tick()
			if session == "idle" {
				w.Check(s, "carries on when there is room", all(exit(s, 0), action(s, "wake"), sentExactly(s, Send{Kind: "tell", Thread: primaryThread, Mode: "auto"})))
			} else {
				w.Check(s, "carries on when there is room", all(exit(s, 0), action(s, "reprompt"), sentExactly(s, Send{Kind: "retry", Thread: primaryThread})))
			}
		})
	}
}

// WaitUnsavedSend: a check sends before it has saved anything, so when the
// state directory cannot be written the send happens and is never counted.
// Reported from this scenario; not assigned yet.
const WaitUnsavedSend Waiting = "a check sends before it can save, unassigned"
