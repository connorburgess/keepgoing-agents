package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const antigravityUsageFixture = `{
  "conversation_id": "",
  "status": "SUCCESS",
  "response": "unused prose",
  "duration_seconds": 0,
  "num_turns": 0,
  "usage": {"input_tokens": 0, "output_tokens": 0, "thinking_tokens": 0, "cache_read_tokens": 0, "total_tokens": 0},
  "command": {
    "name": "usage",
    "data": {
      "description": "Within each group, models share a weekly limit and a 5-hour limit.",
      "groups": [
        {
          "name": "Gemini Models",
          "description": "Gemini models",
          "buckets": [
            {"id": "gemini-weekly", "name": "Weekly Limit Remaining", "window": "weekly", "remaining_fraction": 0.75, "reset_time": "2026-11-26T23:56:55Z"},
            {"id": "gemini-5h", "name": "Five Hour Limit Remaining", "window": "5h", "remaining_fraction": 1, "reset_time": "2026-11-20T04:56:55Z"}
          ]
        },
        {
          "name": "Claude and GPT models",
          "description": "Partner models",
          "buckets": [
            {"id": "3p-weekly", "name": "Weekly Limit Remaining", "window": "weekly", "remaining_fraction": 0.5, "reset_time": "2026-11-26T23:56:55Z"},
            {"id": "3p-5h", "name": "Five Hour Limit Remaining", "window": "5h", "remaining_fraction": 0.1, "reset_time": "2026-11-20T04:56:55Z"}
          ]
        }
      ]
    }
  }
}`

func TestAccountAntigravityQuotaParse(t *testing.T) {
	d, reason := accountAntigravityQuotaParse([]byte(antigravityUsageFixture), nil)
	if reason != "" || d == nil || len(d.Buckets) != 4 || d.Partial || d.Omitted != 0 {
		t.Fatalf("parse: reason=%q details=%+v", reason, d)
	}
	by := map[string]float64{}
	for _, b := range d.Buckets {
		if len(b.Windows) != 1 || b.Windows[0].DurationMinutes == nil {
			t.Fatalf("bucket %s windows=%v", b.Key, b.Windows)
		}
		by[b.Key] = b.Windows[0].UsedPercent
	}
	if by["gemini-weekly"] != 25 || by["gemini-5h"] != 0 || by["3p-weekly"] != 50 || by["3p-5h"] != 90 {
		t.Fatalf("used percents: %#v", by)
	}
}

func TestAccountAntigravityQuotaParseErrors(t *testing.T) {
	cases := []struct {
		name string
		raw  []byte
		err  error
		want string
	}{
		{"timeout", nil, context.DeadlineExceeded, "timeout"},
		{"cancelled", nil, context.Canceled, "cancelled"},
		{"output limit", nil, errAntigravityQuotaOutputLimit, "schema_unavailable"},
		{"exit", nil, &exec.ExitError{}, "quota_unavailable"},
		{"other", nil, errors.New("boom"), "status_unavailable"},
		{"bad json", []byte(`{`), nil, "schema_unavailable"},
		{"wrong command", []byte(`{"status":"SUCCESS","num_turns":0,"command":{"name":"models","data":{}}}`), nil, "schema_unavailable"},
		{"non-success status", []byte(`{"status":"ERROR","num_turns":0,"command":{"name":"usage","data":{"groups":[{"buckets":[{"id":"gemini-5h","window":"5h","remaining_fraction":1,"reset_time":"2026-11-20T04:56:55Z"}]}]}}}`), nil, "schema_unavailable"},
		{"missing status", []byte(`{"num_turns":0,"command":{"name":"usage","data":{"groups":[{"buckets":[{"id":"gemini-5h","window":"5h","remaining_fraction":1,"reset_time":"2026-11-20T04:56:55Z"}]}]}}}`), nil, "schema_unavailable"},
		{"nonzero num_turns", []byte(`{"status":"SUCCESS","num_turns":1,"command":{"name":"usage","data":{"groups":[{"buckets":[{"id":"gemini-5h","window":"5h","remaining_fraction":1,"reset_time":"2026-11-20T04:56:55Z"}]}]}}}`), nil, "schema_unavailable"},
		{"missing num_turns", []byte(`{"status":"SUCCESS","command":{"name":"usage","data":{"groups":[{"buckets":[{"id":"gemini-5h","window":"5h","remaining_fraction":1,"reset_time":"2026-11-20T04:56:55Z"}]}]}}}`), nil, "schema_unavailable"},
		{"missing remaining_fraction", []byte(`{"status":"SUCCESS","num_turns":0,"command":{"name":"usage","data":{"groups":[{"buckets":[{"id":"gemini-5h","window":"5h","reset_time":"2026-11-20T04:56:55Z"}]}]}}}`), nil, "schema_unavailable"},
		{"null remaining_fraction", []byte(`{"status":"SUCCESS","num_turns":0,"command":{"name":"usage","data":{"groups":[{"buckets":[{"id":"gemini-5h","window":"5h","remaining_fraction":null,"reset_time":"2026-11-20T04:56:55Z"}]}]}}}`), nil, "schema_unavailable"},
		{"bad fraction > 1", []byte(`{"status":"SUCCESS","num_turns":0,"command":{"name":"usage","data":{"groups":[{"buckets":[{"id":"gemini-5h","window":"5h","remaining_fraction":1.5,"reset_time":"2026-11-20T04:56:55Z"}]}]}}}`), nil, "schema_unavailable"},
		{"bad fraction < 0", []byte(`{"status":"SUCCESS","num_turns":0,"command":{"name":"usage","data":{"groups":[{"buckets":[{"id":"gemini-5h","window":"5h","remaining_fraction":-0.1,"reset_time":"2026-11-20T04:56:55Z"}]}]}}}`), nil, "schema_unavailable"},
		{"wrong window", []byte(`{"status":"SUCCESS","num_turns":0,"command":{"name":"usage","data":{"groups":[{"buckets":[{"id":"gemini-5h","window":"weekly","remaining_fraction":1,"reset_time":"2026-11-20T04:56:55Z"}]}]}}}`), nil, "schema_unavailable"},
		{"no groups", []byte(`{"status":"SUCCESS","num_turns":0,"command":{"name":"usage","data":{"groups":[]}}}`), nil, "schema_unavailable"},
		{"only unknown bucket", []byte(`{"status":"SUCCESS","num_turns":0,"command":{"name":"usage","data":{"groups":[{"buckets":[{"id":"future-bucket","window":"5h","remaining_fraction":1,"reset_time":"2026-11-20T04:56:55Z"}]}]}}}`), nil, "no_windows"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, reason := accountAntigravityQuotaParse(tc.raw, tc.err)
			if d != nil || reason != tc.want {
				t.Fatalf("got details=%v reason=%q want %q", d, reason, tc.want)
			}
		})
	}
}

func TestAccountAntigravityQuotaParseUnknownExtraBucketSetsPartialAndOmitted(t *testing.T) {
	raw := []byte(`{"status":"SUCCESS","num_turns":0,"command":{"name":"usage","data":{"groups":[{"buckets":[{"id":"gemini-5h","window":"5h","remaining_fraction":0.8,"reset_time":"2026-11-20T04:56:55Z"},{"id":"future-bucket","window":"monthly","remaining_fraction":0.5,"reset_time":"2026-12-01T00:00:00Z"}]}]}}}`)
	d, reason := accountAntigravityQuotaParse(raw, nil)
	if reason != "" || d == nil || len(d.Buckets) != 1 || !d.Partial || d.Omitted != 1 {
		t.Fatalf("got details=%+v reason=%q", d, reason)
	}
}

func TestAccountAntigravityQuotaParseResetTimezone(t *testing.T) {
	raw := []byte(`{"status":"SUCCESS","num_turns":0,"command":{"name":"usage","data":{"groups":[{"buckets":[{"id":"gemini-5h","window":"5h","remaining_fraction":1,"reset_time":"2026-11-20T04:56:55+07:00"}]}]}}}`)
	d, reason := accountAntigravityQuotaParse(raw, nil)
	if reason != "" || d == nil || len(d.Buckets) != 1 || d.Buckets[0].Windows[0].ResetsAt == nil {
		t.Fatalf("parse: %q %#v", reason, d)
	}
	want := time.Date(2026, 11, 19, 21, 56, 55, 0, time.UTC)
	if !d.Buckets[0].Windows[0].ResetsAt.Equal(want) {
		t.Fatalf("reset %s want %s", d.Buckets[0].Windows[0].ResetsAt, want)
	}
}

func TestAccountAntigravityQuotaReadBoundsOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-agy")
	body := "#!/bin/sh\nhead -c 70000 /dev/zero | tr '\\0' 'a'\n"
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := accountAntigravityQuotaRead(context.Background(), script, os.Environ(), dir)
	if !errors.Is(err, errAntigravityQuotaOutputLimit) || raw != nil {
		t.Fatalf("got raw=%d err=%v, want errAntigravityQuotaOutputLimit", len(raw), err)
	}
	if d, reason := accountAntigravityQuotaParse(raw, err); d != nil || reason != "schema_unavailable" {
		t.Fatalf("got details=%v reason=%q, want schema_unavailable", d, reason)
	}
}

func TestAccountsAddRejectsAntigravityExplicitDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	dir := t.TempDir()
	cli := filepath.Join(dir, "agy")
	if err := os.WriteFile(cli, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	var out, errBuf strings.Builder
	e := env{ctx: context.Background(), stdout: &out, stderr: &errBuf}
	code := accountsCommand(e, []string{"add", "--directory", filepath.Join(dir, "accounts"), "--label", "Example Antigravity", "--provider", "antigravity", "--selection", "explicit_directory", "--cli", cli, "--account-dir", dir, "--workdir", dir}, nil)
	if code != exitUsage {
		t.Fatalf("got exit %d stderr=%q, want exitUsage (%d)", code, errBuf.String(), exitUsage)
	}
}
