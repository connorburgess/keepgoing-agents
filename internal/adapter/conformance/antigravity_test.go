package conformance

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
)

func standInAntigravity(dir string, situation Situation, args []string, generation string) int {
	if situation == Crashed {
		work(dir, generation)
		return 0
	}
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 2<<20)
	writeFrame := func(v any) {
		b, _ := json.Marshal(v)
		_, _ = os.Stdout.Write(append(b, '\n'))
	}
	resumedSession := ""
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
			writeFrame(map[string]any{
				"jsonrpc": "2.0",
				"id":      msg.ID,
				"result":  map[string]any{"sessionId": standInSession},
			})
		case "session/resume":
			var p struct {
				SessionID string `json:"sessionId"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			resumedSession = p.SessionID
			writeFrame(map[string]any{
				"jsonrpc": "2.0",
				"id":      msg.ID,
				"result":  map[string]any{},
			})
		case "session/prompt":
			var p struct {
				Prompt []struct {
					Text string `json:"text"`
				} `json:"prompt"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			text := ""
			if len(p.Prompt) > 0 {
				text = p.Prompt[0].Text
			}
			record(dir, Sent{Prompt: text, Argv: args, Session: resumedSession})
			switch situation {
			case Idle:
				writeFrame(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"stopReason": "end_turn"}})
				return 0
			case Active:
				work(dir, generation)
				writeFrame(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"stopReason": "end_turn"}})
				return 0
			case Crashed:
				work(dir, generation)
				return 0
			case Hanging:
				time.Sleep(10 * time.Minute)
				return 0
			case FailedProse:
				writeFrame(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "error": map[string]any{"code": -32000, "message": "You have hit your usage limit, try again later"}})
				return 1
			case Garbage:
				fmt.Println("the agent is doing fine, probably idle")
				return 0
			case Oversized:
				// Exceeds the 1 MiB per-frame bound on stdout.
				fmt.Print(`{"jsonrpc":"2.0","id":`)
				idBytes, _ := json.Marshal(msg.ID)
				_, _ = os.Stdout.Write(idBytes)
				fmt.Print(`,"result":{"stopReason":"end_turn","padding":"`)
				w := bufio.NewWriter(os.Stdout)
				chunk := strings.Repeat("x", 1024)
				for written := 0; written < 2<<20; written += len(chunk) {
					_, _ = w.WriteString(chunk)
				}
				_ = w.Flush()
				fmt.Println(`"}}`)
				return 0
			case ExitNonZero:
				writeFrame(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"stopReason": "end_turn"}})
				return 3
			case FailedUnmapped:
				writeFrame(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"stopReason": "refusal"}})
				return 0
			case FailedStderr:
				fmt.Fprintln(os.Stderr, "rate limit exceeded (429), try again later")
				return 1
			}
			return 0
		}
	}
	if situation == Crashed {
		work(dir, generation)
	}
	return 0
}

func antigravitySubject(t *testing.T) Subject {
	t.Helper()
	root := t.TempDir()
	r := &rig{dir: filepath.Join(root, "stand-in"), stateDir: filepath.Join(root, "state"), turns: true}
	project, home, temp := filepath.Join(root, "project"), filepath.Join(root, "home"), filepath.Join(root, "tmp")
	r.retainAgent = trackFixtureWrappers(t, project)
	for _, dir := range []string{r.dir, r.stateDir, project, home, temp} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(project, "README.md"), []byte("fictional project\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"HOME", "USERPROFILE"} {
		t.Setenv(name, home)
	}
	for _, name := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(name, temp)
	}
	env := map[string]string{envPart: "antigravity", envDir: r.dir, "GORACE": "atexit_sleep_ms=0"}
	r.supports = map[Situation]bool{
		Idle: true, Active: true, FailedProse: true, Garbage: true, Hanging: true,
		Oversized: true, ExitNonZero: true, FailedUnmapped: true, FailedStderr: true, Crashed: true,
	}
	return Subject{
		Name:      "antigravity",
		Rig:       turnRig{r},
		Protected: []string{project, home, temp},
		CallLimit: 10 * time.Second,
		StateDir:  r.stateDir,
		Declared:  Declared{ExactResume: true, Stoppable: true, StructuredErrors: false, StructuredWaiting: false},
		New: func() (adapter.Adapter, error) {
			return runner.NewAntigravity(runner.AntigravityConfig{
				Command:          []string{os.Args[0]},
				Workdir:          project,
				Env:              env,
				HandshakeTimeout: 5 * time.Second,
				ExitTimeout:      3 * time.Second,
				CancelTimeout:    3 * time.Second,
				WriteTimeout:     3 * time.Second,
			}, filepath.Join(r.stateDir, "targets", "primary")), nil
		},
	}
}

// TestAntigravityRunnerKeepsTheContract runs the shared adapter conformance
// suite directly against the unregistered runner.Antigravity with a stand-in
// ACP agent, with zero known gaps.
func TestAntigravityRunnerKeepsTheContract(t *testing.T) {
	results := Run(context.Background(), antigravitySubject(t), Options{
		Fresh: func() (Subject, error) { return antigravitySubject(t), nil },
	})
	seen := map[string]bool{}
	for _, r := range results {
		seen[r.ID] = true
		switch r.Outcome {
		case Fail:
			t.Errorf("antigravity: %s\n  contract: %s\n  %s", r.ID, r.Contract, r.Detail)
		case Skipped:
			t.Logf("skipped [antigravity] %s: %s", r.ID, r.Detail)
		}
	}
	for _, c := range Cases() {
		if !seen[c.ID] {
			t.Errorf("antigravity: case %s produced no result", c.ID)
		}
	}
}
