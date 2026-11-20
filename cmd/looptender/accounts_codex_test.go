package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/accounts"
	"github.com/looptender/looptender/internal/testexec"
)

func TestAccountsCodexPlanShapes(t *testing.T) {
	for _, plan := range []string{"free", "go", "plus", "pro", "prolite", "promax", "team", "self_serve_business_prolite", "self_serve_business_usage_based", "business", "ent26", "enterprise_cbp_automation", "enterprise_cbp_usage_based", "enterprise", "edu", "edu_plus", "edu_pro"} {
		got, reason := accountCodexPlan([]byte(fmt.Sprintf(`{"account":{"type":"chatgpt","planType":%q,"email":"private@example.com","accountId":"fictional-private-id"}}`, plan)), nil)
		if got != plan || reason != "" {
			t.Fatalf("%s: %q %q", plan, got, reason)
		}
	}
	for _, tc := range []struct{ raw, reason string }{
		{`{"account":null}`, "no_account"}, {`{"account":{"type":"apiKey"}}`, "api_key"},
		{`{"account":{"type":"amazonBedrock"}}`, "account_type"},
		{`{"account":{"type":"chatgpt","planType":"unknown"}}`, "not_recognized"},
		{`{"account":{"type":"chatgpt","planType":"private@example.com"}}`, "not_recognized"},
		{`{"account":{"type":"chatgpt","planType":null}}`, "not_recognized"},
		{`{"account":{"type":"chatgpt","planType":123}}`, "schema_unavailable"},
		{`{}`, "schema_unavailable"}, {`null`, "schema_unavailable"}, {`{`, "schema_unavailable"},
	} {
		got, reason := accountCodexPlan([]byte(tc.raw), nil)
		if got != "" || reason != tc.reason {
			t.Fatalf("%s: %q %q", tc.raw, got, reason)
		}
	}
}

func TestAccountsCodexPlanIndependentAndStale(t *testing.T) {
	t.Setenv("CODEX_HOME", "/fictional/inherited-home")
	dir, e := accountFixture(t, "codex")
	if err := accounts.Update(dir, func(f *accounts.File) error {
		e.Mode, e.Directory = "explicit_directory", e.Workdir
		e.Context = accounts.Context(*f, e, os.Environ())
		f.Entries[0] = e
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-2 * time.Hour)
	response := `{"account":{"type":"chatgpt","planType":"plus","email":"private@example.com","id":"fictional-private-id","token":"fictional-secret"}}`
	var planErr error
	run := func(_ context.Context, cli string, args, env []string, cwd string) ([]byte, error) {
		if cli != e.CLI || cwd != e.Workdir {
			t.Fatal("wrong selection")
		}
		homes := 0
		for _, v := range env {
			if strings.HasPrefix(v, "CODEX_HOME=") {
				homes++
				if v != "CODEX_HOME="+e.Directory {
					t.Fatal("inherited account used")
				}
			}
		}
		if homes != 1 {
			t.Fatal("selected CODEX_HOME missing or duplicated")
		}
		switch args[0] {
		case "--version":
			return []byte("codex-cli 0.77.0"), nil
		case "login":
			return nil, nil
		case "app-server":
			return []byte(response), planErr
		}
		t.Fatal("unexpected call")
		return nil, nil
	}
	check := func(at time.Time) accounts.Card {
		t.Helper()
		if err := accountCheck(context.Background(), dir, e.ID, "thr_example_owner", run, at); err != nil {
			t.Fatal(err)
		}
		f, err := accounts.Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		return accounts.Project(f, at).Cards[0]
	}
	c := check(old)
	if c.Plan.Value != "plus" || c.Plan.Source != "codex account/read" {
		t.Fatalf("missing plan %+v", c.Plan)
	}
	planErr = context.DeadlineExceeded
	c = check(old.Add(time.Hour))
	if c.Auth.State != "reported_present" || c.Auth.Freshness != "fresh" || c.Plan.Value != "plus" || c.Plan.Freshness != "stale" || c.Plan.Reason != "timeout" || !c.Plan.ObservedAt.Equal(old) {
		t.Fatalf("independent evidence lost: %+v", c)
	}
	c = check(old.Add(90 * time.Minute))
	if !c.Plan.ObservedAt.Equal(old) {
		t.Fatal("repeated failure refreshed plan")
	}
	planErr = nil
	response = `{"account":{"type":"apiKey"}}`
	c = check(old.Add(100 * time.Minute))
	if c.Plan.Value != "" || c.Plan.Missing != "api_key" || c.Auth.State != "reported_present" {
		t.Fatalf("stale plan survived account type change: %+v", c)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, accounts.FileName))
	projected, _ := json.Marshal(c)
	for _, secret := range []string{"private@example.com", "fictional-private-id", "fictional-secret"} {
		if bytes.Contains(raw, []byte(secret)) || bytes.Contains(projected, []byte(secret)) {
			t.Fatal("private data retained")
		}
	}
}

func TestAccountsCodexRPC(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix stand-in execution; parsing tests are portable")
	}
	for _, kind := range []string{"plus", "wrong_id", "unsupported", "malformed", "failure", "timeout", "output_limit"} {
		t.Run(kind, func(t *testing.T) {
			dir, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			cli := filepath.Join(dir, "codex-fixture")
			script := `#!/bin/sh
[ "$1" = app-server ] || exit 21
[ "$CODEX_HOME" = "$EXPECTED_HOME" ] || exit 22
[ "$PWD" = "$EXPECTED_WORK" ] || exit 23
IFS= read -r init
[ "$init" = '{"id":1,"method":"initialize","params":{"clientInfo":{"name":"looptender","version":"1"}}}' ] || exit 24
printf '%s\n' '{"id":1,"result":{}}'
IFS= read -r ready
[ "$ready" = '{"method":"initialized"}' ] || exit 25
IFS= read -r request
[ "$request" = '{"id":2,"method":"account/read","params":{"refreshToken":false}}' ] || exit 26
`
			switch kind {
			case "plus":
				script += `printf '%s\n' '{"method":"account/updated","params":{"planType":"pro"}}' '{"id":2,"result":{"account":{"type":"chatgpt","planType":"plus","email":"private@example.com"}}}'` + "\nIFS= read -r extra\nexit 27\n"
			case "wrong_id":
				script += `printf '%s\n' '{"id":3,"result":{"account":{"type":"chatgpt","planType":"pro"}}}'`
			case "unsupported":
				script += `printf '%s\n' '{"id":2,"error":{"code":-32601,"message":"private@example.com"}}'`
			case "malformed":
				script += `printf '%s\n' '{bad'`
			case "failure":
				script += "exit 1"
			case "timeout":
				script += "IFS= read -r extra"
			case "output_limit":
				script += `i=0; while [ "$i" -lt 7000 ]; do printf '%s\n' 'fictional-private-output'; i=$((i+1)); done`
			}
			testexec.Script(t, cli, script)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if kind == "timeout" {
				var c context.CancelFunc
				ctx, c = context.WithTimeout(ctx, 100*time.Millisecond)
				defer c()
			}
			raw, err := accountExec(ctx, cli, []string{"app-server"}, []string{"CODEX_HOME=" + dir, "EXPECTED_HOME=" + dir, "EXPECTED_WORK=" + dir}, dir)
			plan, reason := accountCodexPlan(raw, err)
			if kind == "plus" {
				if plan != "plus" || reason != "" {
					t.Fatalf("%q %q %v", plan, reason, err)
				}
			} else if plan != "" || reason == "" {
				t.Fatalf("accepted %s: %q %q", kind, plan, reason)
			}
			if kind == "timeout" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("timeout: %v", err)
			}
			if kind == "unsupported" && reason != "unsupported" {
				t.Fatalf("unsupported: %s", reason)
			}
			if err != nil && strings.Contains(err.Error(), "private@example.com") {
				t.Fatal("raw error leaked")
			}
		})
	}
}
