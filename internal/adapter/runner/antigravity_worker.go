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

const antigravityStderrLimit = 64 << 10

type antigravityTurnSpec struct {
	Turn        string `json:"turn"`
	HandshakeMs int64  `json:"handshakeMs"`
	ExitMs      int64  `json:"exitMs"`
	CancelMs    int64  `json:"cancelMs"`
	WriteMs     int64  `json:"writeMs"`
}

// antigravityPipeWriter bounds every frame write to the agent's stdin pipe so
// a prompt larger than the OS pipe buffer cannot block indefinitely if the
// agent stops reading.
type antigravityPipeWriter struct {
	w       *os.File
	timeout time.Duration
	onTrip  func()
}

func (p *antigravityPipeWriter) Write(b []byte) (int, error) {
	_ = p.w.SetWriteDeadline(time.Now().Add(p.timeout))
	timer := time.AfterFunc(p.timeout, p.onTrip)
	n, err := p.w.Write(b)
	timer.Stop()
	_ = p.w.SetWriteDeadline(time.Time{})
	return n, err
}

// runAntigravityACPTurn is the detached worker for one prompt. It owns the
// agent process tree it starts: stdin and stdout are private pipes to this
// process, and stderr is copied into a size-bounded file nothing reads. The
// prompt reservation and any human-input evidence are saved by the hold
// checkpoint before the corresponding frames leave. Afterwards the worker
// saves one outcome record and the agent's exit status.
//
// Four waits are bounded by terminating the agent's process group or job:
// session setup, individual stdin frame writes, draining after a permission
// cancellation, and process exit after the turn. Operator stop terminates the
// agent tree and wrapper group/job through the wrapper's owned stop channel.
func runAntigravityACPTurn(dir string, argv []string, raw string) int {
	d := Dir(dir)
	var spec antigravityTurnSpec
	if json.Unmarshal([]byte(raw), &spec) != nil || !validStopID(spec.Turn) ||
		spec.HandshakeMs <= 0 || spec.ExitMs <= 0 || spec.CancelMs <= 0 || spec.WriteMs <= 0 {
		return 2 // not a turn Send started: touch nothing
	}
	current := func() bool {
		turn, ok, err := d.antigravityTurn()
		return err == nil && ok && turn == spec.Turn
	}
	// The exit record is shared by the target. Only the current turn's worker
	// may write it; a worker overtaken by a later turn leaves it alone.
	var agentErr *os.File // the agent's stderr file, once opened
	record := func(code int) int {
		// Closed before the exit code can be seen: the next start may move
		// the file, and on Windows an open file cannot be moved.
		if agentErr != nil {
			agentErr.Close()
		}
		if !current() {
			return code
		}
		tmp := d.path("exit.tmp-" + spec.Turn)
		if os.WriteFile(tmp, []byte(strconv.Itoa(code)+"\n"), 0o600) == nil {
			if !current() || publishExit(dir, tmp) != nil {
				os.Remove(tmp)
			}
		}
		return code
	}
	turnDir := d.antigravityTurnDir(spec.Turn)
	out := antigravityOutcome{turn: spec.Turn, failed: true}
	// Refuse to run for anything but the turn Send recorded last.
	if !current() {
		return 2
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
	agentErr = stderr
	// Explicit pipes, so this process decides when each end closes and bounds stderr.
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
	errRead, agentErrPipe, err := os.Pipe()
	if err != nil {
		agentIn.Close()
		toAgent.Close()
		fromAgent.Close()
		agentOut.Close()
		return fail(2)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	configureAgentProcess(cmd)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = agentIn, agentOut, agentErrPipe
	cmd.Env = turnChildEnv()
	err = cmd.Start()
	agentIn.Close()
	agentOut.Close()
	agentErrPipe.Close()
	if err != nil {
		toAgent.Close()
		fromAgent.Close()
		errRead.Close()
		return fail(127)
	}
	tree := newAgentTree(cmd)
	unregister := registerOwnedChildTerminator(tree.Kill)
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		_, _ = io.Copy(&limitedWriter{w: stderr, left: antigravityStderrLimit}, errRead)
	}()
	waited := make(chan error, 1)
	go func() {
		waitErr := cmd.Wait()
		unregister()
		tree.MarkReaped()
		tree.Close()
		waited <- waitErr
	}()

	abortAgent := func() {
		tree.Kill()
		_ = toAgent.Close()
		_ = fromAgent.Close()
	}

	// Session setup must finish within its limit. The checkpoint marks the end
	// of setup: it runs just before the prompt is written.
	var setupOnce, cancelOnce sync.Once
	setupDone := make(chan struct{})
	cancelStart := make(chan struct{})
	exchangeDone := make(chan struct{})
	var timers sync.WaitGroup
	timers.Add(2)
	go func() {
		defer timers.Done()
		timer := time.NewTimer(time.Duration(spec.HandshakeMs) * time.Millisecond)
		defer timer.Stop()
		select {
		case <-setupDone:
		case <-exchangeDone:
		case <-timer.C:
			abortAgent()
		}
	}()
	go func() {
		defer timers.Done()
		select {
		case <-cancelStart:
		case <-exchangeDone:
			return
		}
		timer := time.NewTimer(time.Duration(spec.CancelMs) * time.Millisecond)
		defer timer.Stop()
		select {
		case <-exchangeDone:
		case <-timer.C:
			abortAgent()
		}
	}()
	hold := turnDir.AntigravityACPHoldCheckpoint(spec.Turn)
	checkpoint := func(ctx context.Context, c AntigravityACPCheckpoint) error {
		if c.BeforePrompt {
			setupOnce.Do(func() { close(setupDone) })
			// Setup can take long enough for a later turn to be recorded. A
			// worker that is no longer current must not write its prompt.
			if !current() {
				return errAntigravityRecords
			}
		}
		err := hold(ctx, c)
		if err == nil && c.NeedsUser {
			cancelOnce.Do(func() { close(cancelStart) })
		}
		return err
	}
	writer := &antigravityPipeWriter{
		w:       toAgent,
		timeout: time.Duration(spec.WriteMs) * time.Millisecond,
		onTrip:  abortAgent,
	}
	result, exErr := ExchangeAntigravityACP(context.Background(), fromAgent, writer, saved, cwd, string(prompt), checkpoint)
	close(exchangeDone)
	timers.Wait()
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
	// output is discarded unread; an agent still running after the limit has
	// its whole process group or job terminated and its exit recorded unknown.
	_ = toAgent.Close()
	go func() { _, _ = io.Copy(io.Discard, fromAgent) }()
	code := -1
	timer := time.NewTimer(time.Duration(spec.ExitMs) * time.Millisecond)
	defer timer.Stop()
	select {
	case err := <-waited:
		code = exitCodeOf(err)
	case <-timer.C:
		tree.Kill()
		<-waited
	}
	_ = fromAgent.Close()
	select {
	case <-stderrDone:
	case <-time.After(100 * time.Millisecond):
		_ = errRead.Close()
		<-stderrDone
	}
	_ = errRead.Close()
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
