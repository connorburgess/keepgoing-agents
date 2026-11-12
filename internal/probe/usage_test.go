package probe

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter/runner"
	"github.com/connorburgess/keepgoing-agents/internal/config"
)

// All events below are fictional. Reset times are arbitrary epoch seconds.
const (
	resultLine = `{"type":"result","subtype":"success","is_error":false,"session_id":"s","result":"OK"}`
	eventA     = `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","resetsAt":1900000000,"rateLimitType":"seven_day","unifiedWindows":{"seven_day":{"utilization":0.22,"resetsAt":1900000000,"status":"allowed"}}}}`
	eventB     = `{"type":"rate_limit_event","rate_limit_info":{"unifiedWindows":{"seven_day":{"utilization":0.25,"resetsAt":1900000000,"status":"allowed_warning"},"example_window":{"utilization":0.5,"resetsAt":1900003600,"status":"allowed"}}}}`
)

func at(sec int64) *time.Time { t := time.Unix(sec, 0).UTC(); return &t }

func TestClaudeUsage(t *testing.T) {
	many := `{"type":"rate_limit_event","rate_limit_info":{"unifiedWindows":{`
	for i := 0; i < 12; i++ {
		if i > 0 {
			many += ","
		}
		many += `"w` + string(rune('a'+i)) + `":{"utilization":0.1}`
	}
	many += `}}}`
	cases := []struct {
		name   string
		stdout string
		want   []UsageWindow
	}{
		{"no output", "", nil},
		{"json output without events", resultLine, nil},
		{"one event", `{"type":"system","subtype":"init"}` + "\n" + eventA + "\n" + resultLine,
			[]UsageWindow{{Key: "seven_day", Used: 0.22, ResetsAt: at(1900000000), Status: "allowed"}}},
		{"later event overrides and adds windows", eventA + "\n" + eventB + "\n" + resultLine, []UsageWindow{
			{Key: "example_window", Used: 0.5, ResetsAt: at(1900003600), Status: "allowed"},
			{Key: "seven_day", Used: 0.25, ResetsAt: at(1900000000), Status: "allowed_warning"},
		}},
		{"event after the result still counts", resultLine + "\n" + eventA,
			[]UsageWindow{{Key: "seven_day", Used: 0.22, ResetsAt: at(1900000000), Status: "allowed"}}},
		{"truncated event line", eventA[:len(eventA)-9] + "\n" + resultLine, nil},
		{"non-JSON lines", "starting\n" + eventA + "\nnot json {", []UsageWindow{{Key: "seven_day", Used: 0.22, ResetsAt: at(1900000000), Status: "allowed"}}},
		{"another event type with the same body", strings.Replace(eventA, "rate_limit_event", "other_event", 1), nil},
		{"no rate_limit_info", `{"type":"rate_limit_event"}`, nil},
		{"info without windows", `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","resetsAt":1900000000}}`, nil},
		{"windows is a list", `{"type":"rate_limit_event","rate_limit_info":{"unifiedWindows":[{"utilization":0.2}]}}`, nil},
		{"windows is null", `{"type":"rate_limit_event","rate_limit_info":{"unifiedWindows":null}}`, nil},
		{"window is not an object", `{"type":"rate_limit_event","rate_limit_info":{"unifiedWindows":{"seven_day":0.2}}}`, nil},
		{"missing utilization", `{"type":"rate_limit_event","rate_limit_info":{"unifiedWindows":{"seven_day":{"resetsAt":1900000000,"status":"allowed"}}}}`, nil},
		{"utilization as text", `{"type":"rate_limit_event","rate_limit_info":{"unifiedWindows":{"seven_day":{"utilization":"0.2"}}}}`, nil},
		// A percentage scale would exceed 1; it must not be read as a fraction.
		{"utilization above one", `{"type":"rate_limit_event","rate_limit_info":{"unifiedWindows":{"seven_day":{"utilization":22}}}}`, nil},
		{"negative utilization", `{"type":"rate_limit_event","rate_limit_info":{"unifiedWindows":{"seven_day":{"utilization":-0.1}}}}`, nil},
		{"bounds are inclusive", `{"type":"rate_limit_event","rate_limit_info":{"unifiedWindows":{"a":{"utilization":0},"b":{"utilization":1}}}}`,
			[]UsageWindow{{Key: "a", Used: 0}, {Key: "b", Used: 1}}},
		{"null utilization", `{"type":"rate_limit_event","rate_limit_info":{"unifiedWindows":{"seven_day":{"utilization":null,"status":"allowed"}}}}`, nil},
		// Only utilization is required; other fields of another type are left out.
		{"mistyped reset and status keep the window", `{"type":"rate_limit_event","rate_limit_info":{"unifiedWindows":{"seven_day":{"utilization":0.4,"resetsAt":"soon","status":7}}}}`,
			[]UsageWindow{{Key: "seven_day", Used: 0.4}}},
		{"mistyped reset keeps a valid status", `{"type":"rate_limit_event","rate_limit_info":{"unifiedWindows":{"seven_day":{"utilization":0.4,"resetsAt":{"at":1},"status":"allowed"}}}}`,
			[]UsageWindow{{Key: "seven_day", Used: 0.4, Status: "allowed"}}},
		{"null reset and status keep the window", `{"type":"rate_limit_event","rate_limit_info":{"unifiedWindows":{"seven_day":{"utilization":0.4,"resetsAt":null,"status":null}}}}`,
			[]UsageWindow{{Key: "seven_day", Used: 0.4}}},
		{"implausible reset and free-text status are dropped", `{"type":"rate_limit_event","rate_limit_info":{"unifiedWindows":{"seven_day":{"utilization":0.4,"resetsAt":-5,"status":"please see the example.com notice"}}}}`,
			[]UsageWindow{{Key: "seven_day", Used: 0.4}}},
		{"millisecond reset is refused", `{"type":"rate_limit_event","rate_limit_info":{"unifiedWindows":{"seven_day":{"utilization":0.4,"resetsAt":1900000000000}}}}`,
			[]UsageWindow{{Key: "seven_day", Used: 0.4}}},
		{"key with other characters is refused", `{"type":"rate_limit_event","rate_limit_info":{"unifiedWindows":{"seven day/1":{"utilization":0.4},"ok.key-1":{"utilization":0.3}}}}`,
			[]UsageWindow{{Key: "ok.key-1", Used: 0.3}}},
		{"over-long key is refused", `{"type":"rate_limit_event","rate_limit_info":{"unifiedWindows":{"` + strings.Repeat("k", 65) + `":{"utilization":0.4}}}}`, nil},
		{"one bad window does not hide a good one", `{"type":"rate_limit_event","rate_limit_info":{"unifiedWindows":{"bad":{"utilization":"x"},"good":{"utilization":0.6}}}}`,
			[]UsageWindow{{Key: "good", Used: 0.6}}},
	}
	for _, tc := range cases {
		got := ClaudeUsage([]byte(tc.stdout))
		if len(got) == 0 && len(tc.want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %+v, want %+v", tc.name, got, tc.want)
		}
	}
	if got := ClaudeUsage([]byte(many)); len(got) != maxUsageWindows || got[0].Key != "wa" || got[7].Key != "wh" {
		t.Errorf("cap: %+v", got)
	}
}

func TestCodexUsage(t *testing.T) {
	reset := time.Unix(1900000000, 0).UTC()
	got := CodexUsage(runner.CodexLimits{Windows: []runner.CodexWindow{
		{Name: "primary", UsedPercent: 40, ResetsAt: reset},
		{Name: "secondary", UsedPercent: 100},
		{Name: "tertiary", UsedPercent: 101},
		{Name: "negative", UsedPercent: -1},
	}})
	want := []UsageWindow{{Key: "primary", Used: 0.4, ResetsAt: &reset}, {Key: "secondary", Used: 1}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if got := CodexUsage(runner.CodexLimits{Blocked: true}); len(got) != 0 {
		t.Errorf("blocked without windows: %+v", got)
	}
}

func TestStoredUsageLoadsDefensively(t *testing.T) {
	good := `{"at":"2026-11-09T12:00:00Z","source":"claude_rate_limit_event","identity":"0123abcd","windows":[{"key":"seven_day","used":0.22,"status":"allowed"}]}`
	cases := []struct {
		name      string
		json      string
		available bool
	}{
		{"good", good, true},
		{"not an object", `"seven_day 22%"`, false},
		{"wrong field types", `{"at":5,"source":[],"windows":{}}`, false},
		{"no observation time", `{"source":"claude_rate_limit_event","identity":"0123abcd","windows":[{"key":"seven_day","used":0.22}]}`, false},
		{"no account identity", `{"at":"2026-11-09T12:00:00Z","source":"claude_rate_limit_event","windows":[{"key":"seven_day","used":0.22}]}`, false},
		{"free-text source", `{"at":"2026-11-09T12:00:00Z","source":"see example.com for details","identity":"0123abcd","windows":[{"key":"seven_day","used":0.22}]}`, false},
		{"no windows", `{"at":"2026-11-09T12:00:00Z","source":"claude_rate_limit_event","identity":"0123abcd","windows":[]}`, false},
		{"only out-of-range windows", `{"at":"2026-11-09T12:00:00Z","source":"claude_rate_limit_event","identity":"0123abcd","windows":[{"key":"seven_day","used":3}]}`, false},
	}
	for _, tc := range cases {
		var u Usage
		if err := json.Unmarshal([]byte(tc.json), &u); err != nil || u.Available() != tc.available {
			t.Errorf("%s: available=%t err=%v (%+v)", tc.name, u.Available(), err, u)
		}
	}
	var nilUsage *Usage
	if nilUsage.Available() {
		t.Error("nil reading is available")
	}
}

// The identity changes with anything that decides which account a probe
// reads, and with nothing else. It never contains the values themselves.
func TestUsageIdentity(t *testing.T) {
	base := func() config.Candidate {
		c := config.Candidate{ID: "example", Probe: config.Probe{Kind: "claude", CLI: "claude", Env: map[string]string{"CLAUDE_CONFIG_DIR": "/example/account-a", "EXAMPLE": "1"}}}
		c.Agent = config.Agent{Adapter: "bb", Provider: "claude-code", Group: "claude-account-a", BB: &config.BB{Thread: "thr_example", Model: "target-model"}}
		return c
	}
	want := UsageIdentity(base())
	if len(want) != 32 || strings.Contains(want, "account") {
		t.Fatalf("identity %q", want)
	}
	same := map[string]func(*config.Candidate){
		"candidate id":  func(c *config.Candidate) { c.ID = "renamed" },
		"target thread": func(c *config.Candidate) { c.BB.Thread = "thr_other" },
		"models":        func(c *config.Candidate) { c.BB.Model, c.Probe.Model = "other-model", "cheap-model" },
		"disabled":      func(c *config.Candidate) { c.Disabled = true },
	}
	for name, mut := range same {
		c := base()
		mut(&c)
		if UsageIdentity(c) != want {
			t.Errorf("%s changed the identity", name)
		}
	}
	different := map[string]func(*config.Candidate){
		"provider":        func(c *config.Candidate) { c.Provider = "codex" },
		"group":           func(c *config.Candidate) { c.Group = "claude-account-b" },
		"probe kind":      func(c *config.Candidate) { c.Probe.Kind = "command" },
		"probe cli":       func(c *config.Candidate) { c.Probe.CLI = "claude-other" },
		"account dir":     func(c *config.Candidate) { c.Probe.Env["CLAUDE_CONFIG_DIR"] = "/example/account-b" },
		"env key added":   func(c *config.Candidate) { c.Probe.Env["OTHER"] = "1" },
		"env key removed": func(c *config.Candidate) { delete(c.Probe.Env, "EXAMPLE") },
	}
	for name, mut := range different {
		c := base()
		mut(&c)
		if UsageIdentity(c) == want {
			t.Errorf("%s did not change the identity", name)
		}
	}
	u := &Usage{At: time.Unix(1900000000, 0), Source: UsageSourceClaude, Identity: want, Windows: []UsageWindow{{Key: "seven_day", Used: 0.2}}}
	if !u.For(want) || u.For("") || u.For("other") || (*Usage)(nil).For(want) {
		t.Error("For")
	}
}

// A failure capture keeps the end of the output, where the result or error
// event is, and never the Claude initialization message.
func TestCaptureKeepsTheTailAndOmitsClaudeInit(t *testing.T) {
	init := `{"type":"system","subtype":"init","cwd":"/example/private/workdir","session_id":"example-session","tools":["Example"]}`
	retry := `{"type":"system","subtype":"api_retry","attempt":1}`
	result := `{"type":"result","subtype":"error_during_execution","is_error":true,"api_error_status":429}`
	filler := `{"type":"assistant","message":{"content":[{"type":"text","text":"` + strings.Repeat("x", 1000) + `"}]}}`
	big := init + "\n" + retry + "\n" + strings.Repeat(filler+"\n", 100) + eventA + "\n" + result
	got := string(capture("claude", []byte(big)))
	if len(got) != rawCap || !strings.HasSuffix(got, eventA+"\n"+result) || strings.Contains(got, "/example/private/workdir") || strings.Contains(got, "example-session") {
		t.Fatalf("claude capture: len=%d tail=%q", len(got), got[len(got)-80:])
	}
	small := string(capture("claude", []byte(init+"\n"+retry+"\n"+result)))
	if small != retry+"\n"+result {
		t.Fatalf("small claude capture: %q", small)
	}
	// Other kinds keep every line; only the size is bounded, from the end.
	if got := string(capture("codex", []byte(init+"\n"+result))); got != init+"\n"+result {
		t.Fatalf("codex capture: %q", got)
	}
	if got := capture("command", []byte(strings.Repeat("a", rawCap)+"tail")); len(got) != rawCap || !strings.HasSuffix(string(got), "tail") {
		t.Fatalf("command capture: len=%d", len(got))
	}
}
