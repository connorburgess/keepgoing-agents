package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/control"
	"github.com/looptender/looptender/internal/lock"
	"github.com/looptender/looptender/internal/registry"
	"github.com/looptender/looptender/internal/state"
)

func allowanceFixture(t *testing.T) (*panelChanges, registry.Entry) {
	t.Helper()
	t.Setenv("BB_THREAD_ID", "")
	d, _ := dockFixtureReader(t)
	c, err := newPanelChanges(context.Background(), d.directory, d, time.Now(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	all, err := registry.Load(d.directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range all.Entries {
		if e.Loop != "billing-api-migration" {
			continue
		}
		if err := os.WriteFile(filepath.Join(e.StateDir, state.SendsName), []byte(`{"version":1,"used":40,"seq":40,"settled":40}`), 0600); err != nil {
			t.Fatal(err)
		}
		return c, e
	}
	t.Fatal("missing fictional loop")
	return nil, registry.Entry{}
}

func TestPanelSendAllowanceKeepsCountAndHold(t *testing.T) {
	c, e := allowanceFixture(t)
	if _, err := control.Update(e.StateDir, func(f *control.File) { f.AwaitingUser = true; f.Paused = true }); err != nil {
		t.Fatal(err)
	}
	beforeControl, err := os.ReadFile(filepath.Join(e.StateDir, control.FileName))
	if err != nil {
		t.Fatal(err)
	}
	beforeLedger, _ := os.ReadFile(filepath.Join(e.StateDir, state.SendsName))
	key := dockKey(e.ID)
	review, err := c.sendAllowanceChange(key, 500, nil)
	if err != nil {
		t.Fatal(err)
	}
	if review.used != 40 {
		t.Fatal("used attempts were lost")
	}
	if _, err := c.sendAllowanceChange(key, 500, review); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(e.ConfigPath)
	if err != nil || cfg.Loop.Limits.MaxSends != 500 {
		t.Fatal("allowance not saved", err)
	}
	afterControl, _ := os.ReadFile(filepath.Join(e.StateDir, control.FileName))
	afterLedger, _ := os.ReadFile(filepath.Join(e.StateDir, state.SendsName))
	if !bytes.Equal(beforeControl, afterControl) || !bytes.Equal(beforeLedger, afterLedger) {
		t.Fatal("control or accounting changed")
	}
	if _, err := c.sendAllowanceChange(key, 500, review); err == nil {
		t.Fatal("replay accepted")
	}
}

func TestPanelSendAllowanceRefusesChangedEvidence(t *testing.T) {
	for _, kind := range []string{"caller", "ledger", "control", "source", "missing-ledger", "malformed-ledger", "busy"} {
		t.Run(kind, func(t *testing.T) {
			c, e := allowanceFixture(t)
			key := dockKey(e.ID)
			review, err := c.sendAllowanceChange(key, 500, nil)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "caller":
				c.caller = "thr_example_owner"
			case "ledger":
				err = os.WriteFile(filepath.Join(e.StateDir, state.SendsName), []byte(`{"version":1,"used":41,"seq":41}`), 0600)
			case "missing-ledger":
				err = os.Remove(filepath.Join(e.StateDir, state.SendsName))
			case "malformed-ledger":
				err = os.WriteFile(filepath.Join(e.StateDir, state.SendsName), []byte(`{}`), 0600)
			case "control":
				_, err = control.Update(e.StateDir, func(f *control.File) { f.Paused = !f.Paused })
			case "source":
				var b []byte
				b, err = os.ReadFile(e.ConfigPath)
				if err == nil {
					err = os.WriteFile(e.ConfigPath, append(b, []byte("\n# edited\n")...), 0600)
				}
			case "busy":
				var held *lock.Lock
				held, err = lock.Acquire(e.StateDir)
				if err == nil {
					defer held.Release()
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(e.ConfigPath)
			if _, err := c.sendAllowanceChange(key, 500, review); err == nil {
				t.Fatal("changed evidence accepted")
			}
			after, _ := os.ReadFile(e.ConfigPath)
			if !bytes.Equal(before, after) {
				t.Fatal("refusal wrote config")
			}
		})
	}
}

func TestPanelSendAllowanceFormIsSingleUse(t *testing.T) {
	c, e := allowanceFixture(t)
	key := dockKey(e.ID)
	first := c.prepareSendAllowance("page", key, 500)
	if first.Preview == nil {
		t.Fatal(first)
	}
	if got := c.confirmSendAllowance("other-page", key, first.Preview.Form); got.Result == nil || got.Result.Outcome != "review_again" {
		t.Fatal(got)
	}
	if got := c.confirmSendAllowance("page", key, first.Preview.Form); got.Result == nil || got.Result.Outcome != "review_again" {
		t.Fatal("mismatched use did not spend form", got)
	}
	second := c.prepareSendAllowance("page", key, 500)
	if second.Preview == nil {
		t.Fatal(second)
	}
	if got := c.confirmSendAllowance("page", key, second.Preview.Form); got.Result == nil || got.Result.Outcome != "done" {
		t.Fatal(got)
	}
}

func TestPanelSendAllowanceExactInput(t *testing.T) {
	for _, body := range []string{
		`{"action":"set_send_allowance","allowance":500,"allowance":600}`,
		`{"Action":"set_send_allowance","allowance":500}`,
		`{"action":"set_send_allowance","allowance":0}`,
		`{"action":"set_send_allowance","allowance":500.0}`,
		`{"action":"set_send_allowance","allowance":null}`,
		`{"action":"set_send_allowance","allowance":500,"resume":true}`,
		`{"action":"set_send_allowance","allowance":500} {}`,
	} {
		if _, ok := panelReadSendAllowance(strings.NewReader(body), true); ok {
			t.Fatalf("accepted %s", body)
		}
	}
	if n, ok := panelReadSendAllowance(strings.NewReader(`{"action":"set_send_allowance","allowance":500}`), true); !ok || n != 500 {
		t.Fatal(n, ok)
	}
	if _, ok := panelReadSendAllowance(strings.NewReader(`{"action":"set_send_allowance","allowance":500}`), false); ok {
		t.Fatal("confirmation supplied a new allowance")
	}
}

func TestPanelSendAllowanceHTTPGuards(t *testing.T) {
	for _, changes := range []bool{false, true} {
		t.Run(fmt.Sprint(changes), func(t *testing.T) {
			t.Setenv("BB_THREAD_ID", "")
			w := newPanelChangeWorld(t, changes)
			e := w.entry(t, "billing-api-migration")
			if err := os.WriteFile(filepath.Join(e.StateDir, state.SendsName), []byte(`{"version":1,"used":40,"seq":40}`), 0600); err != nil {
				t.Fatal(err)
			}
			key := dockKey(e.ID)
			target := func(r *http.Request) {
				r.URL.Path = strings.Replace(r.URL.Path, "api/v1/loops/", panelSendAllowancePrefix, 1)
			}
			body := `{"action":"set_send_allowance","allowance":500}`
			resp, reply := w.post(t, key, "prepare", w.form, body, target)
			if !changes {
				if resp.StatusCode != http.StatusForbidden {
					t.Fatal(resp.StatusCode)
				}
				return
			}
			if resp.StatusCode != http.StatusOK || reply.Preview == nil {
				t.Fatal(resp.StatusCode, reply)
			}
			for _, spoil := range []func(*http.Request){
				func(r *http.Request) { r.Header.Set("Origin", "https://example.com") },
				func(r *http.Request) { r.Header.Set(panelFormHeader, "wrong") },
				func(r *http.Request) { r.Header.Del("Cookie") },
				func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
			} {
				response, _ := w.post(t, key, "prepare", w.form, body, func(r *http.Request) { target(r); spoil(r) })
				if response.StatusCode != http.StatusForbidden {
					t.Fatal("guard accepted request", response.StatusCode)
				}
			}
			response, result := w.post(t, key, "confirm", reply.Preview.Form, `{"action":"set_send_allowance"}`, target)
			if response.StatusCode != http.StatusOK || result.Result == nil || result.Result.Outcome != "done" {
				t.Fatal(response.StatusCode, result)
			}
		})
	}
}
