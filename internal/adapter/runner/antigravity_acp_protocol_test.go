package runner

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

const acpFixtureSession = "session-café-项目/opaque"
const acpInitResponse = `{"jsonrpc":"2.0","id":"keepgoing-init","result":{"protocolVersion":1,"agentCapabilities":{"sessionCapabilities":{"resume":{}}}}}`

func acpTestReceive(t *testing.T, g *AntigravityACPGate, frame string) [][]byte {
	t.Helper()
	out, err := g.Receive([]byte(frame))
	if err != nil {
		t.Fatalf("receive %s: %v", frame, err)
	}
	return out
}

func acpTestGate(t *testing.T, saved string) *AntigravityACPGate {
	t.Helper()
	g, err := NewAntigravityACPGate(saved)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Initialize(); err != nil {
		t.Fatal(err)
	}
	acpTestReceive(t, g, acpInitResponse)
	if _, err := g.OpenSession(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	result := `{"sessionId":"` + acpFixtureSession + `"}`
	if saved != "" {
		result = `{}`
	}
	acpTestReceive(t, g, `{"jsonrpc":"2.0","id":"keepgoing-open","result":`+result+`}`)
	if _, err := g.Prompt("fictional task"); err != nil {
		t.Fatal(err)
	}
	return g
}

func acpPermission(id, session string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"method":"session/request_permission","params":{"sessionId":%q,"toolCall":{"toolCallId":"call-example","title":"fictional command"},"options":[{"optionId":"allow","name":"Allow once","kind":"allow_once"},{"optionId":"deny","name":"Deny","kind":"reject_once"}]}}`, id, session)
}

func acpResult(reason string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":"keepgoing-prompt","result":{"stopReason":%q}}`, reason)
}

func TestAntigravityACPHandshakeAndExactSession(t *testing.T) {
	for _, saved := range []string{"", acpFixtureSession} {
		t.Run(fmt.Sprintf("saved=%t", saved != ""), func(t *testing.T) {
			g, err := NewAntigravityACPGate(saved)
			if err != nil {
				t.Fatal(err)
			}
			frame, err := g.Initialize()
			if err != nil {
				t.Fatal(err)
			}
			var init struct {
				Method string
				Params struct {
					ProtocolVersion    int
					ClientCapabilities map[string]any
					ClientInfo         map[string]string
				}
			}
			if err := json.Unmarshal(frame, &init); err != nil {
				t.Fatal(err)
			}
			if init.Method != "initialize" || init.Params.ProtocolVersion != 1 || len(init.Params.ClientCapabilities) != 0 || init.Params.ClientInfo["name"] != "keepgoing" {
				t.Fatalf("misleading handshake: %s", frame)
			}
			acpTestReceive(t, g, acpInitResponse)
			cwd := filepath.Join(t.TempDir(), "café-项目")
			frame, err = g.OpenSession(cwd)
			if err != nil {
				t.Fatal(err)
			}
			var open struct {
				Method string
				Params struct {
					Cwd        string
					SessionID  string
					MCPServers []any
				}
			}
			if err := json.Unmarshal(frame, &open); err != nil {
				t.Fatal(err)
			}
			wantMethod := "session/new"
			result := `{"sessionId":"` + acpFixtureSession + `"}`
			if saved != "" {
				wantMethod = "session/resume"
				result = `{}`
			}
			if open.Method != wantMethod || open.Params.Cwd != cwd || open.Params.SessionID != saved || open.Params.MCPServers == nil || len(open.Params.MCPServers) != 0 {
				t.Fatalf("setup: %s", frame)
			}
			acpTestReceive(t, g, `{"jsonrpc":"2.0","id":"keepgoing-open","result":`+result+`}`)
			text := "fictional café\n项目 task\x00"
			frame, err = g.Prompt(text)
			if err != nil {
				t.Fatal(err)
			}
			var prompt struct {
				Method string
				Params struct {
					SessionID string
					Prompt    []struct{ Type, Text string }
				}
			}
			if err := json.Unmarshal(frame, &prompt); err != nil {
				t.Fatal(err)
			}
			if prompt.Method != "session/prompt" || prompt.Params.SessionID != acpFixtureSession || len(prompt.Params.Prompt) != 1 || prompt.Params.Prompt[0].Text != text || prompt.Params.Prompt[0].Type != "text" {
				t.Fatalf("prompt: %s", frame)
			}
			acpTestReceive(t, g, acpResult("end_turn"))
			if got := g.End(); got.Failed || got.NeedsUser || !got.TurnReturned || got.SessionID != acpFixtureSession {
				t.Fatalf("outcome: %+v", got)
			}
			if frame, err := g.Prompt("second"); err == nil || frame != nil {
				t.Fatal("gate allowed a second turn")
			}
		})
	}
}

func TestAntigravityACPPermissionCancelsAndLatches(t *testing.T) {
	for _, terminal := range []string{"end_turn", "cancelled", "error", "EOF", "malformed"} {
		t.Run(terminal, func(t *testing.T) {
			g := acpTestGate(t, acpFixtureSession)
			for i, id := range []string{`42`, `"42"`, `"opaque-request"`} {
				frames := acpTestReceive(t, g, acpPermission(id, acpFixtureSession))
				want := 1
				if i == 0 {
					want = 2
				}
				if len(frames) != want {
					t.Fatalf("cancel frames: %q", frames)
				}
				if i == 0 {
					var cancel struct {
						Method string
						ID     json.RawMessage
						Params struct{ SessionID string }
					}
					if err := json.Unmarshal(frames[0], &cancel); err != nil {
						t.Fatal(err)
					}
					if cancel.Method != "session/cancel" || cancel.ID != nil || cancel.Params.SessionID != acpFixtureSession {
						t.Fatalf("cancel: %s", frames[0])
					}
				}
				var reply struct {
					ID     json.RawMessage
					Result struct {
						Outcome struct {
							Outcome  string
							OptionID *string
						}
					}
				}
				if err := json.Unmarshal(frames[len(frames)-1], &reply); err != nil {
					t.Fatal(err)
				}
				if string(reply.ID) != id || reply.Result.Outcome.Outcome != "cancelled" || reply.Result.Outcome.OptionID != nil {
					t.Fatalf("permission selected or wrong correlation: %s", frames[len(frames)-1])
				}
			}
			switch terminal {
			case "error":
				_, _ = g.Receive([]byte(`{"jsonrpc":"2.0","id":"keepgoing-prompt","error":{"code":-32000,"message":"fictional quota auth"}}`))
			case "EOF":
				g.End()
			case "malformed":
				_, _ = g.Receive([]byte(`{`))
			default:
				acpTestReceive(t, g, acpResult(terminal))
			}
			if got := g.Outcome(); !got.NeedsUser || got.TurnReturned {
				t.Fatalf("lost hold: %+v", got)
			}
			if frame, err := g.Prompt("retry"); err == nil || frame != nil {
				t.Fatal("interaction permitted retry")
			}
			if !g.End().NeedsUser {
				t.Fatal("EOF cleared hold")
			}
		})
	}
}

func TestAntigravityACPTextAndPendingToolAreNotPermission(t *testing.T) {
	g := acpTestGate(t, "")
	for _, kind := range []string{"agent_message_chunk", "agent_thought_chunk", "tool_call", "tool_call_update"} {
		frame, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": acpFixtureSession, "update": map[string]any{"sessionUpdate": kind, "status": "pending", "toolCallId": "call-example", "content": map[string]string{"type": "text", "text": acpPermission(`9`, acpFixtureSession)}}}})
		if frames, err := g.Receive(frame); err != nil || len(frames) != 0 || g.Outcome().NeedsUser {
			t.Fatalf("text became interaction: %q %v %+v", frames, err, g.Outcome())
		}
	}
	acpTestReceive(t, g, acpResult("end_turn"))
	if !g.Outcome().TurnReturned {
		t.Fatal("content changed terminal evidence")
	}
}

func TestAntigravityACPRejectsBadProtocol(t *testing.T) {
	cases := map[string]string{
		"wrong session":           acpPermission(`1`, "other-session"),
		"duplicate request":       acpPermission(`42`, acpFixtureSession),
		"unknown id":              `{"jsonrpc":"2.0","id":"another-prompt","result":{"stopReason":"end_turn"}}`,
		"ambiguous result":        `{"jsonrpc":"2.0","id":"keepgoing-prompt","result":{},"error":{}}`,
		"method and result":       `{"jsonrpc":"2.0","method":"session/update","params":{},"result":{}}`,
		"duplicate key":           `{"jsonrpc":"2.0","id":"wrong","id":"keepgoing-prompt","result":{"stopReason":"end_turn"}}`,
		"duplicate reason":        `{"jsonrpc":"2.0","id":"keepgoing-prompt","result":{"stopReason":"cancelled","stopReason":"end_turn"}}`,
		"null result":             `{"jsonrpc":"2.0","id":"keepgoing-prompt","result":null}`,
		"truncated":               `{"jsonrpc":`,
		"batch":                   `[]`,
		"trailing":                acpResult("end_turn") + `{}`,
		"unpaired request id":     acpPermission(`"\ud800"`, acpFixtureSession),
		"unpaired session":        strings.Replace(acpPermission(`1`, acpFixtureSession), acpFixtureSession, `\ud800`, 1),
		"null request id":         acpPermission(`null`, acpFixtureSession),
		"fraction request id":     acpPermission(`1.5`, acpFixtureSession),
		"duplicate options":       strings.Replace(acpPermission(`2`, acpFixtureSession), `"optionId":"deny"`, `"optionId":"allow"`, 1),
		"unknown option kind":     strings.Replace(acpPermission(`2`, acpFixtureSession), `"allow_once"`, `"future"`, 1),
		"missing options":         strings.Replace(acpPermission(`2`, acpFixtureSession), `"options"`, `"notOptions"`, 1),
		"wrong protocol":          strings.Replace(acpResult("end_turn"), `"2.0"`, `"1.0"`, 1),
		"unknown stop reason":     acpResult("fictional_limit"),
		"unsupported elicitation": fmt.Sprintf(`{"jsonrpc":"2.0","id":4,"method":"elicitation/create","params":{"sessionId":%q,"mode":"form","message":"fictional question"}}`, acpFixtureSession),
		"unsupported execution":   fmt.Sprintf(`{"jsonrpc":"2.0","id":4,"method":"terminal/create","params":{"sessionId":%q}}`, acpFixtureSession),
		"invalid utf8":            string([]byte{0xff}),
		"oversize":                strings.Repeat(" ", antigravityACPFrameLimit+1),
	}
	for name, frame := range cases {
		t.Run(name, func(t *testing.T) {
			g := acpTestGate(t, "")
			if name == "duplicate request" {
				acpTestReceive(t, g, acpPermission(`42`, acpFixtureSession))
			}
			if out, err := g.Receive([]byte(frame)); err == nil || len(out) != 0 {
				t.Fatalf("accepted invalid evidence: %q %v", out, err)
			}
			if got := g.Outcome(); !got.Failed || got.TurnReturned {
				t.Fatalf("failure not closed: %+v", got)
			}
			if out, err := g.Prompt("retry"); err == nil || out != nil {
				t.Fatal("failure allowed prompt")
			}
			if out, err := g.OpenSession(t.TempDir()); err == nil || out != nil {
				t.Fatal("failure allowed fresh session")
			}
		})
	}
}

func TestAntigravityACPSetupGates(t *testing.T) {
	for name, frame := range map[string]string{
		"version":           strings.Replace(acpInitResponse, `"protocolVersion":1`, `"protocolVersion":2`, 1),
		"missing resume":    strings.Replace(acpInitResponse, `"resume":{}`, `"loadSession":true`, 1),
		"null resume":       strings.Replace(acpInitResponse, `"resume":{}`, `"resume":null`, 1),
		"wrong response id": strings.Replace(acpInitResponse, `"keepgoing-init"`, `"wrong"`, 1),
		"auth error":        `{"jsonrpc":"2.0","id":"keepgoing-init","error":{"code":-32000,"message":"Authentication required"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			g, _ := NewAntigravityACPGate(acpFixtureSession)
			_, _ = g.Initialize()
			if _, err := g.Receive([]byte(frame)); err == nil {
				t.Fatal("invalid setup accepted")
			}
			if b, err := g.OpenSession(t.TempDir()); err == nil || b != nil {
				t.Fatal("setup failure allowed session")
			}
		})
	}
	for _, id := range []string{" ", "session\nother", strings.Repeat("a", 257), string([]byte{0xff})} {
		if _, err := NewAntigravityACPGate(id); err == nil {
			t.Fatalf("invalid saved ID %q", id)
		}
	}
	for _, cwd := range []string{"", "relative/path"} {
		g, _ := NewAntigravityACPGate("")
		_, _ = g.Initialize()
		acpTestReceive(t, g, acpInitResponse)
		if b, err := g.OpenSession(cwd); err == nil || b != nil {
			t.Fatalf("accepted relative cwd %q", cwd)
		}
	}
	for _, saved := range []string{"", acpFixtureSession} {
		g, _ := NewAntigravityACPGate(saved)
		_, _ = g.Initialize()
		acpTestReceive(t, g, acpInitResponse)
		_, _ = g.OpenSession(t.TempDir())
		result := `{"sessionId":"other"}`
		if saved == "" {
			result = `{"sessionId":null}`
		}
		if b, err := g.Receive([]byte(`{"jsonrpc":"2.0","id":"keepgoing-open","result":` + result + `}`)); err == nil || len(b) != 0 {
			t.Fatal("bad session accepted")
		}
	}
	g, _ := NewAntigravityACPGate("")
	if _, err := g.Prompt("early"); err == nil {
		t.Fatal("prompt before handshake")
	}
}

func TestAntigravityACPTerminalAndBounds(t *testing.T) {
	for _, reason := range []string{"end_turn", "max_tokens", "max_turn_requests", "refusal", "cancelled"} {
		g := acpTestGate(t, "")
		acpTestReceive(t, g, acpResult(reason))
		if got := g.End(); got.Failed || got.TurnReturned != (reason == "end_turn") {
			t.Fatalf("reason %s: %+v", reason, got)
		}
		if _, err := g.Receive([]byte(acpResult(reason))); err == nil || g.Outcome().TurnReturned {
			t.Fatal("duplicate terminal accepted")
		}
	}
	g := acpTestGate(t, "")
	if got := g.End(); !got.Failed || got.TurnReturned {
		t.Fatal("incomplete became completed")
	}
	t.Run("permission count", func(t *testing.T) {
		g := acpTestGate(t, "")
		for i := 0; i < antigravityACPPermissionLimit; i++ {
			acpTestReceive(t, g, acpPermission(fmt.Sprint(i), acpFixtureSession))
		}
		if _, err := g.Receive([]byte(acpPermission(`100`, acpFixtureSession))); err == nil || !g.Outcome().NeedsUser {
			t.Fatal("permission bound or hold lost")
		}
	})
	t.Run("frame count", func(t *testing.T) {
		g := acpTestGate(t, "")
		frame := fmt.Sprintf(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":%q,"update":{"sessionUpdate":"agent_message_chunk"}}}`, acpFixtureSession)
		for g.frames < antigravityACPFrameCount {
			acpTestReceive(t, g, frame)
		}
		if _, err := g.Receive([]byte(frame)); err == nil {
			t.Fatal("frame count unbounded")
		}
	})
	t.Run("total bytes", func(t *testing.T) {
		g := acpTestGate(t, "")
		frame := fmt.Sprintf(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":%q,"update":{"sessionUpdate":"agent_message_chunk","content":%q}}}`, acpFixtureSession, strings.Repeat("x", antigravityACPFrameLimit/2))
		for len(frame) <= antigravityACPTotalLimit-g.bytes {
			acpTestReceive(t, g, frame)
		}
		if _, err := g.Receive([]byte(frame)); err == nil {
			t.Fatal("byte count unbounded")
		}
	})
	t.Run("encoded prompt", func(t *testing.T) {
		g, _ := NewAntigravityACPGate("")
		_, _ = g.Initialize()
		acpTestReceive(t, g, acpInitResponse)
		_, _ = g.OpenSession(t.TempDir())
		acpTestReceive(t, g, `{"jsonrpc":"2.0","id":"keepgoing-open","result":{"sessionId":"example"}}`)
		b, err := g.Prompt(string(bytes.Repeat([]byte{0}, antigravityACPFrameLimit/2)))
		if err == nil || b != nil || !g.Outcome().Failed {
			t.Fatal("escaped prompt overflow accepted")
		}
	})
}

func TestAntigravityACPUnicodeIdentities(t *testing.T) {
	for _, tc := range []struct {
		raw, want string
		valid     bool
	}{
		{`"\ud83d\ude00"`, "😀", true},
		{`"\\ud800"`, `\ud800`, true},
		{`"\ufffd"`, "�", true},
		{`"\ud800"`, "", false},
		{`"\udfff"`, "", false},
		{`"\ud800\u0041"`, "", false},
	} {
		var got string
		valid := acpString(map[string]json.RawMessage{"id": json.RawMessage(tc.raw)}, "id", &got)
		if valid != tc.valid || valid && got != tc.want {
			t.Fatalf("%s: %q, %t", tc.raw, got, valid)
		}
	}
}

func TestAntigravityACPReadyUpdatesAndResumeFailure(t *testing.T) {
	for _, saved := range []string{"", acpFixtureSession} {
		for _, scenario := range []string{"update", "wrong session", "early permission", "setup error"} {
			t.Run(fmt.Sprintf("saved=%t/%s", saved != "", scenario), func(t *testing.T) {
				g, _ := NewAntigravityACPGate(saved)
				_, _ = g.Initialize()
				acpTestReceive(t, g, acpInitResponse)
				_, _ = g.OpenSession(t.TempDir())
				if scenario == "setup error" {
					if _, err := g.Receive([]byte(`{"jsonrpc":"2.0","id":"keepgoing-open","error":{"code":-32002,"message":"fictional missing session"}}`)); err == nil {
						t.Fatal("setup error accepted")
					}
					if b, err := g.OpenSession(t.TempDir()); err == nil || b != nil {
						t.Fatal("setup error allowed fresh fallback")
					}
					return
				}
				result := `{"sessionId":"` + acpFixtureSession + `"}`
				if saved != "" {
					result = `{}`
				}
				acpTestReceive(t, g, `{"jsonrpc":"2.0","id":"keepgoing-open","result":`+result+`}`)
				session := acpFixtureSession
				if scenario == "wrong session" {
					session = "unrelated"
				}
				frame := fmt.Sprintf(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":%q,"update":{"sessionUpdate":"available_commands_update","availableCommands":[]}}}`, session)
				if scenario == "early permission" {
					frame = acpPermission(`5`, session)
				}
				out, err := g.Receive([]byte(frame))
				if scenario != "update" {
					if err == nil || len(out) != 0 {
						t.Fatal("invalid ready message accepted")
					}
					if b, err := g.Prompt("fictional task"); err == nil || b != nil {
						t.Fatal("invalid ready evidence allowed prompt")
					}
					return
				}
				if err != nil || len(out) != 0 {
					t.Fatalf("ready update rejected: %v", err)
				}
				if _, err := g.Prompt("fictional task"); err != nil {
					t.Fatal(err)
				}
				acpTestReceive(t, g, acpResult("end_turn"))
				if !g.End().TurnReturned {
					t.Fatal("ready update blocked prompt completion")
				}
			})
		}
	}
}
