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
	"github.com/looptender/looptender/internal/lock"
	"github.com/looptender/looptender/internal/snapshot"
	"github.com/looptender/looptender/internal/testexec"
)

const quotaResponse = `{"rateLimits":{"limitId":"codex","primary":{"usedPercent":0,"windowDurationMins":300,"resetsAt":1900000000},"secondary":{"usedPercent":42},"credits":{"balance":"fictional-secret"},"planType":"plus","limitName":"private@example.com"},"accountId":"fictional-private-id"}`

func TestAccountQuotaResponseAllowlist(t *testing.T) {
	w, r := accountQuotaParse([]byte(quotaResponse), nil)
	if r != "" || len(w) != 2 || w[0].UsedPercent != 0 || w[1].UsedPercent != 42 {
		t.Fatal(w, r)
	}
	for _, tc := range []struct{ raw, reason string }{
		{`null`, "no_windows"}, {`{}`, "no_windows"}, {`{"rateLimits":null}`, "no_windows"},
		{`{"rateLimits":{"primary":null,"secondary":null}}`, "no_windows"},
		{`{"rateLimits":{"limitId":"other","primary":{"usedPercent":10}}}`, "unknown_bucket"},
		{`{"rateLimitsByLimitId":{"other":{"primary":{"usedPercent":10}}}}`, "unknown_bucket"},
		{`{"rateLimits":{"primary":{"usedPercent":1000}}}`, "schema_unavailable"},
		{`{"rateLimits":{"primary":{"usedPercent":-1}}}`, "schema_unavailable"},
		{`{"rateLimits":{"primary":{"usedPercent":null}}}`, "schema_unavailable"},
		{`{"rateLimits":{"primary":{"usedPercent":1.5}}}`, "schema_unavailable"},
		{`{"rateLimits":{"primary":{"usedPercent":1,"usedPercent":2}}}`, "schema_unavailable"},
		{`{"rateLimits":{"tertiary":{"usedPercent":1}}}`, "unknown_windows"},
		{`{"rateLimits":{"primary":{"usedPercent":1,"future":7}}}`, "unknown_windows"},
		{`{"rateLimits":{"primary":{"usedPercent":1,"windowDurationMins":0}}}`, "schema_unavailable"},
		{`{"rateLimits":{"primary":{"usedPercent":1,"resetsAt":-1}}}`, "schema_unavailable"},
		{`{"rateLimits":[]}`, "schema_unavailable"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			w, r := accountQuotaParse([]byte(tc.raw), nil)
			if len(w) != 0 || r != tc.reason {
				t.Fatal(w, r)
			}
		})
	}
	w, r = accountQuotaParse([]byte(`{"rateLimits":{"primary":{"usedPercent":90}},"rateLimitsByLimitId":{"codex":{"primary":{"usedPercent":7}},"other":{"primary":{"usedPercent":80}}}}`), nil)
	if r != "" || len(w) != 1 || w[0].UsedPercent != 7 {
		t.Fatal("buckets joined", w, r)
	}
	for _, tc := range []struct {
		err    error
		reason string
	}{{context.DeadlineExceeded, "timeout"}, {context.Canceled, "cancelled"}, {errors.New("unsupported"), "unsupported"}, {errors.New("fictional-sensitive-error"), "status_unavailable"}} {
		w, r := accountQuotaParse(nil, tc.err)
		if len(w) != 0 || r != tc.reason {
			t.Fatal(w, r)
		}
	}
}

func TestAccountQuotaSelectionPrivacyAndFailedRefresh(t *testing.T) {
	t.Setenv("BB_THREAD_ID", "thr_example_actual")
	t.Setenv("CODEX_HOME", "/fictional/inherited")
	dir, e := accountFixture(t, "codex")
	if err := accounts.Update(dir, func(f *accounts.File) error {
		e.Mode = "explicit_directory"
		e.Directory = e.Workdir
		e.Context = accounts.Context(*f, e, os.Environ())
		f.Entries[0] = e
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, accounts.FileName))
	at := time.Now().UTC().Add(-time.Hour)
	var failure error
	run := func(ctx context.Context, provider, cli string, env []string, wd string) ([]byte, error) {
		if cli != e.CLI || wd != e.Workdir {
			t.Fatal("wrong selection")
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 5*time.Second {
			t.Fatal("unbounded read")
		}
		homes := 0
		for _, v := range env {
			if strings.HasPrefix(v, "BB_THREAD_ID=") {
				t.Fatal("session passed")
			}
			if strings.HasPrefix(v, "CODEX_HOME=") {
				homes++
				if v != "CODEX_HOME="+e.Directory {
					t.Fatal("wrong home")
				}
			}
		}
		if homes != 1 {
			t.Fatal(homes)
		}
		return []byte(quotaResponse), failure
	}
	v, err := accountQuotaRefresh(context.Background(), dir, e.ID, nil, run, os.Environ(), func() time.Time { return at })
	if err != nil || v.State != "reported" {
		t.Fatal(v, err)
	}
	failure = context.DeadlineExceeded
	v, err = accountQuotaRefresh(context.Background(), dir, e.ID, nil, run, os.Environ(), time.Now)
	if err != nil || v.FailedRefreshReason != "timeout" || v.Freshness != "stale" || !v.ObservedAt.Equal(at) {
		t.Fatal(v, err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, accounts.FileName))
	if !bytes.Equal(before, after) {
		t.Fatal("catalog changed")
	}
	cache, _ := os.ReadFile(filepath.Join(dir, accounts.QuotaName))
	view, _ := json.Marshal(v)
	for _, secret := range []string{"fictional-secret", "fictional-private-id", "private@example.com", "thr_example_actual", e.Workdir} {
		if bytes.Contains(cache, []byte(secret)) || bytes.Contains(view, []byte(secret)) {
			t.Fatal("private evidence retained")
		}
	}
}

func TestAccountQuotaMutationAndUnavailableFences(t *testing.T) {
	for _, kind := range []string{"revision", "removed", "executable", "executable_replaced", "workdir", "directory", "symlink", "context", "corrupt"} {
		t.Run(kind, func(t *testing.T) {
			dir, e := accountFixture(t, "codex")
			if kind == "workdir" {
				workdir := filepath.Join(e.Workdir, "selected-workdir")
				if err := os.Mkdir(workdir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := accounts.Update(dir, func(f *accounts.File) error {
					e.Workdir = workdir
					e.Context = accounts.Context(*f, e, os.Environ())
					f.Entries[0] = e
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "directory" {
				e.Mode = "explicit_directory"
				e.Directory = filepath.Join(e.Workdir, "selected-account")
				if err := os.Mkdir(e.Directory, 0700); err != nil {
					t.Fatal(err)
				}
				if err := accounts.Update(dir, func(f *accounts.File) error {
					e.Context = accounts.Context(*f, e, os.Environ())
					f.Entries[0] = e
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "symlink" && runtime.GOOS == "windows" {
				t.Skip("Unix symlink fixture")
			}
			if kind == "context" {
				t.Setenv("CODEX_HOME", "/fictional/changed")
			}
			if kind == "corrupt" {
				if err := os.WriteFile(filepath.Join(dir, accounts.QuotaName), []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			run := func(context.Context, string, string, []string, string) ([]byte, error) {
				calls++
				switch kind {
				case "revision", "removed":
					if err := accounts.Update(dir, func(f *accounts.File) error {
						if kind == "removed" {
							f.Entries = []accounts.Entry{}
						} else {
							f.Entries[0].Revision = strings.Repeat("a", 32)
						}
						return nil
					}); err != nil {
						t.Fatal(err)
					}
				case "executable":
					if err := os.WriteFile(e.CLI, []byte("replacement binary"), 0700); err != nil {
						t.Fatal(err)
					}
				case "executable_replaced":
					info, err := os.Stat(e.CLI)
					if err != nil {
						t.Fatal(err)
					}
					raw, err := os.ReadFile(e.CLI)
					if err != nil {
						t.Fatal(err)
					}
					stage := e.CLI + "-replacement"
					if err := os.WriteFile(stage, raw, info.Mode().Perm()); err != nil {
						t.Fatal(err)
					}
					if err := os.Chtimes(stage, info.ModTime(), info.ModTime()); err != nil {
						t.Fatal(err)
					}
					if err := snapshot.Replace(stage, e.CLI); err != nil {
						t.Fatal(err)
					}
				case "workdir", "directory":
					path := e.Workdir
					if kind == "directory" {
						path = e.Directory
					}
					old := path + "-moved"
					if err := os.Rename(path, old); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { os.RemoveAll(old) })
					if err := os.Mkdir(path, 0700); err != nil {
						t.Fatal(err)
					}
				case "symlink":
					other := e.CLI + "-other"
					if err := os.Rename(e.CLI, other); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(other, e.CLI); err != nil {
						t.Fatal(err)
					}
				}
				return []byte(quotaResponse), nil
			}
			_, err := accountQuotaRefresh(context.Background(), dir, e.ID, nil, run, os.Environ(), time.Now)
			if err == nil {
				t.Fatal("changed selection published")
			}
			if (kind == "context" || kind == "corrupt") && calls != 0 {
				t.Fatal("query before validation")
			}
			if kind != "corrupt" {
				if _, err := os.Stat(filepath.Join(dir, accounts.QuotaName)); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("cache published", err)
				}
			}
		})
	}
}

func TestAccountQuotaSingleFlight(t *testing.T) {
	dir, e := accountFixture(t, "codex")
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		_, err := accountQuotaRefresh(context.Background(), dir, e.ID, nil, func(context.Context, string, string, []string, string) ([]byte, error) {
			close(entered)
			<-release
			return []byte(quotaResponse), nil
		}, os.Environ(), time.Now)
		done <- err
	}()
	<-entered
	_, err := accountQuotaRefresh(context.Background(), dir, e.ID, nil, func(context.Context, string, string, []string, string) ([]byte, error) {
		t.Error("overlapping read")
		return nil, nil
	}, os.Environ(), time.Now)
	close(release)
	if !errors.Is(err, lock.ErrHeld) {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestAccountQuotaRPC(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix stand-in execution; portable parser/cache coverage remains")
	}
	for _, kind := range []string{"ok", "wrong_id", "duplicate_id", "unsupported", "malformed", "timeout", "output_limit"} {
		t.Run(kind, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			cli := filepath.Join(root, "fixture-cli")
			script := `#!/bin/sh
read init
case "$init" in *'"method":"initialize"'*) ;; *) exit 3;; esac
printf '%s\n' '{"id":1,"result":{}}'
read initialized
case "$initialized" in *'"method":"initialized"'*) ;; *) exit 4;; esac
read request
case "$request" in *'"method":"account/rateLimits/read"'*'"excludeResetCreditDetails":true'*) ;; *'"excludeResetCreditDetails":true'*'"method":"account/rateLimits/read"'*) ;; *) exit 5;; esac
[ "$CODEX_HOME" = "$PWD" ] || exit 6
`
			switch kind {
			case "ok":
				script += `printf '%s\n' '{"id":2,"result":{"rateLimits":{"primary":{"usedPercent":0}}}}'`
			case "wrong_id":
				script += `printf '%s\n' '{"id":3,"result":{}}'`
			case "duplicate_id":
				script += `printf '%s\n' '{"id":2,"id":2,"result":{}}'`
			case "unsupported":
				script += `printf '%s\n' '{"id":2,"error":{"code":-32601,"message":"private@example.com"}}'`
			case "malformed":
				script += `printf '%s\n' '{'`
			case "timeout":
				script += `read extra`
			case "output_limit":
				script += `awk 'BEGIN {for(i=0;i<70000;i++)printf "x"; print ""}'`
			}
			testexec.Script(t, cli, script)
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			raw, err := accountQuotaRead(ctx, "codex", cli, []string{"PATH=" + os.Getenv("PATH"), "CODEX_HOME=" + root}, root)
			w, r := accountQuotaParse(raw, err)
			if kind == "ok" {
				if err != nil || r != "" || len(w) != 1 || w[0].UsedPercent != 0 {
					t.Fatal(w, r, err)
				}
			} else if r == "" || len(w) != 0 {
				t.Fatal("failure accepted", kind, w, r)
			}
			if kind == "timeout" && r != "timeout" {
				t.Fatal(r)
			}
			if kind == "unsupported" && r != "unsupported" {
				t.Fatal(r)
			}
		})
	}
}

// Opt-in, fictional browser input. It reads no installed catalog or provider.
func TestAccountQuotaBrowserFixture(t *testing.T) {
	destination := os.Getenv("LOOPTENDER_QUOTA_BROWSER_FIXTURE")
	if destination == "" {
		t.Skip("browser fixture not requested")
	}
	reader, _ := dockFixtureReader(t)
	dir, e := accountFixture(t, "codex")
	reader.accountsDir = dir
	at := reader.clock()
	if err := accounts.Update(dir, func(f *accounts.File) error {
		f.Entries[0].Label = "Example studio"
		o := accounts.Observation{Revision: e.Revision, Context: e.Context, At: at, Auth: "reported_present", Plan: "plus"}
		f.Entries[0].Last = &o
		f.Entries[0].Attempt = &o
		for _, label := range []string{"Example older reading", "Example unavailable", strings.Repeat("Example", 15)} {
			next := accounts.NewEntry(*f, e, "", at, os.Environ())
			next.Label = label
			f.Entries = append(f.Entries, next)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f, _ := accounts.Load(dir)
	for i, entry := range f.Entries {
		now := at.Add(-time.Minute)
		raw := []byte(quotaResponse)
		if i == 1 {
			now = at.Add(-2 * time.Hour)
			raw = []byte(strings.ReplaceAll(quotaResponse, "1900000000", fmt.Sprint(at.Add(-time.Hour).Unix())))
		}
		if i == 2 {
			raw = []byte(`{"rateLimits":null}`)
		}
		if _, err := accountQuotaRefresh(context.Background(), dir, entry.ID, nil, func(context.Context, string, string, []string, string) ([]byte, error) { return raw, nil }, os.Environ(), func() time.Time { return now }); err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			if _, err := accountQuotaRefresh(context.Background(), dir, entry.ID, nil, func(context.Context, string, string, []string, string) ([]byte, error) {
				return nil, context.DeadlineExceeded
			}, os.Environ(), func() time.Time { return at }); err != nil {
				t.Fatal(err)
			}
		}
	}
	report := reader.read(context.Background(), false)
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestAccountQuotaCommandRejectsImplicitSelection(t *testing.T) {
	for _, args := range [][]string{{"usage"}, {"usage", "--provider", "codex"}, {"usage", "--account-dir", "/example"}} {
		var out, errs bytes.Buffer
		if got := accountsCommand(env{context.Background(), &out, &errs}, args, nil); got != exitUsage {
			t.Fatal(args, got)
		}
	}
}

func TestAccountQuotaMissingCachedPercentageRefusesRPC(t *testing.T) {
	for _, kind := range []string{"missing", "null"} {
		t.Run(kind, func(t *testing.T) {
			dir, e := accountFixture(t, "codex")
			run := func(context.Context, string, string, []string, string) ([]byte, error) {
				return []byte(quotaResponse), nil
			}
			if _, err := accountQuotaRefresh(context.Background(), dir, e.ID, nil, run, os.Environ(), time.Now); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, accounts.QuotaName)
			raw, _ := os.ReadFile(path)
			var document map[string]any
			if err := json.Unmarshal(raw, &document); err != nil {
				t.Fatal(err)
			}
			entry := document["entries"].([]any)[0].(map[string]any)
			for _, field := range []string{"lastSuccess", "lastAttempt"} {
				w := entry[field].(map[string]any)["windows"].([]any)[0].(map[string]any)
				if kind == "missing" {
					delete(w, "usedPercent")
				} else {
					w["usedPercent"] = nil
				}
			}
			malformed, _ := json.Marshal(document)
			if err := os.WriteFile(path, malformed, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := accounts.LoadQuota(dir); !errors.Is(err, accounts.ErrInvalid) {
				t.Fatal("unknown percent accepted", err)
			}
			f, _ := accounts.Load(dir)
			view := accounts.QuotaViews(dir, f, os.Environ(), time.Now())[e.ID]
			if view.State != "unavailable" || view.Reason != "cache_unavailable" || len(view.Windows) != 0 {
				t.Fatal(view)
			}
			calls := 0
			_, err := accountQuotaRefresh(context.Background(), dir, e.ID, nil, func(context.Context, string, string, []string, string) ([]byte, error) {
				calls++
				return []byte(quotaResponse), nil
			}, os.Environ(), time.Now)
			after, _ := os.ReadFile(path)
			if !errors.Is(err, accounts.ErrInvalid) || calls != 0 || !bytes.Equal(malformed, after) {
				t.Fatal("malformed cache queried or replaced", calls, err)
			}
		})
	}
}

func TestAccountQuotaUnsupportedProviderIsUnavailable(t *testing.T) {
	dir, e := accountFixture(t, "claude-code")
	if err := accounts.Update(dir, func(f *accounts.File) error {
		e.Provider, e.Mode, e.CLI, e.Workdir = "other", "externally_managed", "", ""
		e.Context = accounts.Context(*f, e, os.Environ())
		f.Entries[0] = e
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, accounts.FileName))
	calls := 0
	v, err := accountQuotaRefresh(context.Background(), dir, e.ID, nil, func(context.Context, string, string, []string, string) ([]byte, error) { calls++; return nil, nil }, os.Environ(), time.Now)
	if err != nil || calls != 0 || v.State != "unavailable" || v.Reason != "unsupported_provider" || len(v.Windows) != 0 {
		t.Fatal(v, err, calls)
	}
	var out, errs bytes.Buffer
	if code := accountsUsage(env{context.Background(), &out, &errs}, dir, e.ID, true); code != exitFailed {
		t.Fatal("unavailable quota reported command success", code)
	}
	after, _ := os.ReadFile(filepath.Join(dir, accounts.FileName))
	if !bytes.Equal(before, after) || !strings.Contains(out.String(), `"reason":"unsupported_provider"`) || strings.Contains(out.String(), "usedPercent") {
		t.Fatal("unsupported-provider evidence changed catalog or implied usage")
	}
}
