package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/accounts"
	"github.com/looptender/looptender/internal/testexec"
)

func claudeQuotaFixture(data ...string) string {
	ids := []string{"quota-init", "quota-hooks", "quota-usage"}
	var out strings.Builder
	for i, value := range data {
		out.WriteString(`{"type":"control_response","response":{"subtype":"success","request_id":"` + ids[i] + `","response":` + value + "}}\n")
	}
	return out.String()
}

func TestClaudeQuotaOwnedProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix process fixture; control/parser tests are portable")
	}
	for _, mode := range []string{"reading", "unverified-wrapper", "timeout", "wrong-id", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			dir, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			cli := filepath.Join(dir, "claude-fixture")
			script := `#!/bin/sh
[ "$CLAUDE_CONFIG_DIR" = "$EXPECTED_HOME" ] || exit 21
[ "$PWD" = "$EXPECTED_WORK" ] || exit 22
[ "$BB_THREAD_ID" = thr_example_owner ] || exit 23
printf '%s\n' "$@" > "$EXPECTED_WORK/argv"
IFS= read -r init || exit 24
[ "$init" = '{"request":{"hooks":{},"mcpServers":{},"subtype":"initialize"},"request_id":"quota-init","type":"control_request"}' ] || exit 25
if [ "$FIXTURE_MODE" = timeout ]; then IFS= read -r unexpected; exit 26; fi
if [ "$FIXTURE_MODE" = oversize ]; then i=0; while [ "$i" -lt 7000 ]; do printf '%s' 'fictional-private'; i=$((i+1)); done; exit 27; fi
if [ "$FIXTURE_MODE" = wrong-id ]; then printf '%s\n' '{"type":"control_response","response":{"subtype":"success","request_id":"other","response":{}}}'; IFS= read -r unexpected; exit 28; fi
printf '%s\n' '{"type":"control_response","response":{"subtype":"success","request_id":"quota-init","response":{"claude_code_version":"2.1.326","hooks_applied":true,"session_state":"idle"}}}'
IFS= read -r hooks || exit 29
[ "$hooks" = '{"request":{"subtype":"get_hooks_listing"},"request_id":"quota-hooks","type":"control_request"}' ] || exit 30
printf '%s\n' '{"type":"control_response","response":{"subtype":"success","request_id":"quota-hooks","response":{"policy":{"allDisabled":true,"policyHookCount":0}}}}'
IFS= read -r usage || exit 31
[ "$usage" = '{"request":{"skip_behaviors":true,"subtype":"get_usage"},"request_id":"quota-usage","type":"control_request"}' ] || exit 32
printf '%s\n' '{"type":"control_response","response":{"subtype":"success","request_id":"quota-usage","response":{"rate_limits_available":true,"rate_limits":{"five_hour":{"utilization":42}},"behaviors":null}}}'
IFS= read -r unexpected
printf '%s\n' "$unexpected" > "$EXPECTED_WORK/unexpected"
exit 33
`
			testexec.Script(t, cli, script)
			limit := time.Second
			if mode == "timeout" {
				limit = 100 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), limit)
			defer cancel()
			start := time.Now()
			run := accountClaudeQuotaProcess
			if mode == "unverified-wrapper" {
				run = accountClaudeQuotaRead
			}
			raw, err := run(ctx, cli, []string{"CLAUDE_CONFIG_DIR=" + dir, "EXPECTED_HOME=" + dir, "EXPECTED_WORK=" + dir, "BB_THREAD_ID=thr_example_owner", "FIXTURE_MODE=" + mode}, dir)
			if time.Since(start) > 2*time.Second {
				t.Fatal("owned child cleanup exceeded bound")
			}
			if mode == "reading" {
				d, reason := accountClaudeQuotaParse(raw, err)
				if reason != "" || d.Buckets[0].Windows[0].UsedPercent != 42 {
					t.Fatalf("selected context read failed: %s %v", reason, err)
				}
			} else if err == nil || raw != nil {
				t.Fatalf("accepted %s", mode)
			}
			if mode == "timeout" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("timeout lost: %v", err)
			}
			argv, argvErr := os.ReadFile(filepath.Join(dir, "argv"))
			if mode == "unverified-wrapper" {
				if !errors.Is(argvErr, os.ErrNotExist) || err.Error() != "unverified_cli" {
					t.Fatal("unsupported CLI started parked session")
				}
			} else if argvErr != nil || string(argv) != strings.Join(accountClaudeQuotaArgs, "\n")+"\n" {
				t.Fatalf("unexpected launch options: %q %v", argv, argvErr)
			}
			if _, err := os.Stat(filepath.Join(dir, "unexpected")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("child received another request or unbounded graceful shutdown")
			}
		})
	}
}

const claudeQuotaInit = `{"claude_code_version":"2.1.326","hooks_applied":true,"session_state":"idle","account":{"email":"private@example.com"},"models":["private-model"]}`
const claudeQuotaHooks = `{"policy":{"allDisabled":true,"policyHookCount":0},"hooks":[{"commandText":"fictional private hook"}]}`
const claudeQuotaUsage = `{"rate_limits_available":true,"rate_limits":{"five_hour":{"utilization":0,"resets_at":null},"seven_day":{"utilization":64.5,"resets_at":"2030-01-07T00:00:00Z"}},"session":{"total_cost_usd":999,"model_usage":{"private-model":123}},"subscription_type":"private-plan","behaviors":null}`

func TestClaudeQuotaControlBoundary(t *testing.T) {
	good := claudeQuotaFixture(claudeQuotaInit, claudeQuotaHooks, claudeQuotaUsage)
	for _, tc := range []struct {
		name, response, reason string
		requests               int
	}{
		{"reported", good, "", 3},
		{"ui-invalidation", `{"type":"system","subtype":"ui_invalidate","event":"private-event","session_id":"private-session"}` + "\n" + good, "", 3},
		{"other-system", `{"type":"system","subtype":"init"}` + "\n" + good, "schema_unavailable", 1},
		{"version", strings.Replace(good, "2.1.326", "2.1.327", 1), "isolation_unavailable", 1},
		{"hooks-not-applied", strings.Replace(good, `"hooks_applied":true`, `"hooks_applied":false`, 1), "isolation_unavailable", 1},
		{"active", strings.Replace(good, `"idle"`, `"running"`, 1), "isolation_unavailable", 1},
		{"hooks-enabled", strings.Replace(good, `"allDisabled":true`, `"allDisabled":false`, 1), "isolation_unavailable", 2},
		{"managed-hook", strings.Replace(good, `"policyHookCount":0`, `"policyHookCount":1`, 1), "isolation_unavailable", 2},
		{"unreadable-policy", strings.Replace(good, `"policyHookCount":0`, `"policyHookCount":0,"policyUnreadable":true`, 1), "isolation_unavailable", 2},
		{"missing-policy", strings.Replace(good, `"policyHookCount":0`, `"ignored":0`, 1), "isolation_unavailable", 2},
		{"behavior-output", strings.Replace(good, `"behaviors":null`, `"behaviors":{"private":"transcript"}`, 1), "isolation_unavailable", 3},
		{"missing-skip-proof", strings.Replace(good, `,"behaviors":null`, ``, 1), "isolation_unavailable", 3},
		{"wrong-correlation", strings.Replace(good, `"quota-hooks"`, `"wrong-id"`, 1), "schema_unavailable", 2},
		{"duplicate-key", strings.Replace(good, `"allDisabled":true`, `"allDisabled":false,"allDisabled":true`, 1), "schema_unavailable", 2},
		{"case-alias", strings.Replace(good, `"allDisabled":true`, `"allDisabled":true,"AllDisabled":false`, 1), "isolation_unavailable", 2},
		{"permission-request", `{"type":"control_request","request_id":"private","request":{"subtype":"can_use_tool"}}` + "\n" + good, "schema_unavailable", 1},
		{"model-output", `{"type":"assistant","message":{"content":"private"}}` + "\n" + good, "schema_unavailable", 1},
		{"raw-error", strings.Replace(good, `"subtype":"success"`, `"subtype":"error","error":"private@example.com"`, 1), "status_unavailable", 1},
		{"oversize", strings.Repeat("x", 64<<10) + "\n" + good, "status_unavailable", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sent bytes.Buffer
			raw, err := accountClaudeQuotaExchange(strings.NewReader(tc.response), &sent)
			if tc.reason != "" {
				if err == nil || err.Error() != tc.reason || raw != nil {
					t.Fatalf("expected %s, got %q %v", tc.reason, raw, err)
				}
			} else if err != nil || !bytes.Contains(raw, []byte(`"utilization":64.5`)) {
				t.Fatalf("missing minimized reading: %q %v", raw, err)
			}
			lines := bytes.Split(bytes.TrimSpace(sent.Bytes()), []byte{'\n'})
			if len(lines) != tc.requests {
				t.Fatalf("sent %d requests, want %d", len(lines), tc.requests)
			}
			for i, line := range lines {
				var frame struct {
					Type    string `json:"type"`
					Request struct {
						Subtype string `json:"subtype"`
						Skip    *bool  `json:"skip_behaviors"`
					} `json:"request"`
				}
				if json.Unmarshal(line, &frame) != nil || frame.Type != "control_request" || frame.Request.Subtype != []string{"initialize", "get_hooks_listing", "get_usage"}[i] {
					t.Fatalf("unexpected request: %s", line)
				}
				if i == 2 && (frame.Request.Skip == nil || !*frame.Request.Skip) {
					t.Fatal("usage may scan persisted conversations")
				}
			}
			for _, secret := range []string{"private@example.com", "private-model", "private-plan", "fictional private hook", "total_cost_usd", "model_usage", "subscription_type", "behaviors"} {
				if bytes.Contains(raw, []byte(secret)) || (err != nil && strings.Contains(err.Error(), secret)) {
					t.Fatal("unallowlisted provider evidence escaped")
				}
			}
		})
	}
}

func TestClaudeQuotaScopes(t *testing.T) {
	for _, tc := range []struct {
		name, limits, reason string
		rows                 int
		partial              bool
	}{
		{"independent", `{"five_hour":{"utilization":0},"seven_day":{"utilization":64.5},"seven_day_opus":{"utilization":8},"seven_day_sonnet":{"utilization":9},"seven_day_oauth_apps":{"utilization":10}}`, "", 5, false},
		{"missing-percent", `{"five_hour":{"resets_at":null}}`, "schema_unavailable", 0, false},
		{"null-percent", `{"five_hour":{"utilization":null}}`, "schema_unavailable", 0, false},
		{"outside-range", `{"five_hour":{"utilization":101}}`, "schema_unavailable", 0, false},
		{"string-percent", `{"five_hour":{"utilization":"42"}}`, "schema_unavailable", 0, false},
		{"percent-alias", `{"five_hour":{"utilization":42,"Utilization":0}}`, "schema_unavailable", 0, false},
		{"scope-alias", `{"five_hour":{"utilization":42},"Five_Hour":{"utilization":0}}`, "schema_unavailable", 0, false},
		{"reset-invalid", `{"five_hour":{"utilization":42,"resets_at":"private@example.com"}}`, "schema_unavailable", 0, false},
		{"reset-null", `{"five_hour":{"utilization":42,"resets_at":null}}`, "", 1, false},
		{"no-evidence", `{"five_hour":null,"seven_day":null}`, "no_windows", 0, false},
		{"unknown-only", `{"unknown_window":{"utilization":42}}`, "no_windows", 0, false},
		{"partial", `{"five_hour":{"utilization":0},"model_scoped":[{"display_name":"private@example.com","utilization":99}],"extra_usage":{"monthly_limit":20,"currency":"USD"}}`, "", 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, reason := accountClaudeQuotaParse([]byte(`{"rate_limits_available":true,"rate_limits":`+tc.limits+`}`), nil)
			if reason != tc.reason {
				t.Fatalf("want %q, got %q", tc.reason, reason)
			}
			if reason != "" {
				if d != nil {
					t.Fatal("failed parsing retained data")
				}
				return
			}
			if d == nil || len(d.Buckets) != tc.rows || d.Partial != tc.partial {
				t.Fatalf("wrong independent evidence: %+v", d)
			}
			b, _ := json.Marshal(d)
			for _, private := range []string{"private@example.com", "USD", "monthly_limit", "model_scoped"} {
				if bytes.Contains(b, []byte(private)) {
					t.Fatal("private/unsupported data retained")
				}
			}
			if tc.rows == 5 && (d.Buckets[0].Key != "five_hour" || d.Buckets[0].Windows[0].UsedPercent != 0 || d.Buckets[1].Key != "seven_day" || d.Buckets[1].Windows[0].UsedPercent != 64.5) {
				t.Fatal("scope substitution or explicit zero lost")
			}
		})
	}
}

func TestClaudeQuotaPreloadRefusedBeforeLaunch(t *testing.T) {
	for _, key := range []string{"NODE_OPTIONS", "NODE_PATH", "BUN_OPTIONS", "BUN_INSPECT", "DYLD_INSERT_LIBRARIES", "LD_PRELOAD"} {
		t.Run(key, func(t *testing.T) {
			raw, err := accountClaudeQuotaRead(context.Background(), "fictional missing CLI", []string{key + "=fictional-injection", "BB_THREAD_ID=thr_example_owner"}, "")
			if raw != nil || err == nil || err.Error() != "isolation_unavailable" {
				t.Fatal("preload environment admitted", err)
			}
		})
	}
	if !accountClaudeQuotaEnvironment([]string{"CLAUDE_CONFIG_DIR=/example/selected", "CLAUDE_CODE_OAUTH_TOKEN=fictional-token", "BB_THREAD_ID=thr_example_owner"}) {
		t.Fatal("selected auth/caller context rejected")
	}
}

func TestClaudeQuotaRefreshSelectionAndSavedFailure(t *testing.T) {
	t.Setenv("BB_THREAD_ID", "thr_example_owner")
	for _, mode := range []string{"failure", "removed", "executable-replaced", "corrupt-cache"} {
		t.Run(mode, func(t *testing.T) {
			dir, entry := accountFixture(t, "claude-code")
			at := time.Now().UTC().Add(-time.Hour)
			catalog, _ := os.ReadFile(filepath.Join(dir, accounts.FileName))
			calls := 0
			run := func(ctx context.Context, provider, cli string, env []string, cwd string) ([]byte, error) {
				calls++
				if provider != "claude-code" || cli != entry.CLI || cwd != entry.Workdir || !strings.Contains(strings.Join(env, "\n"), "BB_THREAD_ID=thr_example_owner") {
					t.Fatal("selected context/caller provenance changed")
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("unbounded read")
				}
				return []byte(claudeQuotaUsage), nil
			}
			v, err := accountQuotaRefresh(context.Background(), dir, entry.ID, nil, run, os.Environ(), func() time.Time { return at })
			if err != nil || v.State != "reported" || v.Freshness != "provider_unknown" || v.ObservedAt != nil || v.RetrievedAt == nil || !v.RetrievedAt.Equal(at) {
				t.Fatalf("missing isolated selected reading: %+v %v", v, err)
			}
			after, _ := os.ReadFile(filepath.Join(dir, accounts.FileName))
			if !bytes.Equal(catalog, after) {
				t.Fatal("read changed catalog")
			}
			path := filepath.Join(dir, accounts.ClaudeQuotaName)
			before, _ := os.ReadFile(path)
			calls = 0
			if mode == "corrupt-cache" {
				before = bytes.ReplaceAll(before, []byte(`"usedPercent":0`), []byte(`"usedPercent":null`))
				if err := os.WriteFile(path, before, 0600); err != nil {
					t.Fatal(err)
				}
			}
			run = func(context.Context, string, string, []string, string) ([]byte, error) {
				calls++
				if mode == "failure" {
					return nil, context.DeadlineExceeded
				}
				if mode == "removed" {
					if err := accounts.Update(dir, func(f *accounts.File) error { f.Entries = []accounts.Entry{}; return nil }); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "executable-replaced" {
					if err := os.Rename(entry.CLI, entry.CLI+".old"); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(entry.CLI, []byte("fictional changed executable"), 0700); err != nil {
						t.Fatal(err)
					}
				}
				return []byte(claudeQuotaUsage), nil
			}
			v, err = accountQuotaRefresh(context.Background(), dir, entry.ID, nil, run, os.Environ(), func() time.Time { return at.Add(time.Hour) })
			if mode == "failure" {
				if err != nil || v.FailedRefreshReason != "timeout" || !v.RetrievedAt.Equal(at) || v.Freshness != "provider_unknown" {
					t.Fatal("failure changed original evidence", v, err)
				}
				return
			}
			after, _ = os.ReadFile(path)
			if err == nil || !bytes.Equal(before, after) || (mode == "corrupt-cache" && calls != 0) {
				t.Fatal("stale/damaged evidence published or queried", err, calls)
			}
		})
	}
}

func TestClaudeQuotaStagedAdmission(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable fixture")
	}
	for _, mode := range []string{"replace", "invalid", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			staging := t.TempDir()
			t.Setenv("TMPDIR", staging)
			source := filepath.Join(dir, "selected")
			marker := filepath.Join(dir, "ran-unverified")
			testexec.Script(t, source, "#!/bin/sh\nprintf verified")
			original, err := os.ReadFile(source)
			if err != nil {
				t.Fatal(err)
			}
			digest := fmt.Sprintf("%x", sha256.Sum256(original))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "invalid" {
				digest = strings.Repeat("0", 64)
			}
			if mode == "cancel" {
				cancel()
			}
			image, cleanup, err := accountClaudeQuotaStage(ctx, source, digest)
			if mode != "replace" {
				if err == nil {
					cleanup()
					t.Fatal("admitted invalid/cancelled image")
				}
				if image != "" {
					t.Fatal("returned failed admission image")
				}
				entries, readErr := os.ReadDir(staging)
				if readErr != nil || len(entries) != 0 {
					t.Fatalf("failed admission retained staging: %v %v", entries, readErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			info, err := os.Stat(image)
			if err != nil || info.Mode().Perm() != 0500 {
				t.Fatalf("image mode: %v %v", info, err)
			}
			info, err = os.Stat(filepath.Dir(image))
			if err != nil || info.Mode().Perm() != 0700 {
				t.Fatalf("directory mode: %v %v", info, err)
			}
			if err := os.Rename(source, source+".old"); err != nil {
				t.Fatal(err)
			}
			testexec.Script(t, source, "#!/bin/sh\nprintf unverified > '"+marker+"'")
			out, err := exec.Command(image).Output()
			if err != nil || string(out) != "verified" {
				t.Fatalf("staged launch: %q %v", out, err)
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("replacement executed")
			}
			cleanup()
			if _, err := os.Stat(filepath.Dir(image)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("staged image retained")
			}
		})
	}
}
