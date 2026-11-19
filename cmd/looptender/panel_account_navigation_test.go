package main

import (
	"net/http"
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
