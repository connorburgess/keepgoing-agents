package runner

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"

	"github.com/connorburgess/keepgoing-agents/internal/decide"
)

// This protocol foundation is intentionally not a registered adapter. In
// particular, headless SUCCESS does not prove that requested tools were allowed:
// Antigravity can soft-deny permissions while returning a successful response.

const antigravityProtocolLimit = 4 * 1024 * 1024

// EncodeAntigravityInput encodes one prompt for --input-format stream-json.
// The caller must close stdin after this envelope and use --output-format
// stream-json. No prompt belongs in argv, and -p must not accompany this mode.
func EncodeAntigravityInput(prompt string) ([]byte, error) {
	if !utf8.ValidString(prompt) || len(prompt) > antigravityProtocolLimit {
		return nil, errors.New("antigravity input exceeds the supported text format")
	}
	v := struct {
		Event   string `json:"event"`
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}{Event: "user"}
	v.Message.Content = prompt
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if len(b)+1 > antigravityProtocolLimit {
		return nil, errors.New("antigravity input exceeds the supported text format")
	}
	return append(b, '\n'), nil
}

// AntigravityOutcome retains only lifecycle metadata, never response/error prose,
// model-produced structured_output, tool arguments or tool output.
type AntigravityOutcome struct {
	ConversationID string
	HasResult      bool
	// Status is a documented terminal token, or empty for an unknown token.
	Status string
}

// TerminalObservation interprets an exited turn, not a live process. Incomplete
// or unavailable exit evidence stays unknown. SUCCESS means the provider returned
// a response; it does not establish task completion or permission success.
func (o AntigravityOutcome) TerminalObservation(exitKnown bool, exitCode int) decide.Observation {
	unknown := decide.Observation{State: decide.Unknown, ErrorClass: decide.ErrUnknown}
	if !exitKnown || exitCode < 0 || !o.HasResult || !sessionIDPattern.MatchString(o.ConversationID) {
		return unknown
	}
	switch o.Status {
	case "SUCCESS":
		if exitCode == 0 {
			return decide.Observation{State: decide.Idle, ErrorClass: decide.ErrNone}
		}
		return decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown}
	case "WAITING":
		return decide.Observation{State: decide.Pending, ErrorClass: decide.ErrNone, AwaitingInput: true}
	case "ERROR", "CANCELED", "INTERRUPTED", "INVALID":
		return decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown}
	default: // RUNNING after exit and unrecognized statuses are not live evidence.
		return unknown
	}
}

// ParseAntigravityEvents parses the documented one-turn stream-json protocol.
// expectedConversation binds a resumed turn to its saved ID; empty means a new
// conversation. A valid init without a result preserves the ID but is incomplete.
// Malformed, conflicting or unsupported evidence returns no usable outcome.
// The limit bounds parser work; a future adapter must also bound stdout reads.
func ParseAntigravityEvents(stdout []byte, expectedConversation string) (AntigravityOutcome, error) {
	invalid := func() (AntigravityOutcome, error) {
		return AntigravityOutcome{}, errors.New("antigravity lifecycle evidence is unavailable or unsupported")
	}
	if len(stdout) > antigravityProtocolLimit || !utf8.Valid(stdout) ||
		(expectedConversation != "" && !sessionIDPattern.MatchString(expectedConversation)) {
		return invalid()
	}
	var out AntigravityOutcome
	sc := bufio.NewScanner(bytes.NewReader(stdout))
	sc.Buffer(make([]byte, 4096), antigravityProtocolLimit+1)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		if out.HasResult {
			return invalid()
		}
		obj, err := antigravityObject(line)
		if err != nil {
			return invalid()
		}
		event, ok := antigravityString(obj, "event")
		if !ok {
			return invalid()
		}
		switch event {
		case "init":
			if out.ConversationID != "" {
				return invalid()
			}
			id, ok := antigravityString(obj, "conversation_id")
			if !ok || !sessionIDPattern.MatchString(id) || (expectedConversation != "" && id != expectedConversation) {
				return invalid()
			}
			if _, err := antigravityObject(obj["init"]); err != nil {
				return invalid()
			}
			out.ConversationID = id
		case "step_update", "result":
			if out.ConversationID == "" {
				return invalid()
			}
			payload, err := antigravityObject(obj[event])
			if err != nil {
				return invalid()
			}
			id, ok := antigravityString(payload, "conversation_id")
			if !ok || id != out.ConversationID {
				return invalid()
			}
			if event == "result" {
				status, ok := antigravityString(payload, "status")
				if !ok || status == "" {
					return invalid()
				}
				out.HasResult = true
				switch status {
				case "SUCCESS", "ERROR", "CANCELED", "INTERRUPTED", "INVALID", "WAITING", "RUNNING":
					out.Status = status
				}
			}
		default:
			return invalid()
		}
	}
	if sc.Err() != nil {
		return invalid()
	}
	return out, nil
}

// antigravityObject rejects duplicate keys instead of accepting a last-wins
// lifecycle field. Values remain raw JSON, so strings containing JSON are text.
func antigravityObject(b []byte) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(b))
	tok, err := d.Token()
	if err != nil || tok != json.Delim('{') {
		return nil, errors.New("expected an object")
	}
	obj := make(map[string]json.RawMessage)
	for d.More() {
		tok, err := d.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, errors.New("expected a key")
		}
		if _, exists := obj[key]; exists {
			return nil, errors.New("duplicate key")
		}
		var raw json.RawMessage
		if err := d.Decode(&raw); err != nil {
			return nil, err
		}
		obj[key] = raw
	}
	if _, err := d.Token(); err != nil {
		return nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, errors.New("trailing data")
	}
	return obj, nil
}

func antigravityString(obj map[string]json.RawMessage, key string) (string, bool) {
	var s string
	b := obj[key]
	// json.Unmarshal(null, &string) succeeds; null is not a typed string.
	if len(b) == 0 || b[0] != '"' || json.Unmarshal(b, &s) != nil {
		return "", false
	}
	return s, true
}
