package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/evidence"
	"strings"
	"time"
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
	// ConnectionLost means the stream's last connection record for this
	// session said the CLI was reconnecting. It is diagnostic evidence from a
	// structured record seen in one inspected CLI version. It never changes
	// the error class, which stays unknown.
	ConnectionLost bool
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
			// Connection records are not part of the published schema. Read only
			// their subtype, only for the initialized session, and never let one
			// invalidate the turn.
			if typ == "connection" && initialized && cursorString(fields, "session_id", &session) && session == out.SessionID && cursorString(fields, "subtype", &subtype) {
				switch subtype {
				case "reconnecting":
					out.ConnectionLost = true
				case "reconnected":
					out.ConnectionLost = false
				}
			}
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
		out.ConnectionLost = false
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
	if out.ConnectionLost {
		// Diagnostic only: the class below stays unknown, so this can never
		// make a failure eligible for failover.
		detail.Category = "connection_error"
		detail = evidence.Clean(detail)
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
		if err := c.freshStartAllowed(ctx, st); err != nil {
			return err
		}
	}
	return c.dir.Start(c.args(session), c.cfg.Workdir, prompt, envList(c.cfg.Env))
}

// CursorInspectedVersions lists the CLI versions whose source was read to
// confirm that the stream's initialization and user records are written before
// the prompt is submitted, and on which that was then seen in a live run.
var CursorInspectedVersions = map[string]bool{"2026.09.18-9a7762b": true}

var errCursorNoSession = errors.New("cursor: previous turn has no verified session identity; refusing to start a new conversation")

// freshStartAllowed decides whether a turn that left no session may be followed
// by a new conversation. That is allowed for one observed shape only: the turn
// finished with exit code 1, its output file exists and is empty, and the CLI
// is a version known to write its first records before it submits the prompt.
// A live untrusted-workspace refusal had this shape.
//
// The evidence is keepgoing's own record of the turn, not a statement from
// Cursor that no conversation began: the CLI offers none. A wrapper that
// discards output and exits 1 is indistinguishable, which is why the shape is
// this narrow. Any other exit code (including signal-style codes), any output,
// a missing output file, a crashed wrapper or an unverified version keeps the
// refusal, and the loop ends in its ordinary hold for a person.
func (c *Cursor) freshStartAllowed(ctx context.Context, st Status) error {
	if st.Phase != Finished || !st.ExitCodeKnown || st.ExitCode != 1 || len(st.Stdout) != 0 {
		return errCursorNoSession
	}
	if info, err := os.Lstat(c.dir.path("stdout")); err != nil || !info.Mode().IsRegular() || info.Size() != 0 {
		return errCursorNoSession
	}
	version := c.version(ctx)
	if CursorInspectedVersions[version] {
		return nil
	}
	for _, v := range c.cfg.FreshStartVersions {
		if version != "" && v == version {
			return nil
		}
	}
	return errors.New("cursor: the previous turn exited with an error before writing any output, but the installed CLI is not a version whose start order has been verified, so a new conversation is refused; see docs/cursor.md before listing the version in cursor.fresh_start_versions")
}

// version runs only the CLI's version command, bounded in time and output, and
// returns the token when it has exactly the expected shape.
func (c *Cursor) version(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.cfg.CLI, "--version")
	cmd.Dir = c.cfg.Workdir
	cmd.Env = append(os.Environ(), envList(c.cfg.Env)...)
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil || len(out) > 128 {
		return ""
	}
	v := strings.TrimSpace(string(out))
	if !config.CursorVersionPattern.MatchString(v) {
		return ""
	}
	return v
}

func (c *Cursor) Stop(ctx context.Context) error {
	st, err := c.dir.statusWithOutputLimit(cursorMaxOutput)
	if err != nil || st.Phase != Running {
		return err
	}
	return c.dir.requestStop(st.Record.StopKey)
}
