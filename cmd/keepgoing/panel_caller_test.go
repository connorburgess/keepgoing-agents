//go:build unix

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/registry"
)

func panelCallerWorld(t *testing.T, reg string) panelChangeWorld {
	t.Helper()
	dir := filepath.Join(panelShortDir(t), "panel")
	done := panelServeAt(t, dir, reg, true)
	t.Cleanup(func() {
		_, _ = panelControl(context.Background(), dir, panelRequest{Op: "stop"})
		panelStoppedWithin(t, done, "caller fixture")
	})
	rec, ok := panelLive(context.Background(), dir)
	if !ok {
		t.Fatal("panel unavailable")
	}
	page, cookie := panelSession(t, dir, rec)
	_, body := dockRequest(t, http.MethodGet, page+dockServerPath, withCookie(cookie))
	var served dockServed
	if err := json.Unmarshal([]byte(body), &served); err != nil {
		t.Fatal(err)
	}
	return panelChangeWorld{dir: dir, page: page, rec: rec, cookie: cookie, origin: "http://127.0.0.1:" + strconv.Itoa(rec.Port), form: served.Form}
}

func TestPanelCallerResumeOwnershipThroughHTTP(t *testing.T) {
	for _, tc := range []struct {
		name, caller, owner string
		allowed             bool
	}{
		{"cross-owner", "thr_example_agent", "thr_example_other", false},
		{"same-owner", "thr_example_agent", "thr_example_agent", true},
		{"person", "", "thr_example_other", true},
		{"unowned", "thr_example_agent", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BB_THREAD_ID", tc.caller)
			w := newPanelChangeWorld(t, true)
			e := w.entry(t, "billing-api-migration")
			if _, err := control.Update(e.StateDir, func(c *control.File) {
				if tc.owner != "" {
					c.Ownership = &control.Ownership{ThreadID: tc.owner, Generation: 1}
				}
			}); err != nil {
				t.Fatal(err)
			}
			before := controlOf(t, e)
			// A later agent opening cannot replace the startup thread. Its socket
			// request is allowed by the unchanged same-class opening rule.
			if tc.caller != "" {
				t.Setenv("BB_THREAD_ID", "thr_example_later")
				w.page, w.cookie = panelSession(t, w.dir, w.rec)
				_, body := dockRequest(t, http.MethodGet, w.page+dockServerPath, withCookie(w.cookie))
				var served dockServed
				if err := json.Unmarshal([]byte(body), &served); err != nil {
					t.Fatal(err)
				}
				w.form = served.Form
			}
			_, p := w.post(t, dockKey(e.ID), "prepare", w.form, `{"action":"resume"}`, nil)
			if !tc.allowed {
				if p.Preview != nil || p.Result == nil || p.Result.Outcome != "refused" {
					t.Fatalf("%+v", p)
				}
				if !reflect.DeepEqual(before, controlOf(t, e)) {
					t.Fatal("refusal changed controls")
				}
				if _, err := os.Stat(filepath.Join(e.StateDir, clientResumeFile)); !os.IsNotExist(err) {
					t.Fatalf("refused review made ticket: %v", err)
				}
				return
			}
			if p.Preview == nil {
				t.Fatalf("%+v", p)
			}
			raw, _ := json.Marshal(p)
			if strings.Contains(string(raw), "thr_example_") {
				t.Fatal("review exposed caller/owner")
			}
			_, r := w.post(t, dockKey(e.ID), "confirm", p.Preview.Form, `{"action":"resume"}`, nil)
			after := controlOf(t, e)
			if r.Result == nil || r.Result.Outcome != "done" || after.AwaitingUser || !reflect.DeepEqual(before.Ownership, after.Ownership) {
				t.Fatalf("%+v %+v", r, after)
			}
		})
	}
}

func TestPanelCallerResumeRechecksOwnershipAtConfirm(t *testing.T) {
	t.Setenv("BB_THREAD_ID", "thr_example_owner")
	w := newPanelChangeWorld(t, true)
	e := w.entry(t, "billing-api-migration")
	if _, err := control.Update(e.StateDir, func(c *control.File) { c.Ownership = &control.Ownership{ThreadID: "thr_example_owner", Generation: 1} }); err != nil {
		t.Fatal(err)
	}
	_, p := w.post(t, dockKey(e.ID), "prepare", w.form, `{"action":"resume"}`, nil)
	if p.Preview == nil {
		t.Fatalf("%+v", p)
	}
	if _, err := control.Update(e.StateDir, func(c *control.File) { c.Ownership = &control.Ownership{ThreadID: "thr_example_other", Generation: 2} }); err != nil {
		t.Fatal(err)
	}
	before := controlOf(t, e)
	_, r := w.post(t, dockKey(e.ID), "confirm", p.Preview.Form, `{"action":"resume"}`, nil)
	if r.Result == nil || r.Result.Outcome == "done" || !reflect.DeepEqual(before, controlOf(t, e)) {
		t.Fatalf("%+v", r)
	}
	if _, err := os.Stat(filepath.Join(e.StateDir, clientResumeFile)); !os.IsNotExist(err) {
		t.Fatalf("refused confirm made ticket: %v", err)
	}
}

func TestPanelCallerBroadHoldsThroughHTTP(t *testing.T) {
	for _, action := range []string{"pause", "stop"} {
		t.Run(action, func(t *testing.T) {
			t.Setenv("BB_THREAD_ID", "thr_example_agent")
			w := newPanelChangeWorld(t, true)
			e := w.entry(t, "docs-site-refresh")
			if _, err := control.Update(e.StateDir, func(c *control.File) { c.Ownership = &control.Ownership{ThreadID: "thr_example_other", Generation: 1} }); err != nil {
				t.Fatal(err)
			}
			before := controlOf(t, e)
			body := `{"action":"` + action + `"}`
			_, p := w.post(t, dockKey(e.ID), "prepare", w.form, body, nil)
			if p.Preview == nil {
				t.Fatalf("%+v", p)
			}
			_, r := w.post(t, dockKey(e.ID), "confirm", p.Preview.Form, body, nil)
			after := controlOf(t, e)
			if r.Result == nil || r.Result.Outcome != "done" || (action == "pause" && !after.Paused) || (action == "stop" && !after.Stop) || !reflect.DeepEqual(before.Ownership, after.Ownership) {
				t.Fatalf("%+v %+v", r, after)
			}
		})
	}
}

func TestPanelCallerImmediateStopOrdersStartupCallerLast(t *testing.T) {
	f := laneFixture(t)
	reg, key := laneRegistry(t, f)
	t.Setenv("BB_THREAD_ID", "thr_child_a")
	w := panelCallerWorld(t, reg)
	t.Setenv("BB_THREAD_ID", "thr_child_b1")
	_, p := w.post(t, key, "prepare", w.form, `{"action":"stop"}`, nil)
	if p.Preview == nil || p.Preview.Stop == nil {
		t.Fatalf("%+v", p)
	}
	_, r := w.post(t, key, "confirm", p.Preview.Form, `{"action":"stop","lanes":true,"immediately":true}`, nil)
	if r.Result == nil || r.Result.Outcome != "done" {
		t.Fatalf("%+v", r)
	}
	if got := strings.Join(stopCalls(f), ","); got != "thr_primary,thr_child_b1,thr_child_a" {
		t.Fatalf("stop order %s", got)
	}
}

func TestPanelCallerGroupStopOrdersOwnLoopLast(t *testing.T) {
	f := laneFixture(t)
	reg, _ := laneRegistry(t, f)
	raw, err := os.ReadFile(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Both fictional loops use the same stand-in recorder, so the command
	// order across loops is observed directly, without real BB controls.
	other := strings.NewReplacer("name: example-loop", "name: z-other-loop", "state_dir: state", "state_dir: other-state", "thread: thr_primary", "thread: thr_other").Replace(string(raw))
	path := filepath.Join(f.dir, "other.yaml")
	write(t, path, other, 0600)
	if code, out, errs := registryRun(t, reg, "add", "-c", path); code != exitOK {
		t.Fatal(out, errs)
	}
	write(t, filepath.Join(f.bbDir, "thr_other.show"), `{"thread":{"status":"active"}}`, 0600)
	entries, err := registry.Load(reg)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries.Entries {
		if _, err := control.Update(e.StateDir, func(c *control.File) { c.Enabled = true; c.Paused = false; c.AwaitingUser = false }); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("BB_THREAD_ID", "thr_primary")
	w := panelCallerWorld(t, reg)
	t.Setenv("BB_THREAD_ID", "thr_other")
	_, p := w.post(t, panelGroupKey, "prepare", w.form, `{"action":"stop"}`, nil)
	if p.Preview == nil {
		t.Fatalf("%+v", p)
	}
	_, r := w.post(t, panelGroupKey, "confirm", p.Preview.Form, `{"action":"stop"}`, nil)
	if r.Result == nil || r.Result.Outcome != "done" {
		t.Fatalf("%+v", r)
	}
	if got := strings.Join(stopCalls(f), ","); got != "thr_other,thr_primary" {
		t.Fatalf("stop order %s", got)
	}
	if !strings.Contains(strings.Join(r.Result.Lines, "\n"), "the loop of this conversation, stopped last") {
		t.Fatalf("%+v", r.Result)
	}
}

// Exercise the CLI-spawned background process, not only an in-process handler:
// the exact startup thread survives the allowlisted environment and later reuse.
func TestPanelCallerSubprocessRetainsOriginalThreadAcrossReuse(t *testing.T) {
	t.Setenv("BB_THREAD_ID", "thr_example_startup")
	proc := newPanelProcessWorld(t)
	if code, out, errs := proc.open("--allow-changes", "--no-open"); code != exitOK {
		t.Fatalf("%d %s %s", code, out, errs)
	}
	rec := proc.record()
	entries, err := registry.Load(proc.registry)
	if err != nil {
		t.Fatal(err)
	}
	var own, other registry.Entry
	for _, e := range entries.Entries {
		if e.Loop == "billing-api-migration" {
			own = e
		}
		if e.Loop == "docs-site-refresh" {
			other = e
		}
	}
	for _, e := range []registry.Entry{own, other} {
		owner := "thr_example_later"
		if e.ID == own.ID {
			owner = "thr_example_startup"
		}
		if _, err := control.Update(e.StateDir, func(c *control.File) {
			c.Ownership = &control.Ownership{ThreadID: owner, Generation: 1}
			c.Paused = true
		}); err != nil {
			t.Fatal(err)
		}
	}
	proc.env = append(proc.env, "BB_THREAD_ID=thr_example_later")
	if code, out, errs := proc.open("--allow-changes", "--no-open"); code != exitOK {
		t.Fatalf("reuse: %d %s %s", code, out, errs)
	}
	if proc.record() != rec {
		t.Fatal("same-class reuse replaced process")
	}
	t.Setenv("BB_THREAD_ID", "thr_example_later")
	page, cookie := panelSession(t, proc.dir, rec)
	_, body := dockRequest(t, http.MethodGet, page+dockServerPath, withCookie(cookie))
	var served dockServed
	if err := json.Unmarshal([]byte(body), &served); err != nil {
		t.Fatal(err)
	}
	w := panelChangeWorld{dir: proc.dir, page: page, cookie: cookie, rec: rec, form: served.Form, origin: "http://127.0.0.1:" + strconv.Itoa(rec.Port)}
	_, denied := w.post(t, dockKey(other.ID), "prepare", w.form, `{"action":"resume"}`, nil)
	if denied.Preview != nil || denied.Result == nil || denied.Result.Outcome != "refused" || !controlOf(t, other).Paused {
		t.Fatalf("later caller promoted: %+v", denied)
	}
	_, p := w.post(t, dockKey(own.ID), "prepare", w.form, `{"action":"resume"}`, nil)
	if p.Preview == nil {
		t.Fatalf("startup caller lost: %+v", p)
	}
	_, r := w.post(t, dockKey(own.ID), "confirm", p.Preview.Form, `{"action":"resume"}`, nil)
	if r.Result == nil || r.Result.Outcome != "done" || controlOf(t, own).Paused {
		t.Fatalf("%+v", r)
	}
}

// Confirm must enforce the caller independently of the review-time guard.
// Injecting a form simulates reaching that boundary with a valid control digest;
// it is not an HTTP mechanism for creating forms.
func TestPanelCallerConfirmationIndependentlyChecksOwnership(t *testing.T) {
	t.Setenv("BB_THREAD_ID", "thr_example_agent")
	d, entries := clientReviewWorld(t)
	e := entries["billing-api-migration"]
	if _, err := control.Update(e.StateDir, func(c *control.File) { c.Ownership = &control.Ownership{ThreadID: "thr_example_other", Generation: 1} }); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	c, err := newPanelChanges(context.Background(), d.directory, d, now, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	key := dockKey(e.ID)
	reviewed, refused := panelReview(context.Background(), d.directory, d, key, "resume", now)
	if refused != nil {
		t.Fatalf("%+v", refused)
	}
	c.keep("test-form", panelForm{session: "test-session", key: key, action: "resume", id: e.ID, digest: reviewed.digest, expires: reviewed.preview.ExpiresAt})
	before := controlOf(t, e)
	r := c.confirm("test-session", key, "resume", "test-form", holdOptions{})
	if r.Result == nil || r.Result.Outcome == "done" || !reflect.DeepEqual(before, controlOf(t, e)) {
		t.Fatalf("%+v", r)
	}
	if _, err := os.Stat(filepath.Join(e.StateDir, clientResumeFile)); !os.IsNotExist(err) {
		t.Fatalf("unauthorized confirm made ticket: %v", err)
	}
}
