package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
)

// No Antigravity CLI is installed where these tests run. The agent is this
// test binary acting as a stand-in ACP server with fictional data; nothing
// here establishes the real CLI's command line, versions or behaviour.

// An opaque, non-UUID identity with spaces and non-ASCII text: it must be
// resumed exactly as given.
const antigravitySession = "fictional conversation café-项目 01"

// antigravityStandIn speaks just enough ACP v1 for the worker: it answers
// initialization, session setup and one prompt, and appends what it was sent
// to a log. Its stderr and message text carry misleading words on purpose.
func antigravityStandIn() {
	mode, target, release := os.Getenv("KEEPGOING_AGY_MODE"), os.Getenv("KEEPGOING_AGY_TARGET"), os.Getenv("KEEPGOING_AGY_RELEASE")
	note := func(v map[string]any) {
		b, _ := json.Marshal(v)
		f, err := os.OpenFile(os.Getenv("KEEPGOING_TEST_RESULT"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			os.Exit(2)
		}
		_, _ = f.Write(append(b, '\n'))
		_ = f.Close()
	}
	onDisk := func(name string) int {
		found, _ := filepath.Glob(filepath.Join(target, antigravityTurnsDir, "*", name))
		return len(found)
	}
	send := func(v map[string]any) {
		v["jsonrpc"] = "2.0"
		b, _ := json.Marshal(v)
		_, _ = os.Stdout.Write(append(b, '\n'))
	}
	awaitRelease := func() {
		for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			if _, err := os.Stat(release); err == nil {
				return
			}
		}
	}
	_, _ = os.Stderr.WriteString("fictional rate limit 429 authentication network failure; permission denied\n")
	cwd, _ := os.Getwd()
	note(map[string]any{"event": "start", "pid": os.Getpid(), "args": os.Args[1:], "cwd": cwd, "value": os.Getenv("KEEPGOING_AGY_VALUE"),
		"wrapperEnv": os.Getenv(envTurnDir) + os.Getenv(envTurnArgv) + os.Getenv(envTurnKey) + os.Getenv(envTurnACP)})
	if mode == "garbage" {
		_, _ = os.Stdout.WriteString("rate limit 429: this is not a protocol frame\n")
	}
	r := bufio.NewReaderSize(os.Stdin, 2<<20)
	read := func() (struct {
		ID     json.RawMessage
		Method string
		Params map[string]json.RawMessage
		Result json.RawMessage
	}, bool) {
		var f struct {
			ID     json.RawMessage
			Method string
			Params map[string]json.RawMessage
			Result json.RawMessage
		}
		line, err := r.ReadBytes('\n')
		if err != nil {
			return f, false
		}
		_ = json.Unmarshal(line, &f)
		return f, true
	}
	text := func(raw json.RawMessage) string {
		var s string
		_ = json.Unmarshal(raw, &s)
		return s
	}
	for {
		f, ok := read()
		if !ok {
			break
		}
		switch f.Method {
		case "initialize":
			if mode == "hang" {
				time.Sleep(60 * time.Second)
				os.Exit(0)
			}
			send(map[string]any{"id": f.ID, "result": map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{"sessionCapabilities": map[string]any{"resume": map[string]any{}}}}})
		case "session/new":
			note(map[string]any{"event": "new", "cwd": text(f.Params["cwd"])})
			if mode == "slow-setup" {
				awaitRelease()
			}
			send(map[string]any{"id": f.ID, "result": map[string]any{"sessionId": antigravitySession}})
		case "session/resume":
			note(map[string]any{"event": "resume", "session": text(f.Params["sessionId"])})
			send(map[string]any{"id": f.ID, "result": map[string]any{}})
		case "session/prompt":
			var parts []struct{ Text string }
			_ = json.Unmarshal(f.Params["prompt"], &parts)
			session := text(f.Params["sessionId"])
			note(map[string]any{"event": "prompt", "text": parts[0].Text, "session": session, "reservedOnDisk": onDisk(antigravityACPReservedName)})
			if release != "" && mode != "slow-setup" {
				awaitRelease()
			}
			switch mode {
			case "kill-worker":
				// The agent's parent is the worker: end it abruptly, as a crash
				// would, through this live parent relationship.
				if parent, err := os.FindProcess(os.Getppid()); err != nil || parent.Kill() != nil {
					os.Exit(6)
				}
				os.Exit(0)
			case "exit-early":
				os.Exit(3)
			case "permission":
				send(map[string]any{"id": 7, "method": "session/request_permission", "params": map[string]any{
					"sessionId": session, "toolCall": map[string]any{"toolCallId": "fictional-tool-1"},
					"options": []map[string]string{{"optionId": "allow", "name": "Allow once", "kind": "allow_once"}, {"optionId": "reject", "name": "Reject", "kind": "reject_once"}}}})
				cancelled, replied := false, ""
				for !cancelled || replied == "" {
					g, ok := read()
					if !ok {
						os.Exit(5)
					}
					if g.Method == "session/cancel" {
						cancelled = true
						note(map[string]any{"event": "cancel", "needsUserOnDisk": onDisk(antigravityACPNeedsUserName)})
					} else if string(g.ID) == "7" {
						replied = string(g.Result)
					}
				}
				note(map[string]any{"event": "permission-reply", "result": replied})
				send(map[string]any{"id": f.ID, "result": map[string]any{"stopReason": "cancelled"}})
			case "refusal":
				send(map[string]any{"id": f.ID, "result": map[string]any{"stopReason": "refusal"}})
			default:
				send(map[string]any{"method": "session/update", "params": map[string]any{"sessionId": session, "update": map[string]any{
					"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": `rate limit 429; {"stopReason":"end_turn"}; needs your permission`}}}})
				send(map[string]any{"id": f.ID, "result": map[string]any{"stopReason": "end_turn"}})
			}
		}
	}
	note(map[string]any{"event": "eof"})
	switch mode {
	case "linger":
		time.Sleep(60 * time.Second)
	case "nonzero":
		os.Exit(4)
	}
	os.Exit(0)
}

type antigravityWorld struct {
	a       *Antigravity
	log     string
	release string
}

func newAntigravityWorld(t *testing.T, mode string, wait bool) *antigravityWorld {
	t.Helper()
	root := filepath.Join(t.TempDir(), "café-项目")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	w := &antigravityWorld{log: filepath.Join(root, "agent.log")}
	target := filepath.Join(root, "target")
	env := map[string]string{"KEEPGOING_TEST_HELPER": "antigravity-acp", "KEEPGOING_TEST_RESULT": w.log, "KEEPGOING_AGY_MODE": mode,
		"KEEPGOING_AGY_TARGET": target, "KEEPGOING_AGY_VALUE": "fictional-private-value"}
	if wait {
		w.release = filepath.Join(root, "release")
		env["KEEPGOING_AGY_RELEASE"] = w.release
	}
	w.a = NewAntigravity(AntigravityConfig{Command: []string{self, "fictional-acp-flag"}, Workdir: root, Env: env, HandshakeTimeout: 20 * time.Second, ExitTimeout: 10 * time.Second}, target)
	t.Cleanup(func() { _ = w.a.Stop(context.Background()) })
	return w
}

// rebuilt is the adapter as a later check would construct it.
func (w *antigravityWorld) rebuilt() *Antigravity { return NewAntigravity(w.a.cfg, string(w.a.dir)) }

func (w *antigravityWorld) events(t *testing.T) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(w.log)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var v map[string]any
		if line != "" && json.Unmarshal([]byte(line), &v) == nil {
			out = append(out, v)
		}
	}
	return out
}

func (w *antigravityWorld) named(t *testing.T, event string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, e := range w.events(t) {
		if e["event"] == event {
			out = append(out, e)
		}
	}
	return out
}

func (w *antigravityWorld) await(t *testing.T, event string, n int) []map[string]any {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if got := w.named(t, event); len(got) >= n {
			return got
		}
	}
	t.Fatalf("stand-in never logged %d %q events: %v", n, event, w.events(t))
	return nil
}

func (w *antigravityWorld) send(t *testing.T, prompt string) {
	t.Helper()
	if err := w.rebuilt().Send(context.Background(), prompt, adapter.ModeAuto); err != nil {
		t.Fatalf("send: %v", err)
	}
}

func (w *antigravityWorld) observe(t *testing.T) decide.Observation {
	t.Helper()
	obs, err := w.rebuilt().Observe(context.Background())
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	return obs
}

func (w *antigravityWorld) turns(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(string(w.a.dir), antigravityTurnsDir))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func unknownError(obs decide.Observation) bool {
	return obs.State == decide.Error && obs.ErrorClass == decide.ErrUnknown && !obs.DeferredInteraction && !obs.AwaitingInput
}

func TestAntigravityRunnerTwoTurnsResumeExactlyOnThisPlatform(t *testing.T) {
	w := newAntigravityWorld(t, "ok", true)
	ctx := context.Background()
	if obs := w.observe(t); obs.State != decide.Idle {
		t.Fatalf("initial %+v", obs)
	}
	prompt := "fictional café\n项目 task with \"quotes\""
	w.send(t, prompt)
	got := w.await(t, "prompt", 1)[0]
	if got["text"] != prompt || got["session"] != antigravitySession {
		t.Fatalf("prompt frame: %v", got)
	}
	// The reservation was on disk before the prompt frame reached the agent.
	if got["reservedOnDisk"] != float64(1) {
		t.Fatalf("prompt left before its reservation was saved: %v", got)
	}
	start := w.named(t, "start")[0]
	if args, _ := json.Marshal(start["args"]); string(args) != `["fictional-acp-flag"]` || strings.Contains(string(args), "task") {
		t.Fatalf("agent argv %s", args)
	}
	if start["value"] != "fictional-private-value" || start["wrapperEnv"] != "" {
		t.Fatalf("agent environment: %v", start)
	}
	assertCursorWorkdir(t, start["cwd"].(string), w.a.cfg.Workdir)
	assertCursorWorkdir(t, w.named(t, "new")[0]["cwd"].(string), w.a.cfg.Workdir)
	if obs := w.observe(t); obs.State != decide.Active {
		t.Fatalf("running %+v", obs)
	}
	// A running turn refuses another send, from this adapter or a rebuilt one.
	var unsupported adapter.ErrUnsupported
	if err := w.rebuilt().Send(ctx, "duplicate", adapter.ModeQueue); !errors.As(err, &unsupported) {
		t.Fatalf("send to a running turn: %v", err)
	}
	if err := os.WriteFile(w.release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, w.a.dir, Finished)
	if obs := w.observe(t); obs.State != decide.Idle || obs.ErrorClass != decide.ErrNone || obs.DeferredInteraction {
		t.Fatalf("returned turn %+v", obs)
	}
	if session, err := w.a.dir.antigravitySession(); err != nil || session != antigravitySession {
		t.Fatalf("saved conversation %q %v", session, err)
	}
	if len(w.named(t, "prompt")) != 1 || len(w.turns(t)) != 1 {
		t.Fatalf("one send must be one prompt in one turn: %v", w.events(t))
	}
	// The protocol stream is never stored, and the prompt is not in the records.
	if _, err := os.Stat(w.a.dir.path("stdout")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("protocol output was written to a file")
	}
	for _, turn := range w.turns(t) {
		for _, name := range []string{antigravityACPReservedName, antigravityOutcomeName} {
			raw, err := os.ReadFile(filepath.Join(string(w.a.dir), antigravityTurnsDir, turn, name))
			// Turn identities are random hex, so look for words, not digits.
			if err != nil || strings.Contains(string(raw), "task") || strings.Contains(string(raw), "quotes") || strings.Contains(string(raw), "rate limit") || strings.Contains(string(raw), "permission") {
				t.Fatalf("%s: %v %s", name, err, raw)
			}
		}
	}

	// Second turn, from a rebuilt adapter: exact resume, never a new conversation.
	second := "second fictional prompt"
	w.send(t, second)
	prompts := w.await(t, "prompt", 2)
	if prompts[1]["text"] != second || prompts[1]["session"] != antigravitySession {
		t.Fatalf("second prompt: %v", prompts[1])
	}
	if resumes := w.named(t, "resume"); len(resumes) != 1 || resumes[0]["session"] != antigravitySession || len(w.named(t, "new")) != 1 {
		t.Fatalf("resume: %v", w.events(t))
	}
	waitFor(t, w.a.dir, Finished)
	if obs := w.observe(t); obs.State != decide.Idle {
		t.Fatalf("second turn %+v", obs)
	}
	if len(w.turns(t)) != 2 || len(w.named(t, "prompt")) != 2 {
		t.Fatalf("turns %v", w.turns(t))
	}
}

func TestAntigravityRunnerHoldsACancelledInteractionAcrossRestarts(t *testing.T) {
	w := newAntigravityWorld(t, "permission", false)
	ctx := context.Background()
	w.send(t, "fictional task needing a tool")
	waitFor(t, w.a.dir, Finished)
	// The hold was on disk before the cancellation reached the agent, and the
	// reply selected no option.
	if cancel := w.await(t, "cancel", 1)[0]; cancel["needsUserOnDisk"] != float64(1) {
		t.Fatalf("cancellation left before the hold was saved: %v", cancel)
	}
	if reply := w.await(t, "permission-reply", 1)[0]["result"]; reply != `{"outcome":{"outcome":"cancelled"}}` {
		t.Fatalf("permission reply %v", reply)
	}
	for i := 0; i < 3; i++ { // every restart sees the same evidence
		obs := w.observe(t)
		if !obs.DeferredInteraction || obs.State == decide.Idle || obs.State == decide.Active || obs.ErrorClass != decide.ErrUnknown {
			t.Fatalf("restart %d: %+v", i, obs)
		}
		if err := w.rebuilt().Send(ctx, "must not be sent", adapter.ModeAuto); err == nil {
			t.Fatal("sent on top of an unadopted interaction")
		}
	}
	if len(w.named(t, "prompt")) != 1 || len(w.turns(t)) != 1 {
		t.Fatalf("a refused send still reached the agent: %v", w.events(t))
	}
	// Adoption is the supervisor's record that its hold is saved. Afterwards the
	// cancelled turn is an ordinary unknown failure, and an explicit resume may
	// send again, into the same conversation.
	if err := w.rebuilt().AdoptInteraction(ctx); err != nil {
		t.Fatal(err)
	}
	if err := w.rebuilt().AdoptInteraction(ctx); err != nil {
		t.Fatalf("adoption is not idempotent: %v", err)
	}
	if obs := w.observe(t); !unknownError(obs) {
		t.Fatalf("after adoption %+v", obs)
	}
	w.send(t, "after an explicit resume")
	if resumes := w.await(t, "resume", 1); resumes[0]["session"] != antigravitySession {
		t.Fatalf("resume %v", resumes)
	}
	waitFor(t, w.a.dir, Finished)
	// The same permission request holds the new turn too; the first turn's
	// adoption does not pre-clear it.
	if obs := w.observe(t); !obs.DeferredInteraction {
		t.Fatalf("second interaction was not held: %+v", obs)
	}
}

func TestAntigravityRunnerFailuresStayUnknown(t *testing.T) {
	for mode, tc := range map[string]struct {
		phase    Phase
		reserved bool
	}{
		"refusal":    {Finished, true},  // a documented stop reason that is not a return
		"garbage":    {Finished, false}, // prose instead of a protocol frame
		"exit-early": {Finished, true},  // the agent dies after the prompt was written
		"nonzero":    {Finished, true},  // end_turn, then a failing exit status
	} {
		t.Run(mode, func(t *testing.T) {
			w := newAntigravityWorld(t, mode, false)
			w.send(t, "fictional task")
			waitFor(t, w.a.dir, tc.phase)
			obs := w.observe(t)
			if !unknownError(obs) {
				t.Fatalf("%+v", obs)
			}
			if obs.ResetsAt != (time.Time{}) || obs.Retrying {
				t.Fatalf("invented provider evidence: %+v", obs)
			}
			session, err := w.a.dir.antigravitySession()
			if err != nil || (session != "") != tc.reserved {
				t.Fatalf("saved conversation %q %v", session, err)
			}
			// The supervisor's bounded error recovery may send again: a new turn,
			// resuming exactly the conversation whenever a prompt had been reserved.
			w.send(t, "fictional retry")
			waitFor(t, w.a.dir, tc.phase)
			if tc.reserved {
				if resumes := w.await(t, "resume", 1); resumes[0]["session"] != antigravitySession || len(w.named(t, "new")) != 1 {
					t.Fatalf("retry did not resume exactly: %v", w.events(t))
				}
			} else if len(w.named(t, "resume")) != 0 {
				t.Fatalf("resumed a conversation no prompt was reserved for: %v", w.events(t))
			}
			if len(w.turns(t)) != 2 {
				t.Fatalf("turns %v", w.turns(t))
			}
		})
	}
	t.Run("agent cannot start", func(t *testing.T) {
		w := newAntigravityWorld(t, "ok", false)
		w.a.cfg.Command = []string{filepath.Join(w.a.cfg.Workdir, "absent-agent")}
		w.send(t, "fictional task")
		waitFor(t, w.a.dir, Finished)
		if obs := w.observe(t); !unknownError(obs) {
			t.Fatalf("%+v", obs)
		}
		if session, _ := w.a.dir.antigravitySession(); session != "" {
			t.Fatalf("saved a conversation without one: %q", session)
		}
	})
	t.Run("no command", func(t *testing.T) {
		w := newAntigravityWorld(t, "ok", false)
		w.a.cfg.Command = nil
		if err := w.a.Send(context.Background(), "fictional task", adapter.ModeAuto); err == nil || len(w.turns(t)) != 0 {
			t.Fatalf("sent without a command: %v", err)
		}
	})
}

func TestAntigravityRunnerBoundsSetupAndExit(t *testing.T) {
	t.Run("setup that never answers", func(t *testing.T) {
		w := newAntigravityWorld(t, "hang", false)
		w.a.cfg.HandshakeTimeout = 300 * time.Millisecond
		start := time.Now()
		w.send(t, "fictional task")
		waitFor(t, w.a.dir, Finished)
		if took := time.Since(start); took > 20*time.Second {
			t.Fatalf("worker waited %s for setup", took)
		}
		if obs := w.observe(t); !unknownError(obs) {
			t.Fatalf("%+v", obs)
		}
		// No prompt was reserved, so none left the worker and no conversation is saved.
		if len(w.named(t, "prompt")) != 0 {
			t.Fatal("prompt reached an agent that never finished setup")
		}
		if session, _ := w.a.dir.antigravitySession(); session != "" {
			t.Fatalf("saved conversation %q", session)
		}
	})
	t.Run("agent that will not exit", func(t *testing.T) {
		w := newAntigravityWorld(t, "linger", false)
		w.a.cfg.ExitTimeout = 300 * time.Millisecond
		start := time.Now()
		w.send(t, "fictional task")
		waitFor(t, w.a.dir, Finished)
		if took := time.Since(start); took > 20*time.Second {
			t.Fatalf("worker waited %s for exit", took)
		}
		// The turn returned, but the exit status is unknown: not a clean return.
		st, _ := w.a.dir.Status()
		if st.ExitCodeKnown {
			t.Fatalf("exit status of a killed agent reported as known: %+v", st)
		}
		if obs := w.observe(t); !unknownError(obs) {
			t.Fatalf("%+v", obs)
		}
	})
	t.Run("a cancelled check does nothing", func(t *testing.T) {
		w := newAntigravityWorld(t, "ok", false)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := w.a.Observe(ctx); err == nil {
			t.Fatal("observed in a cancelled check")
		}
		if err := w.a.Send(ctx, "fictional task", adapter.ModeAuto); err == nil || len(w.turns(t)) != 0 {
			t.Fatalf("sent in a cancelled check: %v", err)
		}
		if err := w.a.Stop(ctx); err == nil {
			t.Fatal("stopped in a cancelled check")
		}
		if err := w.a.AdoptInteraction(ctx); err == nil {
			t.Fatal("adopted in a cancelled check")
		}
	})
	t.Run("limits are clamped", func(t *testing.T) {
		a := NewAntigravity(AntigravityConfig{HandshakeTimeout: time.Hour, ExitTimeout: -1}, t.TempDir())
		if h, e := a.limits(); h != antigravityMaxLimit || e != antigravityDefaultExit {
			t.Fatalf("%s %s", h, e)
		}
	})
}

func TestAntigravityRunnerOwnedStopOnThisPlatform(t *testing.T) {
	w := newAntigravityWorld(t, "ok", true)
	ctx := context.Background()
	w.send(t, "fictional task")
	w.await(t, "prompt", 1)
	st, err := w.a.dir.Status()
	if err != nil || st.Phase != Running || !validStopID(st.Record.StopKey) {
		t.Fatalf("%+v %v", st, err)
	}
	// Point the saved record at this test process. If a stop ever signalled the
	// saved process ID, the test itself would be the one to die; the request must
	// instead reach the worker that holds this turn's key.
	original, err := os.ReadFile(w.a.dir.path("run.json"))
	if err != nil {
		t.Fatal(err)
	}
	st.Record.PID = os.Getpid()
	forged, _ := json.Marshal(st.Record)
	write(t, w.a.dir.path("run.json"), string(forged))
	if err := w.rebuilt().Stop(ctx); err != nil {
		t.Fatal(err)
	}
	write(t, w.a.dir.path("run.json"), string(original))
	waitFor(t, w.a.dir, Crashed)
	if obs := w.observe(t); !unknownError(obs) {
		t.Fatalf("stopped turn %+v", obs)
	}
	// The agent ends with the worker's group or job. It must actually be gone:
	// it never saw its stdin close, and its process no longer exists.
	agent := int(w.named(t, "start")[0]["pid"].(float64))
	for deadline := time.Now().Add(15 * time.Second); alive(agent); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("agent process %d survived the stop", agent)
		}
	}
	if len(w.named(t, "eof")) != 0 || len(w.named(t, "prompt")) != 1 {
		t.Fatalf("agent ran on after the stop: %v", w.events(t))
	}
	// The prompt had been reserved, so the next turn resumes that conversation.
	if err := os.WriteFile(w.release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	w.send(t, "fictional retry after stop")
	if resumes := w.await(t, "resume", 1); resumes[0]["session"] != antigravitySession {
		t.Fatalf("%v", resumes)
	}
	waitFor(t, w.a.dir, Finished)
	if obs := w.observe(t); obs.State != decide.Idle {
		t.Fatalf("%+v", obs)
	}
	// Stopping a finished turn is a no-op.
	if err := w.rebuilt().Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

// A worker that dies after reserving its prompt leaves a reservation with no
// outcome and no exit record. Whether the prompt was delivered is unknown; the
// conversation is not. Here the stand-in agent kills its own parent, the
// worker, right after the prompt arrives.
func TestAntigravityRunnerReconcilesAnUnconfirmedReservation(t *testing.T) {
	w := newAntigravityWorld(t, "kill-worker", false)
	w.send(t, "fictional task")
	w.await(t, "prompt", 1)
	waitFor(t, w.a.dir, Crashed)
	turn, ok, err := w.a.dir.antigravityTurn()
	if err != nil || !ok {
		t.Fatal(err)
	}
	turnDir := w.a.dir.antigravityTurnDir(turn)
	if _, has, _ := turnDir.antigravityOutcome(); has {
		t.Fatal("fixture: the worker was meant to die before saving an outcome")
	}
	before, err := os.ReadFile(turnDir.path(antigravityACPReservedName))
	if err != nil {
		t.Fatalf("the reservation must have been saved before the prompt: %v", err)
	}
	for i := 0; i < 2; i++ {
		if obs := w.observe(t); !unknownError(obs) {
			t.Fatalf("restart %d: %+v", i, obs)
		}
	}
	if session, err := w.a.dir.antigravitySession(); err != nil || session != antigravitySession {
		t.Fatalf("conversation from the reservation: %q %v", session, err)
	}
	// Observing replays nothing. A later send is a new turn that resumes exactly
	// the reserved conversation and leaves the old records untouched.
	if len(w.named(t, "prompt")) != 1 || len(w.named(t, "start")) != 1 {
		t.Fatalf("observation started an agent: %v", w.events(t))
	}
	w.a.cfg.Env["KEEPGOING_AGY_MODE"] = "ok"
	w.send(t, "fictional recovery prompt")
	waitFor(t, w.a.dir, Finished)
	if resumes := w.await(t, "resume", 1); resumes[0]["session"] != antigravitySession || len(w.named(t, "new")) != 1 {
		t.Fatalf("%v", w.events(t))
	}
	if after, _ := os.ReadFile(turnDir.path(antigravityACPReservedName)); string(after) != string(before) || len(w.turns(t)) != 2 {
		t.Fatal("the unconfirmed turn's record was replaced")
	}
	if obs := w.observe(t); obs.State != decide.Idle {
		t.Fatalf("%+v", obs)
	}
}

// If the supervisor dies between starting a worker and recording which worker
// it started, that worker is alive but no record identifies it. Nothing may be
// sent beside it, whatever an older run record says.
func TestAntigravityRunnerFailsClosedOnAnUnconfirmedWorker(t *testing.T) {
	unconfirmed := func(t *testing.T, w *antigravityWorld) {
		t.Helper()
		turn, err := randomStopID()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(string(w.a.dir.antigravityTurnDir(turn)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := w.a.dir.saveAntigravityTurn(turn); err != nil {
			t.Fatal(err)
		}
	}
	refused := func(t *testing.T, w *antigravityWorld, prompts int) {
		t.Helper()
		for i := 0; i < 2; i++ {
			obs, err := w.rebuilt().Observe(context.Background())
			if !errors.Is(err, errAntigravityUnconfirmed) || obs.State != decide.Unknown || obs.ErrorClass != decide.ErrUnknown {
				t.Fatalf("observe %+v %v", obs, err)
			}
			if err := w.rebuilt().Send(context.Background(), "must not be sent", adapter.ModeAuto); !errors.Is(err, errAntigravityUnconfirmed) {
				t.Fatalf("send beside an unconfirmed worker: %v", err)
			}
		}
		if len(w.named(t, "prompt")) != prompts {
			t.Fatalf("a refused send reached an agent: %v", w.events(t))
		}
	}
	t.Run("first turn, no run record", func(t *testing.T) {
		w := newAntigravityWorld(t, "ok", false)
		unconfirmed(t, w)
		refused(t, w, 0)
	})
	t.Run("an earlier turn's run record is not this turn's", func(t *testing.T) {
		w := newAntigravityWorld(t, "ok", false)
		w.send(t, "fictional task")
		waitFor(t, w.a.dir, Finished)
		if obs := w.observe(t); obs.State != decide.Idle {
			t.Fatalf("%+v", obs)
		}
		unconfirmed(t, w)
		refused(t, w, 1)
	})
	t.Run("a start that created no process is not unconfirmed", func(t *testing.T) {
		w := newAntigravityWorld(t, "ok", false)
		good := w.a.cfg.Workdir
		w.a.cfg.Workdir = filepath.Join(good, "absent-directory")
		if err := w.a.Send(context.Background(), "fictional task", adapter.ModeAuto); err == nil {
			t.Fatal("started a worker in a directory that does not exist")
		}
		// Nothing was started, so the failure is an ordinary unknown error and a
		// later send may proceed, as a new conversation.
		if obs := w.observe(t); !unknownError(obs) {
			t.Fatalf("%+v", obs)
		}
		w.a.cfg.Workdir = good
		w.send(t, "fictional retry")
		waitFor(t, w.a.dir, Finished)
		if obs := w.observe(t); obs.State != decide.Idle || len(w.named(t, "new")) != 1 || len(w.named(t, "prompt")) != 1 {
			t.Fatalf("%+v %v", obs, w.events(t))
		}
	})
}

// A worker that is no longer the current turn's must neither write a prompt
// nor touch the target's shared exit record.
func TestAntigravityRunnerOvertakenWorkerStaysSilent(t *testing.T) {
	overtake := func(t *testing.T, w *antigravityWorld) {
		t.Helper()
		turn, _ := randomStopID()
		if err := w.a.dir.saveAntigravityTurn(turn); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(w.release, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		w.await(t, "eof", 1) // the worker has closed the agent's stdin
		time.Sleep(300 * time.Millisecond)
		if _, err := os.Stat(w.a.dir.path("exit")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("an overtaken worker wrote the shared exit record: %v", err)
		}
	}
	t.Run("overtaken during setup", func(t *testing.T) {
		w := newAntigravityWorld(t, "slow-setup", true)
		w.send(t, "fictional task")
		w.await(t, "new", 1)
		overtake(t, w)
		if len(w.named(t, "prompt")) != 0 {
			t.Fatalf("an overtaken worker wrote its prompt: %v", w.events(t))
		}
		if found, _ := filepath.Glob(filepath.Join(string(w.a.dir), antigravityTurnsDir, "*", antigravityACPReservedName)); len(found) != 0 {
			t.Fatalf("an overtaken worker reserved a prompt: %v", found)
		}
	})
	t.Run("overtaken after its prompt", func(t *testing.T) {
		w := newAntigravityWorld(t, "ok", true)
		w.send(t, "fictional task")
		w.await(t, "prompt", 1)
		overtake(t, w)
	})
}

// The variable that selects the ACP worker means something only when the
// Antigravity runner sets it. Inherited from the supervisor's environment or
// set in a loop's own env, it must not change any other runner's turn.
func TestInheritedACPSelectorCannotDivertOtherRunners(t *testing.T) {
	hostile := `{"turn":"` + strings.Repeat("a", 64) + `","handshakeMs":1,"exitMs":1}`
	t.Setenv(envTurnACP, hostile)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for name, start := range map[string]func(root, result string, env map[string]string) (Dir, error){
		"claude": func(root, result string, env map[string]string) (Dir, error) {
			c := NewClaude(config.Claude{CLI: self, Workdir: root, Env: env}, filepath.Join(root, "target"))
			return c.dir, c.Send(ctx, "fictional prompt", adapter.ModeAuto)
		},
		"codex": func(root, result string, env map[string]string) (Dir, error) {
			c := NewCodex(config.Codex{CLI: self, Workdir: root, Env: env}, filepath.Join(root, "target"))
			return c.dir, c.Send(ctx, "fictional prompt", adapter.ModeAuto)
		},
		"cursor": func(root, result string, env map[string]string) (Dir, error) {
			c := NewCursor(config.Cursor{CLI: self, Workdir: root, Env: env}, filepath.Join(root, "target"))
			return c.dir, c.Send(ctx, "fictional prompt", adapter.ModeAuto)
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			result := filepath.Join(root, "result")
			dir, err := start(root, result, map[string]string{"KEEPGOING_TEST_HELPER": "environment", "KEEPGOING_TEST_RESULT": result, envTurnACP: hostile})
			if err != nil {
				t.Fatal(err)
			}
			waitFor(t, dir, Finished)
			st, err := dir.Status()
			if err != nil || !st.ExitCodeKnown || st.ExitCode != 0 {
				t.Fatalf("the turn did not run as an ordinary turn: %+v %v", st, err)
			}
			got, err := os.ReadFile(result)
			if err != nil || !strings.HasSuffix(string(got), "|fictional prompt") || strings.Contains(string(got), "handshakeMs") {
				t.Fatalf("agent input or environment: %q %v", got, err)
			}
			if _, err := os.Stat(filepath.Join(string(dir), antigravityTurnsDir)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("an ordinary turn created ACP records")
			}
		})
	}
}

func TestAntigravityRunnerRefusesInconsistentRecords(t *testing.T) {
	finished := func(t *testing.T) *antigravityWorld {
		w := newAntigravityWorld(t, "ok", false)
		w.send(t, "fictional task")
		waitFor(t, w.a.dir, Finished)
		if obs := w.observe(t); obs.State != decide.Idle {
			t.Fatalf("%+v", obs)
		}
		return w
	}
	current := func(t *testing.T, w *antigravityWorld) Dir {
		turn, ok, err := w.a.dir.antigravityTurn()
		if err != nil || !ok {
			t.Fatal(err)
		}
		return w.a.dir.antigravityTurnDir(turn)
	}
	other, _ := randomStopID()
	for name, damage := range map[string]func(t *testing.T, w *antigravityWorld){
		"turn pointer is not a record": func(t *testing.T, w *antigravityWorld) {
			write(t, w.a.dir.path(antigravityTurnName), `{"version":1,"turn":"../escape"}`)
		},
		"turn pointer has an extra key": func(t *testing.T, w *antigravityWorld) {
			write(t, w.a.dir.path(antigravityTurnName), `{"version":1,"turn":"`+other+`","note":"x"}`)
		},
		"turn pointer is missing": func(t *testing.T, w *antigravityWorld) {
			if err := os.Remove(w.a.dir.path(antigravityTurnName)); err != nil {
				t.Fatal(err)
			}
		},
		"saved conversation differs from the reservation": func(t *testing.T, w *antigravityWorld) {
			write(t, w.a.dir.path(antigravitySessionName), `{"version":1,"sessionId":"another fictional conversation"}`)
		},
		"saved conversation is unreadable": func(t *testing.T, w *antigravityWorld) {
			write(t, w.a.dir.path(antigravitySessionName), `{"version":2,"sessionId":"x"}`)
		},
		"outcome names another turn": func(t *testing.T, w *antigravityWorld) {
			write(t, current(t, w).path(antigravityOutcomeName), `{"version":1,"turn":"`+other+`","sessionId":"`+antigravitySession+`","returned":true,"needsUser":false,"failed":false}`)
		},
		"outcome names another conversation": func(t *testing.T, w *antigravityWorld) {
			turn, _, _ := w.a.dir.antigravityTurn()
			write(t, current(t, w).path(antigravityOutcomeName), `{"version":1,"turn":"`+turn+`","sessionId":"another","returned":true,"needsUser":false,"failed":false}`)
		},
		"outcome claims both a return and a failure": func(t *testing.T, w *antigravityWorld) {
			turn, _, _ := w.a.dir.antigravityTurn()
			write(t, current(t, w).path(antigravityOutcomeName), `{"version":1,"turn":"`+turn+`","sessionId":"`+antigravitySession+`","returned":true,"needsUser":false,"failed":true}`)
		},
		"outcome flag is not a boolean": func(t *testing.T, w *antigravityWorld) {
			turn, _, _ := w.a.dir.antigravityTurn()
			write(t, current(t, w).path(antigravityOutcomeName), `{"version":1,"turn":"`+turn+`","sessionId":"`+antigravitySession+`","returned":"true","needsUser":false,"failed":false}`)
		},
		"return without a reservation": func(t *testing.T, w *antigravityWorld) {
			if err := os.Remove(current(t, w).path(antigravityACPReservedName)); err != nil {
				t.Fatal(err)
			}
		},
		"hold record is unreadable": func(t *testing.T, w *antigravityWorld) {
			write(t, current(t, w).path(antigravityACPReservedName), `not json`)
		},
		"start confirmation names another worker": func(t *testing.T, w *antigravityWorld) {
			turn, _, _ := w.a.dir.antigravityTurn()
			write(t, current(t, w).path(antigravityStartedName), `{"version":1,"turn":"`+turn+`","stopKey":"`+other+`"}`)
		},
		"start both confirmed and denied": func(t *testing.T, w *antigravityWorld) {
			turn, _, _ := w.a.dir.antigravityTurn()
			write(t, current(t, w).path(antigravityNotStartedName), `{"version":1,"turn":"`+turn+`"}`)
		},
		"unexpected protocol output file": func(t *testing.T, w *antigravityWorld) {
			write(t, w.a.dir.path("stdout"), strings.Repeat("x", antigravityStdoutLimit+1))
		},
	} {
		t.Run(name, func(t *testing.T) {
			w := finished(t)
			damage(t, w)
			obs, err := w.rebuilt().Observe(context.Background())
			if err == nil || obs.State != decide.Unknown || obs.ErrorClass != decide.ErrUnknown {
				t.Fatalf("observe %+v %v", obs, err)
			}
			if strings.Contains(err.Error(), antigravitySession) || strings.Contains(err.Error(), string(w.a.dir)) {
				t.Fatalf("error echoes private data: %v", err)
			}
			if err := w.rebuilt().Send(context.Background(), "must not be sent", adapter.ModeAuto); err == nil {
				t.Fatal("sent on top of inconsistent records")
			}
			if len(w.named(t, "prompt")) != 1 {
				t.Fatalf("a refused send reached the agent: %v", w.events(t))
			}
		})
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAntigravityRunnerRefusesOversizedPromptBeforeStarting(t *testing.T) {
	w := newAntigravityWorld(t, "ok", false)
	if err := w.a.Send(context.Background(), strings.Repeat("x", antigravityACPFrameLimit+1), adapter.ModeAuto); err == nil {
		t.Fatal("oversized prompt accepted")
	}
	if err := w.a.Send(context.Background(), "bad \xff text", adapter.ModeAuto); err == nil {
		t.Fatal("invalid text accepted")
	}
	if len(w.turns(t)) != 0 || len(w.events(t)) != 0 {
		t.Fatalf("a refused prompt started a turn: %v", w.events(t))
	}
	if obs := w.observe(t); obs.State != decide.Idle {
		t.Fatalf("%+v", obs)
	}
}
