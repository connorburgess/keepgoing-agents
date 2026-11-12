package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/evidence"
)

// Cursor runs one print-mode turn per send. Only provider lifecycle fields and
// the owned wrapper's exit status establish completion, never response text.
// The documented print interface has no structured failure taxonomy; failures
// remain unknown and cannot trigger failover.
type Cursor struct {
	cfg config.Cursor
	dir Dir
}

func NewCursor(cfg config.Cursor, dir string) *Cursor { return &Cursor{cfg: cfg, dir: Dir(dir)} }
func (c *Cursor) Name() string                        { return "cursor" }

// CursorOutcome deliberately excludes message, tool and error prose.
type CursorOutcome struct {
	SessionID     string
	Success       bool
	UnsafeSession bool
}

const cursorMaxOutput = 8 << 20
const cursorMaxEvent = 2 << 20

// ParseCursorEvents accepts the documented stream-json initialization and
// terminal success pair. A stream ending after a complete initialization can
// retain that session for a retry, but never establishes success. Conflicting
// identities prevent resumption into a different conversation.
func ParseCursorEvents(stdout []byte) CursorOutcome {
	var out CursorOutcome
	if len(stdout) > cursorMaxOutput {
		return CursorOutcome{UnsafeSession: true}
	}
	initialized, terminal, malformed := false, false, false
	for _, line := range bytes.Split(bytes.TrimSpace(stdout), []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if len(line) > cursorMaxEvent {
			return CursorOutcome{UnsafeSession: true}
		}
		fields, err := cursorObject(line)
		if err != nil {
			malformed = true
			out.UnsafeSession = true
			continue
		}
		var typ, subtype, session string
		if !cursorString(fields, "type", &typ) {
			malformed = true
			continue
		}
		if terminal {
			malformed = true
		}
		lifecycle := typ == "result"
		if typ == "system" {
			lifecycle = cursorString(fields, "subtype", &subtype) && subtype == "init"
		}
		documented := lifecycle || typ == "user" || typ == "assistant" || typ == "tool_call"
		if !documented {
			continue
		}
		if !cursorString(fields, "session_id", &session) || !sessionIDPattern.MatchString(session) || (initialized && out.SessionID != session) {
			out.UnsafeSession = true
			malformed = true
			continue
		}
		if typ == "system" {
			if initialized || terminal {
				malformed = true
				out.UnsafeSession = true
				continue
			}
			out.SessionID = session
			initialized = true
		} else {
			if !initialized {
				malformed = true
				out.UnsafeSession = true
				continue
			}
			if typ == "result" {
				var isError *bool
				booleanOK := json.Unmarshal(fields["is_error"], &isError) == nil && isError != nil
				out.Success = !terminal && cursorString(fields, "subtype", &subtype) && subtype == "success" && booleanOK && !*isError
				terminal = true
			}
		}
	}
	out.Success = out.Success && !malformed && !out.UnsafeSession
	if out.UnsafeSession {
		out.SessionID = ""
	}
	return out
}

// cursorObject rejects duplicate top-level keys instead of accepting JSON's
// last-key-wins ambiguity. Nested payloads are retained as raw JSON and never
// interpreted as provider lifecycle records.
func cursorObject(line []byte) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(line))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("invalid event")
	}
	fields := map[string]json.RawMessage{}
	for d.More() {
		token, err = d.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, errors.New("invalid event key")
		}
		if _, exists := fields[key]; exists {
			return nil, errors.New("duplicate event key")
		}
		var value json.RawMessage
		if err = d.Decode(&value); err != nil {
			return nil, err
		}
		fields[key] = value
	}
	if _, err = d.Token(); err != nil {
		return nil, err
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, errors.New("trailing event data")
	}
	return fields, nil
}

func cursorString(fields map[string]json.RawMessage, key string, value *string) bool {
	raw, ok := fields[key]
	return ok && len(raw) > 0 && raw[0] == '"' && json.Unmarshal(raw, value) == nil
}

// remember checks existing metadata before saving. Do not silently replace a
// saved identity or ignore a persistence failure before the next send.
func (c *Cursor) remember(out CursorOutcome) (string, error) {
	if out.UnsafeSession {
		return "", errors.New("cursor: conflicting or invalid session identity; inspect the private turn records")
	}
	saved, err := c.dir.InspectSession()
	if err != nil {
		return "", errors.New("cursor: saved session identity is unavailable")
	}
	if out.SessionID == "" {
		return saved, nil
	}
	if saved != "" && saved != out.SessionID {
		return "", errors.New("cursor: returned session differs from the saved session")
	}
	if saved == "" {
		if err := c.dir.SaveSession(out.SessionID); err != nil {
			return "", errors.New("cursor: could not save session identity")
		}
	}
	return out.SessionID, nil
}

func (c *Cursor) Observe(ctx context.Context) (decide.Observation, error) {
	st, err := c.dir.statusWithOutputLimit(cursorMaxOutput)
	if err != nil {
		return decide.Observation{State: decide.Unknown, ErrorClass: decide.ErrUnknown, Evidence: runnerEvidence(st)}, err
	}
	detail := runnerEvidence(st)
	switch st.Phase {
	case NeverRun:
		return decide.Observation{State: decide.Idle, ErrorClass: decide.ErrNone, Evidence: detail}, nil
	case Running:
		return decide.Observation{State: decide.Active, ErrorClass: decide.ErrNone, Evidence: detail}, nil
	}
	out := ParseCursorEvents(st.Stdout)
	if _, err := c.remember(out); err != nil {
		return decide.Observation{State: decide.Unknown, ErrorClass: decide.ErrUnknown, Evidence: detail}, err
	}
	detail.Source = "cursor_events"
	detail = evidence.Clean(detail)
	if st.Phase == Finished && st.ExitCodeKnown && st.ExitCode == 0 && out.Success {
		return decide.Observation{State: decide.Idle, ErrorClass: decide.ErrNone, Evidence: detail}, nil
	}
	return decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown, Evidence: detail}, nil
}

func (c *Cursor) args(session string) []string {
	argv := []string{c.cfg.CLI, "-p", "--output-format", "stream-json"}
	if c.cfg.Model != "" {
		argv = append(argv, "--model", c.cfg.Model)
	}
	if session != "" {
		argv = append(argv, "--resume="+session)
	}
	return argv
}

// Send passes task text only through private stdin. It never requests automatic
// approval, changes workspace trust, or resumes a provider's most recent chat.
func (c *Cursor) Send(ctx context.Context, prompt string, mode adapter.Mode) error {
	st, err := c.dir.statusWithOutputLimit(cursorMaxOutput)
	if err != nil {
		return err
	}
	if st.Phase == Running {
		return adapter.ErrUnsupported{Op: fmt.Sprintf("sending to a running headless turn (%s mode)", mode)}
	}
	var out CursorOutcome
	if st.Phase != NeverRun {
		out = ParseCursorEvents(st.Stdout)
	}
	session, err := c.remember(out)
	if err != nil {
		return err
	}
	if st.Phase != NeverRun && session == "" {
		return errors.New("cursor: previous turn has no verified session identity; refusing to start a new conversation")
	}
	return c.dir.Start(c.args(session), c.cfg.Workdir, prompt, envList(c.cfg.Env))
}

func (c *Cursor) Stop(ctx context.Context) error {
	st, err := c.dir.statusWithOutputLimit(cursorMaxOutput)
	if err != nil || st.Phase != Running {
		return err
	}
	return c.dir.requestStop(st.Record.StopKey)
}
