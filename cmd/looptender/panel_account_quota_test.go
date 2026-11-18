package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/accounts"
)

func TestPanelAccountQuotaGuardsAndSingleUseReview(t *testing.T) {
	dir, e := accountFixture(t, "codex")
	reader := &dockReader{accountsDir: dir}
	c, err := newPanelChanges(context.Background(), "", reader, time.Now(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	c.quotaRun = func(context.Context, string, []string, string) ([]byte, error) {
		calls++
		return []byte(quotaResponse), nil
	}
	s, err := newPanelState(54321, time.Hour, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	session, _ := s.newSession()
	h := &panelHandler{state: s, guard: newDockGuard(54321), page: newDockServer(54321, s, reader), changes: c}
	request := func(step, form, cookie, body string, edit func(*http.Request)) (int, panelChangeReply) {
		r := httptest.NewRequest(http.MethodPost, s.base()+s.pagePath()+panelAccountQuotaPrefix+e.ID+"/"+step, strings.NewReader(body))
		r.Header.Set("Origin", s.base())
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		r.Header.Set("Sec-Fetch-Dest", "empty")
		r.Header.Set("Sec-Fetch-Mode", "cors")
		r.Header.Set(panelFormHeader, form)
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: panelCookieName, Value: cookie})
		}
		if edit != nil {
			edit(r)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var reply panelChangeReply
		_ = json.Unmarshal(w.Body.Bytes(), &reply)
		return w.Code, reply
	}
	body := `{"action":"subscription_quota"}`
	for _, kind := range []string{"origin", "metadata", "cookie", "csrf", "read_only", "unknown_field", "wrong_action"} {
		b := body
		cookie := session
		form := c.prepareValue(session)
		var edit func(*http.Request)
		switch kind {
		case "origin":
			edit = func(r *http.Request) { r.Header.Set("Origin", "https://example.com") }
		case "metadata":
			edit = func(r *http.Request) { r.Header.Del("Sec-Fetch-Site") }
		case "cookie":
			cookie = ""
		case "csrf":
			form = "wrong"
		case "read_only":
			h.changes = nil
		case "unknown_field":
			b = `{"action":"subscription_quota","accountDirectory":"fictional"}`
		case "wrong_action":
			b = `{"action":"resume"}`
		}
		status, _ := request("prepare", form, cookie, b, edit)
		h.changes = c
		if status == 200 {
			t.Fatalf("%s accepted", kind)
		}
	}
	if calls != 0 {
		t.Fatal("guard failure queried provider")
	}
	status, p := request("prepare", c.prepareValue(session), session, body, nil)
	if status != 200 || p.Preview == nil || calls != 0 {
		t.Fatal(status, p, calls)
	}
	if _, err := accounts.LoadQuota(dir); err != nil {
		t.Fatal(err)
	}
	_, result := request("confirm", p.Preview.Form, session, body, nil)
	if result.Result == nil || result.Result.Outcome != "done" || calls != 1 {
		t.Fatal(result, calls)
	}
	_, result = request("confirm", p.Preview.Form, session, body, nil)
	if result.Result == nil || result.Result.Outcome != "review_again" || calls != 1 {
		t.Fatal("replay read", result, calls)
	}
	_, p = request("prepare", c.prepareValue(session), session, body, nil)
	other, _ := s.newSession()
	_, result = request("confirm", p.Preview.Form, other, body, nil)
	if result.Result.Outcome != "review_again" || calls != 1 {
		t.Fatal("session crossover")
	}
	_, result = request("confirm", p.Preview.Form, session, body, nil)
	if result.Result.Outcome != "review_again" || calls != 1 {
		t.Fatal("foreign use did not spend review")
	}
	_, p = request("prepare", c.prepareValue(session), session, body, nil)
	if err := accounts.Update(dir, func(f *accounts.File) error { f.Entries[0].Revision = strings.Repeat("b", 32); return nil }); err != nil {
		t.Fatal(err)
	}
	_, result = request("confirm", p.Preview.Form, session, body, nil)
	if result.Result == nil || result.Result.Outcome != "refused" || calls != 1 {
		t.Fatal("changed selection queried provider", result, calls)
	}
	// The page capability is present only with the existing write authorization.
	for _, on := range []bool{false, true} {
		h.changes = nil
		if on {
			h.changes = c
		}
		w := httptest.NewRecorder()
		h.served(w)(session)
		var served dockServed
		_ = json.Unmarshal(w.Body.Bytes(), &served)
		if served.AccountQuota != on {
			t.Fatal("wrong capability", served)
		}
	}
}

func TestPanelAccountQuotaExpiredAndChangedExecutable(t *testing.T) {
	for _, kind := range []string{"expired", "cli", "context"} {
		t.Run(kind, func(t *testing.T) {
			dir, e := accountFixture(t, "codex")
			now := time.Now().UTC()
			c, _ := newPanelChanges(context.Background(), "", &dockReader{accountsDir: dir}, now, func() time.Time { return now })
			calls := 0
			c.quotaRun = func(context.Context, string, []string, string) ([]byte, error) {
				calls++
				return []byte(quotaResponse), nil
			}
			p := c.prepareAccountQuota("session", e.ID)
			if p.Preview == nil {
				t.Fatal(p)
			}
			switch kind {
			case "expired":
				now = now.Add(panelHoldFormTTL)
			case "cli":
				_ = os.WriteFile(e.CLI, []byte("different executable"), 0700)
			case "context":
				c.quotaEnv = append(c.quotaEnv, "OPENAI_API_KEY=fictional-new-context")
			}
			r := c.confirmAccountQuota("session", e.ID, p.Preview.Form)
			if r.Result == nil || r.Result.Outcome == "done" || calls != 0 {
				t.Fatal(r, calls)
			}
		})
	}
}

// The native controls confirmation accepts complete authority text up to
// 1024 UTF-8 bytes. Its smoke contract independently requires the full CLI
// statement, not an ellipsis.
func TestPanelAccountQuotaExposureFitsNativeConfirmation(t *testing.T) {
	if len(panelExposure) > 1024 {
		t.Fatal("native controls confirmation would refuse the authority statement")
	}
}
