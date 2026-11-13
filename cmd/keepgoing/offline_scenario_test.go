//go:build unix

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter/runner"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/testexec"
)

// The owner's offline scenario for a headless loop, with a stand-in CLI: the
// network drops while a turn runs and stays down for twenty minutes, four
// hours, three days and three weeks, with a captive portal on return.
//
// The stand-ins' failure output is an assumption, not an observation of the
// real CLIs: "structured" prints the documented result shape with an error
// and no recognised code, "silent" exits non-zero with no output at all, and
// "portal" prints an HTML page. Each mode exits 1.

const (
	offlineSession = "00000000-0000-4000-8000-0000000000aa"
	offlinePrompt  = "Continue the fictional headless task."
)

type offlineLoop struct {
	t        *testing.T
	kind     string
	dir, cfg string
	clock    time.Time
}

func newOfflineLoop(t *testing.T, kind string) *offlineLoop {
	t.Helper()
	t.Setenv("BB_THREAD_ID", "")
	dir := t.TempDir()
	cli := filepath.Join(dir, "stand-in-"+kind)
	var online, structured string
	switch kind {
	case "claude":
		online = `echo '{"type":"result","subtype":"success","is_error":false,"session_id":"` + offlineSession + `","api_error_status":null}'`
		structured = `echo '{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"` + offlineSession + `","api_error_status":null}'`
	case "codex":
		online = `echo '{"type":"thread.started","thread_id":"` + offlineSession + `"}'; echo '{"type":"turn.completed","usage":{}}'`
		structured = `echo '{"type":"thread.started","thread_id":"` + offlineSession + `"}'; echo '{"type":"turn.failed","error":{"type":"stream_disconnected"}}'`
	}
	testexec.Script(t, cli, `#!/bin/sh
d="`+dir+`"
case "$1" in app-server) exit 1 ;; esac
echo "$*" >> "$d/args"
cat > "$d/last-prompt"
case "$(cat "$d/mode")" in
online) `+online+` ;;
structured) `+structured+`; exit 1 ;;
silent) exit 1 ;;
portal) echo '<html><body>Sign in to this network</body></html>'; exit 1 ;;
esac
`)
	cfg := filepath.Join(dir, "keepgoing.yaml")
	provider := map[string]string{"claude": "claude-code", "codex": "codex"}[kind]
	write(t, cfg, fmt.Sprintf("version: 1\nloop:\n  name: example-headless\n  state_dir: state\n  adapter: %s\n  provider: %s\n  %s: {cli: %s, workdir: %s}\n  prompt: %s\n", kind, provider, kind, cli, dir, offlinePrompt), 0o600)
	l := &offlineLoop{t: t, kind: kind, dir: dir, cfg: cfg, clock: time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)}
	// A check can leave a detached turn running when the test returns, and
	// its wrapper writes into the temporary directory. Wait for the runner's
	// own evidence that the turn has ended (its exit code is recorded, or its
	// process is gone) before that directory is removed. Registered after
	// TempDir, this cleanup runs before the removal.
	t.Cleanup(l.settle)
	l.mode("online")
	if code, _, errs := l.run("enable"); code != exitOK {
		t.Fatal(errs)
	}
	return l
}

// mode changes what the stand-in does from its next turn. A turn in flight
// finishes first, because the stand-in reads the mode when it starts.
func (l *offlineLoop) mode(m string) {
	l.t.Helper()
	l.settle()
	write(l.t, filepath.Join(l.dir, "mode"), m, 0o600)
}

func (l *offlineLoop) settle() {
	l.t.Helper()
	waitFor(l.t, func() bool {
		st, err := l.turnDir().Status()
		return err == nil && st.Phase != runner.Running
	}, "the stand-in turn to end")
}

func (l *offlineLoop) run(args ...string) (int, string, string) {
	f := &fixture{t: l.t, cfg: l.cfg}
	return f.run(args...)
}

func (l *offlineLoop) turnDir() runner.Dir {
	return runner.Dir(filepath.Join(l.dir, "state", "targets", "primary"))
}

// check advances the clock and runs one check once any turn has ended.
func (l *offlineLoop) check(after time.Duration) (action, reason string) {
	l.t.Helper()
	l.settle()
	l.clock = l.clock.Add(after)
	f := &fixture{t: l.t, cfg: l.cfg}
	_, out, errs := f.run("tick", "--now", l.clock.Format(time.RFC3339))
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		l.t.Fatalf("check output: %v %s %s", err, out, errs)
	}
	return fmt.Sprint(v["action"]), fmt.Sprint(v["reason"])
}

func (l *offlineLoop) sends() []string {
	b, _ := os.ReadFile(filepath.Join(l.dir, "args"))
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func (l *offlineLoop) read(name string) string {
	b, _ := os.ReadFile(filepath.Join(string(l.turnDir()), name))
	return strings.TrimSpace(string(b))
}

func TestOfflineScenarioHeadless(t *testing.T) {
	resumed := map[string]string{"claude": "--resume " + offlineSession, "codex": "exec resume --json"}
	for _, kind := range []string{"claude", "codex"} {
		t.Run(kind, func(t *testing.T) {
			l := newOfflineLoop(t, kind)
			expect := func(after time.Duration, wantAction, wantReason string) {
				t.Helper()
				if action, reason := l.check(after); action != wantAction || !strings.HasPrefix(reason, wantReason) {
					t.Fatalf("after %s: %s (%s); want %s (%s)", after, action, reason, wantAction, wantReason)
				}
			}
			// A healthy first turn starts a new session and its ID is saved.
			expect(0, "wake", "idle")

			// The network drops while the next turn runs. The CLI exits 1. An
			// idle headless session is woken again at the next check, and that
			// check saved the first turn's session ID.
			l.mode("structured")
			expect(10*time.Minute, "wake", "idle")
			if strings.Contains(l.sends()[0], offlineSession) || l.read("session") != offlineSession {
				t.Fatalf("first turn: %v, saved %q", l.sends(), l.read("session"))
			}
			expect(time.Minute, "reprompt", "error: ")
			// What the runner recorded for the failed turn: its exit code and
			// output, with the saved session untouched.
			if l.read("session") != offlineSession {
				t.Fatalf("the failed turn changed the saved session: %q", l.read("session"))
			}
			// Still offline. Re-prompts are spent into the outage with a
			// ten-minute backoff, and the third failure asks for a person.
			expect(time.Minute, "none", "backing off after error")
			expect(10*time.Minute, "reprompt", "error: ")
			expect(10*time.Minute, "reprompt", "error: ")
			expect(10*time.Minute, "await_user", "error persisted after re-prompts")
			ctl, _ := control.Load(filepath.Join(l.dir, "state"))
			if !ctl.AwaitingUser {
				t.Fatal("the loop is not on an awaiting-user hold")
			}
			// Twenty-odd minutes after the drop the loop is held. Nothing
			// changes at four hours, three days or three weeks, online or not:
			// keepgoing does not come back by itself.
			sent := len(l.sends())
			for _, later := range []time.Duration{4 * time.Hour, 3 * 24 * time.Hour, 18 * 24 * time.Hour} {
				expect(later, "none", "awaiting user")
			}
			l.mode("portal") // the network returns behind a captive portal
			expect(time.Hour, "none", "awaiting user")
			l.mode("online")
			expect(time.Hour, "none", "awaiting user")
			if len(l.sends()) != sent {
				t.Fatal("a held loop sent a prompt")
			}

			// Every send after the first resumed the saved session exactly,
			// and every one carried the whole task prompt again.
			for i, args := range l.sends() {
				if i > 0 && !strings.Contains(args, resumed[kind]) || i > 0 && !strings.Contains(args, offlineSession) {
					t.Fatalf("send %d did not resume the saved session: %s", i, args)
				}
			}
			if got, _ := os.ReadFile(filepath.Join(l.dir, "last-prompt")); string(got) != offlinePrompt {
				t.Fatalf("a re-prompt sent %q", got)
			}
			if sent != 5 {
				t.Fatalf("%d sends: %v", sent, l.sends())
			}

			// A person resumes three weeks later. Resume clears the hold but
			// not the spent re-prompts, and a headless session cannot change
			// state without a send, so the next check holds again at once
			// without trying. This is a gap, listed in docs/adapters.md.
			if code, _, errs := l.run("resume"); code != exitOK {
				t.Fatal(errs)
			}
			expect(time.Minute, "await_user", "error persisted after re-prompts")
			if len(l.sends()) != sent {
				t.Fatal("resume alone sent a prompt")
			}
			// Today's way out is to reselect the target, which starts a new
			// error episode, and then resume. Behind the portal the cycle
			// repeats against the same session and ends on hold.
			l.mode("portal")
			recoverLoop := func() {
				t.Helper()
				for _, args := range [][]string{{"retarget", "primary"}, {"resume"}} {
					if code, _, errs := l.run(args...); code != exitOK {
						t.Fatal(args, errs)
					}
				}
			}
			recoverLoop()
			expect(time.Minute, "reprompt", "error: ")
			expect(10*time.Minute, "reprompt", "error: ")
			expect(10*time.Minute, "reprompt", "error: ")
			expect(10*time.Minute, "await_user", "error persisted after re-prompts")
			// Once the network is really back, the same steps recover the
			// loop in the same session.
			l.mode("online")
			recoverLoop()
			expect(time.Minute, "reprompt", "error: ")
			expect(time.Minute, "wake", "idle")
			last := l.sends()[len(l.sends())-1]
			if !strings.Contains(last, offlineSession) {
				t.Fatalf("recovery did not resume the session: %s", last)
			}
		})
	}
}

// If the very first turn fails before the CLI reports a session, nothing is
// saved, so each re-prompt starts a new conversation with the whole task.
func TestOfflineFirstTurnStartsANewSessionOnEveryReprompt(t *testing.T) {
	for _, kind := range []string{"claude", "codex"} {
		t.Run(kind, func(t *testing.T) {
			l := newOfflineLoop(t, kind)
			l.mode("silent")
			if action, _ := l.check(0); action != "wake" {
				t.Fatal(action)
			}
			for _, after := range []time.Duration{time.Minute, 10 * time.Minute, 10 * time.Minute} {
				if action, reason := l.check(after); action != "reprompt" {
					t.Fatalf("%s (%s)", action, reason)
				}
			}
			if action, _ := l.check(10 * time.Minute); action != "await_user" {
				t.Fatal(action)
			}
			if l.read("session") != "" || len(l.sends()) != 4 {
				t.Fatalf("saved %q after %d sends", l.read("session"), len(l.sends()))
			}
			for i, args := range l.sends() {
				if strings.Contains(args, "resume") {
					t.Fatalf("send %d resumed a session that was never reported: %s", i, args)
				}
			}
			if got, _ := os.ReadFile(filepath.Join(l.dir, "last-prompt")); string(got) != offlinePrompt {
				t.Fatalf("%q", got)
			}
		})
	}
}
