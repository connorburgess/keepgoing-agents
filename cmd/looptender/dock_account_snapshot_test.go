package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestDockSavedAccountsSnapshotAuthorizationAndLifetime(t *testing.T) {
	base, addr, d := dockServe(t)
	resp, body := dockRequest(t, "GET", base+dockAPIPath, nil)
	var main dockReport
	if resp.StatusCode != 200 || json.Unmarshal([]byte(body), &main) != nil || len(main.AccountsReference) != 32 || len(main.Accounts.Cards) != 0 {
		t.Fatal(resp.StatusCode, body)
	}
	endpoint := base + dockAccountsPath + "?reference=" + main.AccountsReference
	resp, body = dockRequest(t, "GET", endpoint, nil)
	var snapshot dockAccountSnapshot
	if resp.StatusCode != 200 || json.Unmarshal([]byte(body), &snapshot) != nil || snapshot.Reference != main.AccountsReference || snapshot.ReadAt != main.ReadAt {
		t.Fatal(resp.StatusCode, body)
	}
	for _, tc := range []struct {
		url, method string
		edit        func(*http.Request)
	}{
		{"http://" + addr + "/" + dockAccountsPath + "?reference=" + main.AccountsReference, "GET", nil},
		{endpoint, "GET", func(r *http.Request) { r.Header.Set("Origin", "https://example.com") }},
		{endpoint, "GET", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }},
		{endpoint, "POST", nil},
	} {
		if resp, _ := dockRequest(t, tc.method, tc.url, tc.edit); resp.StatusCode != 403 {
			t.Fatalf("unauthorized saved read:%d", resp.StatusCode)
		}
	}
	for _, ref := range []string{"", strings.Repeat("z", 32), strings.Repeat("a", 32)} {
		if resp, _ := dockRequest(t, "GET", base+dockAccountsPath+"?reference="+ref, nil); resp.StatusCode != 409 {
			t.Fatal("unknown reference accepted", resp.StatusCode)
		}
	}
	d.mu.Lock()
	d.cache = nil
	d.mu.Unlock()
	_, err := d.json(false)
	if err != nil {
		t.Fatal(err)
	}
	if resp, _ := dockRequest(t, "GET", endpoint, nil); resp.StatusCode != 409 {
		t.Fatal("superseded reference accepted")
	}
	d.accountsMu.Lock()
	for key, c := range d.accountBodies {
		c.at = time.Now().Add(-dockAccountsLifetime - time.Second)
		d.accountBodies[key] = c
		endpoint = base + dockAccountsPath + "?reference=" + c.reference
	}
	d.accountsMu.Unlock()
	if resp, _ := dockRequest(t, "GET", endpoint, nil); resp.StatusCode != 409 {
		t.Fatal("expired reference accepted")
	}
}

func TestDockSavedAccountsReadNeverStartsAReport(t *testing.T) {
	d, _ := dockFixtureReader(t)
	// A companion read without a preceding main snapshot cannot enter the
	// registry/daemon/provider paths, even when the reader context is unusable.
	if _, err := d.accountsJSON(strings.Repeat("a", 32)); err == nil {
		t.Fatal("invented snapshot")
	}
	reference := strings.Repeat("b", 32)
	d.accountBodies = map[bool]dockAccountCached{false: {at: time.Now(), reference: reference, body: []byte(`{"schemaVersion":1}`)}}
	d.directory = "" // no registry is needed by this path
	d.query = func(context.Context, string, string, bool) daemonStatus {
		t.Fatal("saved endpoint entered daemon read")
		return daemonStatus{}
	}
	if body, err := d.accountsJSON(reference); err != nil || string(body) != `{"schemaVersion":1}` {
		t.Fatal(string(body), err)
	}
}

func TestPanelSavedAccountsRequiresExistingSession(t *testing.T) {
	w := newPanelChangeWorld(t, false)
	resp, body := dockRequest(t, "GET", w.page+dockAPIPath, withCookie(w.cookie))
	var main dockReport
	if resp.StatusCode != 200 || json.Unmarshal([]byte(body), &main) != nil {
		t.Fatal(resp.StatusCode, body)
	}
	endpoint := w.page + dockAccountsPath + "?reference=" + main.AccountsReference
	if resp, _ := dockRequest(t, "GET", endpoint, withCookie(w.cookie)); resp.StatusCode != 200 {
		t.Fatal("valid session refused", resp.StatusCode)
	}
	if resp, _ := dockRequest(t, "GET", endpoint, nil); resp.StatusCode != 403 {
		t.Fatal("sessionless saved read accepted")
	}
	if resp, _ := dockRequest(t, "GET", endpoint, func(r *http.Request) { r.AddCookie(w.cookie); r.Header.Set("Origin", "https://example.com") }); resp.StatusCode != 403 {
		t.Fatal("foreign-origin saved read accepted")
	}
}
