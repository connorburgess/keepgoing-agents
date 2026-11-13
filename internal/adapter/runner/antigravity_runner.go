package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/evidence"
)

// AntigravityConfig is what the runner needs to start one ACP turn. Command is
// the provider's ACP server command with its arguments. No default is assumed:
// no installed Antigravity CLI has been examined, so the command, its flags
// and its supported versions are unverified.
type AntigravityConfig struct {
	Command []string
	Workdir string
	Env     map[string]string
	// HandshakeTimeout bounds initialization and session setup inside the
	// detached worker; ExitTimeout bounds the wait for the agent process to
	// exit after its turn. Zero selects the defaults.
	HandshakeTimeout time.Duration
	ExitTimeout      time.Duration
}

// Antigravity is a detached, primary-only runner for one ACP conversation.
// Each Send starts one owned worker that performs one checkpointed prompt
// exchange and exits. It is deliberately not registered with configuration,
// the adapter factory or guided setup: the permission contract (roadmap A2)
// is unresolved, so nothing can select it for unattended looping. It has no
// probe, limit, reset-time or fallback role.
//
// Only transport evidence decides an observation: the worker's exit status,
// the saved hold records and the worker's outcome record. Message, tool and
// error text never do, and every failure is unknown.
type Antigravity struct {
	cfg AntigravityConfig
	dir Dir
}

// NewAntigravity returns the runner for one target directory.
func NewAntigravity(cfg AntigravityConfig, dir string) *Antigravity {
	return &Antigravity{cfg: cfg, dir: Dir(dir)}
}

// Name implements adapter.Adapter.
func (a *Antigravity) Name() string { return "antigravity" }

const (
	antigravityTurnName    = "acp-turn.json"
	antigravitySessionName = "acp-session.json"
	antigravityOutcomeName = "acp-outcome.json"
	antigravityTurnsDir    = "acp-turns"
	// The worker stores no protocol output. A stdout file here is not ours.
	antigravityStdoutLimit = 1024

	antigravityDefaultHandshake = 2 * time.Minute
	antigravityDefaultExit      = 10 * time.Second
	antigravityMaxLimit         = 10 * time.Minute
)

var errAntigravityRecords = errors.New("antigravity: saved turn records are unavailable or inconsistent; inspect the private target directory")

// antigravityOutcome is what the worker saw the exchange end with. It holds
// identities and three facts, never prompt, response or error text.
type antigravityOutcome struct {
	turn, session               string
	returned, needsUser, failed bool
}

func (d Dir) antigravityTurnDir(turn string) Dir {
	return Dir(filepath.Join(string(d), antigravityTurnsDir, turn))
}

// readAntigravityObject requires a bounded regular file holding one JSON
// object with exactly n keys and version 1. A missing file is (nil, nil).
func (d Dir) readAntigravityObject(name string, n int) (map[string]json.RawMessage, error) {
	p := d.path(name)
	info, err := os.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > antigravityACPHoldLimit {
		return nil, errAntigravityRecords
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, errAntigravityRecords
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, antigravityACPHoldLimit+1))
	if err != nil || len(b) > antigravityACPHoldLimit {
		return nil, errAntigravityRecords
	}
	fields, err := cursorObject(b)
	if err != nil || len(fields) != n || string(fields["version"]) != "1" {
		return nil, errAntigravityRecords
	}
	return fields, nil
}

// antigravityTurn reads which turn is current. Turn identities are the
// runner's own random values, so anything else is refused.
func (d Dir) antigravityTurn() (string, bool, error) {
	fields, err := d.readAntigravityObject(antigravityTurnName, 2)
	if err != nil || fields == nil {
		return "", false, err
	}
	var turn string
	if !acpString(fields, "turn", &turn) || !validStopID(turn) {
		return "", false, errAntigravityRecords
	}
	return turn, true, nil
}

// saveAntigravityTurn replaces the pointer to the current turn. Only Send
// writes it, before the worker exists.
func (d Dir) saveAntigravityTurn(turn string) error {
	b, _ := json.Marshal(struct {
		Version int    `json:"version"`
		Turn    string `json:"turn"`
	}{1, turn})
	f, err := os.CreateTemp(string(d), ".acp-turn-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(b, '\n')); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), d.path(antigravityTurnName))
}

// antigravitySession reads the conversation this target resumes. Empty means
// none is saved. The identity is opaque and kept exactly.
func (d Dir) antigravitySession() (string, error) {
	fields, err := d.readAntigravityObject(antigravitySessionName, 2)
	if err != nil || fields == nil {
		return "", err
	}
	var session string
	if !acpString(fields, "sessionId", &session) || !acpIdentifier(session) {
		return "", errAntigravityRecords
	}
	return session, nil
}

// rememberAntigravitySession saves the conversation once and never replaces
// it: a target has one conversation, and a turn that names another is refused.
func (d Dir) rememberAntigravitySession(session string) error {
	saved, err := d.antigravitySession()
	if err != nil {
		return err
	}
	if saved == "" {
		b, _ := json.Marshal(struct {
			Version   int    `json:"version"`
			SessionID string `json:"sessionId"`
		}{1, session})
		if _, err := d.publishAntigravityOnce(antigravitySessionName, b); err != nil {
			return errAntigravityRecords
		}
		if saved, err = d.antigravitySession(); err != nil {
			return err
		}
	}
	if saved != session {
		return errAntigravityRecords
	}
	return nil
}

func (d Dir) antigravityOutcome() (antigravityOutcome, bool, error) {
	fields, err := d.readAntigravityObject(antigravityOutcomeName, 6)
	if err != nil || fields == nil {
		return antigravityOutcome{}, false, err
	}
	var out antigravityOutcome
	flag := func(key string, v *bool) bool {
		switch string(fields[key]) {
		case "true":
			*v = true
		case "false":
		default:
			return false
		}
		return true
	}
	if !acpString(fields, "turn", &out.turn) || !validStopID(out.turn) || !acpString(fields, "sessionId", &out.session) ||
		(out.session != "" && !acpIdentifier(out.session)) ||
		!flag("returned", &out.returned) || !flag("needsUser", &out.needsUser) || !flag("failed", &out.failed) {
		return antigravityOutcome{}, false, errAntigravityRecords
	}
	return out, true, nil
}

// saveAntigravityOutcome is called once by the worker, after the exchange.
func (d Dir) saveAntigravityOutcome(out antigravityOutcome) error {
	b, err := json.Marshal(struct {
		Version   int    `json:"version"`
		Turn      string `json:"turn"`
		SessionID string `json:"sessionId"`
		Returned  bool   `json:"returned"`
		NeedsUser bool   `json:"needsUser"`
		Failed    bool   `json:"failed"`
	}{1, out.turn, out.session, out.returned, out.needsUser, out.failed})
	if err != nil {
		return err
	}
	created, err := d.publishAntigravityOnce(antigravityOutcomeName, b)
	if err == nil && !created {
		err = errAntigravityRecords
	}
	return err
}

// antigravityView is everything saved about the latest turn, already checked
// for agreement between the records.
type antigravityView struct {
	st      Status
	turn    string
	turnDir Dir
	hasTurn bool
	hold    AntigravityACPHold
	hasHold bool
	out     antigravityOutcome
	hasOut  bool
}

// view reads the saved records and refuses any disagreement between them, so
// neither an observation nor a send is ever built on evidence that names
// another turn or another conversation.
func (a *Antigravity) view() (antigravityView, error) {
	var v antigravityView
	var err error
	if v.st, err = a.dir.statusWithOutputLimit(antigravityStdoutLimit); err != nil {
		return v, errAntigravityRecords
	}
	if v.turn, v.hasTurn, err = a.dir.antigravityTurn(); err != nil {
		return v, err
	}
	if !v.hasTurn {
		if v.st.Phase != NeverRun {
			return v, errAntigravityRecords
		}
		if _, err := a.dir.antigravitySession(); err != nil {
			return v, err
		}
		return v, nil
	}
	v.turnDir = a.dir.antigravityTurnDir(v.turn)
	if v.hold, v.hasHold, err = v.turnDir.AntigravityACPHold(); err != nil {
		return v, errAntigravityRecords
	}
	if v.out, v.hasOut, err = v.turnDir.antigravityOutcome(); err != nil {
		return v, err
	}
	if v.hasHold && v.hold.Turn != v.turn {
		return v, errAntigravityRecords
	}
	if v.hasOut {
		// A returned turn or a cancelled interaction is impossible without the
		// reservation that precedes the prompt, and all records name one session.
		if v.out.turn != v.turn || ((v.out.returned || v.out.needsUser) && !v.hasHold) ||
			(v.hasHold && v.out.session != "" && v.out.session != v.hold.SessionID) ||
			(v.out.returned && (v.out.needsUser || v.out.failed)) {
			return v, errAntigravityRecords
		}
	}
	if v.hasHold {
		// The reservation is written before the prompt, so the conversation is
		// known even when the worker died right after it.
		if err := a.dir.rememberAntigravitySession(v.hold.SessionID); err != nil {
			return v, err
		}
	} else if _, err := a.dir.antigravitySession(); err != nil {
		return v, err
	}
	return v, nil
}

// Observe reports the latest turn from saved transport evidence only.
func (a *Antigravity) Observe(ctx context.Context) (decide.Observation, error) {
	unknown := decide.Observation{State: decide.Unknown, ErrorClass: decide.ErrUnknown}
	if err := ctx.Err(); err != nil {
		return unknown, err
	}
	v, err := a.view()
	detail := runnerEvidence(v.st)
	unknown.Evidence = detail
	if err != nil {
		return unknown, err
	}
	failed := decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown, Evidence: detail}
	switch v.st.Phase {
	case NeverRun:
		if v.hasHold {
			return failed, nil // a reservation without a worker record: not a clean start
		}
		return decide.Observation{State: decide.Idle, ErrorClass: decide.ErrNone, Evidence: detail}, nil
	case Running:
		// Also while a cancelled turn drains: its evidence is reported once the
		// worker has exited, so no send can overlap it.
		return decide.Observation{State: decide.Active, ErrorClass: decide.ErrNone, Evidence: detail}, nil
	}
	detail.Source = "antigravity_acp"
	detail = evidence.Clean(detail)
	failed.Evidence = detail
	if v.hasHold && v.hold.Deferred() {
		failed.DeferredInteraction = true
		return failed, nil
	}
	returned := v.st.Phase == Finished && v.st.ExitCodeKnown && v.st.ExitCode == 0 &&
		v.hasHold && !v.hold.NeedsUser && v.hasOut && v.out.returned
	if returned {
		return decide.Observation{State: decide.Idle, ErrorClass: decide.ErrNone, Evidence: detail}, nil
	}
	return failed, nil
}

func (a *Antigravity) limits() (time.Duration, time.Duration) {
	clamp := func(d, def time.Duration) time.Duration {
		if d <= 0 {
			return def
		}
		return min(d, antigravityMaxLimit)
	}
	return clamp(a.cfg.HandshakeTimeout, antigravityDefaultHandshake), clamp(a.cfg.ExitTimeout, antigravityDefaultExit)
}

// Send starts one worker for one prompt. The prompt reaches the agent only as
// a framed message on its private stdin, never in arguments. A running turn,
// unadopted human-input evidence and unreadable records all refuse the send.
// A finished or dead earlier turn is rolled over to a new turn directory; when
// that turn reserved a prompt, the new one resumes exactly its conversation.
// It never falls back from a saved conversation to a new one.
func (a *Antigravity) Send(ctx context.Context, prompt string, mode adapter.Mode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(a.cfg.Command) == 0 || a.cfg.Command[0] == "" {
		return errors.New("antigravity: no ACP command is configured")
	}
	v, err := a.view()
	if err != nil {
		return err
	}
	if v.st.Phase == Running {
		return adapter.ErrUnsupported{Op: fmt.Sprintf("sending to a running Antigravity turn (%s mode)", mode)}
	}
	if v.hasHold && v.hold.Deferred() {
		return errors.New("antigravity: a cancelled interaction is waiting for a person; refusing to send")
	}
	session, err := a.dir.antigravitySession()
	if err != nil {
		return err
	}
	// Fail before a worker exists on anything the exchange would refuse.
	workdir, err := filepath.Abs(a.cfg.Workdir)
	if err != nil || a.cfg.Workdir == "" {
		return errors.New("antigravity: a working directory is required")
	}
	if _, err := acpOpenSessionFrame(session, workdir); err != nil {
		return errors.New("antigravity: the working directory cannot be sent")
	}
	promptSession := session
	if promptSession == "" {
		promptSession = strings.Repeat("<", 256) // the largest identity the exchange reserves for
	}
	if _, err := acpPromptFrame(promptSession, prompt); err != nil {
		return errors.New("antigravity: the prompt exceeds the supported message format")
	}
	turn, err := randomStopID()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(string(a.dir.antigravityTurnDir(turn)), 0o700); err != nil {
		return err
	}
	if err := a.dir.saveAntigravityTurn(turn); err != nil {
		return errors.New("antigravity: could not record the new turn")
	}
	handshake, exit := a.limits()
	spec, _ := json.Marshal(antigravityTurnSpec{Turn: turn, HandshakeMs: handshake.Milliseconds(), ExitMs: exit.Milliseconds()})
	return a.dir.Start(a.cfg.Command, workdir, prompt, append(envList(a.cfg.Env), envTurnACP+"="+string(spec)))
}

// Stop asks the live worker to end its own turn through the owned channel.
// A saved process ID is never signalled.
func (a *Antigravity) Stop(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	st, err := a.dir.statusWithOutputLimit(antigravityStdoutLimit)
	if err != nil || st.Phase != Running {
		return err
	}
	return a.dir.requestStop(st.Record.StopKey)
}

// AdoptInteraction implements adapter.InteractionAdopter for the current turn.
func (a *Antigravity) AdoptInteraction(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	turn, ok, err := a.dir.antigravityTurn()
	if err != nil || !ok {
		return errAntigravityRecords
	}
	return a.dir.antigravityTurnDir(turn).AdoptAntigravityACPHold(turn)
}
