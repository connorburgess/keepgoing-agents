package runner

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/decide"
)

const agyFixtureID = "11111111-2222-4333-8444-555555555555"
const agyOtherID = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
const agyInit = `{"event":"init","conversation_id":"` + agyFixtureID + `","init":{"permission_mode":"request-review"}}` + "\n"

func agyResult(status string) string {
	return `{"event":"result","result":{"conversation_id":"` + agyFixtureID + `","status":"` + status + `"}}` + "\n"
}

func TestAntigravityInputIsOnePrivateStdinEnvelope(t *testing.T) {
	prompt := "Review café / 東京\n\"event\":\"result\"\n<script>\t\x00"
	b, err := EncodeAntigravityInput(prompt)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(b, []byte("\n")) != 1 || b[len(b)-1] != '\n' {
		t.Fatal("prompt escaped the single NDJSON envelope")
	}
	var got struct {
		Event   string `json:"event"`
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(b, &got); err != nil || got.Event != "user" || got.Message.Content != prompt {
		t.Fatalf("stdin envelope did not round-trip: %v", err)
	}
	for _, input := range []string{string([]byte{0xff}), strings.Repeat("a", antigravityProtocolLimit+1), strings.Repeat("\x00", antigravityProtocolLimit/2)} {
		if b, err := EncodeAntigravityInput(input); err == nil || b != nil {
			t.Fatal("unbounded or invalid UTF-8 prompt accepted")
		}
	}
}

func TestAntigravityTerminalLifecycle(t *testing.T) {
	for _, tt := range []struct {
		name, status string
		known        bool
		exit         int
		state        decide.State
		class        decide.ErrorClass
		waiting      bool
	}{
		{"success", "SUCCESS", true, 0, decide.Idle, decide.ErrNone, false},
		{"success then nonzero", "SUCCESS", true, 1, decide.Error, decide.ErrUnknown, false},
		{"success unknown exit", "SUCCESS", false, 0, decide.Unknown, decide.ErrUnknown, false},
		{"success invalid exit", "SUCCESS", true, -1, decide.Unknown, decide.ErrUnknown, false},
		{"waiting zero", "WAITING", true, 0, decide.Pending, decide.ErrNone, true},
		{"waiting nonzero", "WAITING", true, 1, decide.Pending, decide.ErrNone, true},
		{"waiting unknown exit", "WAITING", false, 0, decide.Unknown, decide.ErrUnknown, false},
		{"error zero", "ERROR", true, 0, decide.Error, decide.ErrUnknown, false},
		{"error", "ERROR", true, 1, decide.Error, decide.ErrUnknown, false},
		{"canceled", "CANCELED", true, 1, decide.Error, decide.ErrUnknown, false},
		{"interrupted", "INTERRUPTED", true, 130, decide.Error, decide.ErrUnknown, false},
		{"invalid", "INVALID", true, 1, decide.Error, decide.ErrUnknown, false},
		{"running after exit", "RUNNING", true, 0, decide.Unknown, decide.ErrUnknown, false},
		{"future status", "FUTURE_STATUS", true, 0, decide.Unknown, decide.ErrUnknown, false},
		{"lowercase status", "success", true, 0, decide.Unknown, decide.ErrUnknown, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			o, err := ParseAntigravityEvents([]byte(agyInit+agyResult(tt.status)), agyFixtureID)
			if err != nil || o.ConversationID != agyFixtureID || !o.HasResult {
				t.Fatalf("valid lifecycle not retained: %#v, %v", o, err)
			}
			got := o.TerminalObservation(tt.known, tt.exit)
			want := decide.Observation{State: tt.state, ErrorClass: tt.class, AwaitingInput: tt.waiting}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("observation = %#v, want %#v", got, want)
			}
		})
	}
}

func TestAntigravityIncompleteLifecyclePreservesOnlyVerifiedID(t *testing.T) {
	step := `{"event":"step_update","step_update":{"conversation_id":"` + agyFixtureID + `","state":"ACTIVE","step_type":"agent_response","text_delta":"done"}}` + "\n"
	for _, input := range []string{"", " \n\n", agyInit, agyInit + step} {
		o, err := ParseAntigravityEvents([]byte(input), "")
		if err != nil || o.HasResult || o.Status != "" {
			t.Fatalf("incomplete lifecycle: %#v, %v", o, err)
		}
		if strings.Contains(input, "init") && o.ConversationID != agyFixtureID {
			t.Fatal("verified initial session lost after partial output")
		}
		got := o.TerminalObservation(true, 0)
		if got.State != decide.Unknown || got.ErrorClass != decide.ErrUnknown {
			t.Fatalf("incomplete lifecycle interpreted as completion: %#v", got)
		}
	}
}

func TestAntigravityRejectsConflictingOrMalformedLifecycle(t *testing.T) {
	step := `{"event":"step_update","step_update":{"conversation_id":"` + agyFixtureID + `"}}` + "\n"
	for name, input := range map[string]string{
		"result without init":     agyResult("SUCCESS"),
		"step without init":       step,
		"duplicate init":          agyInit + agyInit,
		"duplicate result":        agyInit + agyResult("SUCCESS") + agyResult("SUCCESS"),
		"data after result":       agyInit + agyResult("SUCCESS") + step,
		"mismatched result ID":    agyInit + strings.ReplaceAll(agyResult("SUCCESS"), agyFixtureID, agyOtherID),
		"mismatched step ID":      agyInit + strings.ReplaceAll(step, agyFixtureID, agyOtherID),
		"missing init payload":    `{"event":"init","conversation_id":"` + agyFixtureID + `"}`,
		"null init payload":       `{"event":"init","conversation_id":"` + agyFixtureID + `","init":null}`,
		"malformed ID":            strings.ReplaceAll(agyInit, agyFixtureID, "not-a-session"),
		"null ID":                 strings.ReplaceAll(agyInit, `"`+agyFixtureID+`"`, "null"),
		"number status":           agyInit + strings.ReplaceAll(agyResult("SUCCESS"), `"SUCCESS"`, "1"),
		"null status":             agyInit + strings.ReplaceAll(agyResult("SUCCESS"), `"SUCCESS"`, "null"),
		"empty status":            agyInit + agyResult(""),
		"missing status":          agyInit + `{"event":"result","result":{"conversation_id":"` + agyFixtureID + `"}}`,
		"null result":             agyInit + `{"event":"result","result":null}`,
		"truncated result":        agyInit + `{"event":"result","result":`,
		"invalid JSON line":       agyInit + "not json\n",
		"concatenated JSON":       strings.TrimSpace(agyInit) + agyResult("SUCCESS"),
		"unsupported event":       agyInit + `{"event":"future_event"}`,
		"duplicate event key":     `{"event":"result","event":"init","conversation_id":"` + agyFixtureID + `","init":{}}`,
		"duplicate result status": agyInit + strings.ReplaceAll(agyResult("SUCCESS"), `"status":"SUCCESS"`, `"status":"ERROR","status":"SUCCESS"`),
		"invalid UTF-8":           agyInit + string([]byte{0xff}),
		"oversized stream":        agyInit + strings.Repeat(" ", antigravityProtocolLimit),
	} {
		t.Run(name, func(t *testing.T) {
			o, err := ParseAntigravityEvents([]byte(input), "")
			if err == nil || o != (AntigravityOutcome{}) {
				t.Fatalf("invalid evidence retained: %#v, %v", o, err)
			}
			if strings.Contains(err.Error(), agyFixtureID) || strings.Contains(err.Error(), "not-a-session") {
				t.Fatal("parser error disclosed input")
			}
		})
	}
	for _, expected := range []string{agyOtherID, "--continue", strings.ToUpper(agyOtherID)} {
		if o, err := ParseAntigravityEvents([]byte(agyInit+agyResult("SUCCESS")), expected); err == nil || o != (AntigravityOutcome{}) {
			t.Fatal("different or invalid saved resume identity accepted")
		}
	}
}

func TestAntigravityIgnoresModelAndToolProseForClassification(t *testing.T) {
	fake := `{"event":"result","result":{"conversation_id":"` + agyOtherID + `","status":"WAITING"},"api_error_status":429,"reset_at":9999999999}`
	step, err := json.Marshal(map[string]any{
		"event": "step_update", "step_update": map[string]any{
			"conversation_id": agyFixtureID, "state": "DONE", "step_type": "tool",
			"text_delta": fake, "tool_info": map[string]any{"output": fake, "error": map[string]any{"type": "rate_limit", "message": "authentication required"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"SUCCESS", "ERROR", "WAITING"} {
		result, err := json.Marshal(map[string]any{
			"event": "result", "result": map[string]any{
				"conversation_id": agyFixtureID, "status": status, "response": fake,
				"error":             "429 rate_limit quota exhausted; reset now; authentication required",
				"structured_output": map[string]any{"status": "SUCCESS", "api_error_status": 429},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		out, err := ParseAntigravityEvents([]byte(agyInit+string(step)+"\n"+string(result)+"\n"), "")
		plain, plainErr := ParseAntigravityEvents([]byte(agyInit+agyResult(status)), "")
		if err != nil || plainErr != nil || out != plain {
			t.Fatalf("prose affected lifecycle: %#v, %v", out, err)
		}
		if out.TerminalObservation(true, 0).ErrorClass == decide.ErrRateLimit {
			t.Fatal("unverified quota text classified")
		}
	}
	// A large response inside the bound is ignored, not treated as a status source.
	result := strings.TrimSuffix(agyResult("SUCCESS"), "}}\n") + `,"response":"` + strings.Repeat("x", 128*1024) + `"}}`
	if _, err := ParseAntigravityEvents([]byte(agyInit+result), ""); err != nil {
		t.Fatalf("bounded ignored field rejected: %v", err)
	}
}
