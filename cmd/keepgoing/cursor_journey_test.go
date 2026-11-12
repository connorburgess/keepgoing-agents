package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter/runner"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/supervise"
	"gopkg.in/yaml.v3"
)

const journeyCursorSession = "00000000-0000-4000-8000-0000000000c1"

// This native stand-in is launched by the real detached turn wrapper. It emits
// initialization before waiting, so a stopped turn can retain a verified ID.
func cursorJourneyHelper() {
	root := os.Getenv("KEEPGOING_CURSOR_JOURNEY")
	if root == "" {
		return
	}
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(2)
	}
	var turn string
	for n := 1; n <= 20; n++ {
		turn = filepath.Join(root, fmt.Sprintf("turn-%d", n))
		err = os.Mkdir(turn, 0700)
		if err == nil {
			break
		}
		if !os.IsExist(err) || n == 20 {
			os.Exit(2)
		}
	}
	capture, _ := json.Marshal(struct {
		Args   []string
		Prompt string
	}{os.Args[1:], string(input)})
	if os.WriteFile(filepath.Join(turn, "capture"), capture, 0600) != nil {
		os.Exit(2)
	}
	fmt.Printf("{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":%q}\n", journeyCursorSession)
	if os.WriteFile(filepath.Join(turn, "ready"), nil, 0600) != nil {
		os.Exit(2)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if mode, err := os.ReadFile(filepath.Join(turn, "release")); err == nil {
			if string(mode) == "success" {
				fmt.Printf("{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"session_id\":%q}\n", journeyCursorSession)
				os.Exit(0)
			}
			fmt.Fprintln(os.Stderr, "fictional rate limit 429 authentication network failure")
			os.Exit(1)
		}
		time.Sleep(10 * time.Millisecond)
	}
	os.Exit(3)
}

type cursorJourney struct {
	t         *testing.T
	root, cfg string
	target    runner.Dir
	now       time.Time
}

func newCursorJourney(t *testing.T) *cursorJourney {
	t.Helper()
	t.Setenv("BB_THREAD_ID", "")
	root := filepath.Join(t.TempDir(), "café-项目")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "state")
	cfg := config.Config{Version: 1, Loop: config.Loop{Name: "cursor-fixture", StateDir: stateDir,
		Agent:  config.Agent{Adapter: "cursor", Cursor: &config.Cursor{CLI: self, Workdir: root, Model: "example-model", Env: map[string]string{"KEEPGOING_CURSOR_JOURNEY": root}}},
		Prompt: "fictional café\n项目 task", Errors: config.ErrorRules{MaxReprompts: 2, Backoff: config.Duration{Duration: time.Minute}},
	}}
	body, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	j := &cursorJourney{t: t, root: root, cfg: filepath.Join(root, "loop.yaml"), target: runner.Dir(filepath.Join(stateDir, "targets", "primary")), now: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)}
	if err := os.WriteFile(j.cfg, body, 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		st, err := j.target.Status()
		if err == nil && st.Phase == runner.Running {
			if err := j.target.Stop(); err != nil {
				t.Errorf("fixture cleanup: %v", err)
				return
			}
			j.waitPhase(runner.Crashed)
		}
	})
	j.command("enable")
	return j
}

// Each invocation reloads configuration/control/state and builds a new adapter.
// This models command restart, not restarting the daemon process or a service.
func (j *cursorJourney) command(command string, extra ...string) []byte {
	j.t.Helper()
	args := append([]string{command, "-c", j.cfg}, extra...)
	var out, errOut bytes.Buffer
	if code := run(context.Background(), args, &out, &errOut); code != 0 {
		j.t.Fatalf("%v exit %d: %s %s", args, code, &out, &errOut)
	}
	return out.Bytes()
}
func (j *cursorJourney) tick() supervise.Result {
	j.t.Helper()
	var result supervise.Result
	raw := j.command("tick", "--now", j.now.Format(time.RFC3339))
	if err := json.Unmarshal(raw, &result); err != nil {
		j.t.Fatalf("tick JSON: %v %s", err, raw)
	}
	j.now = j.now.Add(time.Second)
	return result
}
func (j *cursorJourney) expectAction(action string) supervise.Result {
	j.t.Helper()
	got := j.tick()
	if got.Action != action {
		j.t.Fatalf("wanted %s: %+v", action, got)
	}
	return got
}
func (j *cursorJourney) waitPhase(phase runner.Phase) {
	j.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		st, err := j.target.Status()
		if err == nil && st.Phase == phase {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	st, err := j.target.Status()
	j.t.Fatalf("wanted %s, got %+v %v", phase, st, err)
}
func (j *cursorJourney) capture(n int, resume bool) {
	j.t.Helper()
	turn := filepath.Join(j.root, fmt.Sprintf("turn-%d", n))
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(turn, "ready")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			j.t.Fatal("fixture not ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var got struct {
		Args   []string
		Prompt string
	}
	body, err := os.ReadFile(filepath.Join(turn, "capture"))
	if err != nil {
		j.t.Fatal(err)
	}
	if err := json.Unmarshal(body, &got); err != nil {
		j.t.Fatal(err)
	}
	want := []string{"-p", "--output-format", "stream-json", "--model", "example-model"}
	if resume {
		want = append(want, "--resume="+journeyCursorSession)
	}
	if !reflect.DeepEqual(got.Args, want) || got.Prompt != "fictional café\n项目 task" {
		j.t.Fatalf("unexpected invocation: %+v", got)
	}
}
func (j *cursorJourney) release(n int, mode string) {
	j.t.Helper()
	if err := os.WriteFile(filepath.Join(j.root, fmt.Sprintf("turn-%d", n), "release"), []byte(mode), 0600); err != nil {
		j.t.Fatal(err)
	}
	j.waitPhase(runner.Finished)
}
func (j *cursorJourney) count(want int) {
	j.t.Helper()
	entries, err := filepath.Glob(filepath.Join(j.root, "turn-*"))
	if err != nil || len(entries) != want {
		j.t.Fatalf("turns %v, want %d: %v", entries, want, err)
	}
}
func (j *cursorJourney) resume() { j.command("resume"); j.command("enable") }

func TestCursorCommandActiveHoldsAndResume(t *testing.T) {
	for _, hold := range []string{"pause", "await-user", "complete", "disable"} {
		t.Run(hold, func(t *testing.T) {
			j := newCursorJourney(t)
			j.expectAction("wake")
			j.capture(1, false)
			j.command(hold)
			j.expectAction("none")
			j.expectAction("none")
			j.waitPhase(runner.Running)
			j.count(1)
			// Finishing while held cannot start a new turn on a reconstructed tick.
			j.release(1, "success")
			j.expectAction("none")
			j.count(1)
			j.resume()
			j.expectAction("wake")
			j.capture(2, true)
			j.command("complete")
			j.release(2, "success")
			j.expectAction("none")
			j.count(2)
		})
	}
}

func TestCursorCommandStopOverridesActiveHolds(t *testing.T) {
	for _, hold := range []string{"pause", "await-user", "complete", "disable"} {
		t.Run(hold, func(t *testing.T) {
			j := newCursorJourney(t)
			j.expectAction("wake")
			j.capture(1, false)
			j.command(hold)
			j.command("stop")
			j.expectAction("stop")
			j.waitPhase(runner.Crashed)
			j.expectAction("none")
			j.count(1)
			j.resume()
			got := j.expectAction("reprompt")
			if got.ErrorClass != "unknown" || got.Handoff != nil {
				t.Fatalf("stopped partial stream: %+v", got)
			}
			j.capture(2, true)
			j.command("complete")
			j.release(2, "success")
			j.expectAction("none")
			j.count(2)
		})
	}
}

func TestCursorCommandUnknownRecoveryIsBounded(t *testing.T) {
	j := newCursorJourney(t)
	j.expectAction("wake")
	j.capture(1, false)
	j.release(1, "failure")
	for n := 2; n <= 3; n++ {
		got := j.expectAction("reprompt")
		if got.ErrorClass != "unknown" || got.Handoff != nil || len(got.Probes) != 0 {
			t.Fatalf("stderr classified or failed over: %+v", got)
		}
		j.capture(n, true)
		j.release(n, "failure")
		if n == 2 {
			j.expectAction("none")
			j.count(2)
			j.now = j.now.Add(time.Minute)
		}
	}
	got := j.expectAction("await_user")
	if got.ErrorClass != "unknown" {
		t.Fatalf("failure class: %+v", got)
	}
	j.now = j.now.Add(time.Hour)
	j.expectAction("none")
	j.expectAction("none")
	j.count(3)
}
