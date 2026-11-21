package main

import (
	"strings"
	"testing"
)

// G1 (design pass 2): the server read ran before the hidden-tab check, and
// any read resets the panel's idle timer, so a forgotten hidden tab kept
// keepgoing panel running for ever. A hidden tab now reads nothing until it
// is shown, and then reads once.
func TestDockHiddenTabReadsNothingG1(t *testing.T) {
	js := dockUIText(t, "dock.js")
	load := jsFunction(t, js, "function load(byHand) {")
	hidden := strings.Index(load, "if (document.hidden && !byHand) { state.missed = true; return; }")
	server, api := strings.Index(load, "loadServer();"), strings.Index(load, "getJSON(API")
	if hidden < 0 || server < 0 || api < 0 || hidden > server || hidden > api {
		t.Errorf("load() must return on a hidden tab before any read:\n%s", load)
	}
	// Nothing is scheduled from a hidden tab; showing it reads once.
	if strings.Contains(load[:server], "schedule()") {
		t.Error("a hidden tab must not keep a timer going")
	}
	if !strings.Contains(load[strings.Index(load, "getJSON(API"):], "loadUsage();") {
		t.Error("the Usage read must follow a report read, never run from a hidden tab")
	}
	if !strings.Contains(js, "(state.missed || (state.lastOk && Date.now() - state.lastOk > 5000))) { clearTimeout(state.timer); load(true); }") {
		t.Error("showing the tab must read once when a read was missed")
	}
}

// G2 (design pass 2): reads had no timeout, so a server that accepted the
// connection and never answered left the page stuck, and a read out when
// identifiers were toggled was applied anyway. Main/server reads use
// getJSON; Usage, explicit quota and saved Accounts each own a bounded read.
// Every bounded path passes its abort signal, clears its timer and handles
// failure. Replies for a superseded snapshot or identifier view are dropped.
func TestDockReadsTimeOutG2(t *testing.T) {
	js := dockUIText(t, "dock.js")
	for _, want := range []string{
		"function readTimeoutMs() { return Math.min(30, Math.max(4, refreshSeconds() - 2)) * 1000; }",
		"var ctl = new AbortController();",
		"setTimeout(function () { ctl.abort(); }, readTimeoutMs());",
		"signal: ctl.signal",
		".finally(function () { clearTimeout(timer); });",
		"if (asked !== state.identifiers) { state.again = true; return; }",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("dock.js lacks %q", want)
		}
	}
	// Inventory fetch owners rather than treating a new transport as a count
	// increment. Each bounded read must retain its own deadline, signal, cleanup
	// and failure path; generic reviewed changes deliberately keep unknown-save
	// semantics without automatic timeout/replay.
	owners := []string{"function getJSON(url) {", "function loadUsage() {", "function loadSavedAccounts(retry) {", "function accountQuotaPost(id, step, form, recovery) {", "function post(path, form, body) {", "function accountConnectPost(c, step, form, attempt) {", "function allowancePost(key, step, form, amount) {"}
	ownedFetches := 0
	for _, head := range owners {
		source := jsFunction(t, js, head)
		n := strings.Count(source, "fetch(")
		if n != 1 {
			t.Errorf("%s owns %d fetches, want one", head, n)
		}
		ownedFetches += n
	}
	if n := strings.Count(js, "fetch("); n != ownedFetches {
		t.Errorf("%d fetches but only %d have reviewed owners", n, ownedFetches)
	}
	for _, tc := range []struct {
		head     string
		required []string
	}{
		{"function getJSON(url) {", []string{"new AbortController()", "ctl.abort()", "readTimeoutMs()", "signal: ctl.signal", "if (!r.ok) throw", ".finally(function () { clearTimeout(timer); });"}},
		{"function loadUsage() {", []string{"new AbortController()", "abort.abort()", "usageTimeout()", "signal: abort.signal", "if (!r.ok) throw", ".catch(function () { state.usageFailed = true; })", "clearTimeout(timer)", "state.usageBusy = false"}},
		{"function loadSavedAccounts(retry) {", []string{"new AbortController()", "ctl.abort()", "readTimeoutMs()", "signal: ctl.signal", "if (!r.ok) throw", ".catch(function () { if (current()) savedAccounts.status = \"failed\"; })", ".finally(function () {", "clearTimeout(timer)", "seq === savedAccounts.serial", "state.data.accountsReference === ref", "state.identifiers === asked"}},
		{"function accountConnectPost(c, step, form, attempt) {", []string{"new AbortController()", "ctl.abort()", "15000", "signal: ctl.signal", "if (!r.ok)", ".finally(function () { clearTimeout(timer); });"}},
		{"function allowancePost(key, step, form, amount) {", []string{"new AbortController()", "abort.abort()", "15000", "signal: abort.signal", "if (!r.ok)", "allowanceReply(d, key, step)", ".finally(function () { clearTimeout(timer); });"}},
		{"function accountQuotaPost(id, step, form, recovery) {", []string{"new AbortController()", "abort.abort()", "15000", "signal: abort.signal", "if (!r.ok)", ".finally(function () { clearTimeout(timer); });"}},
	} {
		source := jsFunction(t, js, tc.head)
		for _, want := range tc.required {
			if !strings.Contains(source, want) {
				t.Errorf("%s lacks timeout/failure protection %q", tc.head, want)
			}
		}
	}
	saved := jsFunction(t, js, "function loadSavedAccounts(retry) {")
	finallyAt := strings.Index(saved, ".finally(function () {")
	if finallyAt < 0 {
		t.Fatal("saved-read cleanup missing")
	}
	cleanup := saved[finallyAt:]
	clearAt, staleAt := strings.Index(cleanup, "clearTimeout(timer)"), strings.Index(cleanup, "if (!current()) return")
	if clearAt < 0 || staleAt < 0 || clearAt > staleAt {
		t.Error("superseded saved reads must clear their timer before returning")
	}
	if !strings.Contains(jsFunction(t, js, "function loadServer() {"), "getJSON(SERVER)") {
		t.Error("loadServer must read through getJSON")
	}
	// A timed-out read fails into the one failed-read notice; no second notice.
	load := jsFunction(t, js, "function load(byHand) {")
	if strings.Count(load, "notice(") != 2 || !strings.Contains(load, `notice(schema ? "schema" : "failed", text, schema || state.failedLong);`) {
		t.Error("a failed or timed-out read must go through the existing notice()")
	}
}

// jsFunction is the source of the function that starts with head, up to the
// first line that closes it at its own indentation.
func jsFunction(t *testing.T, js, head string) string {
	t.Helper()
	i := strings.Index(js, head)
	if i < 0 {
		t.Fatalf("dock.js lacks %q", head)
	}
	line := strings.LastIndex(js[:i], "\n") + 1
	end := strings.Index(js[i:], "\n"+js[line:i]+"}\n")
	if end < 0 {
		t.Fatalf("no end for %q", head)
	}
	return js[i : i+end]
}

func TestDockQuotaOnlyReadsAfterExplicitReview(t *testing.T) {
	js := dockUIText(t, "dock.js")
	post := jsFunction(t, js, "function accountQuotaPost(id, step, form, recovery) {")
	for _, want := range []string{"abort.abort()", "15000", "signal: abort.signal", `method: "POST"`, `action: recovery ? "review_selection" : "subscription_quota"`, `recovery ? "api/v1/account-selections/" : "api/v1/accounts/"`, "accountQuotaReply(d, id, step, recovery)", "clearTimeout(timer)"} {
		if !strings.Contains(post, want) {
			t.Errorf("quota POST lacks %q", want)
		}
	}
	for _, head := range []string{"function load(byHand) {", "function loadServer() {", "function loadUsage() {", "function loadSavedAccounts(retry) {", "function schedule() {"} {
		source := jsFunction(t, js, head)
		if strings.Contains(source, "accountQuotaPost(") || strings.Contains(source, "accountQuotaRequest(") {
			t.Errorf("automatic quota read in %s", head)
		}
	}
}
