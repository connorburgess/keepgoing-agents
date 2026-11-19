package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

// An account menu link only selects a page. It must neither mint a different
// action nor accept arbitrary fragments, and its opening remains single-use.
func TestPanelAccountOpeningSelectsOnlyThatAccount(t *testing.T) {
	dir, rec, _ := panelServeInProcess(t, time.Hour)
	id := strings.Repeat("a", 32)
	address := panelOpenAddress(t, dir, rec, "a"+id)
	target, _ := panelRedeem(t, address)
	if !strings.HasSuffix(target, "/#/accounts/"+id) {
		t.Fatalf("wrong account target: %q", target)
	}
	if resp, _ := dockRequest(t, http.MethodGet, address, nil); resp.StatusCode == http.StatusOK {
		t.Fatal("opening replay accepted")
	}
}

func TestPanelAccountConnectCapabilityIsExplicit(t *testing.T) {
	c, _, _ := connectFixture(t)
	state, err := newPanelState(54321, time.Hour, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	session, _ := state.newSession()
	h := &panelHandler{state: state, guard: newDockGuard(54321), page: newDockServer(54321, state, c.reader)}
	for _, on := range []bool{false, true} {
		h.changes = nil
		if on {
			h.changes = c
		}
		w := httptest.NewRecorder()
		h.served(w)(session)
		var v dockServed
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		var want []string
		if on {
			want = panelConnectProvidersFor(runtime.GOOS, runtime.GOARCH)
		}
		if !reflect.DeepEqual(v.AccountConnectProviders, want) {
			t.Fatal("connection capability differs from explicit mode/platform", v.AccountConnectProviders, want)
		}
	}
}
