package runner

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	antigravityACPFrameLimit      = 1 << 20
	antigravityACPTotalLimit      = 4 << 20
	antigravityACPFrameCount      = 4096
	antigravityACPPermissionLimit = 64
)

// AntigravityACPOutcome is protocol evidence only. It grants no permission,
// enables no unattended mode, and does not establish task completion. NeedsUser
// is latched even if cancellation later returns end_turn, fails, or disconnects.
type AntigravityACPOutcome = ACPOutcome

// ACPOutcome is what one ACP prompt exchange established, from typed protocol
// fields only. It never holds response text or a permission's description.
type ACPOutcome struct {
	SessionID    string
	NeedsUser    bool
	TurnReturned bool
	Failed       bool
	// StopReason is the prompt response's typed stop reason, when it was one
	// the protocol defines; "" otherwise.
	StopReason string
	// ErrorCode is the JSON-RPC error code the agent answered the prompt
	// with, when it answered with an error.
	ErrorCode *int
}

// ACPOptions are what differs between agents that speak ACP.
type ACPOptions struct {
	// RequireResume refuses an agent that does not advertise session
	// resume, even for a new session.
	RequireResume bool
}

// AntigravityACPGate is the gate with the options the Antigravity groundwork
// was written for.
type AntigravityACPGate = ACPGate

// AntigravityACPGate handles one ACP v1 prompt on one connection. It has no I/O,
// storage, adapter registration or automatic-loop eligibility. A future worker
// must persist interaction holds and implement typed question handoff before
// unattended operation; this gate deliberately advertises neither elicitation
// nor filesystem/terminal execution capabilities. It is not safe for concurrent
// use. Create a new gate for an explicitly authorized subsequent turn.
type ACPGate struct {
	phase         string
	saved         string
	opts          ACPOptions
	out           ACPOutcome
	bytes, frames int
	permissions   map[string]bool
	// resume is the method that continues a saved session, as negotiated:
	// session/resume, session/load, or "" when the agent offers neither.
	resume string
}

// NewAntigravityACPGate accepts an exact opaque saved session ID, or empty for
// an explicitly new session. It never falls back from resume to session/new.
func NewAntigravityACPGate(saved string) (*AntigravityACPGate, error) {
	return NewACPGate(saved, ACPOptions{RequireResume: true})
}

// NewACPGate accepts an exact opaque saved session ID, or empty for an
// explicitly new session. It never falls back from continuing a session to
// starting a new one.
func NewACPGate(saved string, opts ACPOptions) (*ACPGate, error) {
	if saved != "" && !acpIdentifier(saved) {
		return nil, errors.New("acp: invalid saved session identity")
	}
	return &ACPGate{phase: "new", saved: saved, opts: opts, permissions: map[string]bool{}}, nil
}

func (g *AntigravityACPGate) fail() error {
	g.out.Failed, g.out.TurnReturned = true, false
	g.phase = "failed"
	return errors.New("acp: unsupported or invalid protocol evidence")
}

// Outcome returns a copy, never response text or permission descriptions.
func (g *AntigravityACPGate) Outcome() AntigravityACPOutcome { return g.out }

// End marks EOF/timeout. Incomplete evidence cannot become a returned turn.
func (g *AntigravityACPGate) End() AntigravityACPOutcome {
	if g.phase != "done" {
		_ = g.fail()
	}
	return g.out
}

// Initialize encodes a truthful, minimal ACP v1 client handshake.
func (g *AntigravityACPGate) Initialize() ([]byte, error) {
	if g.phase != "new" {
		return nil, g.fail()
	}
	b, err := acpRequest("keepgoing-init", "initialize", map[string]any{
		"protocolVersion": 1, "clientCapabilities": map[string]any{},
		"clientInfo": map[string]string{"name": "keepgoing", "version": "unreleased"},
	})
	if err != nil {
		return nil, g.fail()
	}
	g.phase = "initializing"
	return b, nil
}

// OpenSession requires negotiated exact-resume support even for a fresh session.
// cwd must be absolute; it is not a sandbox or ownership guarantee.
func (g *AntigravityACPGate) OpenSession(cwd string) ([]byte, error) {
	if g.phase != "initialized" {
		return nil, g.fail()
	}
	// A saved session is continued only by a method the agent advertised.
	if g.saved != "" && g.resume == "" {
		return nil, g.fail()
	}
	b, err := acpOpenSessionFrameWith(g.saved, cwd, g.resume)
	if err != nil {
		return nil, g.fail()
	}
	g.phase = "opening"
	return b, nil
}

func acpOpenSessionFrame(saved, cwd string) ([]byte, error) {
	return acpOpenSessionFrameWith(saved, cwd, "session/resume")
}

func acpOpenSessionFrameWith(saved, cwd, resume string) ([]byte, error) {
	if len(cwd) > antigravityACPFrameLimit || !filepath.IsAbs(cwd) || !utf8.ValidString(cwd) || strings.ContainsRune(cwd, 0) {
		return nil, errors.New("acp: invalid working directory")
	}
	method := "session/new"
	params := map[string]any{"cwd": cwd, "mcpServers": []any{}}
	if saved != "" {
		method = resume
		params["sessionId"] = saved
	}
	return acpRequest("keepgoing-open", method, params)
}

// Prompt encodes private stdin data for a future worker. It allows only one
// prompt per gate, after session setup has returned with a verified identity.
func (g *AntigravityACPGate) Prompt(text string) ([]byte, error) {
	if g.phase != "ready" {
		return nil, g.fail()
	}
	b, err := acpPromptFrame(g.out.SessionID, text)
	if err != nil {
		return nil, g.fail()
	}
	g.phase = "prompting"
	return b, nil
}

func acpPromptFrame(session, text string) ([]byte, error) {
	if !utf8.ValidString(text) || len(text) > antigravityACPFrameLimit {
		return nil, errors.New("acp: invalid prompt")
	}
	return acpRequest("keepgoing-prompt", "session/prompt", map[string]any{
		"sessionId": session, "prompt": []any{map[string]string{"type": "text", "text": text}},
	})
}

// Receive consumes one complete JSON-RPC frame, not arbitrary stream chunks.
// A future transport must bound reads before calling it. Returned frames are
// cancellation only; no path selects a permission option. The first permission
// emits session/cancel before its cancelled reply. Subsequent pending requests
// receive cancelled replies while the matching prompt response is drained.
func (g *AntigravityACPGate) Receive(frame []byte) ([][]byte, error) {
	if g.phase == "new" || g.phase == "failed" || g.phase == "done" || len(frame) > antigravityACPFrameLimit || !utf8.Valid(frame) || g.frames >= antigravityACPFrameCount || len(frame) > antigravityACPTotalLimit-g.bytes {
		return nil, g.fail()
	}
	g.frames++
	g.bytes += len(frame)
	fields, err := cursorObject(frame)
	if err != nil || !acpStringEquals(fields, "jsonrpc", "2.0") {
		return nil, g.fail()
	}
	_, method := fields["method"]
	_, result := fields["result"]
	_, rpcError := fields["error"]
	_, params := fields["params"]
	id, hasID := fields["id"]
	if method {
		if result || rpcError || !params {
			return nil, g.fail()
		}
		return g.receiveMethod(fields, id, hasID)
	}
	if !hasID || result == rpcError || params {
		return nil, g.fail()
	}
	var request string
	if json.Unmarshal(id, &request) != nil {
		return nil, g.fail()
	}
	want := map[string]string{"initializing": "keepgoing-init", "opening": "keepgoing-open", "prompting": "keepgoing-prompt", "cancelling": "keepgoing-prompt"}[g.phase]
	if want == "" || request != want {
		return nil, g.fail()
	}
	if rpcError {
		// An error answer to the prompt is still a failed turn; its typed
		// code is kept as evidence, its message never.
		if g.phase == "prompting" || g.phase == "cancelling" {
			if e, err := cursorObject(fields["error"]); err == nil {
				var code int
				if json.Unmarshal(e["code"], &code) == nil {
					g.out.ErrorCode = &code
				}
			}
		}
		return nil, g.fail()
	}
	// session/load answers with null once the history has been replayed.
	if g.phase == "opening" && g.saved != "" && g.resume == "session/load" && string(fields["result"]) == "null" {
		g.out.SessionID, g.phase = g.saved, "ready"
		return nil, nil
	}
	payload, err := cursorObject(fields["result"])
	if err != nil {
		return nil, g.fail()
	}
	switch g.phase {
	case "initializing":
		var version int
		if json.Unmarshal(payload["protocolVersion"], &version) != nil || version != 1 {
			return nil, g.fail()
		}
		capabilities, err := cursorObject(payload["agentCapabilities"])
		if err != nil {
			return nil, g.fail()
		}
		// How a saved session is continued, from what the agent advertises:
		// session/resume when it has that capability, else session/load.
		if sessions, err := cursorObject(capabilities["sessionCapabilities"]); err == nil {
			if _, err = cursorObject(sessions["resume"]); err == nil {
				g.resume = "session/resume"
			}
		}
		if g.resume == "" && !g.opts.RequireResume && string(capabilities["loadSession"]) == "true" {
			g.resume = "session/load"
		}
		if g.resume == "" && g.opts.RequireResume {
			return nil, g.fail()
		}
		g.phase = "initialized"
	case "opening":
		session := g.saved
		if g.saved == "" {
			if !acpString(payload, "sessionId", &session) || !acpIdentifier(session) {
				return nil, g.fail()
			}
		} else if _, ok := payload["sessionId"]; ok && !acpStringEquals(payload, "sessionId", session) {
			return nil, g.fail()
		}
		g.out.SessionID, g.phase = session, "ready"
	case "prompting", "cancelling":
		var reason string
		if !acpString(payload, "stopReason", &reason) {
			return nil, g.fail()
		}
		switch reason {
		case "end_turn", "max_tokens", "max_turn_requests", "refusal", "cancelled":
			g.out.StopReason = reason
			g.out.TurnReturned = reason == "end_turn" && !g.out.NeedsUser
			g.phase = "done"
		default:
			return nil, g.fail()
		}
	}
	return nil, nil
}

func (g *AntigravityACPGate) receiveMethod(fields map[string]json.RawMessage, id json.RawMessage, hasID bool) ([][]byte, error) {
	var method string
	// session/load replays the saved conversation as updates before it
	// answers, so updates for that session are expected while opening.
	replaying := g.phase == "opening" && g.saved != "" && g.resume == "session/load"
	if !acpString(fields, "method", &method) || (g.phase != "ready" && g.phase != "prompting" && g.phase != "cancelling" && !replaying) {
		return nil, g.fail()
	}
	session := g.out.SessionID
	if replaying {
		if method != "session/update" {
			return nil, g.fail()
		}
		session = g.saved
	}
	params, err := cursorObject(fields["params"])
	if err != nil || !acpStringEquals(params, "sessionId", session) {
		return nil, g.fail()
	}
	switch method {
	case "session/update":
		if hasID {
			return nil, g.fail()
		}
		update, err := cursorObject(params["update"])
		if err != nil {
			return nil, g.fail()
		}
		var kind string
		if !acpString(update, "sessionUpdate", &kind) {
			return nil, g.fail()
		}
		// These updates cannot authorize completion or permission clearance. Content,
		// titles and tool output remain opaque, including typed-looking model text.
		switch kind {
		case "user_message_chunk", "agent_message_chunk", "agent_thought_chunk", "tool_call", "tool_call_update", "plan", "available_commands_update", "current_mode_update", "config_option_update", "session_info_update", "usage_update":
			return nil, nil
		default:
			return nil, g.fail()
		}
	case "session/request_permission":
		if g.phase != "prompting" && g.phase != "cancelling" {
			return nil, g.fail()
		}
		key, valid := acpRequestID(id)
		if !hasID || !valid || g.permissions[key] || len(g.permissions) >= antigravityACPPermissionLimit {
			return nil, g.fail()
		}
		tool, err := cursorObject(params["toolCall"])
		var toolID string
		if err != nil || !acpString(tool, "toolCallId", &toolID) || !acpIdentifier(toolID) || !acpPermissionOptions(params["options"]) {
			return nil, g.fail()
		}
		g.permissions[key], g.out.NeedsUser, g.out.TurnReturned = true, true, false
		var frames [][]byte
		if g.phase != "cancelling" {
			cancel, _ := acpRequest("", "session/cancel", map[string]string{"sessionId": g.out.SessionID})
			frames = append(frames, cancel)
		}
		reply, _ := acpEncode(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"outcome": map[string]string{"outcome": "cancelled"}}})
		frames = append(frames, reply)
		g.phase = "cancelling"
		return frames, nil
	default:
		// Includes unadvertised elicitation, filesystem and terminal requests. Do
		// not fabricate answers, execute actions, or infer intent from their text.
		return nil, g.fail()
	}
}

func acpPermissionOptions(raw json.RawMessage) bool {
	var options []json.RawMessage
	if json.Unmarshal(raw, &options) != nil || len(options) == 0 || len(options) > 16 {
		return false
	}
	seen := map[string]bool{}
	for _, rawOption := range options {
		option, err := cursorObject(rawOption)
		var id, name, kind string
		if err != nil || !acpString(option, "optionId", &id) || !acpIdentifier(id) || seen[id] || !acpString(option, "name", &name) || !acpString(option, "kind", &kind) {
			return false
		}
		switch kind {
		case "allow_once", "allow_always", "reject_once", "reject_always":
		default:
			return false
		}
		seen[id] = true
	}
	return true
}

func acpIdentifier(s string) bool {
	if s == "" || len(s) > 256 || !utf8.ValidString(s) || strings.TrimSpace(s) == "" {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func acpRequestID(raw json.RawMessage) (string, bool) {
	var s string
	if acpString(map[string]json.RawMessage{"id": raw}, "id", &s) {
		return "s:" + s, acpIdentifier(s)
	}
	n, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil {
		return "", false
	}
	return "n:" + strconv.FormatInt(n, 10), true
}

// encoding/json replaces unpaired UTF-16 surrogates. Reject them in control
// strings so two distinct wire identities cannot silently become the same ID.
func acpString(fields map[string]json.RawMessage, key string, value *string) bool {
	raw, ok := fields[key]
	if !ok || len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, value) != nil {
		return false
	}
	for i := 1; i < len(raw)-1; i++ {
		if raw[i] != '\\' {
			continue
		}
		i++
		if raw[i] != 'u' {
			continue
		}
		n, err := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		if err != nil {
			return false
		}
		i += 4
		if n >= 0xdc00 && n <= 0xdfff {
			return false
		}
		if n >= 0xd800 && n <= 0xdbff {
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return false
			}
			low, err := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}

func acpStringEquals(fields map[string]json.RawMessage, key, expected string) bool {
	var s string
	return acpString(fields, key, &s) && s == expected
}

func acpRequest(id, method string, params any) ([]byte, error) {
	frame := map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
	if id != "" {
		frame["id"] = id
	}
	return acpEncode(frame)
}

func acpEncode(frame any) ([]byte, error) {
	b, err := json.Marshal(frame)
	if err != nil || len(b)+1 > antigravityACPFrameLimit {
		return nil, errors.New("acp: encoded frame exceeds limit")
	}
	return append(b, '\n'), nil
}
