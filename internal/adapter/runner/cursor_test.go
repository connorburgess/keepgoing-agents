package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
)

const cursorSession = "00000000-0000-4000-8000-0000000000c1"
const cursorInit = `{"type":"system","subtype":"init","session_id":"` + cursorSession + `"}`
const cursorResult = `{"type":"result","subtype":"success","is_error":false,"session_id":"` + cursorSession + `","result":"fictional rate limit 429 auth network"}`
const cursorSuccess = cursorInit + "\n" + cursorResult + "\n"

func TestCursorEventContract(t *testing.T) {
	tests := []struct {
		name, raw       string
		success, unsafe bool
		session         string
	}{
		{"success", cursorSuccess, true, false, cursorSession},
		{"text ignored", cursorInit + "\n" + `{"type":"assistant","session_id":"` + cursorSession + `","message":{"content":[{"text":"rate limited"}]}}` + "\n" + cursorResult, true, false, cursorSession},
		{"partial", cursorInit, false, false, cursorSession},
		{"empty", "", false, false, ""},
		{"prose only", "rate limit 429", false, true, ""},
		{"missing init", cursorResult, false, true, ""},
		{"missing boolean", cursorInit + "\n" + strings.Replace(cursorResult, `"is_error":false,`, "", 1), false, false, cursorSession},
		{"null boolean", strings.Replace(cursorSuccess, `"is_error":false`, `"is_error":null`, 1), false, false, cursorSession},
		{"error boolean", strings.Replace(cursorSuccess, `"is_error":false`, `"is_error":true`, 1), false, false, cursorSession},
		{"unknown subtype", strings.Replace(cursorSuccess, `"subtype":"success"`, `"subtype":"other"`, 1), false, false, cursorSession},
		{"broken tail", cursorSuccess + `{"type":`, false, true, ""},
		{"duplicate terminal", cursorSuccess + cursorResult, false, false, cursorSession},
		{"duplicate init", cursorInit + "\n" + cursorSuccess, false, true, ""},
		{"post terminal", cursorSuccess + `{"type":"assistant"}`, false, true, ""},
		{"conflicting identities", cursorInit + "\n" + strings.ReplaceAll(cursorResult, cursorSession, "00000000-0000-4000-8000-0000000000c2"), false, true, ""},
		{"missing identity", strings.ReplaceAll(cursorSuccess, cursorSession, ""), false, true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseCursorEvents([]byte(tt.raw))
			if got.Success != tt.success || got.UnsafeSession != tt.unsafe || got.SessionID != tt.session {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func cursorFixture(t *testing.T, output string, code string, wait bool) (*Cursor, string, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "café-项目")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	capture := filepath.Join(root, "capture.json")
	release := filepath.Join(root, "release")
	env := map[string]string{"KEEPGOING_TEST_HELPER": "cursor", "KEEPGOING_TEST_RESULT": capture, "KEEPGOING_CURSOR_FIXTURE_VALUE": "fictional-private-value", "KEEPGOING_CURSOR_FIXTURE_STDOUT": output, "KEEPGOING_CURSOR_FIXTURE_EXIT": code}
	if wait {
		env["KEEPGOING_CURSOR_FIXTURE_WAIT"] = release
	}
	c := NewCursor(config.Cursor{CLI: self, Workdir: root, Model: "example-model", Env: env}, filepath.Join(root, "target"))
	t.Cleanup(func() { _ = c.Stop(context.Background()) })
	return c, capture, release
}

func readCursorCapture(t *testing.T, p string) struct {
	Args               []string
	Prompt, Cwd, Value string
} {
	t.Helper()
	var got struct {
		Args               []string
		Prompt, Cwd, Value string
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(p)
		if err == nil && json.Unmarshal(b, &got) == nil {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("fixture did not record its input")
	return got
}

func TestCursorLifecycleOnThisPlatform(t *testing.T) {
	ctx := context.Background()
	c, capture, release := cursorFixture(t, cursorSuccess, "0", true)
	obs, err := c.Observe(ctx)
	if err != nil || obs.State != decide.Idle {
		t.Fatalf("initial %+v %v", obs, err)
	}
	prompt := "fictional café\n项目 task"
	if err := c.Send(ctx, prompt, adapter.ModeAuto); err != nil {
		t.Fatal(err)
	}
	got := readCursorCapture(t, capture)
	expected := []string{"-p", "--output-format", "stream-json", "--model", "example-model"}
	if !reflect.DeepEqual(got.Args, expected) {
		t.Fatalf("fixture argv = %q, want %q", got.Args, expected)
	}
	if got.Prompt != prompt {
		t.Fatalf("fixture stdin = %q, want %q", got.Prompt, prompt)
	}
	if got.Value != "fictional-private-value" {
		t.Fatalf("fixture environment = %q", got.Value)
	}
	assertCursorWorkdir(t, got.Cwd, c.cfg.Workdir)
	if obs, err = c.Observe(ctx); err != nil || obs.State != decide.Active {
		t.Fatalf("active %+v %v", obs, err)
	}
	if err := c.Send(ctx, "duplicate", adapter.ModeQueue); err == nil {
		t.Fatal("sent to running turn")
	}
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, c.dir, Finished)
	if obs, err = c.Observe(ctx); err != nil || obs.State != decide.Idle || obs.Evidence.Source != "cursor_events" {
		t.Fatalf("finished %+v %v", obs, err)
	}
	restarted := NewCursor(c.cfg, string(c.dir))
	if err := os.Remove(capture); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Send(ctx, "second", adapter.ModeAuto); err != nil {
		t.Fatal(err)
	}
	got = readCursorCapture(t, capture)
	if !reflect.DeepEqual(got.Args, append(expected, "--resume="+cursorSession)) {
		t.Fatalf("resume args %v", got.Args)
	}
	waitFor(t, c.dir, Finished)
	// A separate target starts a new conversation, even with the same provider.
	other := NewCursor(c.cfg, filepath.Join(c.cfg.Workdir, "other-target"))
	if err := os.Remove(capture); err != nil {
		t.Fatal(err)
	}
	if err := other.Send(ctx, "separate", adapter.ModeAuto); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Stop(ctx) })
	got = readCursorCapture(t, capture)
	if !reflect.DeepEqual(got.Args, expected) {
		t.Fatalf("cross-target resume: %v", got.Args)
	}
	waitFor(t, other.dir, Finished)
}

func TestCursorFailuresRemainUnknown(t *testing.T) {
	for _, tt := range []struct {
		name, out, exit string
		retry           bool
	}{
		// Nothing on stdout and a known nonzero exit: no conversation began, so a
		// retry may start one. A live untrusted-workspace failure has this shape.
		{"stderr only", "", "1", true},
		{"whitespace only", "\n", "1", false},
		{"apparent success nonzero", cursorSuccess, "1", true},
		{"partial init", cursorInit, "1", true},
		{"missing terminal zero", cursorInit, "0", true},
		{"malformed zero", "not-json", "0", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, _, _ := cursorFixture(t, tt.out, tt.exit, false)
			ctx := context.Background()
			if err := c.Send(ctx, "fixture", adapter.ModeAuto); err != nil {
				t.Fatal(err)
			}
			waitFor(t, c.dir, Finished)
			obs, err := c.Observe(ctx)
			if obs.ErrorClass != decide.ErrUnknown || (obs.State != decide.Error && obs.State != decide.Unknown) {
				t.Fatalf("%+v %v", obs, err)
			}
			err = c.Send(ctx, "retry", adapter.ModeAuto)
			if (err == nil) != tt.retry {
				t.Fatalf("retry=%v err=%v", tt.retry, err)
			}
			if tt.retry {
				waitFor(t, c.dir, Finished)
			}
		})
	}
}

func TestCursorSessionSafety(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []string{"conflict", "unreadable", "unavailable parent", "crash without identity"} {
		t.Run(kind, func(t *testing.T) {
			c, _, _ := cursorFixture(t, cursorSuccess, "0", false)
			if err := os.MkdirAll(string(c.dir), 0700); err != nil {
				t.Fatal(err)
			}
			if kind == "crash without identity" {
				record, _ := json.Marshal(Record{PID: -1})
				if err := os.WriteFile(c.dir.path("run.json"), record, 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := c.Send(ctx, "fixture", adapter.ModeAuto); err != nil {
					t.Fatal(err)
				}
				waitFor(t, c.dir, Finished)
				switch kind {
				case "conflict":
					if err := c.dir.SaveSession("00000000-0000-4000-8000-0000000000c2"); err != nil {
						t.Fatal(err)
					}
				case "unreadable":
					if err := os.Mkdir(c.dir.path("session"), 0700); err != nil {
						t.Fatal(err)
					}
				case "unavailable parent":
					// An inaccessible metadata parent must fail closed.
					if err := os.RemoveAll(string(c.dir)); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(string(c.dir), nil, 0600); err != nil {
						t.Fatal(err)
					}
					if _, err := c.remember(ParseCursorEvents([]byte(cursorSuccess))); err == nil {
						t.Fatal("ignored unavailable metadata")
					}
					return
				}
			}
			if err := c.Send(ctx, "retry", adapter.ModeAuto); err == nil {
				t.Fatal("unsafe fresh or changed session")
			}
		})
	}
}

func TestCursorOwnedStopOnThisPlatform(t *testing.T) {
	c, capture, _ := cursorFixture(t, cursorSuccess, "0", true)
	ctx := context.Background()
	if err := c.Send(ctx, "fixture", adapter.ModeAuto); err != nil {
		t.Fatal(err)
	}
	readCursorCapture(t, capture)
	if err := c.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, c.dir, Crashed)
	obs, err := c.Observe(ctx)
	if err != nil || obs.State != decide.Error || obs.ErrorClass != decide.ErrUnknown {
		t.Fatalf("stop %+v %v", obs, err)
	}
	if err := c.Send(ctx, "retry", adapter.ModeAuto); err == nil {
		t.Fatal("cancelled turn without identity started fresh")
	}
}

// Windows may report the same directory using an 8.3 alias, and macOS may
// resolve a temporary-directory symlink. Compare identity, not path spelling.
func assertCursorWorkdir(t *testing.T, got, want string) {
	t.Helper()
	if !filepath.IsAbs(got) {
		t.Fatalf("fixture workdir is not absolute: %q", got)
	}
	actual, err := os.Stat(got)
	if err != nil {
		t.Fatalf("stat fixture workdir %q: %v", got, err)
	}
	expected, err := os.Stat(want)
	if err != nil {
		t.Fatalf("stat configured workdir %q: %v", want, err)
	}
	if !actual.IsDir() || !os.SameFile(actual, expected) {
		t.Fatalf("fixture workdir %q differs from configured directory %q", got, want)
	}
}

func TestCursorRejectsAmbiguousAndOversizedEvents(t *testing.T) {
	for _, raw := range []string{
		strings.Replace(cursorSuccess, `"is_error":false`, `"is_error":true,"is_error":false`, 1),
		strings.Replace(cursorSuccess, `"session_id":"`+cursorSession+`"`, `"session_id":"00000000-0000-4000-8000-0000000000c2","session_id":"`+cursorSession+`"`, 1),
		cursorInit + "\n" + `{"type":"assistant","session_id":"00000000-0000-4000-8000-0000000000c2"}` + "\n" + cursorResult,
		cursorResult + "\n" + cursorInit,
		cursorInit + "\n" + `{"type":"user","session_id":null}`,
		strings.Repeat(" ", cursorMaxOutput+1),
		`{"type":"assistant","message":"` + strings.Repeat("x", cursorMaxEvent) + `"}`,
	} {
		out := ParseCursorEvents([]byte(raw))
		if out.Success || !out.UnsafeSession || out.SessionID != "" {
			t.Fatalf("ambiguous stream accepted: %+v", out)
		}
	}
	// Nested model-produced JSON is not a lifecycle record.
	out := ParseCursorEvents([]byte(cursorInit + "\n" + `{"type":"assistant","session_id":"` + cursorSession + `","message":{"type":"result","is_error":false,"session_id":"different"}}`))
	if out.Success || out.UnsafeSession || out.SessionID != cursorSession {
		t.Fatalf("nested prose affected lifecycle: %+v", out)
	}
}

func TestCursorBoundsStdoutBeforeParsing(t *testing.T) {
	c, _, _ := cursorFixture(t, cursorSuccess, "0", false)
	if err := os.MkdirAll(string(c.dir), 0700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"run.json": "{}", "exit": "0", "stdout": strings.Repeat("x", cursorMaxOutput+1)} {
		if err := os.WriteFile(c.dir.path(name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.Observe(context.Background()); err == nil {
		t.Fatal("oversized output observed as success")
	}
	if err := c.Send(context.Background(), "fixture", adapter.ModeAuto); err == nil {
		t.Fatal("oversized output allowed next send")
	}
}

func TestCursorMissingOutputIsUnknown(t *testing.T) {
	c, _, _ := cursorFixture(t, cursorSuccess, "0", false)
	if err := os.MkdirAll(string(c.dir), 0700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"run.json": "{}", "exit": "0"} {
		if err := os.WriteFile(c.dir.path(name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	obs, err := c.Observe(context.Background())
	if err != nil || obs.State != decide.Error || obs.ErrorClass != decide.ErrUnknown {
		t.Fatalf("%+v %v", obs, err)
	}
	if err := c.Send(context.Background(), "fixture", adapter.ModeAuto); err == nil {
		t.Fatal("missing output silently created a new session")
	}
}

// A fresh conversation after a turn with no session is allowed for one shape
// only: exit code 1, an existing empty output file, and a CLI version whose
// start order is verified. Everything else keeps the refusal, including a
// stand-in that swallowed the prompt and exited with another code.
func TestCursorFreshStartNeedsExactShapeAndVerifiedVersion(t *testing.T) {
	const other = "2030.01.02-abcdef0"
	for _, tc := range []struct {
		name     string
		files    map[string]string
		version  string
		extra    []string
		retry    bool
		resume   bool
		explains bool
	}{
		{"empty_output_exit_1", map[string]string{"run.json": "{}", "exit": "1", "stdout": ""}, "", nil, true, false, false},
		{"empty_output_exit_2", map[string]string{"run.json": "{}", "exit": "2", "stdout": ""}, "", nil, false, false, false},
		{"empty_output_exit_127", map[string]string{"run.json": "{}", "exit": "127", "stdout": ""}, "", nil, false, false, false},
		{"empty_output_exit_130", map[string]string{"run.json": "{}", "exit": "130", "stdout": ""}, "", nil, false, false, false},
		{"empty_output_exit_143", map[string]string{"run.json": "{}", "exit": "143", "stdout": ""}, "", nil, false, false, false},
		{"missing_output_exit_1", map[string]string{"run.json": "{}", "exit": "1"}, "", nil, false, false, false},
		{"empty_output_exit_0", map[string]string{"run.json": "{}", "exit": "0", "stdout": ""}, "", nil, false, false, false},
		{"empty_output_unreadable_exit", map[string]string{"run.json": "{}", "exit": "not-a-number", "stdout": ""}, "", nil, false, false, false},
		{"empty_output_no_exit_record", map[string]string{"run.json": "{}", "stdout": ""}, "", nil, false, false, false},
		{"some_output_exit_1", map[string]string{"run.json": "{}", "exit": "1", "stdout": "rate limit\n"}, "", nil, false, false, false},
		{"unverified_version", map[string]string{"run.json": "{}", "exit": "1", "stdout": ""}, other, nil, false, false, true},
		{"unverified_version_listed", map[string]string{"run.json": "{}", "exit": "1", "stdout": ""}, other, []string{"2031.01.01-0000000", other}, true, false, false},
		{"another_version_listed", map[string]string{"run.json": "{}", "exit": "1", "stdout": ""}, other, []string{"2031.01.01-0000000"}, false, false, true},
		{"version_command_fails", map[string]string{"run.json": "{}", "exit": "1", "stdout": ""}, "fail", []string{other}, false, false, true},
		{"version_not_a_token", map[string]string{"run.json": "{}", "exit": "1", "stdout": ""}, "2026.09.18-9a7762b extra", nil, false, false, true},
		// A verified session never needs the exception, whatever the version.
		{"initialized_exit_1", map[string]string{"run.json": "{}", "exit": "1", "stdout": cursorInit + "\n"}, other, nil, true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, capture, _ := cursorFixture(t, cursorSuccess, "0", false)
			c.cfg.Env["KEEPGOING_CURSOR_FIXTURE_VERSION"] = tc.version
			c.cfg.FreshStartVersions = tc.extra
			if err := os.MkdirAll(string(c.dir), 0700); err != nil {
				t.Fatal(err)
			}
			for name, content := range tc.files {
				if err := os.WriteFile(c.dir.path(name), []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := c.Send(context.Background(), "fixture", adapter.ModeAuto)
			if (err == nil) != tc.retry {
				t.Fatalf("send error %v", err)
			}
			if !tc.retry {
				if _, statErr := os.Stat(capture); !os.IsNotExist(statErr) {
					t.Fatal("refused send still started a turn")
				}
				// The reason names the version rule only when that is what refused it,
				// and never the version, the path or the prompt.
				if strings.Contains(err.Error(), "fresh_start_versions") != tc.explains || strings.Contains(err.Error(), other) || strings.Contains(err.Error(), "fixture") {
					t.Fatalf("refusal reason: %v", err)
				}
				return
			}
			got := readCursorCapture(t, capture)
			want := []string{"-p", "--output-format", "stream-json", "--model", "example-model"}
			if tc.resume {
				want = append(want, "--resume="+cursorSession)
			}
			if !reflect.DeepEqual(got.Args, want) {
				t.Fatalf("argv %q", got.Args)
			}
			waitFor(t, c.dir, Finished)
		})
	}
}

const cursorReconnecting = `{"type":"connection","subtype":"reconnecting","session_id":"` + cursorSession + `","timestamp_ms":1893456000000,"attempt":1,"endpoint_url":"https://example.com/fictional"}`
const cursorReconnected = `{"type":"connection","subtype":"reconnected","session_id":"` + cursorSession + `","timestamp_ms":1893456001000}`
const cursorRetry = `{"type":"retry","subtype":"starting","session_id":"` + cursorSession + `","timestamp_ms":1893456002000,"attempt":1,"is_resume":true}`

// Connection and retry records are structured but unpublished. They may add
// diagnostic evidence; they must never decide success, identity or error class.
func TestCursorConnectionRecordsAreDiagnosticOnly(t *testing.T) {
	other := strings.ReplaceAll(cursorReconnecting, cursorSession, "00000000-0000-4000-8000-0000000000c2")
	quoted := `{"type":"assistant","session_id":"` + cursorSession + `","message":{"content":[{"text":"{\"type\":\"connection\",\"subtype\":\"reconnecting\"}"}]}}`
	for _, tc := range []struct {
		name, raw     string
		success, lost bool
		session       string
	}{
		{"lost_then_no_result", cursorInit + "\n" + cursorReconnecting + "\n" + cursorRetry + "\n" + cursorReconnecting, false, true, cursorSession},
		{"recovered_then_success", cursorInit + "\n" + cursorReconnecting + "\n" + cursorReconnected + "\n" + cursorRetry + "\n" + cursorResult, true, false, cursorSession},
		{"recovered_then_no_result", cursorInit + "\n" + cursorReconnecting + "\n" + cursorReconnected, false, false, cursorSession},
		{"lost_again", cursorInit + "\n" + cursorReconnecting + "\n" + cursorReconnected + "\n" + cursorReconnecting, false, true, cursorSession},
		// A success record still decides the turn; the wrapper's exit decides the rest.
		{"lost_but_result_present", cursorInit + "\n" + cursorReconnecting + "\n" + cursorResult, true, true, cursorSession},
		{"retry_alone", cursorInit + "\n" + cursorRetry, false, false, cursorSession},
		{"other_session", cursorInit + "\n" + other, false, false, cursorSession},
		{"before_init", cursorReconnecting + "\n" + cursorInit, false, false, cursorSession},
		{"missing_session", cursorInit + "\n" + `{"type":"connection","subtype":"reconnecting"}`, false, false, cursorSession},
		{"non_string_subtype", cursorInit + "\n" + `{"type":"connection","subtype":1,"session_id":"` + cursorSession + `"}`, false, false, cursorSession},
		{"unknown_subtype", cursorInit + "\n" + strings.Replace(cursorReconnecting, "reconnecting", "degraded", 1), false, false, cursorSession},
		{"model_text_is_not_a_record", cursorInit + "\n" + quoted, false, false, cursorSession},
		{"unsafe_stream_reports_nothing", cursorInit + "\n" + cursorReconnecting + "\n" + `{"type":`, false, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseCursorEvents([]byte(tc.raw))
			if got.Success != tc.success || got.ConnectionLost != tc.lost || got.SessionID != tc.session {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

// What the inspected CLI does when the connection is lost mid-turn, as far as
// its source shows: reconnecting records, then exit 1 with text on stderr and
// no result record. The turn stays an unknown error, the session is kept, and
// the next send resumes exactly that session with the prompt given once.
func TestCursorConnectionLossKeepsSessionAndStaysUnknown(t *testing.T) {
	ctx := context.Background()
	lost := cursorInit + "\n" + `{"type":"user","session_id":"` + cursorSession + `","message":{"role":"user"}}` + "\n" + cursorReconnecting + "\n" + cursorRetry + "\n" + cursorReconnecting + "\n"
	c, capture, _ := cursorFixture(t, lost, "1", false)
	if err := c.Send(ctx, "first prompt", adapter.ModeAuto); err != nil {
		t.Fatal(err)
	}
	waitFor(t, c.dir, Finished)
	for i := 0; i < 3; i++ {
		obs, err := c.Observe(ctx)
		if err != nil || obs.State != decide.Error || obs.ErrorClass != decide.ErrUnknown {
			t.Fatalf("observation %+v %v", obs, err)
		}
		if obs.Evidence.Source != "cursor_events" || obs.Evidence.Category != "connection_error" || obs.Retrying || !obs.ResetsAt.IsZero() {
			t.Fatalf("evidence %+v", obs)
		}
	}
	if id, err := c.dir.InspectSession(); err != nil || id != cursorSession {
		t.Fatalf("session %q %v", id, err)
	}
	// A reconstructed adapter, as after a supervisor restart, resumes it.
	if err := os.Remove(capture); err != nil {
		t.Fatal(err)
	}
	recovered := cursorInit + "\n" + cursorReconnecting + "\n" + cursorReconnected + "\n" + cursorResult + "\n"
	env := map[string]string{}
	for k, v := range c.cfg.Env {
		env[k] = v
	}
	env["KEEPGOING_CURSOR_FIXTURE_STDOUT"], env["KEEPGOING_CURSOR_FIXTURE_EXIT"] = recovered, "0"
	cfg := c.cfg
	cfg.Env = env
	restarted := NewCursor(cfg, string(c.dir))
	if err := restarted.Send(ctx, "second prompt", adapter.ModeAuto); err != nil {
		t.Fatal(err)
	}
	got := readCursorCapture(t, capture)
	want := []string{"-p", "--output-format", "stream-json", "--model", "example-model", "--resume=" + cursorSession}
	if !reflect.DeepEqual(got.Args, want) || got.Prompt != "second prompt" {
		t.Fatalf("resume argv %q prompt %q", got.Args, got.Prompt)
	}
	waitFor(t, c.dir, Finished)
	obs, err := restarted.Observe(ctx)
	if err != nil || obs.State != decide.Idle || obs.ErrorClass != decide.ErrNone || obs.Evidence.Category == "connection_error" {
		t.Fatalf("recovered turn %+v %v", obs, err)
	}
}
