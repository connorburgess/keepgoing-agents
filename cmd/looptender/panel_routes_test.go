//go:build unix

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/accounts"
	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/control"
)

func routePanelFixture(t *testing.T) (*routesFixture, *panelChanges, accounts.Entry) {
	t.Helper()
	r := newRoutesFixture(t)
	dir, a := accountFixture(t, "claude-code")
	c, err := newPanelChanges(context.Background(), r.registry, &dockReader{}, time.Now(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	c.caller = ""
	c.routeCatalog = dir
	return r, c, a
}
func TestPanelRoutePreviewSaveAndSingleUse(t *testing.T) {
	r, c, a := routePanelFixture(t)
	before, _ := os.ReadFile(r.own.cfg)
	p := c.prepareRoutes("page", panelRouteInput{Op: "add", Account: a.ID, Model: "example-large", Limit: "owner-selected-limit", Behavior: config.EnterHeadless})
	if p.Outcome != "preview" || p.Form == "" || p.AccountLabels[p.Changes[0].Route] != a.Label {
		t.Fatalf("preview: %+v", p)
	}
	if _, err := os.Stat(r.list); !os.IsNotExist(err) {
		t.Fatal("preview wrote route list")
	}
	out := c.confirmRoutes("page", p.Form, "add")
	if out.Outcome != "changed" || out.AccountLabels[out.Changes[0].Route] != a.Label {
		t.Fatalf("save: %+v", out)
	}
	saved := r.read()
	if !strings.Contains(saved, "account: "+a.ID) || !strings.Contains(saved, "group: owner-selected-limit") {
		t.Fatal(saved)
	}
	if again := c.confirmRoutes("page", p.Form, "add"); again.Outcome != "review_again" || r.read() != saved {
		t.Fatalf("replay: %+v", again)
	}
	after, _ := os.ReadFile(r.own.cfg)
	if string(before) != string(after) {
		t.Fatal("route save enrolled a loop or changed its settings")
	}
}
func TestPanelRouteCallerStaleAccountAndListRefuseWrites(t *testing.T) {
	for _, change := range []string{"caller", "account", "list", "session", "expiry"} {
		t.Run(change, func(t *testing.T) {
			r, c, a := routePanelFixture(t)
			input := panelRouteInput{Op: "add", Account: a.ID, Model: "example-large", Limit: "declared-limit", Behavior: config.EnterHeadless}
			if change == "caller" {
				c.caller = "thr_fictional_owner"
				p := c.prepareRoutes("page", input)
				if p.Outcome != "refused" {
					t.Fatalf("caller gained authority: %+v", p)
				}
				return
			}
			p := c.prepareRoutes("page", input)
			if p.Form == "" {
				t.Fatalf("preview %+v", p)
			}
			page := "page"
			switch change {
			case "account":
				if err := accounts.Update(c.routeCatalog, func(f *accounts.File) error {
					f.Entries[0] = accounts.Revise(*f, f.Entries[0], f.Entries[0], "", time.Now(), os.Environ())
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			case "list":
				write(t, r.list, routesStandardList, 0600)
			case "session":
				page = "other-page"
			case "expiry":
				c.now = func() time.Time { return time.Now().Add(3 * time.Minute) }
			}
			old, _ := os.ReadFile(r.list)
			out := c.confirmRoutes(page, p.Form, "add")
			if out.Outcome != "review_again" {
				t.Fatalf("%s: %+v", change, out)
			}
			after, _ := os.ReadFile(r.list)
			if string(after) != string(old) {
				t.Fatal("stale review wrote")
			}
		})
	}
}
func TestPanelRouteMoveReviewBindsFollowingLoopConfiguration(t *testing.T) {
	r, c, _ := routePanelFixture(t)
	write(t, r.list, routesStandardList, 0600)
	p := c.prepareRoutes("page", panelRouteInput{Op: "move", ID: "codex-new", To: 1})
	if p.Form == "" {
		t.Fatalf("preview %+v", p)
	}
	b, err := os.ReadFile(r.follower)
	if err != nil {
		t.Fatal(err)
	}
	write(t, r.follower, string(b)+"# concurrent edit\n", 0600)
	out := c.confirmRoutes("page", p.Form, "move")
	if out.Outcome != "review_again" || r.read() != routesStandardList {
		t.Fatalf("stale loop: %+v", out)
	}
}
func TestPanelRouteStrictRequestsAndForgery(t *testing.T) {
	_, c, _ := routePanelFixture(t)
	for _, raw := range []string{`{"op":"read","op":"add"}`, `{"Op":"read"}`, `{"op":"read","caller":""}`, `{"op":"read"} {}`, `{"op":"add","to":"1"}`} {
		if _, ok := panelReadRoute(strings.NewReader(raw)); ok {
			t.Fatalf("accepted %s", raw)
		}
	}
	for _, step := range []string{"read", "models", "prepare"} {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"op":"read"}`))
		req.Header.Set(panelFormHeader, "wrong")
		rec := httptest.NewRecorder()
		c.serveRoutes(rec, req, panelRoutesPrefix+step, "page")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("forgery %s: %d", step, rec.Code)
		}
	}
}
func TestPanelRouteInvalidSemanticsAndPathsStayPrivate(t *testing.T) {
	_, c, a := routePanelFixture(t)
	for _, input := range []panelRouteInput{
		{Op: "add", Account: a.ID, Model: "example-large", Limit: "limit", Behavior: config.EnterNewConversation},
		{Op: "add", Account: a.ID, Model: "example-large", Limit: "", Behavior: config.EnterHeadless},
		{Op: "add", Account: "not-an-account", Model: "example-large", Limit: "limit", Behavior: config.EnterHeadless},
		{Op: "add", Account: a.ID, Model: "example-large; run", Limit: "limit", Behavior: config.EnterHeadless},
	} {
		p := c.prepareRoutes("page", input)
		if p.Outcome != "refused" || p.Form != "" {
			t.Fatalf("accepted %+v: %+v", input, p)
		}
	}
	ed := c.routeEditor()
	b, _ := json.Marshal(ed)
	if strings.Contains(string(b), filepath.Dir(c.routeCatalog)) || strings.Contains(string(b), "accountDirectory") || strings.Contains(string(b), "configPath") {
		t.Fatal("private path in editor")
	}
}

func TestPanelRoutePartialFollowReportsEveryLoopAndStops(t *testing.T) {
	requests := []routeChangeRequest{{op: "follow_standard", loop: "example-one"}, {op: "follow_standard", loop: "example-two"}, {op: "follow_standard", loop: "example-three"}}
	calls := 0
	reply := panelApplyReviewedRoutes(requests, func(i int, req routeChangeRequest) routeChangeOut {
		calls++
		out := newRouteChangeOut(req)
		if i == 0 {
			out.Outcome = "changed"
		} else {
			out.Outcome = "review_again"
		}
		return out
	})
	if calls != 2 || reply.Outcome != "partial" || len(reply.Changes) != 3 || reply.Changes[0].Outcome != "changed" || reply.Changes[1].Outcome != "review_again" || reply.Changes[2].Outcome != "not_attempted" || reply.Changes[2].Loop != "example-three" {
		t.Fatalf("partial report: calls=%d %+v", calls, reply)
	}
}
func TestPanelRouteFollowPreservesSettingsAndConcurrentChange(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(map[bool]string{false: "save", true: "stale"}[stale], func(t *testing.T) {
			r, c, _ := routePanelFixture(t)
			write(t, r.list, routesStandardList, 0600)
			e := c.routeEditor()
			key := ""
			for _, l := range e.Loops {
				if l.View.Loop == "example-loop" {
					key = l.Key
				}
			}
			stateDir := filepath.Join(r.own.dir, "state")
			if err := control.Save(stateDir, control.File{Enabled: true, Paused: true, AwaitingUser: true, EpisodeHold: true, Note: "Example allowance hold"}); err != nil {
				t.Fatal(err)
			}
			heldBefore, _ := os.ReadFile(filepath.Join(stateDir, control.FileName))
			before, _ := os.ReadFile(r.own.cfg)
			p := c.prepareRoutes("page", panelRouteInput{Op: "follow_standard", Loops: []string{key}})
			if p.Form == "" {
				t.Fatalf("preview %+v", p)
			}
			if stale {
				write(t, r.own.cfg, string(before)+"# concurrent configuration edit\n", 0600)
			}
			out := c.confirmRoutes("page", p.Form, "follow_standard")
			if stale {
				if out.Outcome != "review_again" {
					t.Fatalf("stale %+v", out)
				}
				return
			}
			if out.Outcome != "changed" {
				t.Fatalf("save %+v", out)
			}
			heldAfter, _ := os.ReadFile(filepath.Join(stateDir, control.FileName))
			if string(heldAfter) != string(heldBefore) {
				t.Fatal("route enrollment changed a hold or control record")
			}
			after, _ := os.ReadFile(r.own.cfg)
			want := strings.Replace(string(before), "  fallbacks: fallbacks.yaml\n", "  routes: standard\n", 1)
			if string(after) != want {
				t.Fatalf("unreviewed setting changed: %s", after)
			}
		})
	}
}

func TestPanelRouteEndpointUsesExistingNetworkAndSessionGuards(t *testing.T) {
	for _, changes := range []bool{false, true} {
		t.Run(map[bool]string{false: "read-only", true: "changes-on"}[changes], func(t *testing.T) {
			w := newPanelChangeWorld(t, changes)
			resp, body := dockRequest(t, http.MethodGet, w.page+"routes-help.html", withCookie(w.cookie))
			if resp.StatusCode != 200 || !strings.Contains(body, "Open an owner panel") {
				t.Fatal("local route help unavailable")
			}
			for _, bad := range []string{"none", "origin", "session", "form", "content"} {
				resp, _ := w.post(t, "ignored", "prepare", w.form, `{"op":"unsupported"}`, func(req *http.Request) {
					req.URL.Path = strings.Replace(req.URL.Path, "api/v1/loops/ignored/prepare", panelRoutesPrefix+"prepare", 1)
					switch bad {
					case "origin":
						req.Header.Set("Origin", "https://example.com")
					case "session":
						req.Header.Del("Cookie")
					case "form":
						req.Header.Set(panelFormHeader, "wrong")
					case "content":
						req.Header.Set("Content-Type", "text/plain")
					}
				})
				want := http.StatusForbidden
				if changes && bad == "none" {
					want = http.StatusOK
				}
				if resp.StatusCode != want {
					t.Fatalf("%s status=%d want=%d", bad, resp.StatusCode, want)
				}
			}
		})
	}
}
