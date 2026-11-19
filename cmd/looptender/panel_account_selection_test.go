package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/accounts"
)

func TestPanelAccountSelectionRecoveryAndSeparateQuota(t *testing.T) {
	for _, provider := range []string{"codex", "claude-code"} {
		t.Run(provider, func(t *testing.T) { testPanelSelectionRecovery(t, provider) })
	}
}
func testPanelSelectionRecovery(t *testing.T, provider string) {
	if provider == "claude-code" && (runtime.GOOS != "darwin" || runtime.GOARCH != "arm64") {
		t.Skip("Claude quota acquisition capability requires Darwin arm64; local recovery negatives run independently")
	}
	for _, mode := range []string{"inherited", "explicit_directory"} {
		t.Run(mode, func(t *testing.T) {
			key := "CODEX_SESSION_ID"
			if provider == "claude-code" {
				key = "CLAUDE_CODE_OAUTH_TOKEN"
			}
			t.Setenv(key, "fictional-terminal-session")
			t.Setenv("BB_THREAD_ID", "thr_example_panel_owner")
			dir, e := accountFixture(t, provider)
			if mode == "explicit_directory" {
				if err := accounts.Update(dir, func(f *accounts.File) error {
					e.Mode, e.Directory = mode, e.Workdir
					e.Context = accounts.Context(*f, e, os.Environ())
					f.Entries[0] = e
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			run := func(context.Context, string, string, []string, string) ([]byte, error) {
				calls++
				if provider == "claude-code" {
					return []byte(claudeQuotaUsage), nil
				}
				return []byte(quotaResponse), nil
			}
			if _, err := accountQuotaRefresh(context.Background(), dir, e.ID, nil, run, os.Environ(), time.Now); err != nil {
				t.Fatal(err)
			}
			cacheName := accounts.QuotaName
			if provider == "claude-code" {
				cacheName = accounts.ClaudeQuotaName
			}
			cached, err := os.ReadFile(filepath.Join(dir, cacheName))
			if err != nil {
				t.Fatal(err)
			}
			catalogBefore, _ := os.ReadFile(filepath.Join(dir, accounts.FileName))
			c, _ := newPanelChanges(context.Background(), "", &dockReader{accountsDir: dir}, time.Now(), time.Now)
			// This is the actual panel launch allowlist, not a hand-edited context.
			c.quotaEnv = panelServerEnvironment()
			c.quotaRun = run
			f, _ := accounts.Load(dir)
			before := accounts.QuotaViews(dir, f, c.quotaEnv, time.Now())[e.ID]
			if before.Reason != "selection_review_required" {
				t.Fatal(before)
			}
			p := c.prepareAccountSelection("session", e.ID)
			if p.Preview == nil || calls != 1 {
				t.Fatal(p, calls)
			}
			afterPrepare, _ := os.ReadFile(filepath.Join(dir, accounts.FileName))
			if !bytes.Equal(catalogBefore, afterPrepare) {
				t.Fatal("prepare wrote catalog")
			}
			raw, _ := json.Marshal(p)
			if bytes.Contains(raw, []byte("fictional-terminal-session")) || bytes.Contains(raw, []byte("thr_example_panel_owner")) {
				t.Fatal("environment or provenance disclosed")
			}
			r := c.confirmAccountSelection("session", e.ID, p.Preview.Form)
			if r.Result == nil || r.Result.Outcome != "done" || calls != 1 {
				t.Fatal(r, calls)
			}
			f, _ = accounts.Load(dir)
			updated := *accounts.Find(&f, e.ID)
			if updated.Revision == e.Revision || updated.Context == e.Context || updated.UpdatedBy != "thr_example_panel_owner" || updated.Last != nil || updated.Attempt != nil {
				t.Fatal("revision/provenance/observation recovery failed")
			}
			fixed := updated
			fixed.Revision, fixed.Context, fixed.UpdatedAt, fixed.UpdatedBy = e.Revision, e.Context, e.UpdatedAt, e.UpdatedBy
			if !reflect.DeepEqual(fixed, e) {
				t.Fatal("selection fields changed")
			}
			afterCache, _ := os.ReadFile(filepath.Join(dir, cacheName))
			if !bytes.Equal(cached, afterCache) {
				t.Fatal("recovery changed saved quota")
			}
			v := accounts.QuotaViews(dir, f, c.quotaEnv, time.Now())[e.ID]
			if v.Reason != "context_changed" {
				t.Fatal("old cache must allow a separate refresh, not another recovery", v)
			}
			r = c.confirmAccountSelection("session", e.ID, p.Preview.Form)
			if r.Result.Outcome != "review_again" {
				t.Fatal("replay accepted")
			}
			q := c.prepareAccountQuota("session", e.ID)
			if q.Preview == nil || calls != 1 {
				t.Fatal(q, calls)
			}
			r = c.confirmAccountQuota("session", e.ID, q.Preview.Form)
			if r.Result.Outcome != "done" || calls != 2 {
				t.Fatal(r, calls)
			}
			v = accounts.QuotaViews(dir, f, c.quotaEnv, time.Now())[e.ID]
			if v.State != "reported" || (provider == "claude-code" && (v.Freshness != "provider_unknown" || v.RetrievedAt == nil || v.ObservedAt != nil)) {
				t.Fatal(v)
			}
		})
	}
}

func TestPanelAccountSelectionReviewDriftAndSpentForms(t *testing.T) {
	for _, provider := range []string{"codex", "claude-code"} {
		t.Run(provider, func(t *testing.T) { testPanelSelectionDrift(t, provider) })
	}
}
func testPanelSelectionDrift(t *testing.T, provider string) {
	for _, kind := range []string{"revision", "label", "provider", "environment", "caller", "cli", "workdir", "expired", "cancelled", "busy", "session", "quota_ticket", "selection_ticket"} {
		t.Run(kind, func(t *testing.T) {
			dir, e := accountFixture(t, provider)
			if kind == "workdir" {
				wd := filepath.Join(e.Workdir, "reviewed-workdir")
				if err := os.Mkdir(wd, 0700); err != nil {
					t.Fatal(err)
				}
				if err := accounts.Update(dir, func(f *accounts.File) error {
					e.Workdir = wd
					e.Context = accounts.Context(*f, e, os.Environ())
					f.Entries[0] = e
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			now := time.Now().UTC()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c, _ := newPanelChanges(ctx, "", &dockReader{accountsDir: dir}, now, func() time.Time { return now })
			calls := 0
			c.quotaRun = func(context.Context, string, string, []string, string) ([]byte, error) {
				calls++
				return []byte(quotaResponse), nil
			}
			p := c.prepareAccountSelection("session", e.ID)
			if p.Preview == nil {
				t.Fatal(p)
			}
			session := "session"
			switch kind {
			case "revision", "label", "provider":
				if err := accounts.Update(dir, func(f *accounts.File) error {
					if kind == "revision" {
						f.Entries[0].Revision = strings.Repeat("a", 32)
					} else if kind == "provider" {
						f.Entries[0].Provider = "claude-code"
						if provider == "claude-code" {
							f.Entries[0].Provider = "codex"
						}
					} else {
						f.Entries[0].Label = "Different workshop"
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			case "environment":
				c.quotaEnv = append(c.quotaEnv, "OPENAI_API_KEY=fictional-sensitive-value")
			case "caller":
				c.caller = "thr_example_other"
			case "cli":
				if err := os.WriteFile(e.CLI, []byte("changed executable content"), 0700); err != nil {
					t.Fatal(err)
				}
			case "workdir":
				// The spelling stays identical, but the reviewed directory identity changes.
				moved := e.Workdir + "-old"
				if err := os.Rename(e.Workdir, moved); err != nil {
					t.Fatal(err)
				}
				defer os.RemoveAll(moved)
				if err := os.Mkdir(e.Workdir, 0700); err != nil {
					t.Fatal(err)
				}
			case "expired":
				now = now.Add(panelHoldFormTTL)
			case "cancelled":
				cancel()
			case "session":
				session = "other"
			case "quota_ticket":
				if !panelQuotaProviderSupported(provider) {
					t.Skip("quota action unavailable on this platform; recovery checks still execute")
				}
				q := c.prepareAccountQuota("session", e.ID)
				if q.Preview == nil {
					t.Fatal(q)
				}
				p = q
			case "selection_ticket":
				r := c.confirmAccountQuota("session", e.ID, p.Preview.Form)
				if r.Result.Outcome != "review_again" {
					t.Fatal(r)
				}
			}
			var release func()
			if kind == "busy" {
				g, err := accounts.CheckGuard(dir, e.ID)
				if err != nil {
					t.Fatal(err)
				}
				release = func() { g.Release() }
				defer release()
			}
			before, _ := os.ReadFile(filepath.Join(dir, accounts.FileName))
			r := c.confirmAccountSelection(session, e.ID, p.Preview.Form)
			if r.Result == nil || r.Result.Outcome == "done" || calls != 0 {
				t.Fatal(r, calls)
			}
			after, _ := os.ReadFile(filepath.Join(dir, accounts.FileName))
			if !bytes.Equal(before, after) {
				t.Fatal("refusal published catalog")
			}
			r = c.confirmAccountSelection("session", e.ID, p.Preview.Form)
			if r.Result.Outcome != "review_again" {
				t.Fatal("refusal did not spend form", r)
			}
		})
	}
}

func TestPanelAccountSelectionUnsupportedProviderRefuses(t *testing.T) {
	dir, _ := accountFixture(t, "codex")
	if err := accounts.Update(dir, func(f *accounts.File) error {
		e := f.Entries[0]
		e.Provider, e.Mode, e.CLI, e.Workdir = "other", "externally_managed", "", ""
		f.Entries[0] = accounts.NewEntry(*f, e, "thr_example_owner", time.Now(), os.Environ())
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f, _ := accounts.Load(dir)
	before, _ := os.ReadFile(filepath.Join(dir, accounts.FileName))
	c, _ := newPanelChanges(context.Background(), "", &dockReader{accountsDir: dir}, time.Now(), time.Now)
	r := c.prepareAccountSelection("session", f.Entries[0].ID)
	after, _ := os.ReadFile(filepath.Join(dir, accounts.FileName))
	if r.Preview != nil || r.Result == nil || r.Result.Outcome != "refused" || !bytes.Equal(before, after) {
		t.Fatal("unsupported provider recovery accepted", r)
	}
}

func TestPanelAccountSelectionHTTPAuthorization(t *testing.T) {
	dir, e := accountFixture(t, "codex")
	c, _ := newPanelChanges(context.Background(), "", &dockReader{accountsDir: dir}, time.Now(), time.Now)
	s, _ := newPanelState(54321, time.Hour, time.Now)
	session, _ := s.newSession()
	h := &panelHandler{state: s, guard: newDockGuard(54321), page: newDockServer(54321, s, c.reader), changes: c}
	before, _ := os.ReadFile(filepath.Join(dir, accounts.FileName))
	for _, kind := range []string{"origin", "metadata", "cookie", "csrf", "read_only", "path_override", "alias", "wrong_action", "valid"} {
		body := `{"action":"review_selection"}`
		if kind == "path_override" {
			body = `{"action":"review_selection","executable":"fictional"}`
		}
		if kind == "alias" {
			body = `{"Action":"review_selection"}`
		}
		if kind == "wrong_action" {
			body = `{"action":"subscription_quota"}`
		}
		r := httptest.NewRequest(http.MethodPost, s.base()+s.pagePath()+panelAccountSelectionPrefix+e.ID+"/prepare", strings.NewReader(body))
		r.Header.Set("Origin", s.base())
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		r.Header.Set("Sec-Fetch-Dest", "empty")
		r.Header.Set("Sec-Fetch-Mode", "cors")
		r.Header.Set(panelFormHeader, c.prepareValue(session))
		r.AddCookie(&http.Cookie{Name: panelCookieName, Value: session})
		switch kind {
		case "origin":
			r.Header.Set("Origin", "https://example.com")
		case "metadata":
			r.Header.Del("Sec-Fetch-Site")
		case "cookie":
			r.Header.Del("Cookie")
		case "csrf":
			r.Header.Set(panelFormHeader, "wrong")
		case "read_only":
			h.changes = nil
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		h.changes = c
		if (w.Code == 200) != (kind == "valid") {
			t.Fatal(kind, w.Code, w.Body.String())
		}
	}
	after, _ := os.ReadFile(filepath.Join(dir, accounts.FileName))
	if !bytes.Equal(before, after) {
		t.Fatal("HTTP preparation wrote catalog")
	}
	for _, on := range []bool{false, true} {
		h.changes = nil
		if on {
			h.changes = c
		}
		w := httptest.NewRecorder()
		h.served(w)(session)
		var v dockServed
		_ = json.Unmarshal(w.Body.Bytes(), &v)
		if v.AccountSelectionReview != on {
			t.Fatal("wrong recovery capability")
		}
	}
}
