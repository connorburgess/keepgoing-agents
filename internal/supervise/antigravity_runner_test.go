package supervise

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/adapter"
	"github.com/looptender/looptender/internal/adapter/runner"
	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/control"
	"github.com/looptender/looptender/internal/decide"
)

const superviseAGYSession = "session-example/supervise-1"

// runSuperviseAntigravityStandIn is a minimal fictional ACP peer used only by
// TestAntigravityRunnerSupervise* tests. It reads one ACP handshake, session
// setup and prompt from stdin and behaves according to the mode file in its
// fixture directory.
func runSuperviseAntigravityStandIn() {
	fixtureDir := os.Getenv("KEEPGOING_SUPERVISE_AGY_DIR")
	modeBytes, _ := os.ReadFile(filepath.Join(fixtureDir, "mode"))
	mode := strings.TrimSpace(string(modeBytes))

	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 2<<20)
	writeFrame := func(v any) {
		b, _ := json.Marshal(v)
		_, _ = os.Stdout.Write(append(b, '\n'))
	}
	appendLog := func(entry map[string]any) {
		b, _ := json.Marshal(entry)
		f, err := os.OpenFile(filepath.Join(fixtureDir, "log.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return
		}
		_, _ = f.Write(append(b, '\n'))
		_ = f.Close()
	}

	for sc.Scan() {
		var msg struct {
			ID     any             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(sc.Bytes(), &msg) != nil {
			continue
		}
		switch msg.Method {
		case "initialize":
			writeFrame(map[string]any{
				"jsonrpc": "2.0",
				"id":      msg.ID,
				"result": map[string]any{
					"protocolVersion": 1,
					"agentCapabilities": map[string]any{
						"sessionCapabilities": map[string]any{
							"resume": map[string]any{},
						},
					},
				},
			})
		case "session/new":
			appendLog(map[string]any{"op": "session/new"})
			writeFrame(map[string]any{
				"jsonrpc": "2.0",
				"id":      msg.ID,
				"result":  map[string]any{"sessionId": superviseAGYSession},
			})
		case "session/resume":
			var p struct {
				SessionID string `json:"sessionId"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			appendLog(map[string]any{"op": "session/resume", "sessionId": p.SessionID})
			writeFrame(map[string]any{
				"jsonrpc": "2.0",
				"id":      msg.ID,
				"result":  map[string]any{},
			})
		case "session/prompt":
			appendLog(map[string]any{"op": "session/prompt", "mode": mode})
			switch mode {
			case "ok":
				writeFrame(map[string]any{
					"jsonrpc": "2.0",
					"id":      msg.ID,
					"result":  map[string]any{"stopReason": "end_turn"},
				})
				return
			case "permission":
				writeFrame(map[string]any{
					"jsonrpc": "2.0",
					"id":      "perm-1",
					"method":  "session/request_permission",
					"params": map[string]any{
						"sessionId": superviseAGYSession,
						"toolCall":  map[string]any{"toolCallId": "tc-1"},
						"options": []map[string]any{
							{"optionId": "allow", "name": "Allow", "kind": "allow_once"},
						},
					},
				})
				// Wait for session/cancel + cancelled reply, then return cancelled.
				for sc.Scan() {
					var next struct {
						ID     any    `json:"id"`
						Method string `json:"method"`
					}
					_ = json.Unmarshal(sc.Bytes(), &next)
					if next.ID == "perm-1" {
						writeFrame(map[string]any{
							"jsonrpc": "2.0",
							"id":      msg.ID,
							"result":  map[string]any{"stopReason": "cancelled"},
						})
						return
					}
				}
				return
			case "refusal_prose":
				_, _ = os.Stderr.WriteString("rate limit 429 quota exceeded retry after 60s\n")
				writeFrame(map[string]any{
					"jsonrpc": "2.0",
					"id":      msg.ID,
					"result":  map[string]any{"stopReason": "refusal"},
				})
				os.Exit(1)
			case "hang":
				_ = os.WriteFile(filepath.Join(fixtureDir, "hanging"), []byte("1"), 0o600)
				time.Sleep(30 * time.Second)
				return
			}
		}
	}
}

type agySuperviseWorld struct {
	*world
	fixtureDir string
	targetDir  string
	workDir    string
}

func newAGYSuperviseWorld(t *testing.T, withFallback bool) *agySuperviseWorld {
	t.Helper()
	w := newWorld(t, true, withFallback)
	root := t.TempDir()
	fixtureDir := filepath.Join(root, "fixture")
	workDir := filepath.Join(root, "work")
	for _, d := range []string{fixtureDir, workDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	a := &agySuperviseWorld{
		world:      w,
		fixtureDir: fixtureDir,
		targetDir:  filepath.Join(w.dir, "targets", "primary"),
		workDir:    workDir,
	}
	// Stop and settle the owned worker before either TempDir is removed,
	// including when a test assertion aborts after starting a turn.
	t.Cleanup(func() {
		if err := a.newRunner(t).Stop(context.Background()); err != nil {
			t.Errorf("stop fixture worker: %v", err)
		}
		a.waitNotRunning(t)
	})
	return a
}

func (a *agySuperviseWorld) setMode(t *testing.T, mode string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(a.fixtureDir, "mode"), []byte(mode), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (a *agySuperviseWorld) newRunner(t *testing.T) *runner.Antigravity {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return runner.NewAntigravity(runner.AntigravityConfig{
		Command:          []string{self},
		Workdir:          a.workDir,
		HandshakeTimeout: 10 * time.Second,
		ExitTimeout:      5 * time.Second,
		CancelTimeout:    5 * time.Second,
		WriteTimeout:     5 * time.Second,
		Env: map[string]string{
			"KEEPGOING_SUPERVISE_AGY_HELPER": "1",
			"KEEPGOING_SUPERVISE_AGY_DIR":    a.fixtureDir,
			"GORACE":                         "atexit_sleep_ms=0",
		},
	}, a.targetDir)
}

func (a *agySuperviseWorld) tick(t *testing.T, at time.Time) (Result, error) {
	t.Helper()
	opts := a.opts(at)
	opts.NewAdapter = func(ag config.Agent, stateDir, target string) (adapter.Adapter, error) {
		if target == "primary" {
			return a.newRunner(t), nil
		}
		return a.world.opts(at).NewAdapter(ag, stateDir, target)
	}
	return Tick(context.Background(), a.cfg, opts)
}

func (a *agySuperviseWorld) waitNotRunning(t *testing.T) decide.Observation {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		obs, err := a.newRunner(t).Observe(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if obs.State != decide.Active {
			return obs
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("antigravity worker did not exit in time")
	return decide.Observation{}
}

func (a *agySuperviseWorld) readLog(t *testing.T) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(a.fixtureDir, "log.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

func TestAntigravityRunnerSuperviseTwoTurnsExactResume(t *testing.T) {
	w := newAGYSuperviseWorld(t, false)
	w.setMode(t, "ok")
	t0 := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

	res1, err := w.tick(t, t0)
	if err != nil || res1.Action != string(decide.Wake) {
		t.Fatalf("tick 1: res=%+v err=%v", res1, err)
	}
	obs1 := w.waitNotRunning(t)
	if obs1.State != decide.Idle || obs1.ErrorClass != decide.ErrNone {
		t.Fatalf("after turn 1: obs=%+v", obs1)
	}

	res2, err := w.tick(t, t0.Add(10*time.Minute))
	if err != nil || res2.Action != string(decide.Wake) {
		t.Fatalf("tick 2: res=%+v err=%v", res2, err)
	}
	obs2 := w.waitNotRunning(t)
	if obs2.State != decide.Idle || obs2.ErrorClass != decide.ErrNone {
		t.Fatalf("after turn 2: obs=%+v", obs2)
	}

	entries := w.readLog(t)
	var ops []string
	var loadedSession string
	for _, e := range entries {
		op, _ := e["op"].(string)
		ops = append(ops, op)
		if op == "session/resume" {
			loadedSession, _ = e["sessionId"].(string)
		}
	}
	wantOps := "session/new,session/prompt,session/resume,session/prompt"
	if got := strings.Join(ops, ","); got != wantOps {
		t.Fatalf("ops=%q want %q", got, wantOps)
	}
	if loadedSession != superviseAGYSession {
		t.Fatalf("resumed session=%q want %q", loadedSession, superviseAGYSession)
	}
}

func TestAntigravityRunnerSupervisePermissionHoldAndResume(t *testing.T) {
	w := newAGYSuperviseWorld(t, false)
	w.setMode(t, "permission")
	t0 := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

	res1, err := w.tick(t, t0)
	if err != nil || res1.Action != string(decide.Wake) {
		t.Fatalf("tick 1: res=%+v err=%v", res1, err)
	}
	obs1 := w.waitNotRunning(t)
	if !obs1.DeferredInteraction {
		t.Fatalf("expected DeferredInteraction after permission request, got %+v", obs1)
	}

	// Next tick observes DeferredInteraction, saves hold, and adopts it.
	res2, err := w.tick(t, t0.Add(10*time.Minute))
	if err != nil || res2.Action != string(decide.AwaitUser) || res2.Reason != decide.DeferredInteractionReason {
		t.Fatalf("tick 2: res=%+v err=%v", res2, err)
	}
	ctl, err := control.Load(w.dir)
	if err != nil || !ctl.AwaitingUser || ctl.Note != decide.DeferredInteractionReason {
		t.Fatalf("control after hold: %+v err=%v", ctl, err)
	}

	// Repeated tick while held must not send.
	beforeCount := len(w.readLog(t))
	res3, err := w.tick(t, t0.Add(20*time.Minute))
	if err != nil || res3.Action != string(decide.None) {
		t.Fatalf("tick 3 while held: res=%+v err=%v", res3, err)
	}
	if got := len(w.readLog(t)); got != beforeCount {
		t.Fatalf("sent while held: log grew from %d to %d", beforeCount, got)
	}

	// Explicit operator resume clears hold and lets next turn resume the saved conversation.
	w.setMode(t, "ok")
	if _, err := control.Update(w.dir, func(f *control.File) { f.AwaitingUser = false }); err != nil {
		t.Fatal(err)
	}
	res4, err := w.tick(t, t0.Add(30*time.Minute))
	if err != nil || res4.Action != string(decide.Reprompt) {
		t.Fatalf("tick 4 after resume: res=%+v err=%v", res4, err)
	}
	obs4 := w.waitNotRunning(t)
	if obs4.State != decide.Idle || obs4.DeferredInteraction {
		t.Fatalf("after resumed turn: obs=%+v", obs4)
	}
}

func TestAntigravityRunnerSuperviseUnknownErrorNeverFailsOver(t *testing.T) {
	w := newAGYSuperviseWorld(t, true)
	w.setMode(t, "refusal_prose")
	t0 := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

	res1, err := w.tick(t, t0)
	if err != nil || res1.Action != string(decide.Wake) {
		t.Fatalf("tick 1: res=%+v err=%v", res1, err)
	}
	obs1 := w.waitNotRunning(t)
	if obs1.ErrorClass != decide.ErrUnknown {
		t.Fatalf("expected unknown error class, got %+v", obs1)
	}

	res2, err := w.tick(t, t0.Add(10*time.Minute))
	if err != nil {
		t.Fatalf("tick 2: err=%v", err)
	}
	if res2.Action == string(decide.Handoff) || res2.Target != "primary" {
		t.Fatalf("unknown error must never trigger failover: res=%+v", res2)
	}
	// The second tick can start a re-prompt on primary. Observe that turn's
	// completion too; returning here used to race TempDir cleanup with its
	// ACP record publication.
	if obs := w.waitNotRunning(t); obs.ErrorClass != decide.ErrUnknown {
		t.Fatalf("after retry: expected unknown error class, got %+v", obs)
	}
	if fb := w.adapters["fallback"]; fb != nil && len(fb.sent) != 0 {
		t.Fatalf("fallback received %d sends on unknown error", len(fb.sent))
	}
}

func TestAntigravityRunnerSuperviseOwnedStop(t *testing.T) {
	w := newAGYSuperviseWorld(t, false)
	w.setMode(t, "hang")
	t0 := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

	res1, err := w.tick(t, t0)
	if err != nil || res1.Action != string(decide.Wake) {
		t.Fatalf("tick 1: res=%+v err=%v", res1, err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(w.fixtureDir, "hanging")); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(w.fixtureDir, "hanging")); err != nil {
		t.Fatal(fmt.Errorf("stand-in did not reach hanging state: %w", err))
	}

	if _, err := control.Update(w.dir, func(f *control.File) { f.Stop = true }); err != nil {
		t.Fatal(err)
	}
	res2, err := w.tick(t, t0.Add(time.Minute))
	if err != nil || res2.Action != string(decide.Stop) {
		t.Fatalf("tick 2 stop: res=%+v err=%v", res2, err)
	}
	obs := w.waitNotRunning(t)
	if obs.State == decide.Active {
		t.Fatalf("expected runner stopped, got %+v", obs)
	}
}
