package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/state"
	"gopkg.in/yaml.v3"
)

const doctorPrivate = "PRIVATE_VALUE_MUST_NOT_APPEAR"

type doctorFixture struct {
	root, config, state, executable string
	loop                            map[string]any
}

func newDoctorFixture(t *testing.T) *doctorFixture {
	t.Helper()
	root := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	f := &doctorFixture{root: root, config: filepath.Join(root, "keepgoing.yaml"), state: filepath.Join(root, "state"), executable: exe}
	f.loop = map[string]any{"name": "example-loop", "state_dir": f.state, "adapter": "bb", "provider": "codex", "bb": map[string]any{"thread": "thr_example", "cli": exe}, "prompt": doctorPrivate}
	f.save(t)
	return f
}
func (f *doctorFixture) save(t *testing.T) {
	t.Helper()
	b, err := yaml.Marshal(map[string]any{"version": 1, "loop": f.loop})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(f.config, b, 0o600); err != nil {
		t.Fatal(err)
	}
}
func (f *doctorFixture) run(t *testing.T, invoke doctorRun, extra ...string) (int, doctorReport, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	args := append([]string{"-c", f.config, "--json"}, extra...)
	code := doctor(env{context.Background(), &out, &errOut}, args, invoke)
	var r doctorReport
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatalf("invalid JSON: %v %s %s", err, &out, &errOut)
	}
	if strings.Contains(out.String()+errOut.String(), doctorPrivate) {
		t.Fatal("private content leaked")
	}
	return code, r, out.String()
}
func successfulDoctorRun(_ context.Context, _ string, args []string, _ map[string]string, _ string) ([]byte, error) {
	if reflect.DeepEqual(args, []string{"--version"}) {
		return []byte("codex-cli 1.2.3\n"), nil
	}
	return []byte(`{"thread":{"id":"thr_example","status":"idle","messages":["` + doctorPrivate + `"]}}`), nil
}
func doctorCheckByID(t *testing.T, r doctorReport, id string) doctorCheck {
	t.Helper()
	for _, c := range r.Checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("missing %s", id)
	return doctorCheck{}
}

func TestDoctorReadOnlyAndStructuredOutput(t *testing.T) {
	f := newDoctorFixture(t)
	var calls [][]string
	code, r, _ := f.run(t, func(ctx context.Context, cli string, args []string, env map[string]string, workdir string) ([]byte, error) {
		if cli != f.executable {
			t.Fatalf("wrong executable %q", cli)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("command has no timeout")
		}
		calls = append(calls, args)
		return successfulDoctorRun(ctx, cli, args, env, workdir)
	})
	if code != 0 || r.SchemaVersion != 1 || r.ControlState != "disabled" || r.TargetAgent.BBThread != "thr_example" {
		t.Fatalf("bad report: %d %+v", code, r)
	}
	if !reflect.DeepEqual(calls, [][]string{{"--version"}, {"thread", "show", "thr_example", "--json"}}) {
		t.Fatalf("unexpected commands: %v", calls)
	}
	for _, id := range []string{"agent.primary.target", "agent.primary.version"} {
		if doctorCheckByID(t, r, id).Status != "ok" {
			t.Fatal(id)
		}
	}
	for _, id := range []string{"agent.primary.login", "scheduler", "supervisor.version", "probes"} {
		if doctorCheckByID(t, r, id).Status != "unavailable" {
			t.Fatal(id)
		}
	}
	if _, err := os.Stat(f.state); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("doctor created runtime state")
	}
}

func TestDoctorOfflineNeverExecutesOrChangesControl(t *testing.T) {
	f := newDoctorFixture(t)
	if err := control.Save(f.state, control.File{Enabled: true, Paused: true, Note: doctorPrivate}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(f.state, control.FileName))
	if err != nil {
		t.Fatal(err)
	}
	code, r, _ := f.run(t, func(context.Context, string, []string, map[string]string, string) ([]byte, error) {
		t.Fatal("offline executed a command")
		return nil, nil
	}, "--offline")
	if code != 0 || r.ControlState != "paused" {
		t.Fatalf("%d %+v", code, r)
	}
	after, _ := os.ReadFile(filepath.Join(f.state, control.FileName))
	if !bytes.Equal(before, after) {
		t.Fatal("doctor modified control")
	}
	if doctorCheckByID(t, r, "agent.primary.target").Status != "unavailable" {
		t.Fatal("offline target was claimed verified")
	}
}

func TestDoctorRedactsErrorsAndAccountValues(t *testing.T) {
	f := newDoctorFixture(t)
	account := filepath.Join(f.root, doctorPrivate)
	if err := os.Mkdir(account, 0o700); err != nil {
		t.Fatal(err)
	}
	delete(f.loop, "bb")
	f.loop["adapter"] = "codex"
	f.loop["codex"] = map[string]any{"cli": f.executable, "workdir": f.root, "env": map[string]string{"CODEX_HOME": account, "EXAMPLE_SECRET": doctorPrivate}}
	f.save(t)
	code, r, _ := f.run(t, func(_ context.Context, _ string, args []string, env map[string]string, workdir string) ([]byte, error) {
		if (!reflect.DeepEqual(args, []string{"--version"}) && !reflect.DeepEqual(args, []string{"login", "status"})) || env["CODEX_HOME"] != account || workdir != f.root {
			t.Fatal("wrong account or command")
		}
		return []byte(doctorPrivate), errors.New(doctorPrivate)
	})
	if code != 0 || doctorCheckByID(t, r, "agent.primary.account").Status != "ok" || doctorCheckByID(t, r, "agent.primary.version").Status != "unavailable" {
		t.Fatalf("%d %+v", code, r)
	}
	if err := os.WriteFile(f.config, []byte("version: "+doctorPrivate+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, r, _ = f.run(t, successfulDoctorRun)
	if code != 1 || doctorCheckByID(t, r, "config").Status != "error" {
		t.Fatal("invalid config accepted")
	}
}

func TestDoctorTargetResponses(t *testing.T) {
	for _, tc := range []struct {
		name, raw, want string
		cmdErr          error
	}{
		{"found", `{"thread":{"id":"thr_example"}}`, "ok", nil},
		{"missing", `{"ok":false,"error":{"code":"thread_not_found"}}`, "error", errors.New("exit 1")},
		{"archived", `{"thread":{"id":"thr_example","archivedAt":123}}`, "error", nil},
		{"deleted", `{"thread":{"id":"thr_example","deletedAt":123}}`, "error", nil},
		{"wrong id", `{"thread":{"id":"thr_other"}}`, "unavailable", nil},
		{"missing id", `{"thread":{"status":"idle"}}`, "unavailable", nil},
		{"failed command", `{"thread":{"id":"thr_example"}}`, "unavailable", errors.New(doctorPrivate)},
		{"envelope error", `{"ok":false,"error":{"code":"unknown"},"thread":{"id":"thr_example"}}`, "unavailable", nil},
		{"prose", doctorPrivate, "unavailable", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := doctorReport{}
			doctorBB(&r, "target", "thr_example", []byte(tc.raw), tc.cmdErr)
			c := doctorCheckByID(t, r, "target")
			if c.Status != tc.want || strings.Contains(c.Message, doctorPrivate) {
				t.Fatalf("%+v", c)
			}
		})
	}
}

func TestDoctorHeartbeatDoesNotInventScheduler(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		last    time.Time
		enabled bool
		want    string
	}{
		{"fresh setup", time.Time{}, false, "unavailable"}, {"enabled never checked", time.Time{}, true, "warning"},
		{"recent", now.Add(-time.Minute), true, "ok"}, {"stale", now.Add(-time.Hour), true, "warning"},
		{"future", now.Add(time.Hour), true, "warning"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := doctorReport{CheckedAt: now}
			doctorHeartbeat(&r, state.File{CheckedAt: tc.last}, control.File{Enabled: tc.enabled}, true, 30*time.Minute)
			if doctorCheckByID(t, r, "heartbeat").Status != tc.want {
				t.Fatalf("%+v", r)
			}
		})
	}
}

func TestDoctorMissingCLICorruptStateAndPermissions(t *testing.T) {
	f := newDoctorFixture(t)
	f.loop["bb"].(map[string]any)["cli"] = filepath.Join(f.root, "missing-cli")
	f.save(t)
	if err := os.Mkdir(f.state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.state, state.FileName), []byte(doctorPrivate), 0o600); err != nil {
		t.Fatal(err)
	}
	code, r, _ := f.run(t, successfulDoctorRun)
	if code != 1 || doctorCheckByID(t, r, "state").Status != "error" || doctorCheckByID(t, r, "agent.primary.cli").Status != "error" {
		t.Fatalf("%d %+v", code, r)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(f.config, 0o644); err != nil {
			t.Fatal(err)
		}
		_, r, _ = f.run(t, successfulDoctorRun, "--offline")
		if doctorCheckByID(t, r, "config.permissions").Status != "warning" {
			t.Fatal("public config not flagged")
		}
		info, _ := os.Stat(f.config)
		if info.Mode().Perm() != 0o644 {
			t.Fatal("doctor changed permissions")
		}
	}
}

func TestDoctorMissingActiveFallback(t *testing.T) {
	f := newDoctorFixture(t)
	if err := state.Save(f.state, state.File{ActiveTarget: "removed-fallback"}); err != nil {
		t.Fatal(err)
	}
	code, r, _ := f.run(t, successfulDoctorRun, "--offline")
	if code != 1 || r.TargetAgent != nil || doctorCheckByID(t, r, "target").Status != "error" {
		t.Fatalf("wrong fallback identity: %+v", r)
	}
}

func TestDoctorSkipsCustomCommands(t *testing.T) {
	f := newDoctorFixture(t)
	delete(f.loop, "bb")
	f.loop["adapter"] = "process"
	f.loop["process"] = map[string]any{"status_command": []string{f.executable, "status"}, "resume_command": []string{f.executable, "send"}}
	f.save(t)
	code, r, _ := f.run(t, func(context.Context, string, []string, map[string]string, string) ([]byte, error) {
		t.Fatal("custom process executed")
		return nil, nil
	})
	if code != 0 || doctorCheckByID(t, r, "agent.primary.cli").Status != "unavailable" {
		t.Fatalf("%d %+v", code, r)
	}
}

func TestDoctorVersionAndOutputLimit(t *testing.T) {
	for _, s := range []string{"1.2.3", "codex-cli 1.2.3", "2.3.4 (Claude Code)", "bb version v1.2.3"} {
		if doctorVersion([]byte(s)) == "" {
			t.Fatal(s)
		}
	}
	for _, s := range []string{doctorPrivate, "1.2.3 " + doctorPrivate, "1.2.3\n" + doctorPrivate, "1.2.3-" + doctorPrivate} {
		if doctorVersion([]byte(s)) != "" {
			t.Fatal("untrusted version accepted")
		}
	}
	buf := &doctorBuffer{remaining: 4}
	if _, err := io.Copy(buf, strings.NewReader("12345")); err == nil || !buf.exceeded || len(buf.Bytes()) > 4 {
		t.Fatal("output cap bypassed")
	}
}

func cursorDoctorFixture(t *testing.T, cursor map[string]any) *doctorFixture {
	t.Helper()
	// Presence of either variable in the process would skip the status query.
	t.Setenv("CURSOR_API_KEY", "")
	t.Setenv("CURSOR_AUTH_TOKEN", "")
	f := newDoctorFixture(t)
	delete(f.loop, "bb")
	f.loop["adapter"] = "cursor"
	f.loop["provider"] = "cursor"
	cursor["cli"], cursor["workdir"] = f.executable, f.root
	f.loop["cursor"] = cursor
	f.save(t)
	return f
}

func TestDoctorCursorWithEnvironmentCredentialQueriesOnlyVersion(t *testing.T) {
	for _, source := range []string{"config", "process", "token"} {
		t.Run(source, func(t *testing.T) {
			cursor := map[string]any{}
			if source == "config" {
				cursor["env"] = map[string]string{"CURSOR_API_KEY": doctorPrivate}
			}
			f := cursorDoctorFixture(t, cursor)
			switch source {
			case "process":
				t.Setenv("CURSOR_API_KEY", doctorPrivate)
			case "token":
				t.Setenv("CURSOR_AUTH_TOKEN", doctorPrivate)
			}
			calls := 0
			code, r, _ := f.run(t, func(_ context.Context, _ string, args []string, env map[string]string, dir string) ([]byte, error) {
				calls++
				if !reflect.DeepEqual(args, []string{"--version"}) || dir != f.root || (source == "config" && env["CURSOR_API_KEY"] != doctorPrivate) {
					t.Fatal("unexpected diagnostic command or environment")
				}
				return []byte("2030.01.02-abcdef0\n"), nil
			})
			login := doctorCheckByID(t, r, "agent.primary.login")
			if code != 0 || calls != 1 || login.Status != "unavailable" || !strings.Contains(login.Message, "API key or token") {
				t.Fatalf("%d calls=%d %+v", code, calls, r)
			}
		})
	}
	for _, bad := range []string{"2030.01.02-abcdef0\n" + doctorPrivate, "2030.01.02-secret", ""} {
		if doctorAgentVersion("cursor", []byte(bad)) != "" {
			t.Fatal("unrecognized version surfaced")
		}
	}
}

func TestDoctorCursorLoginVersionAndUnavailableCapabilities(t *testing.T) {
	// Fictional identity: none of it may reach the report.
	const identity = `"userInfo":{"email":"person@example.com","userId":424242,"firstName":"Example","lastName":"Person","createdAt":"2030-01-02T03:04:05.000Z"}`
	failed := errors.New("exit status 1")
	for _, tc := range []struct {
		name, status string
		err          error
		want, text   string
	}{
		{"signed_in", `{"status":"authenticated","isAuthenticated":true,"hasAccessToken":true,"hasRefreshToken":true,` + identity + `}`, nil, "ok", "signed-in account"},
		{"signed_in_user_id_only", `{"status":"authenticated","isAuthenticated":true,"hasAccessToken":true,"hasRefreshToken":true,"userInfo":{"userId":424242}}`, nil, "ok", "signed-in account"},
		// The CLI answers "authenticated" from stored tokens even when the account lookup failed.
		{"tokens_without_identity", `{"status":"authenticated","isAuthenticated":true,"hasAccessToken":true,"hasRefreshToken":true,"message":"` + doctorPrivate + `"}`, nil, "unavailable", "not confirmed"},
		{"empty_identity", `{"status":"authenticated","isAuthenticated":true,"hasAccessToken":true,"hasRefreshToken":true,"userInfo":{"email":"","userId":null}}`, nil, "unavailable", "not confirmed"},
		{"signed_in_but_command_failed", `{"status":"authenticated","isAuthenticated":true,"hasAccessToken":true,"hasRefreshToken":true,` + identity + `}`, failed, "unavailable", "could not be established"},
		{"signed_out", `{"status":"unauthenticated","isAuthenticated":false,"hasAccessToken":false,"hasRefreshToken":false,"message":"` + doctorPrivate + `"}`, nil, "warning", "no usable stored login"},
		{"signed_out_nonzero_exit", `{"status":"unauthenticated","isAuthenticated":false,"hasAccessToken":false,"hasRefreshToken":false}`, failed, "warning", "no usable stored login"},
		{"partial", `{"status":"partially-authenticated","isAuthenticated":false,"hasAccessToken":true,"hasRefreshToken":false}`, nil, "warning", "no usable stored login"},
		{"status_contradicts_flag", `{"status":"authenticated","isAuthenticated":false,"hasAccessToken":true,"hasRefreshToken":true,` + identity + `}`, nil, "unavailable", "could not be established"},
		{"flag_contradicts_status", `{"status":"unauthenticated","isAuthenticated":true,"hasAccessToken":true,"hasRefreshToken":true}`, nil, "unavailable", "could not be established"},
		{"missing_token_flag", `{"status":"authenticated","isAuthenticated":true,"hasAccessToken":true,` + identity + `}`, nil, "unavailable", "could not be established"},
		{"unknown_status", `{"status":"` + doctorPrivate + `","isAuthenticated":true,"hasAccessToken":true,"hasRefreshToken":true,` + identity + `}`, nil, "unavailable", "could not be established"},
		{"error_object", `{"status":"error","message":"` + doctorPrivate + `"}`, failed, "unavailable", "could not be established"},
		{"text_form", "✓ Logged in as person@example.com\n", nil, "unavailable", "could not be established"},
		{"not_an_object", `["authenticated"]`, nil, "unavailable", "could not be established"},
		{"empty", ``, failed, "unavailable", "could not be established"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := cursorDoctorFixture(t, map[string]any{"model": "example-model"})
			var calls [][]string
			code, r, raw := f.run(t, func(_ context.Context, _ string, args []string, _ map[string]string, dir string) ([]byte, error) {
				calls = append(calls, args)
				if dir != f.root {
					t.Fatal("diagnostic ran outside the configured project")
				}
				if reflect.DeepEqual(args, []string{"--version"}) {
					return []byte("2026.09.18-9a7762b\n"), nil
				}
				return []byte(tc.status), tc.err
			})
			if code != 0 || !reflect.DeepEqual(calls, [][]string{{"--version"}, {"status", "--format", "json"}}) {
				t.Fatalf("code=%d calls=%v", code, calls)
			}
			login := doctorCheckByID(t, r, "agent.primary.login")
			if login.Status != tc.want || !strings.Contains(login.Message, tc.text) {
				t.Fatalf("login %+v", login)
			}
			for _, private := range []string{"person@example.com", "424242", "Example", "2030-01-02T03"} {
				if strings.Contains(raw, private) {
					t.Fatalf("report contains account detail %q", private)
				}
			}
			version := doctorCheckByID(t, r, "agent.primary.version")
			if version.Status != "ok" || !strings.Contains(version.Message, "2026.09.18-9a7762b") || !strings.Contains(version.Message, "no live turn") {
				t.Fatalf("version %+v", version)
			}
			// Never guessed, whatever the login says.
			for id, text := range map[string]string{"agent.primary.account": "quota", "agent.primary.models": "not checked against the account", "agent.primary.workspace": "trust", "agent.primary.capabilities": "permission"} {
				if c := doctorCheckByID(t, r, id); c.Status != "unavailable" || !strings.Contains(strings.ToLower(c.Message), strings.ToLower(text)) {
					t.Fatalf("%s %+v", id, c)
				}
			}
		})
	}

	t.Run("other_version_and_default_model", func(t *testing.T) {
		f := cursorDoctorFixture(t, map[string]any{})
		code, r, _ := f.run(t, func(_ context.Context, _ string, args []string, _ map[string]string, _ string) ([]byte, error) {
			if reflect.DeepEqual(args, []string{"--version"}) {
				return []byte("2030.01.02-abcdef0\n"), nil
			}
			return nil, failed
		})
		version := doctorCheckByID(t, r, "agent.primary.version")
		models := doctorCheckByID(t, r, "agent.primary.models")
		if code != 0 || version.Status != "warning" || !strings.Contains(version.Message, "unverified") || models.Status != "unavailable" || !strings.Contains(models.Message, "Cursor's own default") {
			t.Fatalf("%d %+v %+v", code, version, models)
		}
	})

	t.Run("offline_queries_nothing", func(t *testing.T) {
		f := cursorDoctorFixture(t, map[string]any{})
		code, r, _ := f.run(t, func(context.Context, string, []string, map[string]string, string) ([]byte, error) {
			t.Fatal("offline doctor executed the CLI")
			return nil, nil
		}, "--offline")
		if code != 0 || doctorCheckByID(t, r, "agent.primary.login").Status != "unavailable" || doctorCheckByID(t, r, "agent.primary.version").Status != "unavailable" {
			t.Fatalf("%d %+v", code, r)
		}
	})
}
