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

	"github.com/looptender/looptender/internal/adapter"
	"github.com/looptender/looptender/internal/decide"
	"github.com/looptender/looptender/internal/evidence"
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
	// exit after its turn; CancelTimeout bounds draining after a permission
	// cancellation; WriteTimeout bounds each stdin frame write. Zero selects
	// the defaults.
	HandshakeTimeout time.Duration
	ExitTimeout      time.Duration
	CancelTimeout    time.Duration
	WriteTimeout     time.Duration
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
	// Pending holds the stop key written before spawn so an unconfirmed worker
	// can still be stopped and cleared without signalling a saved PID.
	antigravityPendingName = "acp-pending.json"
	// Published by Send once the worker it started is recorded, with that
	// worker's stop key; or instead, when no process could be created.
	antigravityStartedName    = "acp-started.json"
	antigravityNotStartedName = "acp-not-started.json"
	antigravityClearedName    = "acp-cleared.json"
	antigravityTurnsDir       = "acp-turns"
	// The worker stores no protocol output. A stdout file here is not ours.
	antigravityStdoutLimit = 1024

	antigravityDefaultHandshake = 2 * time.Minute
	antigravityDefaultExit      = 10 * time.Second
	antigravityDefaultCancel    = 10 * time.Second
	antigravityDefaultWrite     = 10 * time.Second
	antigravityMaxLimit         = 10 * time.Minute
)

var errAntigravityRecords = errors.New("antigravity: saved turn records are unavailable or inconsistent; inspect the private target directory")

// A turn was recorded but Send never confirmed the worker it started, for
// example because the supervisor died in between. A worker may be running
// that no record identifies, so nothing may be sent beside it.
var errAntigravityUnconfirmed = errors.New("antigravity: a turn was recorded without a confirmed worker, which may still be running; inspect the private target directory")

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

// antigravityStart reads how the current turn's start ended: pending stop key
// recorded before spawn, confirmed with the worker's stop key, known not to
// have created a process, explicitly cleared after verifying no worker is
// running, or none of the terminal states.
func (d Dir) antigravityStart(turn string) (key, pendingKey string, started, notStarted, cleared bool, err error) {
	fields, err := d.readAntigravityObject(antigravityPendingName, 3)
	if err != nil {
		return "", "", false, false, false, err
	}
	if fields != nil {
		var recorded string
		if !acpString(fields, "turn", &recorded) || recorded != turn || !acpString(fields, "stopKey", &pendingKey) || !validStopID(pendingKey) {
			return "", "", false, false, false, errAntigravityRecords
		}
	}
	fields, err = d.readAntigravityObject(antigravityStartedName, 3)
	if err != nil {
		return "", "", false, false, false, err
	}
	if fields != nil {
		var recorded string
		if !acpString(fields, "turn", &recorded) || recorded != turn || !acpString(fields, "stopKey", &key) || !validStopID(key) || (pendingKey != "" && key != pendingKey) {
			return "", "", false, false, false, errAntigravityRecords
		}
		started = true
	}
	fields, err = d.readAntigravityObject(antigravityNotStartedName, 2)
	if err != nil {
		return "", "", false, false, false, err
	}
	if fields != nil {
		var recorded string
		if !acpString(fields, "turn", &recorded) || recorded != turn || started {
			return "", "", false, false, false, errAntigravityRecords
		}
		notStarted = true
	}
	fields, err = d.readAntigravityObject(antigravityClearedName, 2)
	if err != nil {
		return "", "", false, false, false, err
	}
	if fields != nil {
		var recorded string
		if !acpString(fields, "turn", &recorded) || recorded != turn || started || notStarted {
			return "", "", false, false, false, errAntigravityRecords
		}
		cleared = true
	}
	return key, pendingKey, started, notStarted, cleared, nil
}

// antigravityView is everything saved about the latest turn, already checked
// for agreement between the records.
type antigravityView struct {
	// noWorker means the current turn is known never to have had a process;
	// st then describes an earlier turn and is not this turn's status.
	noWorker bool
	// cleared means an unconfirmed turn was explicitly stopped/verified dead
	// and cleared so a later send can roll over without discarding saved holds.
	cleared bool
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
	// The run record must be this turn's. Without Send's confirmation it could
	// be an earlier turn's, or missing, while this turn's worker is alive.
	key, _, started, notStarted, cleared, err := v.turnDir.antigravityStart(v.turn)
	if err != nil {
		return v, err
	}
	switch {
	case notStarted:
		v.noWorker = true
	case cleared:
		if v.st.Phase == Running {
			return v, errAntigravityUnconfirmed
		}
		v.cleared = true
	case !started:
		return v, errAntigravityUnconfirmed
	case v.st.Phase == NeverRun || v.st.Record.StopKey != key:
		return v, errAntigravityRecords
	}
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
	if v.noWorker {
		// The send failed before any process existed; nothing left this machine.
		if v.hasHold || v.hasOut {
			return unknown, errAntigravityRecords
		}
		return failed, nil
	}
	if v.cleared {
		detail.Source = "antigravity_acp"
		detail = evidence.Clean(detail)
		failed.Evidence = detail
		if v.hasHold && v.hold.Deferred() {
			failed.DeferredInteraction = true
		}
		return failed, nil
	}
	switch v.st.Phase {
	case NeverRun: // no turn has ever been recorded
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

func (a *Antigravity) limits() (handshake, exit, cancel, write time.Duration) {
	clamp := func(d, def time.Duration) time.Duration {
		if d <= 0 {
			return def
		}
		return min(d, antigravityMaxLimit)
	}
	return clamp(a.cfg.HandshakeTimeout, antigravityDefaultHandshake),
		clamp(a.cfg.ExitTimeout, antigravityDefaultExit),
		clamp(a.cfg.CancelTimeout, antigravityDefaultCancel),
		clamp(a.cfg.WriteTimeout, antigravityDefaultWrite)
}

// pruneAntigravityTurns removes older per-turn directories under acp-turns/,
// keeping at most the current turn and the immediately preceding turn so
// directory count stays bounded without touching the just-superseded record.
func (d Dir) pruneAntigravityTurns(keep ...string) {
	retained := map[string]bool{}
	for _, k := range keep {
		if k != "" {
			retained[k] = true
		}
	}
	turnsRoot := filepath.Join(string(d), antigravityTurnsDir)
	entries, err := os.ReadDir(turnsRoot)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if retained[name] || !validStopID(name) {
			continue
		}
		_ = os.RemoveAll(filepath.Join(turnsRoot, name))
	}
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
	if v.noWorker && (v.hasHold || v.hasOut) {
		return errAntigravityRecords
	}
	if !v.noWorker && !v.cleared && v.st.Phase == Running {
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
	stopKey, err := randomStopID()
	if err != nil {
		return err
	}
	turnDir := a.dir.antigravityTurnDir(turn)
	if err := os.MkdirAll(string(turnDir), 0o700); err != nil {
		return err
	}
	record := func(name string, val any) error {
		b, _ := json.Marshal(val)
		if created, err := turnDir.publishAntigravityOnce(name, b); err != nil || !created {
			return errAntigravityUnconfirmed
		}
		return nil
	}
	// Record the stop key before recording the turn or spawning the worker, so
	// an interrupted start can still be stopped and cleared through its owned
	// channel.
	if err := record(antigravityPendingName, struct {
		Version int    `json:"version"`
		Turn    string `json:"turn"`
		StopKey string `json:"stopKey"`
	}{1, turn, stopKey}); err != nil {
		_ = os.RemoveAll(string(turnDir))
		return errors.New("antigravity: could not record the new turn")
	}
	if err := a.dir.saveAntigravityTurn(turn); err != nil {
		_ = os.RemoveAll(string(turnDir))
		return errors.New("antigravity: could not record the new turn")
	}
	handshake, exit, cancel, write := a.limits()
	spec, _ := json.Marshal(antigravityTurnSpec{
		Turn:        turn,
		HandshakeMs: handshake.Milliseconds(),
		ExitMs:      exit.Milliseconds(),
		CancelMs:    cancel.Milliseconds(),
		WriteMs:     write.Milliseconds(),
	})
	key, err := a.dir.startWithKey(a.cfg.Command, workdir, prompt, envList(a.cfg.Env), string(spec), stopKey)
	if err != nil {
		var none notSpawned
		if errors.As(err, &none) {
			// No process exists for this turn, so a later send need not fear one.
			_ = record(antigravityNotStartedName, struct {
				Version int    `json:"version"`
				Turn    string `json:"turn"`
			}{1, turn})
			a.dir.pruneAntigravityTurns(turn, v.turn)
		}
		return err
	}
	// Confirm which worker belongs to this turn. Until this record exists the
	// turn is unconfirmed and everything fails closed.
	err = record(antigravityStartedName, struct {
		Version int    `json:"version"`
		Turn    string `json:"turn"`
		StopKey string `json:"stopKey"`
	}{1, turn, key})
	if err == nil {
		a.dir.pruneAntigravityTurns(turn, v.turn)
	}
	return err
}

// Stop asks the live worker to end its own turn through the owned channel,
// including an unconfirmed worker whose stop key was recorded before spawn.
// A saved process ID is never signalled.
func (a *Antigravity) Stop(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	st, err := a.dir.statusWithOutputLimit(antigravityStdoutLimit)
	if err != nil || st.Phase != Running {
		return err
	}
	if turn, ok, terr := a.dir.antigravityTurn(); terr == nil && ok {
		_, pendingKey, started, notStarted, cleared, serr := a.dir.antigravityTurnDir(turn).antigravityStart(turn)
		if serr == nil && !started && !notStarted && !cleared && pendingKey != "" && st.Record.StopKey == pendingKey {
			return a.dir.requestStop(pendingKey)
		}
	}
	return a.dir.requestStop(st.Record.StopKey)
}

// ClearUnconfirmed stops any still-running unconfirmed worker through its
// owned stop channel, waits for it to exit, preserves any recorded session or
// unadopted hold, and records that the unconfirmed turn has been cleared so a
// later send may roll over.
func (a *Antigravity) ClearUnconfirmed(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	turn, ok, err := a.dir.antigravityTurn()
	if err != nil || !ok {
		return errAntigravityRecords
	}
	turnDir := a.dir.antigravityTurnDir(turn)
	_, pendingKey, started, notStarted, cleared, err := turnDir.antigravityStart(turn)
	if err != nil {
		return err
	}
	if started || notStarted || cleared {
		return nil
	}
	st, err := a.dir.statusWithOutputLimit(antigravityStdoutLimit)
	if err != nil {
		return errAntigravityRecords
	}
	if st.Phase == Running {
		stopKey := st.Record.StopKey
		if pendingKey != "" && stopKey == pendingKey {
			stopKey = pendingKey
		}
		if !validStopID(stopKey) {
			return errAntigravityUnconfirmed
		}
		if err := a.dir.requestStop(stopKey); err != nil {
			return err
		}
		deadline := time.Now().Add(3 * time.Second)
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			st, err = a.dir.statusWithOutputLimit(antigravityStdoutLimit)
			if err != nil {
				return errAntigravityRecords
			}
			if st.Phase != Running {
				break
			}
			if time.Now().After(deadline) {
				return errAntigravityUnconfirmed
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	hold, hasHold, err := turnDir.AntigravityACPHold()
	if err != nil {
		return errAntigravityRecords
	}
	if hasHold {
		if hold.Turn != turn {
			return errAntigravityRecords
		}
		if err := a.dir.rememberAntigravitySession(hold.SessionID); err != nil {
			return err
		}
	}
	b, _ := json.Marshal(struct {
		Version int    `json:"version"`
		Turn    string `json:"turn"`
	}{1, turn})
	if created, err := turnDir.publishAntigravityOnce(antigravityClearedName, b); err != nil || !created {
		return errAntigravityRecords
	}
	return nil
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
