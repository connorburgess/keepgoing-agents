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
// identifiers were toggled was applied anyway. Every read now goes through
// getJSON, which aborts a little under the refresh interval; the timed-out
// read takes the failed-read path, notice(); a reply for the other
// identifiers view is dropped.
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
	// The report and server reads go through getJSON; fetch is otherwise
	// called by post(), the changes, which are not retried and are not timed
	// out, and by loadUsage(), which has its own timeout and runs only after
	// a report read, so a hidden tab never starts it.
	if n := strings.Count(js, "fetch("); n != 3 || !strings.Contains(jsFunction(t, js, "function loadUsage() {"), "signal: abort.signal") {
		t.Errorf("fetch( appears %d times", n)
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
