// Package conformance is the contract every adapter must keep, written as
// cases that can be run against any of them: a built-in adapter with a
// stand-in for its agent, or an integrator's own wrapper for the process
// adapter. Each case states its contract in plain words and fails with a
// message that says what to change.
//
// A Subject without a Rig is a real agent. Only the case that reads status
// runs against it. Nothing in this package sends a prompt to a real agent.
package conformance

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
)

// Situation is what a stand-in agent is made to be in.
type Situation string

const (
	Idle        Situation = "idle"         // finished its last turn, healthy
	Active      Situation = "active"       // working
	Waiting     Situation = "waiting"      // blocked on a person
	FailedCoded Situation = "failed_coded" // failed, with a machine-readable code
	FailedProse Situation = "failed_prose" // failed, with only prose that names a limit
	Garbage     Situation = "garbage"      // output that is not the agent's format
	Hanging     Situation = "hanging"      // a command that never returns
	Oversized   Situation = "oversized"    // far more output than any real status
	// ExitNonZero is output that looks healthy from a command that failed.
	ExitNonZero Situation = "exit_nonzero"
	// Crashed is a turn that ended without leaving any result.
	Crashed Situation = "crashed"
	// FailedUnmapped is a failure whose machine-readable code maps to no class.
	FailedUnmapped Situation = "failed_unmapped"
	// FailedStderr is a failure described only on standard error.
	FailedStderr Situation = "failed_stderr"
)

// Token is in every prompt a case sends, so a stand-in can look for it.
const Token = "conformance-token-7f3a"

// Sent is one prompt a stand-in received.
type Sent struct {
	Prompt  string   `json:"prompt"`
	Argv    []string `json:"argv"`
	Session string   `json:"session,omitempty"` // the session it was asked to resume
	// InEnv is true when the prompt was in the stand-in's environment.
	InEnv bool `json:"inEnv,omitempty"`
}

// Rig drives a stand-in agent. Stage does whatever it takes for this kind of
// adapter to be in a situation: for an agent that runs one turn per process,
// that means running a turn that ends that way.
type Rig interface {
	Supports(s Situation) bool
	Stage(ctx context.Context, a adapter.Adapter, s Situation) error
	// Sent lists every prompt the stand-in has received, oldest first.
	Sent() ([]Sent, error)
	// Settle waits until nothing the subject started is still running.
	Settle(ctx context.Context, a adapter.Adapter) error
	// CodedClass is the class a FailedCoded situation must be read as.
	CodedClass() decide.ErrorClass
	// ReportedSession is the session identity the stand-in reports for
	// every turn, which a later send must resume.
	ReportedSession() string
}

// ForeignWork is implemented by a rig that can put work in the subject's
// state that this supervisor did not start, to show a stop leaves it alone.
type ForeignWork interface {
	// StartForeign returns a function that reports whether the foreign work
	// is still alive, and one that ends it.
	StartForeign() (alive func() bool, end func(), err error)
}

// AgentCounter is implemented by a rig whose adapter starts the agent itself,
// one process per turn. Running is how many of them are alive.
type AgentCounter interface {
	Running() (int, error)
}

// Declared is what the adapter says about itself.
type Declared struct {
	ExactResume       bool `json:"exactResume"`
	Stoppable         bool `json:"stoppable"`
	StructuredErrors  bool `json:"structuredErrors"`
	StructuredWaiting bool `json:"structuredWaiting"`
}

// Subject is one adapter under test.
type Subject struct {
	Name string
	// New builds the adapter the way the supervisor does. It is called again
	// to model a supervisor that was stopped and started.
	New      func() (adapter.Adapter, error)
	Declared Declared
	Rig      Rig
	// Protected are directories that nothing may change: the project, and
	// anywhere else outside the loop's state directory.
	Protected []string
	// CallLimit is the longest one status read may take. Zero means 30s.
	CallLimit time.Duration
	// StateDir is the loop's state directory, when the subject has one. Cases
	// that look at what the adapter saved are skipped without it.
	StateDir string
}

// Outcome of one case.
type Outcome string

const (
	Pass    Outcome = "pass"
	Fail    Outcome = "fail"
	Skipped Outcome = "skipped"
)

// Result is one case's verdict.
type Result struct {
	ID       string  `json:"id"`
	Contract string  `json:"contract"`
	Outcome  Outcome `json:"outcome"`
	Detail   string  `json:"detail,omitempty"`
}

// Options select how cases run.
type Options struct {
	// Fresh, when set, returns a new subject with empty state for each case,
	// so one case cannot affect another. A real agent has none.
	Fresh func() (Subject, error)
}

// CheckPrompt is what a send case sends to a stand-in. It carries no task.
const CheckPrompt = "keepgoing adapter check: reply OK and stop. " + Token

type run struct {
	ctx context.Context
	s   Subject
	a   adapter.Adapter
	opt Options
}

type caseDef struct {
	id, contract string
	needsRig     bool
	needs        []Situation
	sends        bool
	run          func(r *run) error
}

// skip is returned by a case that cannot apply to this subject.
type skip string

func (s skip) Error() string { return string(s) }

// note is returned by a case that passed and has something to say.
type note string

func (n note) Error() string { return string(n) }

var cases = []caseDef{
	{id: "one-state", contract: "A status read returns exactly one of idle, active, pending, stopping, error or unknown, inside the call limit. An error carries rate_limit, auth, overloaded or network only when something structured says so, and unknown otherwise.",
		run: caseOneState},
	{id: "known-situations", contract: "An idle agent reads as idle and a working agent as active.", needsRig: true, needs: []Situation{Idle, Active}, run: caseKnown},
	{id: "never-guess", contract: "Output that is not the agent's status format reads as unknown, or as an error of class unknown. It is never read as idle, active or a specific error class.", needsRig: true, needs: []Situation{Garbage}, run: caseGarbage},
	{id: "waiting-on-person", contract: "An agent that is blocked on a person is reported as awaiting input, so the supervisor holds and does not send to it.", needsRig: true, needs: []Situation{Waiting}, run: caseWaiting},
	{id: "structured-errors-only", contract: "A failure is classified from a machine-readable code. A failure whose only evidence is prose, on standard output or standard error, even prose that names a limit, is unknown, and so is a code that maps to no class. Neither can ever cause a handoff.", needsRig: true, needs: []Situation{FailedCoded, FailedProse}, run: caseErrors},
	{id: "failed-is-not-healthy", contract: "A command that exits with a failure, or a turn that ends without leaving a result, is a failure of class unknown, whatever it printed. It is never read as idle or working, and never given a specific class.", needsRig: true, run: caseNotHealthy},
	{id: "send-once", contract: "One send delivers the prompt to the agent exactly once, unchanged, on standard input or in a file only the owner can read. The prompt never appears in a command's arguments or environment, and reading status again or restarting the supervisor does not deliver it again.", needsRig: true, needs: []Situation{Idle}, sends: true, run: caseSendOnce},
	{id: "send-while-working", contract: "A send while the agent is working is refused or reaches that same agent. It never starts a second agent beside the first.", needsRig: true, needs: []Situation{Active}, sends: true, run: caseSendWorking},
	{id: "observe-during-send", contract: "Status can be read while a send is in progress: every read is one valid state, and the prompt is still delivered exactly once.", needsRig: true, needs: []Situation{Idle}, sends: true, run: caseObserveDuringSend},
	{id: "state-unwritable", contract: "When the state directory cannot be written, a send either fails and says so or still delivers the prompt once. It never reports success for a prompt that did not arrive.", needsRig: true, needs: []Situation{Idle}, sends: true, run: caseUnwritable},
	{id: "exact-resume", contract: "The session the agent reported is saved in the state directory, and after the supervisor is stopped and started the next send continues exactly that session, never a new one and never \"the most recent\".", needsRig: true, needs: []Situation{Idle}, sends: true, run: caseResume},
	{id: "hang-is-cut", contract: "A status read returns inside the call limit even when the agent's command never returns, and does not report the agent as idle or as failed with a specific class.", needsRig: true, needs: []Situation{Hanging}, run: caseHang},
	{id: "stop-own-work", contract: "Stop ends a turn this supervisor started, and leaves alone anything it did not start.", needsRig: true, needs: []Situation{Active}, run: caseStop},
	{id: "bounded-output", contract: "Output far larger than any real status is not read as healthy: the read ends inside the call limit and reports unknown or an error of class unknown.", needsRig: true, needs: []Situation{Oversized}, run: caseBounded},
}

// Cases lists every case's ID and contract, in order.
func Cases() []Result {
	out := make([]Result, 0, len(cases)+2)
	for _, c := range cases {
		out = append(out, Result{ID: c.id, Contract: c.contract})
	}
	out = append(out, Result{ID: "writes-stay-inside", Contract: contractOutside}, Result{ID: "declared-matches", Contract: contractDeclared})
	return out
}

const (
	contractOutside  = "Nothing is created, changed or removed in the project, the home directory or the temporary directory: everything the adapter writes is under the loop's state directory."
	contractDeclared = "What the adapter declares about itself matches what it does: every capability it declares passed its case."
)

// Run runs every case that applies and returns one result per case.
func Run(ctx context.Context, s Subject, opt Options) []Result {
	var out []Result
	verdict := map[string]Outcome{}
	for _, c := range cases {
		res := Result{ID: c.id, Contract: c.contract}
		subject := s
		reason := ""
		if (c.needsRig || c.sends) && s.Rig == nil {
			reason = "needs a stand-in agent; not run against a real one"
		}
		if reason == "" && s.Rig != nil {
			for _, need := range c.needs {
				if !s.Rig.Supports(need) {
					reason = fmt.Sprintf("this adapter's agent cannot be in the %q situation", need)
				}
			}
		}
		if reason == "" && opt.Fresh != nil {
			fresh, err := opt.Fresh()
			if err != nil {
				res.Outcome, res.Detail = Fail, "could not build a fresh subject: "+err.Error()
				out, verdict[c.id] = append(out, res), Fail
				continue
			}
			subject = fresh
		}
		if reason != "" {
			res.Outcome, res.Detail = Skipped, reason
			out, verdict[c.id] = append(out, res), Skipped
			continue
		}
		before := snapshot(subject.Protected)
		a, err := subject.New()
		if err != nil {
			res.Outcome, res.Detail = Fail, "the adapter could not be built"
			if subject.Rig != nil { // a real agent's error may quote a command or a path
				res.Detail += ": " + err.Error()
			}
		} else {
			err = c.run(&run{ctx: ctx, s: subject, a: a, opt: opt})
			var s skip
			var n note
			switch {
			case errors.As(err, &n):
				res.Outcome, res.Detail = Pass, string(n)
			case errors.As(err, &s):
				res.Outcome, res.Detail = Skipped, string(s)
			case err != nil:
				res.Outcome, res.Detail = Fail, err.Error()
			default:
				res.Outcome = Pass
			}
			if subject.Rig != nil {
				_ = subject.Rig.Settle(ctx, a)
			}
		}
		if diff := changed(before, snapshot(subject.Protected)); diff != "" && verdict["writes-stay-inside"] != Fail {
			verdict["writes-stay-inside"] = Fail
			out = append(out, Result{ID: "writes-stay-inside", Contract: contractOutside, Outcome: Fail,
				Detail: fmt.Sprintf("during %q something outside the state directory changed: %s. Keep every file the adapter or its commands write under the loop's state_dir.", c.id, diff)})
		}
		out, verdict[c.id] = append(out, res), res.Outcome
	}
	if verdict["writes-stay-inside"] != Fail {
		res := Result{ID: "writes-stay-inside", Contract: contractOutside, Outcome: Pass}
		if len(s.Protected) == 0 {
			res.Outcome, res.Detail = Skipped, "no protected directory was named"
		}
		out = append(out, res)
	}
	return append(out, declared(s, verdict))
}

// declared compares the adapter's declarations with the cases that test them.
func declared(s Subject, verdict map[string]Outcome) Result {
	res := Result{ID: "declared-matches", Contract: contractDeclared, Outcome: Pass}
	claims := []struct {
		name string
		on   bool
		id   string
	}{
		{"exact resume", s.Declared.ExactResume, "exact-resume"}, {"can be stopped", s.Declared.Stoppable, "stop-own-work"},
		{"structured errors", s.Declared.StructuredErrors, "structured-errors-only"}, {"structured waiting", s.Declared.StructuredWaiting, "waiting-on-person"},
	}
	var broken, unchecked []string
	for _, c := range claims {
		if !c.on {
			continue
		}
		switch verdict[c.id] {
		case Fail:
			broken = append(broken, c.name)
		case Skipped:
			unchecked = append(unchecked, c.name)
		}
	}
	switch {
	case len(broken) > 0:
		res.Outcome = Fail
		res.Detail = "declared but not kept: " + strings.Join(broken, ", ") + ". Fix the behaviour or stop declaring it; the supervisor relies on a declaration."
	case len(unchecked) > 0:
		res.Outcome, res.Detail = Skipped, "declared but not checked in this run: "+strings.Join(unchecked, ", ")
	}
	return res
}

func (r *run) limit() time.Duration {
	if r.s.CallLimit > 0 {
		return r.s.CallLimit
	}
	return 30 * time.Second
}

// observe reads status and fails if the read outlasts the call limit.
func (r *run) observe() (decide.Observation, error, error) {
	type result struct {
		obs decide.Observation
		err error
	}
	done := make(chan result, 1)
	ctx, cancel := context.WithTimeout(r.ctx, r.limit()+5*time.Second)
	defer cancel()
	started := time.Now()
	go func() {
		obs, err := r.a.Observe(ctx)
		done <- result{obs, err}
	}()
	select {
	case got := <-done:
		if took := time.Since(started); took > r.limit() {
			return got.obs, got.err, fmt.Errorf("a status read took %s, longer than the %s limit. Give the status command a timeout and return unknown when it is hit.", took.Round(time.Millisecond), r.limit())
		}
		return got.obs, got.err, nil
	case <-ctx.Done():
		return decide.Observation{}, nil, fmt.Errorf("a status read did not return within %s. Give the status command a timeout and return unknown when it is hit.", r.limit()+5*time.Second)
	}
}

func knownClass(c decide.ErrorClass) bool {
	switch c {
	case decide.ErrRateLimit, decide.ErrAuth, decide.ErrOverloaded, decide.ErrNetwork, decide.ErrUnknown:
		return true
	}
	return false
}

// valid checks that an observation is one state with a consistent class.
func valid(obs decide.Observation) error {
	switch obs.State {
	case decide.Idle, decide.Pending, decide.Stopping:
		if obs.ErrorClass != "" && obs.ErrorClass != decide.ErrNone {
			return fmt.Errorf("state %q carries error class %q. Report an error class only with state error.", obs.State, obs.ErrorClass)
		}
	case decide.Active:
		// A session that is working through its own retry of a failed request
		// is active and names what it is retrying.
		if obs.ErrorClass != "" && obs.ErrorClass != decide.ErrNone && !knownClass(obs.ErrorClass) {
			return fmt.Errorf("state active carries class %q. Use rate_limit, auth, overloaded, network or unknown.", obs.ErrorClass)
		}
	case decide.Unknown:
		if obs.ErrorClass != "" && obs.ErrorClass != decide.ErrNone && obs.ErrorClass != decide.ErrUnknown {
			return fmt.Errorf("state unknown carries error class %q. When the state cannot be read, do not name a class.", obs.ErrorClass)
		}
	case decide.Error:
		if !knownClass(obs.ErrorClass) {
			return fmt.Errorf("state error carries class %q. Use rate_limit, auth, overloaded, network or unknown.", obs.ErrorClass)
		}
	default:
		return fmt.Errorf("state %q is not one of idle, active, pending, stopping, error, unknown.", obs.State)
	}
	return nil
}

// unknownOnly passes for unknown, or an error of class unknown.
func unknownOnly(obs decide.Observation, what string) error {
	if obs.State == decide.Unknown || (obs.State == decide.Error && obs.ErrorClass == decide.ErrUnknown) {
		return nil
	}
	return fmt.Errorf("%s was read as state %q, class %q. It must read as unknown, or as an error of class unknown: when the evidence is not in the expected structured form, do not guess.", what, obs.State, obs.ErrorClass)
}

func caseOneState(r *run) error {
	obs, readErr, slow := r.observe()
	if slow != nil {
		return slow
	}
	if err := valid(obs); err != nil {
		return err
	}
	// Say what was read, so a wrapper that always answers unknown is seen.
	// The adapter's own error text is not repeated: it may quote a command.
	said := fmt.Sprintf("read as %s", obs.State)
	if obs.State == decide.Error {
		said += fmt.Sprintf(", class %s", obs.ErrorClass)
	}
	if obs.AwaitingInput {
		said += ", awaiting input"
	}
	if readErr != nil {
		said += "; the status read reported an error, so check the status command's output and exit code"
	}
	return note(said)
}

func (r *run) staged(s Situation) (decide.Observation, error) {
	if err := r.s.Rig.Stage(r.ctx, r.a, s); err != nil {
		return decide.Observation{}, fmt.Errorf("the stand-in could not be put in the %q situation: %v", s, err)
	}
	obs, _, slow := r.observe()
	if slow != nil {
		return obs, slow
	}
	return obs, valid(obs)
}

func caseKnown(r *run) error {
	for situation, want := range map[Situation]decide.State{Idle: decide.Idle, Active: decide.Active} {
		obs, err := r.staged(situation)
		if err != nil {
			return err
		}
		if obs.State != want {
			return fmt.Errorf("an agent that is %s was read as %q. Map the agent's own status field to %q.", situation, obs.State, want)
		}
		if situation == Active {
			_ = r.a.Stop(r.ctx)
			_ = r.s.Rig.Settle(r.ctx, r.a)
		}
	}
	return nil
}

func caseGarbage(r *run) error {
	obs, err := r.staged(Garbage)
	if err != nil {
		return err
	}
	return unknownOnly(obs, "output that is not the agent's status format")
}

func caseWaiting(r *run) error {
	if !r.s.Declared.StructuredWaiting {
		return skip("the adapter does not declare that it reports waiting on a person")
	}
	obs, err := r.staged(Waiting)
	if err != nil {
		return err
	}
	if !obs.AwaitingInput {
		return errors.New("an agent blocked on a person was not reported as awaiting input. Report it from the agent's structured status; without it the supervisor would send to a session that is waiting for an approval.")
	}
	return nil
}

func caseErrors(r *run) error {
	if !r.s.Declared.StructuredErrors {
		return skip("the adapter does not declare structured errors, so every failure must be unknown")
	}
	obs, err := r.staged(FailedCoded)
	if err != nil {
		return err
	}
	if want := r.s.Rig.CodedClass(); obs.State != decide.Error || obs.ErrorClass != want {
		return fmt.Errorf("a failure with a machine-readable code was read as state %q, class %q; want error, class %q. Map the agent's error code to a class.", obs.State, obs.ErrorClass, want)
	}
	obs, err = r.staged(FailedProse)
	if err != nil {
		return err
	}
	if obs.State != decide.Error || obs.ErrorClass != decide.ErrUnknown {
		return fmt.Errorf("a failure described only in prose was read as state %q, class %q; want error, class unknown. Never match words in the agent's output: a sentence that mentions a limit is not evidence of one.", obs.State, obs.ErrorClass)
	}
	for situation, what := range map[Situation]string{
		FailedUnmapped: "a failure whose code maps to no class was read as state %q, class %q; want error, class unknown. Map only the codes you know; every other code is unknown.",
		FailedStderr:   "a failure described only on standard error was read as state %q, class %q; want error, class unknown. Never read standard error to classify a failure.",
	} {
		if !r.s.Rig.Supports(situation) {
			continue
		}
		obs, err := r.staged(situation)
		if err != nil {
			return err
		}
		if obs.State != decide.Error || obs.ErrorClass != decide.ErrUnknown {
			return fmt.Errorf(what, obs.State, obs.ErrorClass)
		}
	}
	return nil
}

func caseNotHealthy(r *run) error {
	ran := false
	for situation, what := range map[Situation]string{
		ExitNonZero: "output that looks healthy from a command that exited with a failure",
		Crashed:     "a turn that ended without leaving a result",
	} {
		if !r.s.Rig.Supports(situation) {
			continue
		}
		ran = true
		obs, err := r.staged(situation)
		if err != nil {
			return err
		}
		if obs.State != decide.Error || obs.ErrorClass != decide.ErrUnknown {
			return fmt.Errorf("%s was read as state %q, class %q; want error, class unknown. Check the exit code before the output, and treat a turn with no result as failed.", what, obs.State, obs.ErrorClass)
		}
	}
	if !ran {
		return skip("this adapter's agent can neither exit with a failure nor end without a result")
	}
	return nil
}

func (r *run) sent() ([]Sent, error) {
	sent, err := r.s.Rig.Sent()
	if err != nil {
		return nil, fmt.Errorf("the stand-in's record of what it received could not be read: %v", err)
	}
	return sent, nil
}

// send delivers the check prompt and returns what the stand-in received for it.
func (r *run) send() (Sent, error) {
	before, err := r.sent()
	if err != nil {
		return Sent{}, err
	}
	if err := r.a.Send(r.ctx, CheckPrompt, adapter.ModeAuto); err != nil {
		return Sent{}, fmt.Errorf("the send was refused: %v", err)
	}
	if err := r.s.Rig.Settle(r.ctx, r.a); err != nil {
		return Sent{}, err
	}
	after, err := r.sent()
	if err != nil {
		return Sent{}, err
	}
	if got := len(after) - len(before); got != 1 {
		return Sent{}, fmt.Errorf("one send reached the agent %d times. Deliver each prompt exactly once.", got)
	}
	return after[len(after)-1], nil
}

func caseSendOnce(r *run) error {
	if err := r.s.Rig.Stage(r.ctx, r.a, Idle); err != nil {
		return err
	}
	got, err := r.send()
	if err != nil {
		return err
	}
	if got.Prompt != CheckPrompt {
		return fmt.Errorf("the agent received %q, not the prompt that was sent. Pass the prompt through unchanged.", got.Prompt)
	}
	for _, arg := range got.Argv {
		if strings.Contains(arg, Token) {
			return errors.New("the prompt appeared in the command's arguments, where other users of the machine can read it. Pass it on standard input or in a private file.")
		}
	}
	if got.InEnv {
		return errors.New("the prompt appeared in the agent's environment, which child processes inherit and other tools print. Pass it on standard input or in a private file.")
	}
	if open := r.openPromptFile(); open != "" {
		return fmt.Errorf("the prompt was saved in %s, which other users of the machine can read. Create every file that holds a prompt with mode 0600.", open)
	}
	count := func() (int, error) {
		sent, err := r.sent()
		return len(sent), err
	}
	want, err := count()
	if err != nil {
		return err
	}
	// Reading status, and a supervisor that was stopped and started, must
	// not deliver the prompt again.
	for i := 0; i < 2; i++ {
		if _, _, slow := r.observe(); slow != nil {
			return slow
		}
	}
	again, err := r.s.New()
	if err != nil {
		return fmt.Errorf("the adapter could not be rebuilt: %v", err)
	}
	if _, err := again.Observe(r.ctx); err != nil && r.ctx.Err() != nil {
		return err
	}
	if err := r.s.Rig.Settle(r.ctx, again); err != nil {
		return err
	}
	if got, err := count(); err != nil || got != want {
		return fmt.Errorf("the prompt was delivered again without a new send (%d deliveries, want %d). Reading status or restarting the supervisor must never re-send.", got, want)
	}
	return nil
}

// openPromptFile names a file under the state directory that holds the check
// prompt and that someone other than its owner can read, or "".
func (r *run) openPromptFile() string {
	if r.s.StateDir == "" || runtime.GOOS == "windows" {
		return ""
	}
	found := ""
	_ = filepath.WalkDir(r.s.StateDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || found != "" || !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Mode().Perm()&0o077 == 0 || info.Size() > 1<<20 {
			return nil
		}
		if b, err := os.ReadFile(path); err == nil && strings.Contains(string(b), Token) {
			found, _ = filepath.Rel(r.s.StateDir, path)
		}
		return nil
	})
	return found
}

// savedUnder reports whether some file under the state directory holds text.
func savedUnder(dir, text string) bool {
	found := false
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || found || !d.Type().IsRegular() {
			return nil
		}
		if info, err := d.Info(); err != nil || info.Size() > 1<<20 {
			return nil
		}
		if b, err := os.ReadFile(path); err == nil && strings.Contains(string(b), text) {
			found = true
		}
		return nil
	})
	return found
}

func caseSendWorking(r *run) error {
	counter, counts := r.s.Rig.(AgentCounter)
	if err := r.s.Rig.Stage(r.ctx, r.a, Active); err != nil {
		return fmt.Errorf("the stand-in could not be put in the %q situation: %v", Active, err)
	}
	defer func() { _ = r.a.Stop(r.ctx) }()
	var err error
	running := 0
	if counts {
		// Wait for the working agent to have begun.
		for deadline := time.Now().Add(20 * time.Second); running < 1; time.Sleep(10 * time.Millisecond) {
			if running, err = counter.Running(); err != nil {
				return err
			}
			if time.Now().After(deadline) {
				return errors.New("the stand-in agent did not begin working")
			}
		}
	}
	time.Sleep(100 * time.Millisecond) // the working agent has recorded its own prompt
	before, err := r.sent()
	if err != nil {
		return err
	}
	sendErr := r.a.Send(r.ctx, CheckPrompt, adapter.ModeAuto)
	time.Sleep(300 * time.Millisecond) // long enough for a second agent to begin
	after, err := r.sent()
	if err != nil {
		return err
	}
	delivered := len(after) - len(before)
	if counts {
		now, err := counter.Running()
		if err != nil {
			return err
		}
		if now > running || delivered > 0 {
			return fmt.Errorf("a send while the agent was working started another agent beside it (%d running before, %d after). Refuse a send while a turn this supervisor started is still running.", running, now)
		}
		if sendErr == nil {
			return errors.New("a send while the agent was working reported success, but nothing was delivered. Return an error when a prompt is not sent.")
		}
		return nil
	}
	if delivered > 1 || (sendErr == nil && delivered != 1) {
		return fmt.Errorf("a send while the agent was working was delivered %d times and reported error %v. Deliver it once or refuse it.", delivered, sendErr)
	}
	return nil
}

func caseObserveDuringSend(r *run) error {
	if err := r.s.Rig.Stage(r.ctx, r.a, Idle); err != nil {
		return err
	}
	before, err := r.sent()
	if err != nil {
		return err
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var bad error
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			// A read may fail while the turn's files are being replaced; what
			// it returns must still be one valid state.
			obs, _ := r.a.Observe(r.ctx)
			if err := valid(obs); err != nil && bad == nil {
				bad = fmt.Errorf("a status read during a send was not one valid state: %v", err)
			}
			time.Sleep(time.Millisecond)
		}
	}()
	sendErr := r.a.Send(r.ctx, CheckPrompt, adapter.ModeAuto)
	settleErr := r.s.Rig.Settle(r.ctx, r.a)
	close(stop)
	wg.Wait()
	switch {
	case sendErr != nil:
		return fmt.Errorf("the send was refused while status was being read: %v", sendErr)
	case settleErr != nil:
		return settleErr
	case bad != nil:
		return bad
	}
	after, err := r.sent()
	if err != nil {
		return err
	}
	if got := len(after) - len(before); got != 1 {
		return fmt.Errorf("one send made while status was being read reached the agent %d times. Deliver each prompt exactly once.", got)
	}
	return nil
}

func caseUnwritable(r *run) error {
	if r.s.StateDir == "" {
		return skip("the subject names no state directory")
	}
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		return skip("a directory cannot be made unwritable for this user on this platform")
	}
	if err := r.s.Rig.Stage(r.ctx, r.a, Idle); err != nil {
		return err
	}
	before, err := r.sent()
	if err != nil {
		return err
	}
	var dirs []string
	_ = filepath.WalkDir(r.s.StateDir, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			dirs = append(dirs, path)
		}
		return nil
	})
	for _, dir := range dirs {
		_ = os.Chmod(dir, 0o500)
	}
	sendErr := r.a.Send(r.ctx, CheckPrompt, adapter.ModeAuto)
	for _, dir := range dirs {
		_ = os.Chmod(dir, 0o700)
	}
	if err := r.s.Rig.Settle(r.ctx, r.a); err != nil {
		return err
	}
	// A send that reported success is given time to arrive.
	delivered := 0
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		after, err := r.sent()
		if err != nil {
			return err
		}
		if delivered = len(after) - len(before); sendErr != nil || delivered >= 1 || time.Now().After(deadline) {
			break
		}
	}
	time.Sleep(200 * time.Millisecond) // long enough for a duplicate to show
	if after, err := r.sent(); err == nil {
		delivered = len(after) - len(before)
	}
	switch {
	case sendErr == nil && delivered != 1:
		return fmt.Errorf("with the state directory unwritable, the send reported success but the agent received the prompt %d times. Return the error when the prompt or its record cannot be written.", delivered)
	case delivered > 1:
		return fmt.Errorf("with the state directory unwritable, the prompt was delivered %d times.", delivered)
	}
	return nil
}

func caseResume(r *run) error {
	if !r.s.Declared.ExactResume {
		return skip("the adapter does not declare exact resume; the agent or its wrapper owns which session continues")
	}
	if err := r.s.Rig.Stage(r.ctx, r.a, Idle); err != nil {
		return err
	}
	first, err := r.send()
	if err != nil {
		return err
	}
	if _, _, slow := r.observe(); slow != nil {
		return slow
	}
	// A stopped and started supervisor has only the state directory.
	if want := r.s.Rig.ReportedSession(); r.s.StateDir != "" && !savedUnder(r.s.StateDir, want) {
		return errors.New("the session the agent reported is not saved in the state directory, so a supervisor that is stopped and started could not resume it. Save it in a file there as soon as it is reported.")
	}
	again, err := r.s.New()
	if err != nil {
		return fmt.Errorf("the adapter could not be rebuilt: %v", err)
	}
	r.a = again
	if _, _, slow := r.observe(); slow != nil {
		return slow
	}
	second, err := r.send()
	if err != nil {
		return err
	}
	_ = first
	if want := r.s.Rig.ReportedSession(); second.Session != want {
		if second.Session == "" {
			return errors.New("after a restart the next send did not name a session to resume, so the agent started a new conversation. Save the session the agent reports and resume exactly that one.")
		}
		return fmt.Errorf("after a restart the next send resumed session %q, but the agent had reported %q. Resume exactly the session the agent reported.", second.Session, want)
	}
	return nil
}

func caseHang(r *run) error {
	obs, err := r.staged(Hanging)
	_ = r.a.Stop(r.ctx)
	if err != nil {
		return err
	}
	if obs.State == decide.Idle || (obs.State == decide.Error && obs.ErrorClass != decide.ErrUnknown) {
		return fmt.Errorf("while the agent's command hung, status read as state %q, class %q. A command that does not answer is active or unknown, never idle and never a classified failure.", obs.State, obs.ErrorClass)
	}
	return nil
}

func caseStop(r *run) error {
	if !r.s.Declared.Stoppable {
		return skip("the adapter does not declare that it can be stopped")
	}
	var alive func() bool
	if foreign, ok := r.s.Rig.(ForeignWork); ok {
		var end func()
		var err error
		if alive, end, err = foreign.StartForeign(); err != nil {
			return fmt.Errorf("the stand-in could not start foreign work: %v", err)
		}
		// Nothing of this supervisor's is running yet: a stop now must not
		// reach the foreign work.
		_ = r.a.Stop(r.ctx)
		time.Sleep(300 * time.Millisecond)
		survived := alive()
		end()
		if !survived {
			return errors.New("stop ended work this supervisor did not start. Never signal a process because its ID is saved in a file: stop only a turn this supervisor started and still owns.")
		}
	}
	obs, err := r.staged(Active)
	if err != nil {
		return err
	}
	if obs.State != decide.Active {
		return fmt.Errorf("a running turn read as %q before the stop", obs.State)
	}
	if err := r.a.Stop(r.ctx); err != nil {
		return fmt.Errorf("stopping a running turn failed: %v", err)
	}
	if err := r.s.Rig.Settle(r.ctx, r.a); err != nil {
		return err
	}
	after, _, slow := r.observe()
	if slow != nil {
		return slow
	}
	if after.State == decide.Active {
		return errors.New("the turn was still active after stop returned and the agent had time to end. Make stop end the turn it started.")
	}
	return nil
}

func caseBounded(r *run) error {
	obs, err := r.staged(Oversized)
	if err != nil {
		return err
	}
	return unknownOnly(obs, "output far larger than any real status")
}

type entry struct {
	size int64
	mod  time.Time
	dir  bool
}

func snapshot(roots []string) map[string]entry {
	out := map[string]entry{}
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if info, err := d.Info(); err == nil {
				out[path] = entry{size: info.Size(), mod: info.ModTime(), dir: info.IsDir()}
			}
			return nil
		})
		if _, err := os.Stat(root); err != nil {
			out[root] = entry{size: -1}
		}
	}
	return out
}

// changed names what differs between two snapshots, or "".
func changed(before, after map[string]entry) string {
	var diff []string
	for path, b := range before {
		a, ok := after[path]
		switch {
		case !ok:
			diff = append(diff, "removed "+path)
		case !a.dir && (a.size != b.size || !a.mod.Equal(b.mod)):
			diff = append(diff, "changed "+path)
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			diff = append(diff, "created "+path)
		}
	}
	sort.Strings(diff)
	if len(diff) > 3 {
		diff = append(diff[:3], fmt.Sprintf("and %d more", len(diff)-3))
	}
	return strings.Join(diff, "; ")
}
