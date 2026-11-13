package runner

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// envTurnACP selects the ACP worker in the detached wrapper. Its value is an
// antigravityTurnSpec; it is never passed on to the agent.
const envTurnACP = "KEEPGOING_RUN_TURN_ACP"

type antigravityTurnSpec struct {
	Turn        string `json:"turn"`
	HandshakeMs int64  `json:"handshakeMs"`
	ExitMs      int64  `json:"exitMs"`
}

// runAntigravityACPTurn is the detached worker for one prompt. It owns the
// agent process it starts: the agent's stdin and stdout are private pipes to
// this process, and its stderr goes to a file nothing reads. The prompt
// reservation and any human-input evidence are saved by the hold checkpoint
// before the corresponding frames leave. Afterwards the worker saves one
// outcome record and the agent's exit status.
//
// Three waits are bounded: session setup, by killing the agent this process
// started; the agent's exit after its turn, likewise; and a stop request,
// which the wrapper's own stop channel handles by ending this process group
// or job. The turn itself has no time limit, as for the other runners.
func runAntigravityACPTurn(dir string, argv []string, raw string) int {
	d := Dir(dir)
	record := func(code int) int {
		tmp := d.path("exit.tmp")
		if os.WriteFile(tmp, []byte(strconv.Itoa(code)+"\n"), 0o600) == nil {
			os.Rename(tmp, d.path("exit"))
		}
		return code
	}
	var spec antigravityTurnSpec
	if json.Unmarshal([]byte(raw), &spec) != nil || !validStopID(spec.Turn) || spec.HandshakeMs <= 0 || spec.ExitMs <= 0 {
		return record(2)
	}
	turnDir := d.antigravityTurnDir(spec.Turn)
	out := antigravityOutcome{turn: spec.Turn, failed: true}
	// Refuse to run for anything but the turn Send recorded last.
	if current, ok, err := d.antigravityTurn(); err != nil || !ok || current != spec.Turn {
		return record(2)
	}
	fail := func(code int) int {
		_ = turnDir.saveAntigravityOutcome(out)
		return record(code)
	}
	prompt, err := readBounded(d.path("prompt"), antigravityACPFrameLimit)
	if err != nil {
		return fail(2)
	}
	saved, err := d.antigravitySession()
	if err != nil {
		return fail(2)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fail(2)
	}
	stderr, err := os.OpenFile(d.path("stderr"), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fail(2)
	}
	defer stderr.Close()
	// Explicit pipes, so this process decides when each end closes.
	agentIn, toAgent, err := os.Pipe()
	if err != nil {
		return fail(2)
	}
	fromAgent, agentOut, err := os.Pipe()
	if err != nil {
		agentIn.Close()
		toAgent.Close()
		return fail(2)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = agentIn, agentOut, stderr
	cmd.Env = turnChildEnv()
	err = cmd.Start()
	agentIn.Close()
	agentOut.Close()
	if err != nil {
		toAgent.Close()
		fromAgent.Close()
		return fail(127)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()

	// Session setup must finish within its limit. The checkpoint marks the end
	// of setup: it runs just before the prompt is written.
	var setup sync.Once
	setupDone := make(chan struct{})
	exchangeDone := make(chan struct{})
	go func() {
		timer := time.NewTimer(time.Duration(spec.HandshakeMs) * time.Millisecond)
		defer timer.Stop()
		select {
		case <-setupDone:
		case <-exchangeDone:
		case <-timer.C:
			_ = cmd.Process.Kill() // this process's own child, by its live handle
		}
	}()
	hold := turnDir.AntigravityACPHoldCheckpoint(spec.Turn)
	checkpoint := func(ctx context.Context, c AntigravityACPCheckpoint) error {
		if c.BeforePrompt {
			setup.Do(func() { close(setupDone) })
		}
		return hold(ctx, c)
	}
	result, exErr := ExchangeAntigravityACP(context.Background(), fromAgent, toAgent, saved, cwd, string(prompt), checkpoint)
	close(exchangeDone)
	out.session, out.needsUser = result.SessionID, result.NeedsUser
	out.returned = exErr == nil && result.TurnReturned && !result.NeedsUser && !result.Failed
	out.failed = exErr != nil || result.Failed
	if !acpIdentifier(out.session) {
		out.session = ""
	}
	// The outcome is saved before the wait for exit, so a worker that dies
	// while waiting leaves it behind with an unknown exit status.
	saveErr := turnDir.saveAntigravityOutcome(out)

	// Closing stdin tells the agent the conversation is over. Its remaining
	// output is discarded unread; an agent still running after the limit is
	// killed and its exit status is recorded as unknown.
	toAgent.Close()
	go func() { _, _ = io.Copy(io.Discard, fromAgent) }()
	code := -1
	timer := time.NewTimer(time.Duration(spec.ExitMs) * time.Millisecond)
	defer timer.Stop()
	select {
	case err := <-waited:
		code = exitCodeOf(err)
	case <-timer.C:
		_ = cmd.Process.Kill()
		<-waited
	}
	fromAgent.Close()
	if saveErr != nil && code == 0 {
		code = 2 // never report a clean exit without a saved outcome
	}
	return record(code)
}

func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode() // -1 when ended by a signal: unknown
	}
	return -1
}

func readBounded(path string, limit int64) ([]byte, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, errors.New("runner: input exceeds its limit")
	}
	return b, nil
}
