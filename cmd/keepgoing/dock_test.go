package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/registry"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

var dockNow = time.Date(2030, 3, 4, 12, 0, 0, 0, time.UTC)

func dockFixtureReader(t *testing.T) (*dockReader, string) {
	t.Helper()
	root := t.TempDir()
	dir := dockScratch(t, root, dockNow)
	runsClock(t, dockNow)
	return &dockReader{directory: dir, refresh: dockDefaultRefresh, staleAfter: defaultStaleAfter, query: inspectDaemon, now: func() time.Time { return dockNow }}, root
}

func dockLoopNamed(t *testing.T, r dockReport, name string) dockLoop {
	t.Helper()
	for _, l := range r.Loops {
		if l.Name == name {
			return l
		}
	}
	t.Fatalf("no loop %s in %+v", name, r.Loops)
	return dockLoop{}
}

// treeDigest fingerprints every file and directory under root.
func treeDigest(t *testing.T, root string) string {
	t.Helper()
	h := sha256.New()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s %v %d %d\n", path, info.Mode(), info.Size(), info.ModTime().UnixNano())
		if info.Mode().IsRegular() {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			h.Write(b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func TestDockReportCoversEveryStateFromFixtures(t *testing.T) {
	d, _ := dockFixtureReader(t)
	r := d.read(context.Background(), false)
	if r.SchemaVersion != 1 || !r.ReadOnly || !r.ReadAt.Equal(dockNow) || r.RefreshSeconds != 15 || r.Registry.Availability != "available" || r.Registry.Count != len(dockLoops) || len(r.Loops) != len(dockLoops) {
		t.Fatalf("%+v", r)
	}
	want := map[string]string{
		"docs-site-refresh": "supervising", "billing-api-migration": "waiting_for_you", "mobile-onboarding": "paused", "search-indexer": "waiting_on_limit",
		"release-notes-bot": "finished", "nightly-dependency-bumps": "not_checked_recently", "data-export-cleanup": "off",
	}
	for name, state := range want {
		if l := dockLoopNamed(t, r, name); l.State != state || l.Phrase == "" || l.Detail == "" || l.Evidence != "available" {
			t.Errorf("%s: state %q phrase %q evidence %q", name, l.State, l.Phrase, l.Evidence)
		}
	}
	// Loops that want attention come first.
	if r.Loops[0].Name != "billing-api-migration" || r.Loops[1].Name != "nightly-dependency-bumps" {
		t.Errorf("order: %s, %s", r.Loops[0].Name, r.Loops[1].Name)
	}

	// Needs you: the hold's recorded cause, the owner's note verbatim, and how
	// long it has waited, from the retained history record of the hold.
	if len(r.NeedsYou) != 1 {
		t.Fatalf("%+v", r.NeedsYou)
	}
	n := r.NeedsYou[0]
	if n.Loop != "billing-api-migration" || n.Reason != "A child conversation is waiting for an answer." || !strings.HasPrefix(n.Note, "Needs a decision on the refund table") ||
		n.SinceEvidence != "hold_recorded" || n.WaitedSeconds == nil || *n.WaitedSeconds != int64((2*time.Hour+14*time.Minute)/time.Second) || !strings.Contains(n.Action, "resume") {
		t.Errorf("%+v", n)
	}

	docs := dockLoopNamed(t, r, "docs-site-refresh")
	if c := docs.Children; c.Availability != "current" || c.Working != 3 || c.Stuck != 0 || c.Failed != 1 || c.Phrase != "3 working, 1 failed" {
		t.Errorf("children %+v", c)
	}
	if c := docs.LastCheck; c.Availability != "recent" || c.AgeSeconds == nil || *c.AgeSeconds != 180 || c.Phrase != "Looked; nothing needed" {
		t.Errorf("last check %+v", c)
	}
	if docs.RunningOn != (dockTarget{Availability: "known", Role: "main", Name: "primary", Tool: "BB thread", Provider: "claude-code", Model: "claude-opus-5-5"}) {
		t.Errorf("%+v", docs.RunningOn)
	}
	if docs.NextCheck.Availability != "unknown" || docs.NextCheck.Detail == "" {
		t.Errorf("next check must be unknown without a daemon: %+v", docs.NextCheck)
	}
	if f := docs.Fallbacks; f.Availability != "known" || f.Ready != 1 || f.Eligible != 2 || !f.Enough {
		t.Errorf("%+v", f)
	}
	// The handoff chain: main first, then fallbacks in configured order.
	ch := docs.Chain
	if ch.Availability != "known" || len(ch.HandoffOn) != 1 || ch.HandoffOn[0] != "rate_limit" || len(ch.Entries) != 4 {
		t.Fatalf("%+v", ch)
	}
	for i, w := range []struct{ id, readiness string }{{"primary", "main"}, {"claude-second-account", "ready"}, {"codex-thread", "not_ready"}, {"example-cli", "disabled"}} {
		if e := ch.Entries[i]; e.Position != i || e.ID != w.id || e.Readiness != w.readiness || e.Active != (i == 0) {
			t.Errorf("chain %d: %+v", i, e)
		}
	}
	if u := ch.Entries[0].Usage; u.Availability != "available" || len(u.Windows) != 2 || u.Windows[0].Name != "5-hour" || u.Windows[0].UsedPercent != 38 || u.AgeSeconds == nil || *u.AgeSeconds != 720 {
		t.Errorf("%+v", u)
	}
	if e := ch.Entries[2]; e.Phrase != "Needs signing in again" || e.Usage.Availability != "unavailable" || e.ProbeAge == nil {
		t.Errorf("%+v", e)
	}

	// Held: the child listing is the one verified by the check that held it.
	if c := dockLoopNamed(t, r, "billing-api-migration").Children; c.Availability != "held" || c.Working != 1 || c.Stuck != 1 {
		t.Errorf("%+v", c)
	}

	// A limit with its reset, and a send allowance.
	search := dockLoopNamed(t, r, "search-indexer")
	if w := search.LimitWait; w == nil || w.Passed || w.ResetsAt == nil || !w.ResetsAt.Equal(dockNow.Add(2*time.Hour+20*time.Minute)) {
		t.Errorf("%+v", w)
	}
	if len(search.Limits) != 1 || search.Limits[0].Phrase != "41 of 60" || search.Limits[0].Reached {
		t.Errorf("%+v", search.Limits)
	}
	if search.Fallbacks.Ready != 0 || search.Chain.Entries[1].Phrase != "At its usage limit" || search.Chain.Entries[0].Usage.Windows[0].UsedPercent != 100 {
		t.Errorf("%+v %+v", search.Fallbacks, search.Chain.Entries)
	}

	// Stale evidence is labelled, never shown as current.
	nightly := dockLoopNamed(t, r, "nightly-dependency-bumps")
	if nightly.LastCheck.Availability != "stale" || nightly.Chain.Entries[0].Usage.Availability != "stale" || !nightly.Chain.Entries[0].Usage.Windows[0].Stale || nightly.Chain.Entries[1].Readiness != "not_ready" {
		t.Errorf("%+v %+v", nightly.LastCheck, nightly.Chain.Entries)
	}

	// Missing evidence is explicit.
	off := dockLoopNamed(t, r, "data-export-cleanup")
	if off.LastCheck.Availability != "never" || off.Children.Availability != "none_recorded" || off.Fallbacks.Availability != "none_configured" || off.Chain.Availability != "none_configured" || len(off.Chain.Entries) != 1 {
		t.Errorf("%+v", off)
	}
}

func TestDockProjectionOfUnreadableAndOtherStates(t *testing.T) {
	entry := registry.Entry{ID: "loop_0123456789abcdef0123456789abcdef", Loop: "example-loop"}
	snap := func(c clientControls) clientStatusReport {
		return clientStatusReport{Outcome: "available", Issues: []string{}, Snapshot: &clientSnapshot{Loop: "example-loop", Controls: &c}}
	}
	checked := func(failed int) *statusOut {
		return &statusOut{Target: "primary", TargetConfigured: true, State: state.File{CheckedAt: dockNow.Add(-time.Minute), FailedChecks: failed}, LimitWait: limitWaitStatus{State: "none"}}
	}
	for _, c := range []struct {
		name  string
		in    dockInput
		state string
		need  bool
	}{
		{"stopped wins", dockInput{Client: snap(clientControls{Enabled: true, Stop: true, Paused: true, AwaitingUser: true}), Status: checked(0)}, "stopped", false},
		{"checks failing", dockInput{Client: snap(clientControls{Enabled: true}), Status: checked(3)}, "checks_failing", false},
		{"supervising", dockInput{Client: snap(clientControls{Enabled: true}), Status: checked(0)}, "supervising", false},
		{"status unreadable", dockInput{Client: snap(clientControls{Enabled: true})}, "not_checked_recently", false},
		{"control unreadable", dockInput{Client: clientStatusReport{Outcome: "partial", Issues: []string{"control_unavailable"}, Snapshot: &clientSnapshot{}}}, "unknown", false},
		{"binding gone", dockInput{Client: clientStatusReport{Outcome: "unavailable", Issues: []string{"binding_unavailable"}}}, "unknown", true},
		{"registration gone", dockInput{Client: clientStatusReport{Outcome: "unavailable", Issues: []string{"registration_unavailable"}}}, "unknown", false},
		{"held, cause unknown", dockInput{Client: snap(clientControls{Enabled: true, AwaitingUser: true}), Status: checked(0)}, "waiting_for_you", true},
	} {
		c.in.Entry = entry
		l, need := projectDockLoop(c.in, dockNow, defaultStaleAfter, false)
		if l.State != c.state || (need != nil) != c.need || l.Identifiers != nil {
			t.Errorf("%s: state %q need %v", c.name, l.State, need)
		}
		if c.in.Status == nil && (l.RunningOn.Availability != "unknown" || l.LastCheck.Availability != "unknown" || l.Fallbacks.Availability != "unknown" || l.Children.Availability != "unknown" || l.Chain.Availability != "unknown") {
			t.Errorf("%s: missing evidence must be unknown: %+v", c.name, l)
		}
	}
	// Without a history record of the hold starting, the wait is a lower bound.
	in := dockInput{Entry: entry, Client: snap(clientControls{Enabled: true, AwaitingUser: true}), Status: checked(0)}
	in.Status.Control = control.File{UpdatedAt: dockNow.Add(-time.Hour), Note: "  check the example  "}
	_, need := projectDockLoop(in, dockNow, defaultStaleAfter, false)
	if need.SinceEvidence != "control_changed" || *need.WaitedSeconds != 3600 || need.Note != "check the example" || need.Reason != "It is held for a person." {
		t.Errorf("%+v", need)
	}
	in.Hold = &dockHold{At: dockNow.Add(-2 * time.Hour), Manual: true}
	if _, need = projectDockLoop(in, dockNow, defaultStaleAfter, false); need.SinceEvidence != "hold_recorded" || *need.WaitedSeconds != 7200 || need.Reason != "It was marked as waiting for a person." {
		t.Errorf("%+v", need)
	}
	// An unreadable registry is never an empty list of loops.
	if r := buildDockReport(nil, dockNow, time.Minute, time.Hour, false, false, 0); r.Registry.Availability != "unavailable" || len(r.Loops) != 0 {
		t.Errorf("%+v", r)
	}
}

var dockPrivate = regexp.MustCompile(`thr_[A-Za-z0-9]+|loop_[0-9a-f]{32}`)

func TestDockWithholdsIdentifiersAndPathsAndIsBounded(t *testing.T) {
	d, root := dockFixtureReader(t)
	// An oversized note and a long handoff chain must be cut down.
	f, _ := registry.Load(d.directory)
	var billing registry.Entry
	for _, e := range f.Entries {
		if e.Loop == "billing-api-migration" {
			billing = e
		}
	}
	if _, err := control.Update(billing.StateDir, func(c *control.File) { c.Note = strings.Repeat("long note ", 5000) }); err != nil {
		t.Fatal(err)
	}
	body, err := d.json(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) > 64<<10 {
		t.Errorf("report is %d bytes for seven loops", len(body))
	}
	if m := dockPrivate.Find(body); m != nil {
		t.Errorf("identifier %s in the default report", m)
	}
	for _, private := range []string{root, "keepgoing.yaml", "nonexistent-provider-fixture", "state_dir", "nonexistent-probe"} {
		if bytes.Contains(body, []byte(private)) {
			t.Errorf("%q in the report", private)
		}
	}
	var r dockReport
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatal(err)
	}
	if n := []rune(r.NeedsYou[0].Note); len(n) != dockMaxNote || n[len(n)-1] != '…' {
		t.Errorf("note is %d runes", len(n))
	}
	// On request: the registration and the configured thread, which
	// registered status already prints. Still no path.
	d.cache = nil
	withIDs, err := d.json(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(withIDs, []byte(billing.ID)) || !bytes.Contains(withIDs, []byte("thr_example0001")) || bytes.Contains(withIDs, []byte(root)) || bytes.Contains(withIDs, []byte("thr_child")) || bytes.Contains(withIDs, []byte("thr_example0002")) {
		t.Errorf("identifier view: %s", withIDs)
	}
}

func TestDockChainIsCapped(t *testing.T) {
	in := &dockChainInput{}
	in.Primary.ID = "primary"
	in.Candidates = make([]config.Candidate, dockMaxChain+9)
	for i := range in.Candidates {
		in.Candidates[i].ID = fmt.Sprintf("fallback-%d", i)
	}
	c := dockHandoffChain(in, "primary", dockNow)
	if len(c.Entries) != dockMaxChain+1 || c.Omitted != 9 || c.Availability != "unknown" {
		t.Errorf("%d entries, %d omitted, %s", len(c.Entries), c.Omitted, c.Availability)
	}
}

func TestDockReadWritesNothing(t *testing.T) {
	d, root := dockFixtureReader(t)
	// A registered loop whose runtime directory does not exist yet.
	cfg := filepath.Join(root, "loops", "never-started", "keepgoing.yaml")
	dockWrite(t, cfg, dockConfigYAML("never-started", false, 0))
	var out, errs bytes.Buffer
	if code := run(context.Background(), []string{"registry", "add", "-c", cfg, "--directory", d.directory, "--json"}, &out, &errs); code != 0 {
		t.Fatal(errs.String())
	}
	before := treeDigest(t, root)
	for _, identifiers := range []bool{false, true, false} {
		d.cache = nil
		if _, err := d.json(context.Background(), identifiers); err != nil {
			t.Fatal(err)
		}
	}
	if after := treeDigest(t, root); after != before {
		t.Fatal("a read changed the registry, a configuration or a state directory")
	}
	if _, err := os.Stat(filepath.Join(root, "loops", "never-started", "state")); !os.IsNotExist(err) {
		t.Fatal("a read created a runtime directory")
	}
	r := d.read(context.Background(), false)
	if l := dockLoopNamed(t, r, "never-started"); l.State != "off" || l.LastCheck.Availability != "never" {
		t.Errorf("%+v", l)
	}
}

// dockServe starts the server on a loopback listener and returns its base URL
// (with token) and the address.
func dockServe(t *testing.T) (string, string, *dockReader) {
	t.Helper()
	d, _ := dockFixtureReader(t)
	ln, err := dockListen(0)
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().(*net.TCPAddr)
	if !addr.IP.IsLoopback() || !addr.IP.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Fatalf("listening on %s", addr)
	}
	token, err := dockNewToken()
	if err != nil || len(token) != 64 {
		t.Fatal(token, err)
	}
	srv := newDockServer(addr.Port, dockTokenAuth{token: token}, d)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() { done <- srv.serve(ctx, ln, io.Discard) }()
	t.Cleanup(func() {
		cancel()
		if code := <-done; code != exitOK {
			t.Errorf("serve exit %d", code)
		}
	})
	return srv.url(token), addr.String(), d
}

func dockRequest(t *testing.T, method, url string, edit func(*http.Request)) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	if edit != nil {
		edit(req)
	}
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return resp, string(b)
}

func TestDockServerRefusesWithoutTokenCrossOriginAndWrites(t *testing.T) {
	base, addr, _ := dockServe(t)
	api := base + dockAPIPath

	for _, path := range []string{"", "dock.css", "dock.js", dockAPIPath} {
		resp, body := dockRequest(t, http.MethodGet, base+path, nil)
		if resp.StatusCode != 200 || body == "" {
			t.Errorf("GET %q: %d", path, resp.StatusCode)
		}
		if resp.Header.Get("Access-Control-Allow-Origin") != "" || resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("X-Content-Type-Options") != "nosniff" ||
			!strings.Contains(resp.Header.Get("Content-Security-Policy"), "default-src 'none'") || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
			t.Errorf("GET %q headers: %v", path, resp.Header)
		}
	}
	resp, body := dockRequest(t, http.MethodGet, api, nil)
	var r dockReport
	if resp.Header.Get("Content-Type") != "application/json" || json.Unmarshal([]byte(body), &r) != nil || r.SchemaVersion != 1 || len(r.Loops) != len(dockLoops) {
		t.Fatalf("%s", body)
	}

	wrong := strings.Repeat("0", 64)
	refused := []struct {
		name, method, url string
		edit              func(*http.Request)
	}{
		{"no token", "GET", "http://" + addr + "/", nil},
		{"no token api", "GET", "http://" + addr + "/" + dockAPIPath, nil},
		{"wrong token", "GET", "http://" + addr + "/" + wrong + "/" + dockAPIPath, nil},
		{"token prefix", "GET", base[:len(base)-2] + "/" + dockAPIPath, nil},
		{"token without slash", "GET", strings.TrimSuffix(base, "/"), nil},
		{"token in query only", "GET", "http://" + addr + "/?token=" + strings.Split(base, "/")[3], nil},
		{"foreign origin", "GET", api, func(q *http.Request) { q.Header.Set("Origin", "https://example.com") }},
		{"null origin", "GET", api, func(q *http.Request) { q.Header.Set("Origin", "null") }},
		{"other port origin", "GET", api, func(q *http.Request) { q.Header.Set("Origin", "http://127.0.0.1:1") }},
		{"cross-site fetch", "GET", api, func(q *http.Request) { q.Header.Set("Sec-Fetch-Site", "cross-site") }},
		{"same-site fetch", "GET", api, func(q *http.Request) { q.Header.Set("Sec-Fetch-Site", "same-site") }},
		{"rebound host", "GET", api, func(q *http.Request) { q.Host = "dock.example.com" }},
		{"rebound host with port", "GET", api, func(q *http.Request) { q.Host = "dock.example.com:" + strings.Split(addr, ":")[1] }},
		{"post", "POST", api, nil},
		{"put", "PUT", api, nil},
		{"patch", "PATCH", api, nil},
		{"delete", "DELETE", api, nil},
		{"options preflight", "OPTIONS", api, func(q *http.Request) {
			q.Header.Set("Origin", "https://example.com")
			q.Header.Set("Access-Control-Request-Method", "GET")
		}},
		{"post page", "POST", base, nil},
	}
	for _, c := range refused {
		resp, body := dockRequest(t, c.method, c.url, c.edit)
		if resp.StatusCode != http.StatusForbidden || strings.Contains(body, "schemaVersion") || resp.Header.Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("%s: status %d, %d bytes", c.name, resp.StatusCode, len(body))
		}
	}
	// The page's own same-origin requests are accepted.
	resp, _ = dockRequest(t, http.MethodGet, api, func(q *http.Request) {
		q.Header.Set("Origin", "http://"+addr)
		q.Header.Set("Sec-Fetch-Site", "same-origin")
	})
	if resp.StatusCode != 200 {
		t.Errorf("same origin: %d", resp.StatusCode)
	}
	// Nothing but the page, its two assets and the one endpoint exists.
	for _, path := range []string{"api/v1/pause", "api/v1/dock/", "api/v2/dock", "../", "dockui/index.html", "control"} {
		if resp, _ := dockRequest(t, http.MethodGet, base+path, nil); resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %q: %d", path, resp.StatusCode)
		}
	}
}

func TestDockPageHasNoExternalReferences(t *testing.T) {
	for _, name := range []string{"dockui/index.html", "dockui/dock.css", "dockui/dock.js"} {
		b, err := dockUI.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		// The SVG namespace is an identifier, not a request.
		text := strings.ReplaceAll(string(b), "http://www.w3.org/2000/svg", "")
		for _, banned := range []string{"http://", "https://", "//cdn", "@import", "url(", "innerHTML = d", "eval(", "localStorage", "document.cookie"} {
			if strings.Contains(text, banned) {
				t.Errorf("%s contains %q", name, banned)
			}
		}
	}
}

func TestDockCommandServesUntilCancelledAndValidatesFlags(t *testing.T) {
	d, _ := dockFixtureReader(t)
	for _, args := range [][]string{{"extra"}, {"--port", "70000"}, {"--refresh", "1s"}, {"--refresh", "1h"}, {"--stale-after", "0"}, {"--unknown"}} {
		var out, errs bytes.Buffer
		if code := run(context.Background(), append([]string{"dock"}, args...), &out, &errs); code != exitUsage || out.Len() != 0 {
			t.Errorf("%v: exit %d %s", args, code, out.String())
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	out := &safeBuffer{}
	done := make(chan int, 1)
	go func() { done <- run(ctx, []string{"dock", "--directory", d.directory, "--offline"}, out, io.Discard) }()
	address := regexp.MustCompile(`http://127\.0\.0\.1:\d+/[0-9a-f]{64}/`)
	waitFor(t, func() bool { return address.MatchString(out.String()) }, "the dock address")
	url := address.FindString(out.String())
	resp, body := dockRequest(t, http.MethodGet, url+dockAPIPath, nil)
	if resp.StatusCode != 200 || !strings.Contains(body, `"readOnly":true`) {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	cancel()
	select {
	case code := <-done:
		if code != exitOK {
			t.Fatalf("exit %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("dock did not exit when asked")
	}
	if _, err := net.DialTimeout("tcp", strings.Split(strings.TrimPrefix(url, "http://"), "/")[0], time.Second); err == nil {
		t.Fatal("still listening after exit")
	}
}
