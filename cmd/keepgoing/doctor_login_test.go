package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/control"
	"gopkg.in/yaml.v3"
)

func TestDoctorClaudeLoginResults(t *testing.T) {
	for _, tc := range []struct {
		name, raw, want string
		err             error
	}{
		{"logged in", `{"loggedIn":true,"email":"` + doctorPrivate + `"}`, "ok", nil},
		{"logged out", `{"loggedIn":false}`, "warning", nil},
		{"missing signal", `{}`, "unavailable", nil},
		{"null signal", `{"loggedIn":null}`, "unavailable", nil},
		{"wrong type", `{"loggedIn":"true"}`, "unavailable", nil},
		{"prose", doctorPrivate, "unavailable", nil},
		{"failed success", `{"loggedIn":true}`, "unavailable", errors.New(doctorPrivate)},
		{"timeout false", `{"loggedIn":false}`, "unavailable", context.DeadlineExceeded},
		{"failed false", `{"loggedIn":false}`, "unavailable", errors.New(doctorPrivate)},
		{"trailing output", `{"loggedIn":true}` + doctorPrivate, "unavailable", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := doctorReport{}
			doctorClaudeLogin(&r, "login", []byte(tc.raw), tc.err)
			c := doctorCheckByID(t, r, "login")
			b, _ := json.Marshal(r)
			if c.Status != tc.want || strings.Contains(string(b), doctorPrivate) {
				t.Fatalf("unexpected report: %s", b)
			}
		})
	}
}

func TestDoctorHeadlessLoginUsesTargetContextAndHonorsOffline(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		for _, mode := range []string{"normal", "offline", "extra_args", "missing_cli"} {
			t.Run(provider+"/"+mode, func(t *testing.T) {
				f := newDoctorFixture(t)
				delete(f.loop, "bb")
				f.loop["adapter"], f.loop["provider"] = provider, provider
				key := "CODEX_HOME"
				wantArgs := []string{"login", "status"}
				if provider == "claude" {
					key, wantArgs = "CLAUDE_CONFIG_DIR", []string{"auth", "status", "--json"}
				}
				account := filepath.Join(f.root, doctorPrivate)
				if err := os.Mkdir(account, 0o700); err != nil {
					t.Fatal(err)
				}
				agent := map[string]any{"cli": f.executable, "workdir": f.root, "env": map[string]string{key: account}}
				if mode == "extra_args" {
					agent["extra_args"] = []string{"--example-override"}
				}
				if mode == "missing_cli" {
					agent["cli"] = filepath.Join(f.root, "missing")
				}
				f.loop[provider] = agent
				f.save(t)
				if err := control.Save(f.state, control.File{Enabled: true, Completed: true, Note: doctorPrivate}); err != nil {
					t.Fatal(err)
				}
				before, _ := os.ReadFile(filepath.Join(f.state, control.FileName))
				var calls [][]string
				flags := []string{}
				if mode == "offline" {
					flags = append(flags, "--offline")
				}
				code, r, _ := f.run(t, func(ctx context.Context, cli string, args []string, env map[string]string, cwd string) ([]byte, error) {
					if cli != f.executable || env[key] != account || cwd != f.root {
						t.Fatal("target context was not preserved")
					}
					if _, ok := ctx.Deadline(); !ok {
						t.Fatal("missing command deadline")
					}
					calls = append(calls, args)
					if reflect.DeepEqual(args, []string{"--version"}) {
						return []byte("1.2.3"), nil
					}
					if !reflect.DeepEqual(args, wantArgs) {
						t.Fatalf("unexpected command %v", args)
					}
					return []byte(`{"loggedIn":true,"email":"` + doctorPrivate + `"}`), nil
				}, flags...)
				wantStatus, wantCode, wantCalls := "unavailable", 0, 0
				switch mode {
				case "normal":
					wantStatus, wantCalls = "ok", 2
				case "extra_args":
					wantCalls = 1
				case "missing_cli":
					wantCode = 1
				}
				if code != wantCode || len(calls) != wantCalls || doctorCheckByID(t, r, "agent.primary.login").Status != wantStatus {
					t.Fatalf("code=%d calls=%v report=%+v", code, calls, r)
				}
				after, _ := os.ReadFile(filepath.Join(f.state, control.FileName))
				if string(before) != string(after) {
					t.Fatal("diagnostics changed completion hold")
				}
			})
		}
	}
}

func TestDoctorCodexLoginFailureIsNotLoggedOut(t *testing.T) {
	for _, err := range []error{errors.New(doctorPrivate), context.DeadlineExceeded, context.Canceled} {
		r := doctorReport{}
		doctorCodexLogin(&r, "login", err)
		if c := doctorCheckByID(t, r, "login"); c.Status != "unavailable" || strings.Contains(c.Message, doctorPrivate) {
			t.Fatalf("%+v", c)
		}
	}
}

func TestDoctorLoginKeepsFallbackAccountsSeparate(t *testing.T) {
	f := newDoctorFixture(t)
	var candidates []map[string]any
	for _, id := range []string{"account-a", "account-b", "disabled-account"} {
		dir := filepath.Join(f.root, id)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		candidates = append(candidates, map[string]any{
			"id": id, "adapter": "codex", "disabled": id == "disabled-account",
			"codex": map[string]any{"cli": f.executable, "workdir": dir, "env": map[string]string{"CODEX_HOME": dir}},
		})
	}
	b, err := yaml.Marshal(map[string]any{"version": 1, "candidates": candidates})
	if err != nil {
		t.Fatal(err)
	}
	fallbackPath := filepath.Join(f.root, "fallbacks.yaml")
	if err := os.WriteFile(fallbackPath, b, 0o600); err != nil {
		t.Fatal(err)
	}
	f.loop["fallbacks"] = fallbackPath
	f.save(t)
	var accounts []string
	code, r, _ := f.run(t, func(ctx context.Context, cli string, args []string, env map[string]string, cwd string) ([]byte, error) {
		if !reflect.DeepEqual(args, []string{"login", "status"}) {
			return successfulDoctorRun(ctx, cli, args, env, cwd)
		}
		if cwd != env["CODEX_HOME"] {
			t.Fatal("account/project mismatch")
		}
		accounts = append(accounts, filepath.Base(cwd))
		if filepath.Base(cwd) == "account-b" {
			return []byte(doctorPrivate), errors.New(doctorPrivate)
		}
		return []byte(doctorPrivate), nil
	})
	if code != 0 || !reflect.DeepEqual(accounts, []string{"account-a", "account-b"}) ||
		doctorCheckByID(t, r, "agent.account-a.login").Status != "ok" ||
		doctorCheckByID(t, r, "agent.account-b.login").Status != "unavailable" {
		t.Fatalf("accounts=%v report=%+v", accounts, r)
	}
}
