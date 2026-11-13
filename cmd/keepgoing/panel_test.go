package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/lock"
)

// panelShortDir returns a private directory whose path is short enough for a
// local socket: the usual test directory on macOS is not.
func panelShortDir(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return t.TempDir()
	}
	dir, err := os.MkdirTemp("/tmp", "kgp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

// panelServeInProcess runs the background server in this process against the
// fixture registry and returns its private directory, its own account of
// itself and the fixture tree's root.
func panelServeInProcess(t *testing.T, idle time.Duration) (string, panelRecord, string) {
	t.Helper()
	root := t.TempDir()
	registryDir := dockScratch(t, root, dockNow)
	runsClock(t, dockNow)
	dir, err := panelDirectory(filepath.Join(panelShortDir(t), "panel"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() {
		done <- runPanelServer(ctx, panelServerConfig{dir: dir, registry: registryDir, idle: idle, refresh: dockDefaultRefresh, staleAfter: defaultStaleAfter, offline: true, now: time.Now})
	}()
	stopped := false
	t.Cleanup(func() {
		cancel()
		if !stopped {
			<-done
		}
	})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if rec, ok := panelLive(context.Background(), dir); ok {
			return dir, rec, root
		}
		select {
		case code := <-done:
			stopped = true
			t.Fatalf("panel server exited %d while starting", code)
		case <-time.After(20 * time.Millisecond):
		}
	}
	t.Fatal("panel server did not answer")
	return "", panelRecord{}, ""
}

func panelOpenAddress(t *testing.T, dir string, rec panelRecord, key string) string {
	t.Helper()
	address, err := panelRequestOpening(context.Background(), dir, rec, key, false)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return address
}

var panelRefreshTarget = regexp.MustCompile(`<meta http-equiv="refresh" content="0; url=([^"]+)">`)

// panelRedeem loads an opening address as a browser would and returns where
// the answer sends it and the session cookie it set.
func panelRedeem(t *testing.T, address string) (string, *http.Cookie) {
	t.Helper()
	resp, body := dockRequest(t, http.MethodGet, address, nil)
	m := panelRefreshTarget.FindStringSubmatch(body)
	if resp.StatusCode != http.StatusOK || len(resp.Cookies()) != 1 || m == nil || resp.Header.Get("Location") != "" {
		t.Fatalf("opening: %d %v %q", resp.StatusCode, resp.Header, body)
	}
	return m[1], resp.Cookies()[0]
}

// panelSession redeems a fresh opening address and returns the page address
// and the session cookie.
func panelSession(t *testing.T, dir string, rec panelRecord) (string, *http.Cookie) {
	t.Helper()
	target, c := panelRedeem(t, panelOpenAddress(t, dir, rec, ""))
	return "http://127.0.0.1:" + strconv.Itoa(rec.Port) + target, c
}

func withCookie(c *http.Cookie) func(*http.Request) {
	return func(r *http.Request) {
		if c != nil {
			r.AddCookie(c)
		}
	}
}

func TestPanelOpeningAddressWorksOnceAndBecomesASessionCookie(t *testing.T) {
	dir, rec, _ := panelServeInProcess(t, time.Hour)
	address := panelOpenAddress(t, dir, rec, "l0123456789ab")
	base := "http://127.0.0.1:" + strconv.Itoa(rec.Port)

	// A HEAD request, as a link preview might send, does not spend it.
	if resp, _ := dockRequest(t, http.MethodHead, address, nil); resp.StatusCode != http.StatusMethodNotAllowed || len(resp.Cookies()) != 0 {
		t.Fatalf("HEAD: %d", resp.StatusCode)
	}
	resp, body := dockRequest(t, http.MethodGet, address, nil)
	target := panelRefreshTarget.FindStringSubmatch(body)
	if resp.StatusCode != http.StatusOK || target == nil || strings.Contains(body, "<script") {
		t.Fatalf("first use: %d %q", resp.StatusCode, body)
	}
	location := target[1]
	if !strings.HasPrefix(location, "/s/") || !strings.HasSuffix(location, "/#/loop/l0123456789ab") {
		t.Fatalf("sent on to %q", location)
	}
	pagePath := strings.TrimSuffix(location, "#/loop/l0123456789ab")
	raw := resp.Header.Values("Set-Cookie")
	if len(raw) != 1 {
		t.Fatalf("cookies: %v", raw)
	}
	c := resp.Cookies()[0]
	if c.Name != panelCookieName || len(c.Value) != 64 || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != pagePath || c.MaxAge != 0 || !c.Expires.IsZero() || c.Domain != "" {
		t.Fatalf("cookie attributes: %q", raw[0])
	}
	for _, want := range []string{"HttpOnly", "SameSite=Strict", "Path=" + pagePath} {
		if !strings.Contains(raw[0], want) {
			t.Fatalf("cookie %q lacks %s", raw[0], want)
		}
	}
	// Neither the address nor the page that sends the browser on carries
	// the session.
	if strings.Contains(address, c.Value) || strings.Contains(body, c.Value) {
		t.Fatal("the session value appears in an address or a page")
	}

	// A second use, and a code that never existed, get the same plain page.
	var pages []string
	for _, used := range []string{address, base + "/open/" + strings.Repeat("0", 64), base + "/open/"} {
		resp, body := dockRequest(t, http.MethodGet, used, nil)
		if resp.StatusCode != http.StatusGone || len(resp.Cookies()) != 0 || !strings.Contains(body, `id="command">keepgoing panel</code>`) || strings.Contains(body, "style=") || strings.Contains(body, "<script>") || strings.Count(body, "<script") != 1 {
			t.Fatalf("reused address: %d %q", resp.StatusCode, body)
		}
		pages = append(pages, body)
	}
	if pages[0] != pages[1] || pages[1] != pages[2] {
		t.Fatal("a used code and an unknown code get different pages")
	}

	// The cookie reads the page, its assets and the data endpoint.
	for path, want := range map[string]string{"": "<!doctype html>", "dock.css": "", "dock.js": "", dockAPIPath: `"schemaVersion":1`} {
		resp, body := dockRequest(t, http.MethodGet, base+pagePath+path, withCookie(c))
		if resp.StatusCode != http.StatusOK || !strings.Contains(strings.ToLower(body), strings.ToLower(want)) {
			t.Fatalf("%q with the session: %d", path, resp.StatusCode)
		}
	}
	// Without it, with another value, with two, or under another path,
	// nothing is read.
	other := *c
	other.Value = strings.Repeat("a", 64)
	refusedCookies := map[string]func(*http.Request){
		"no cookie":    nil,
		"wrong cookie": withCookie(&other),
		// The first of two would be the forged one: both orders are refused.
		"two cookies, real first": func(r *http.Request) { r.AddCookie(c); r.AddCookie(&other) },
		"two cookies, real last":  func(r *http.Request) { r.AddCookie(&other); r.AddCookie(c) },
		"the real cookie twice":   func(r *http.Request) { r.AddCookie(c); r.AddCookie(c) },
	}
	for name, edit := range refusedCookies {
		for _, path := range []string{"", dockAPIPath, dockServerPath, "dock.js"} {
			resp, body := dockRequest(t, http.MethodGet, base+pagePath+path, edit)
			if resp.StatusCode != http.StatusForbidden || strings.Contains(body, "schemaVersion") || !strings.Contains(body, "keepgoing panel") {
				t.Fatalf("%s, %q: %d %q", name, path, resp.StatusCode, body)
			}
		}
	}
	for _, path := range []string{"/", "/s/", "/s/" + strings.Repeat("0", 32) + "/", "/" + c.Value + "/", "/api/v1/dock", "/healthz"} {
		resp, body := dockRequest(t, http.MethodGet, base+path, withCookie(c))
		if resp.StatusCode == http.StatusOK || strings.Contains(body, "schemaVersion") || strings.Contains(body, rec.Instance) {
			t.Fatalf("%s was served: %d", path, resp.StatusCode)
		}
	}
}

func TestPanelOpeningAddressExpiresAndIsBounded(t *testing.T) {
	now := time.Date(2030, 3, 4, 12, 0, 0, 0, time.UTC)
	s, err := newPanelState(49152, time.Hour, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	s.dir = t.TempDir()
	code := func(address string) string { return strings.TrimPrefix(address, "http://127.0.0.1:49152/open/") }
	mint := func() string {
		t.Helper()
		address, file, err := s.mint("", false)
		if err != nil || file != "" || !panelCodePattern.MatchString(code(address)) {
			t.Fatal(address, file, err)
		}
		return address
	}
	first := mint()
	now = now.Add(panelOpenTTL - time.Second)
	if _, ok := s.redeem(code(first)); !ok {
		t.Fatal("refused inside its 30 seconds")
	}
	if _, ok := s.redeem(code(first)); ok {
		t.Fatal("used twice")
	}
	second := mint()
	now = now.Add(panelOpenTTL)
	if _, ok := s.redeem(code(second)); ok {
		t.Fatal("accepted at its expiry")
	}
	// Expired codes do not accumulate, pending ones are capped, and a key
	// that is not a page key is refused.
	for i := 0; i < panelMaxOpenings; i++ {
		mint()
	}
	if _, _, err := s.mint("", false); err == nil {
		t.Fatal("more pending opening addresses than the cap")
	}
	now = now.Add(panelOpenTTL)
	mint()
	if len(s.openings) != 1 {
		t.Fatal(len(s.openings))
	}
	for _, bad := range []string{"../x", "l0123", "L0123456789AB", "l0123456789ab/../.."} {
		if _, _, err := s.mint(bad, false); err == nil {
			t.Fatalf("minted for key %q", bad)
		}
	}
	// A launch page is a private file that names the address and is gone as
	// soon as the code is used, or when the code expires unused.
	listing := func() []string {
		entries, _ := os.ReadDir(s.dir)
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		return names
	}
	address, file, err := s.mint("", true)
	if err != nil || address != "" || !panelLaunchPattern.MatchString(file) {
		t.Fatal(address, file, err)
	}
	info, err := os.Lstat(filepath.Join(s.dir, file))
	if err != nil || !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		t.Fatalf("launch page: %v %v", info, err)
	}
	page, _ := os.ReadFile(filepath.Join(s.dir, file))
	m := panelRefreshTarget.FindStringSubmatch(string(page))
	if m == nil || !panelCodePattern.MatchString(code(m[1])) || strings.Contains(string(page), "<script") {
		t.Fatalf("launch page content: %s", page)
	}
	if _, ok := s.redeem(code(m[1])); !ok {
		t.Fatal("the launch page's address was refused")
	}
	if got := listing(); len(got) != 0 {
		t.Fatalf("launch page left after use: %v", got)
	}
	if _, _, err := s.mint("", true); err != nil {
		t.Fatal(err)
	}
	now = now.Add(panelOpenTTL)
	mint() // any later opening clears what expired
	if got := listing(); len(got) != 0 {
		t.Fatalf("launch page left after expiry: %v", got)
	}
	if _, _, err := s.mint("", true); err != nil {
		t.Fatal(err)
	}
	s.removeLaunchPages()
	if got := listing(); len(got) != 0 {
		t.Fatalf("launch page left at exit: %v", got)
	}
	// Sessions are capped; the oldest is dropped.
	firstSession, _ := s.newSession()
	for i := 0; i < panelMaxSessions; i++ {
		_, _ = s.newSession()
	}
	if s.validSession(firstSession) || len(s.sessions) != panelMaxSessions {
		t.Fatal("sessions are not capped")
	}
}

func TestPanelKeepsEveryNetworkCheck(t *testing.T) {
	dir, rec, _ := panelServeInProcess(t, time.Hour)
	page, c := panelSession(t, dir, rec)
	base := "http://127.0.0.1:" + strconv.Itoa(rec.Port)
	headers := func(resp *http.Response, what string) {
		t.Helper()
		h := resp.Header
		if !strings.Contains(h.Get("Content-Security-Policy"), "default-src 'none'") || h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Cache-Control") != "no-store" ||
			h.Get("X-Frame-Options") != "DENY" || h.Get("Referrer-Policy") != "no-referrer" {
			t.Fatalf("%s: security headers missing: %v", what, h)
		}
		for name := range h {
			if strings.HasPrefix(name, "Access-Control-") {
				t.Fatalf("%s: CORS header %s", what, name)
			}
		}
	}
	session := withCookie(c)
	navigation := func(r *http.Request) {
		r.Header.Set("Sec-Fetch-Site", "cross-site")
		r.Header.Set("Sec-Fetch-Mode", "navigate")
		r.Header.Set("Sec-Fetch-Dest", "document")
	}
	// Refused on every route, the opening address included.
	refused := map[string]func(*http.Request){
		"foreign host":       func(r *http.Request) { session(r); r.Host = "evil.example.com" },
		"rebound host":       func(r *http.Request) { session(r); r.Host = "evil.example.com:" + strconv.Itoa(rec.Port) },
		"foreign origin":     func(r *http.Request) { session(r); r.Header.Set("Origin", "https://evil.example.com") },
		"null origin":        func(r *http.Request) { session(r); r.Header.Set("Origin", "null") },
		"cross-site fetch":   func(r *http.Request) { session(r); r.Header.Set("Sec-Fetch-Site", "cross-site") },
		"same-site fetch":    func(r *http.Request) { session(r); r.Header.Set("Sec-Fetch-Site", "same-site") },
		"another port's tab": func(r *http.Request) { session(r); r.Header.Set("Origin", "http://127.0.0.1:1") },
		// What another site can make a browser send, short of a plain
		// top-level navigation.
		"cross-site frame": func(r *http.Request) { session(r); navigation(r); r.Header.Set("Sec-Fetch-Dest", "iframe") },
		"cross-site image": func(r *http.Request) {
			session(r)
			navigation(r)
			r.Header.Set("Sec-Fetch-Mode", "no-cors")
			r.Header.Set("Sec-Fetch-Dest", "image")
		},
		"cross-site navigation, origin": func(r *http.Request) { session(r); navigation(r); r.Header.Set("Origin", "null") },
		"cross-site navigation, host":   func(r *http.Request) { session(r); navigation(r); r.Host = "evil.example.com" },
	}
	fresh := panelOpenAddress(t, dir, rec, "")
	for name, edit := range refused {
		for _, url := range []string{page, page + dockAPIPath, page + dockServerPath, base + "/panel.css", base + "/expired.js", fresh} {
			resp, body := dockRequest(t, http.MethodGet, url, edit)
			if resp.StatusCode != http.StatusForbidden || len(resp.Cookies()) != 0 || strings.Contains(body, "schemaVersion") {
				t.Fatalf("%s, %s: %d %q", name, url, resp.StatusCode, body)
			}
			headers(resp, name)
		}
	}
	// A cross-site top-level navigation is accepted for an opening address
	// and for nothing else: it is what the launch page does.
	for _, url := range []string{page, page + dockAPIPath, page + dockServerPath, base + "/panel.css", base + "/expired.js"} {
		if resp, _ := dockRequest(t, http.MethodGet, url, func(r *http.Request) { session(r); navigation(r) }); resp.StatusCode != http.StatusForbidden {
			t.Fatalf("cross-site navigation to %s: %d", url, resp.StatusCode)
		}
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions} {
		for _, url := range []string{page, page + dockAPIPath, fresh} {
			if resp, _ := dockRequest(t, method, url, session); resp.StatusCode != http.StatusForbidden {
				t.Fatalf("%s %s: %d", method, url, resp.StatusCode)
			}
		}
	}
	// None of that spent the opening address, and a navigation from the
	// launch page redeems it.
	resp, body := dockRequest(t, http.MethodGet, fresh, navigation)
	if resp.StatusCode != http.StatusOK || len(resp.Cookies()) != 1 || panelRefreshTarget.FindStringSubmatch(body) == nil {
		t.Fatalf("the opening address was spent by a refused request, or a navigation was refused: %d", resp.StatusCode)
	}
	headers(resp, "opening")
	// The two static files are the only things served without a session.
	sheet, _ := dockUI.ReadFile("dockui/dock.css")
	script, _ := dockUI.ReadFile("dockui/expired.js")
	for path, want := range map[string]string{"/panel.css": string(sheet), "/expired.js": string(script)} {
		resp, body := dockRequest(t, http.MethodGet, base+path, nil)
		if resp.StatusCode != http.StatusOK || body != want {
			t.Fatalf("%s: %d", path, resp.StatusCode)
		}
		headers(resp, path)
	}
	for _, url := range []string{base + "/open/" + strings.Repeat("0", 64), page, base + "/nothing", base + "/healthz"} {
		resp, _ := dockRequest(t, http.MethodGet, url, nil)
		headers(resp, url)
	}
}

func TestPanelFilesHoldNoSecretAndArePrivate(t *testing.T) {
	dir, rec, _ := panelServeInProcess(t, time.Hour)
	page, c := panelSession(t, dir, rec)
	pending := panelOpenAddress(t, dir, rec, "")
	segment := strings.Split(strings.TrimPrefix(page, "http://"), "/")[2]
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
		info, _ := e.Info()
		if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("%s is %v", e.Name(), info.Mode())
		}
		if !info.Mode().IsRegular() {
			continue
		}
		b, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		for name, secret := range map[string]string{"session": c.Value, "path segment": segment, "opening code": strings.TrimPrefix(pending, fmt.Sprintf("http://127.0.0.1:%d/open/", rec.Port))} {
			if bytes.Contains(b, []byte(secret)) {
				t.Fatalf("%s holds the %s", e.Name(), name)
			}
		}
	}
	if got := strings.Join(names, " "); got != "panel-server.lock panel.json panel.sock" {
		t.Fatalf("private directory holds: %s", got)
	}
	if info, err := os.Stat(dir); err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o700) {
		t.Fatalf("panel directory: %v %v", info.Mode(), err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, panelRecordName))
	var fields map[string]any
	if json.Unmarshal(b, &fields) != nil || len(fields) != 8 {
		t.Fatalf("record fields: %s", b)
	}
	if _, ok := panelLive(context.Background(), t.TempDir()); ok {
		t.Fatal("an empty directory has a live server")
	}
}

// The server's held lock, not its record, says whether a server is running.
// The record can be deleted, replaced or forged without changing the answer,
// and the server puts its own back.
func TestPanelIsFoundByItsLockNotItsRecord(t *testing.T) {
	dir, rec, _ := panelServeInProcess(t, time.Hour)
	record := filepath.Join(dir, panelRecordName)
	status := func() (int, string) {
		var out, errs bytes.Buffer
		code := panelCommand(env{context.Background(), &out, &errs}, []string{"status", "--panel-directory", dir})
		return code, out.String() + errs.String()
	}
	check := func(what string) {
		t.Helper()
		got, ok := panelLive(context.Background(), dir)
		if !ok || got != rec {
			t.Fatalf("%s: found %+v %v", what, got, ok)
		}
		if code, out := status(); code != exitOK || !strings.Contains(out, fmt.Sprintf("port %d", rec.Port)) {
			t.Fatalf("%s: status %d %s", what, code, out)
		}
		// Asked for its status, the server restored its own record.
		if onDisk, ok := readPanelRecord(dir); !ok || onDisk != rec {
			t.Fatalf("%s: record on disk %+v %v", what, onDisk, ok)
		}
		// And an opening still works.
		if _, c := panelSession(t, dir, rec); c == nil {
			t.Fatalf("%s: no session", what)
		}
	}
	check("untouched")
	if err := os.Remove(record); err != nil {
		t.Fatal(err)
	}
	check("record deleted under the running server")
	if err := os.WriteFile(record, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	check("record unreadable")
	// A record naming another program that answers on its port: the forged
	// instance ID and port are never believed.
	other, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	forged := rec
	forged.Port, forged.PID, forged.Instance = other.Addr().(*net.TCPAddr).Port, os.Getpid(), strings.Repeat("1", 32)
	if err := writePanelRecord(dir, forged); err != nil {
		t.Fatal(err)
	}
	check("record forged")
}

func TestPanelWithNoServerClearsLeftoversAndSaysNotAnswering(t *testing.T) {
	dir, err := panelDirectory(filepath.Join(panelShortDir(t), "panel"))
	if err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (int, string) {
		var out, errs bytes.Buffer
		code := panelCommand(env{context.Background(), &out, &errs}, append(args, "--panel-directory", dir))
		return code, out.String() + errs.String()
	}
	// A record, a socket file and a launch page left by a crash, and a live
	// program answering on the recorded port: nothing holds the lock, so no
	// server is running.
	other, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	stale := panelRecord{SchemaVersion: 1, PID: os.Getpid(), Port: other.Addr().(*net.TCPAddr).Port, StartedAt: time.Now().Add(-time.Hour).UTC(), IdleSeconds: 1800, Instance: strings.Repeat("0", 32), Registry: "/private/example", Version: "fixture"}
	leftovers := []string{panelRecordName, panelSocketName, "open-" + strings.Repeat("a", 32) + ".html"}
	if err := writePanelRecord(dir, stale); err != nil {
		t.Fatal(err)
	}
	for _, name := range leftovers[1:] {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("left behind"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if code, out := run("status"); code != exitOK || !strings.Contains(out, "not running") {
		t.Fatalf("status: %d %s", code, out)
	}
	if code, out := run("stop"); code != exitOK || !strings.Contains(out, "not running") {
		t.Fatalf("stop: %d %s", code, out)
	}
	for _, name := range leftovers {
		if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s was left for a person to delete: %v", name, err)
		}
	}
	// Something holds the server's lock and nothing answers on the socket:
	// that is "running but not answering", never "not running".
	held, err := lock.AcquireNamed(dir, panelServerLock)
	if err != nil {
		t.Fatal(err)
	}
	if presence, _ := panelFind(context.Background(), dir); presence != panelNotAnswering {
		t.Fatalf("presence %d", presence)
	}
	if code, out := run("status"); code != exitOK || !strings.Contains(out, "running but not answering") {
		t.Fatalf("status with the lock held: %d %s", code, out)
	}
	var st panelStatus
	_, out := run("status", "--json")
	if json.Unmarshal([]byte(out), &st) != nil || !st.Running || st.Answering || st.Port != 0 {
		t.Fatalf("status json: %s", out)
	}
	if code, out := run("stop"); code != exitFailed || !strings.Contains(out, "running but not answering") {
		t.Fatalf("stop with the lock held: %d %s", code, out)
	}
	_ = held.Release()
	if presence, _ := panelFind(context.Background(), dir); presence != panelAbsent {
		t.Fatalf("presence after release %d", presence)
	}
}

// Whatever answers on the socket, the reply is used only if it is an opening
// for this panel: a loopback address on the server's port, or a launch page
// inside the private directory.
func TestPanelRefusesAnOpeningThatIsNotItsOwn(t *testing.T) {
	dir, err := panelDirectory(filepath.Join(panelShortDir(t), "panel"))
	if err != nil {
		t.Fatal(err)
	}
	rec := panelRecord{SchemaVersion: 1, PID: os.Getpid(), Port: 49152, StartedAt: time.Now().UTC(), IdleSeconds: 1800, Instance: strings.Repeat("0", 32)}
	good := "http://127.0.0.1:49152/open/" + strings.Repeat("a", 64)
	goodFile := "open-" + strings.Repeat("b", 32) + ".html"
	if err := os.WriteFile(filepath.Join(dir, goodFile), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	open := filepath.Join(dir, "open-"+strings.Repeat("c", 32)+".html")
	if err := os.WriteFile(open, []byte("fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	var reply panelReply
	var mu sync.Mutex
	l, err := net.Listen("unix", filepath.Join(dir, panelSocketName))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			var req panelRequest
			_ = json.NewDecoder(c).Decode(&req)
			mu.Lock()
			_ = json.NewEncoder(c).Encode(reply)
			mu.Unlock()
			c.Close()
		}
	}()
	try := func(r panelReply, asFile bool) (string, error) {
		mu.Lock()
		reply = r
		mu.Unlock()
		return panelRequestOpening(context.Background(), dir, rec, "", asFile)
	}
	if got, err := try(panelReply{OK: true, Address: good}, false); err != nil || got != good {
		t.Fatal(got, err)
	}
	if got, err := try(panelReply{OK: true, File: goodFile}, true); err != nil || got != filepath.Join(dir, goodFile) {
		t.Fatal(got, err)
	}
	for name, bad := range map[string]panelReply{
		"a file address":             {OK: true, Address: "file:///private/example/page.html"},
		"another host":               {OK: true, Address: "http://evil.example.com/open/" + strings.Repeat("a", 64)},
		"another port":               {OK: true, Address: "http://127.0.0.1:49153/open/" + strings.Repeat("a", 64)},
		"another path":               {OK: true, Address: "http://127.0.0.1:49152/s/" + strings.Repeat("a", 64)},
		"a short code":               {OK: true, Address: "http://127.0.0.1:49152/open/abc"},
		"a code with more after it":  {OK: true, Address: good + "?next=http://evil.example.com"},
		"credentials in the address": {OK: true, Address: "http://user@127.0.0.1:49152/open/" + strings.Repeat("a", 64)},
		"https":                      {OK: true, Address: "https://127.0.0.1:49152/open/" + strings.Repeat("a", 64)},
		"both an address and a file": {OK: true, Address: good, File: goodFile},
		"nothing":                    {OK: true},
	} {
		if got, err := try(bad, false); err == nil {
			t.Errorf("%s was accepted as an address: %q", name, got)
		}
	}
	for name, bad := range map[string]panelReply{
		"a path outside the directory": {OK: true, File: "../" + goodFile},
		"an absolute path":             {OK: true, File: "/etc/hosts"},
		"another name":                 {OK: true, File: "panel.json"},
		"a file that does not exist":   {OK: true, File: "open-" + strings.Repeat("d", 32) + ".html"},
		"a file others can read":       {OK: true, File: filepath.Base(open)},
		"an address":                   {OK: true, Address: good},
		"both":                         {OK: true, Address: good, File: goodFile},
	} {
		if got, err := try(bad, true); err == nil {
			t.Errorf("%s was accepted as a launch page: %q", name, got)
		}
	}
}

func TestPanelDirectoryRefusesALongPathAndACheckout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("path limits differ")
	}
	parent := panelShortDir(t)
	long := filepath.Join(parent, strings.Repeat("a", 120))
	if _, err := panelDirectory(long); err == nil || !strings.Contains(err.Error(), "too long for a local socket") {
		t.Fatalf("long path: %v", err)
	}
	var out, errs bytes.Buffer
	if code := panelCommand(env{context.Background(), &out, &errs}, []string{"status", "--panel-directory", long}); code != exitUsage || !strings.Contains(errs.String(), "too long for a local socket") {
		t.Fatalf("status with a long path: %d %s", code, errs.String())
	}
	// Refused before anything was created.
	if _, err := os.Lstat(long); !os.IsNotExist(err) {
		t.Fatalf("the refused directory was created: %v", err)
	}
	checkout := panelShortDir(t)
	if err := os.Mkdir(filepath.Join(checkout, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := panelDirectory(filepath.Join(checkout, "panel")); err == nil {
		t.Fatal("a directory inside a checkout was accepted")
	}
	open := panelShortDir(t)
	if err := os.Chmod(open, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := panelDirectory(open); err == nil {
		t.Fatal("a directory others can read was accepted")
	}
}

func TestPanelListUsesThePagesWordsAndWithholdsIdentifiers(t *testing.T) {
	d, root := dockFixtureReader(t)
	report := d.read(context.Background(), false)
	list := buildPanelList(report)
	if list.SchemaVersion != 1 || len(list.Loops) != len(report.Loops) || list.NeedsYou != len(report.NeedsYou) || list.AllClear != report.AllClear || list.Complete != report.Complete {
		t.Fatalf("%+v", list)
	}
	needs := 0
	for i, l := range list.Loops {
		if l.Phrase != report.Loops[i].Phrase || l.State != report.Loops[i].State || l.Name != report.Loops[i].Name || !panelKeyPattern.MatchString(l.Key) {
			t.Fatalf("loop %d: %+v", i, l)
		}
		if l.NeedsYou {
			needs++
		}
	}
	if needs == 0 || needs != len(report.NeedsYou) {
		t.Fatalf("%d loops marked as needing a person, report has %d", needs, len(report.NeedsYou))
	}
	var out, errs bytes.Buffer
	before := treeDigest(t, root)
	if code := panelCommand(env{context.Background(), &out, &errs}, []string{"list", "--json", "--directory", d.directory, "--offline"}); code != exitOK {
		t.Fatalf("list: %d %s", code, errs.String())
	}
	if treeDigest(t, root) != before {
		t.Fatal("panel list changed a file")
	}
	if out.Len() > 64<<10 {
		t.Fatalf("output is %d bytes", out.Len())
	}
	var decoded panelList
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil || len(decoded.Loops) != len(report.Loops) {
		t.Fatal(err, out.String())
	}
	for _, private := range []string{"loop_", "thr_", root, "/private", "\"note\"", "configPath", "stateDir"} {
		if strings.Contains(out.String(), private) {
			t.Fatalf("list output contains %q: %s", private, out.String())
		}
	}
	out.Reset()
	if code := panelCommand(env{context.Background(), &out, &errs}, []string{"list", "--directory", d.directory, "--offline"}); code != exitOK || !strings.Contains(out.String(), ": "+report.Loops[0].Phrase) {
		t.Fatalf("text list: %d %s", code, out.String())
	}
	// Each loop's key opens its page; a name, an ID and a key all resolve.
	key, err := panelLoopKey(d.directory, report.Loops[0].Name)
	if err != nil || key != report.Loops[0].Key {
		t.Fatal(key, err)
	}
	if again, err := panelLoopKey(d.directory, key); err != nil || again != key {
		t.Fatal(again, err)
	}
	if _, err := panelLoopKey(d.directory, "no-such-loop"); err == nil {
		t.Fatal("an unknown loop resolved")
	}
}

// The executables the process tests share: the keepgoing binary and a
// stand-in browser launcher. Each is created once per test binary and run
// once before any test uses it, because macOS can refuse the first launch of
// a new executable under load (it is ended by a signal as it starts).
var (
	panelToolsOnce sync.Once
	panelToolsDir  string
	panelToolsErr  error
	// panelToolsRefused is set when an executable is still refused after
	// several tries: an environment failure, reported as a skip.
	panelToolsRefused string
)

const panelOpenedEnv = "KEEPGOING_TEST_PANEL_OPENED"

func panelTestCleanup() {
	if panelToolsDir != "" {
		os.RemoveAll(panelToolsDir)
	}
}

// warmUp runs an executable until it starts without being ended by a signal.
func warmUp(path string, args ...string) bool {
	for attempt := 0; attempt < 8; attempt++ {
		cmd := exec.Command(path, args...)
		cmd.Env = append(os.Environ(), panelOpenedEnv+"="+os.DevNull)
		err := cmd.Run()
		var exit *exec.ExitError
		if err == nil || (errors.As(err, &exit) && exit.ExitCode() >= 0) {
			return true
		}
		time.Sleep(time.Duration(attempt+1) * 150 * time.Millisecond)
	}
	return false
}

func panelTools(t *testing.T) (binary, launcher string) {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("detached process integration runs on macOS and Linux")
	}
	panelToolsOnce.Do(func() {
		dir, err := os.MkdirTemp("/tmp", "kgpt")
		if err != nil {
			panelToolsErr = err
			return
		}
		if panelToolsDir, err = filepath.EvalSymlinks(dir); err != nil {
			panelToolsErr = err
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if out, err := exec.CommandContext(ctx, "go", "build", "-o", filepath.Join(panelToolsDir, "keepgoing"), ".").CombinedOutput(); err != nil {
			panelToolsErr = fmt.Errorf("build: %v %s", err, out)
			return
		}
		// One short line per call, so concurrent calls cannot interleave.
		// With a second argument of "slow" in the environment it lingers, so
		// a test can look at the process list while it runs.
		script := "#!/bin/sh\nprintf '%s %s\\n' \"$#\" \"$*\" >> \"$" + panelOpenedEnv + "\"\nif [ -n \"$KEEPGOING_TEST_PANEL_LINGER\" ]; then sleep \"$KEEPGOING_TEST_PANEL_LINGER\"; fi\n"
		if err := os.WriteFile(filepath.Join(panelToolsDir, "launcher"), []byte(script), 0o700); err != nil {
			panelToolsErr = err
			return
		}
		if !warmUp(filepath.Join(panelToolsDir, "keepgoing"), "version") {
			panelToolsRefused = "the built keepgoing binary"
		} else if !warmUp(filepath.Join(panelToolsDir, "launcher"), "warm-up") {
			panelToolsRefused = "the stand-in launcher"
		}
	})
	if panelToolsErr != nil {
		t.Fatal(panelToolsErr)
	}
	if panelToolsRefused != "" {
		t.Skipf("environment failure, not a test failure: the system refused to start %s after several tries", panelToolsRefused)
	}
	return filepath.Join(panelToolsDir, "keepgoing"), filepath.Join(panelToolsDir, "launcher")
}

// panelProcessWorld is a fixture registry, a private panel directory and a
// stand-in browser launcher that records exactly what it was given.
type panelProcessWorld struct {
	t        *testing.T
	bin      string
	root     string // the fixture tree: registry and every loop's files
	registry string
	dir      string
	opened   string // file the stand-in launcher appends its arguments to
	env      []string
}

func newPanelProcessWorld(t *testing.T) *panelProcessWorld {
	t.Helper()
	bin, launcher := panelTools(t)
	w := &panelProcessWorld{t: t, bin: bin, root: t.TempDir()}
	w.registry = dockScratch(t, w.root, time.Now().UTC())
	short := panelShortDir(t)
	w.dir = filepath.Join(short, "panel")
	w.opened = filepath.Join(short, "opened")
	w.env = append(os.Environ(), panelOpenerEnv+"="+launcher, panelOpenedEnv+"="+w.opened)
	t.Cleanup(func() { w.run("panel", "stop", "--panel-directory", w.dir) })
	return w
}

func (w *panelProcessWorld) command(args ...string) (*exec.Cmd, *bytes.Buffer, *bytes.Buffer) {
	cmd := exec.Command(w.bin, args...)
	var out, errs bytes.Buffer
	cmd.Stdout, cmd.Stderr, cmd.Env = &out, &errs, w.env
	return cmd, &out, &errs
}

func (w *panelProcessWorld) run(args ...string) (int, string, string) {
	w.t.Helper()
	cmd, out, errs := w.command(args...)
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		w.t.Fatalf("%v: %v", args, err)
	}
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		var exit *exec.ExitError
		switch {
		case err == nil:
			return 0, out.String(), errs.String()
		case errors.As(err, &exit) && exit.ExitCode() >= 0:
			return exit.ExitCode(), out.String(), errs.String()
		}
		w.t.Fatalf("%v: %v", args, err)
	case <-time.After(60 * time.Second):
		_ = cmd.Process.Kill()
		w.t.Fatalf("%v did not finish", args)
	}
	return -1, "", ""
}

func (w *panelProcessWorld) open(extra ...string) (int, string, string) {
	return w.run(append([]string{"panel", "--panel-directory", w.dir, "--directory", w.registry, "--offline"}, extra...)...)
}

// launched waits for the stand-in launcher to have been called want times and
// returns what it was given, requiring exactly one argument per call.
func (w *panelProcessWorld) launched(want int) []string {
	w.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		b, _ := os.ReadFile(w.opened)
		if strings.Count(string(b), "\n") >= want || time.Now().After(deadline) {
			var out []string
			for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
				if line == "" {
					continue
				}
				count, argument, _ := strings.Cut(line, " ")
				if count != "1" || strings.Contains(argument, " ") {
					w.t.Fatalf("the launcher was given %q", line)
				}
				out = append(out, argument)
			}
			if len(out) != want {
				w.t.Fatalf("the launcher was called %d times, want %d", len(out), want)
			}
			return out
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// launchAddress reads the opening address out of a launch page, as a browser
// handed that file would.
func (w *panelProcessWorld) launchAddress(page string) string {
	w.t.Helper()
	if filepath.Dir(page) != w.dir || !panelLaunchPattern.MatchString(filepath.Base(page)) {
		w.t.Fatalf("the launcher was given %q, not a launch page in the private directory", page)
	}
	b, err := os.ReadFile(page)
	m := panelRefreshTarget.FindStringSubmatch(string(b))
	if err != nil || m == nil {
		w.t.Fatalf("launch page %s: %v %s", page, err, b)
	}
	return m[1]
}

func (w *panelProcessWorld) record() panelRecord {
	w.t.Helper()
	rec, ok := panelLive(context.Background(), w.dir)
	if !ok {
		w.t.Fatal("no live panel server")
	}
	return rec
}

func processCommandLines(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("ps", "-axww", "-o", "pid=,command=").Output()
	if err != nil {
		t.Skipf("ps unavailable: %v", err)
	}
	return string(out)
}

func (w *panelProcessWorld) serverProcesses() int {
	n := 0
	for _, line := range strings.Split(processCommandLines(w.t), "\n") {
		if strings.Contains(line, "__panel-server") && strings.Contains(line, "--panel-directory "+w.dir+" ") {
			n++
		}
	}
	return n
}

func TestPanelCommandStartsOneServerReusesItAndStops(t *testing.T) {
	w := newPanelProcessWorld(t)
	before := treeDigest(t, w.root)
	if code, out, _ := w.run("panel", "status", "--panel-directory", w.dir); code != exitOK || !strings.Contains(out, "not running") {
		t.Fatalf("status before: %d %s", code, out)
	}
	code, out, errs := w.open()
	if code != exitOK || !strings.Contains(out, "Started the keepgoing panel") {
		t.Fatalf("first open: %d %s %s", code, out, errs)
	}
	first := w.record()
	// The second call reuses it: same process, same port, same instance.
	if code, out, errs := w.open(); code != exitOK || !strings.HasPrefix(out, "Opened the keepgoing panel") {
		t.Fatalf("second open: %d %s %s", code, out, errs)
	}
	if again := w.record(); again != first {
		t.Fatalf("a second server replaced the first: %+v then %+v", first, again)
	}
	pages := w.launched(2)
	if pages[0] == pages[1] {
		t.Fatalf("launcher calls: %q", pages)
	}
	// The launcher got a file in the private directory, never an address.
	prefix := fmt.Sprintf("http://127.0.0.1:%d/open/", first.Port)
	addresses := []string{w.launchAddress(pages[0]), w.launchAddress(pages[1])}
	for i, a := range addresses {
		if !panelCodePattern.MatchString(strings.TrimPrefix(a, prefix)) {
			t.Fatalf("launch page %d goes to %q", i, a)
		}
		if strings.Contains(pages[i], "/open/") || strings.Contains(pages[i], "http") {
			t.Fatalf("the launcher was given an address: %q", pages[i])
		}
	}

	// Use one address as a browser would. Its launch page is gone at once;
	// then look for the session anywhere another program could see it.
	target, c := panelRedeem(t, addresses[0])
	if _, err := os.Lstat(pages[0]); !os.IsNotExist(err) {
		t.Fatalf("the launch page outlived its use: %v", err)
	}
	segment := strings.Split(target, "/")[2]
	page := fmt.Sprintf("http://127.0.0.1:%d/s/%s/", first.Port, segment)
	if resp, body := dockRequest(t, http.MethodGet, page+dockAPIPath, withCookie(c)); resp.StatusCode != http.StatusOK || !strings.Contains(body, `"readOnly":true`) {
		t.Fatalf("data with the session: %d", resp.StatusCode)
	}
	ps := processCommandLines(t)
	if w.serverProcesses() != 1 {
		t.Fatalf("%d server processes", w.serverProcesses())
	}
	launcherLog, _ := os.ReadFile(w.opened)
	secrets := map[string]string{"session cookie": c.Value, "page path segment": segment, "used opening code": strings.TrimPrefix(addresses[0], prefix), "pending opening code": strings.TrimPrefix(addresses[1], prefix)}
	for name, text := range map[string]string{"process list": ps, "launcher arguments": string(launcherLog), "command output": out} {
		for what, secret := range secrets {
			if strings.Contains(text, secret) {
				t.Fatalf("the %s appears in the %s", what, name)
			}
		}
	}
	// The server writes no log: its private directory holds the record, the
	// socket, the two lock files and the one launch page still pending.
	names := []string{}
	entries, _ := os.ReadDir(w.dir)
	for _, e := range entries {
		names = append(names, e.Name())
		if e.Type().IsRegular() && e.Name() != filepath.Base(pages[1]) {
			b, _ := os.ReadFile(filepath.Join(w.dir, e.Name()))
			for what, secret := range secrets {
				if bytes.Contains(b, []byte(secret)) {
					t.Fatalf("%s holds the %s", e.Name(), what)
				}
			}
		}
	}
	if got := strings.Join(names, " "); got != filepath.Base(pages[1])+" panel-open.lock panel-server.lock panel.json panel.sock" {
		t.Fatalf("private directory holds: %s", got)
	}

	// --no-open prints an address, says who can use it, and starts no
	// browser; --loop opens that loop's page.
	list := buildPanelList((&dockReader{directory: w.registry, refresh: dockDefaultRefresh, staleAfter: defaultStaleAfter, query: inspectDaemon}).read(context.Background(), false))
	code, out, errs = w.open("--no-open", "--loop", list.Loops[0].Name)
	if code != exitOK || !strings.Contains(out, prefix) || !strings.Contains(out, "within 30 seconds") || !strings.Contains(out, "Anyone who can read this terminal can use it") || len(w.launched(2)) != 2 {
		t.Fatalf("--no-open: %d %s %s", code, out, errs)
	}
	printed := out[strings.Index(out, prefix):]
	printed = printed[:strings.IndexAny(printed, " \n")]
	if target, _ := panelRedeem(t, printed); !strings.HasSuffix(target, "#/loop/"+list.Loops[0].Key) {
		t.Fatalf("--loop goes to %s", target)
	}
	if code, _, errs := w.open("--loop", "no-such-loop"); code != exitFailed || !strings.Contains(errs, "no registered loop has that name") {
		t.Fatalf("unknown loop: %d %s", code, errs)
	}

	// Status names the port and says it is in use, never a secret.
	code, out, _ = w.run("panel", "status", "--panel-directory", w.dir)
	if code != exitOK || !strings.Contains(out, fmt.Sprintf("port %d", first.Port)) || !strings.Contains(out, "In use: a page read from it in the last minute") || !strings.Contains(out, "30m0s after the last page stops reading") || strings.Contains(out, "/open/") {
		t.Fatalf("status: %d %s", code, out)
	}
	for what, secret := range secrets {
		if strings.Contains(out, secret) {
			t.Fatalf("status prints the %s", what)
		}
	}
	_, out, _ = w.run("panel", "status", "--json", "--panel-directory", w.dir)
	var st panelStatus
	if json.Unmarshal([]byte(out), &st) != nil || !st.Running || !st.Answering || !st.InUse || st.Port != first.Port || st.IdleSeconds != 1800 || st.IdleRemainingSeconds == nil || *st.IdleRemainingSeconds > 1800 || *st.IdleRemainingSeconds < 1700 {
		t.Fatalf("status json: %s", out)
	}

	// Stop ends the server and removes its record, socket and launch pages.
	// Nothing in any loop's files changed at any point.
	if code, out, errs := w.run("panel", "stop", "--panel-directory", w.dir); code != exitOK || !strings.Contains(out, "Stopped the keepgoing panel") {
		t.Fatalf("stop: %d %s %s", code, out, errs)
	}
	if _, ok := panelLive(context.Background(), w.dir); ok || panelServerHeld(w.dir) {
		t.Fatal("still running after stop")
	}
	entries, _ = os.ReadDir(w.dir)
	names = names[:0]
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if got := strings.Join(names, " "); got != "panel-open.lock panel-server.lock" {
		t.Fatalf("left behind after stop: %s", got)
	}
	if w.serverProcesses() != 0 {
		t.Fatal("the server process is still running")
	}
	if code, out, _ := w.run("panel", "stop", "--panel-directory", w.dir); code != exitOK || !strings.Contains(out, "not running") {
		t.Fatalf("second stop: %d %s", code, out)
	}
	if treeDigest(t, w.root) != before {
		t.Fatal("a file in the registry or a loop's directory changed across start, open, status and stop")
	}
}

// While the launcher is running, nothing that can open the panel is in any
// process's arguments: not the opening code, and not the session it becomes.
func TestPanelPutsNothingRedeemableInAnyProcessArguments(t *testing.T) {
	w := newPanelProcessWorld(t)
	w.env = append(w.env, "KEEPGOING_TEST_PANEL_LINGER=3")
	if code, out, errs := w.open(); code != exitOK {
		t.Fatalf("open: %d %s %s", code, out, errs)
	}
	page := w.launched(1)[0]
	address := w.launchAddress(page)
	rec := w.record()
	code := strings.TrimPrefix(address, fmt.Sprintf("http://127.0.0.1:%d/open/", rec.Port))
	if !panelCodePattern.MatchString(code) {
		t.Fatalf("launch page goes to %q", address)
	}
	// The launcher is still running, with the launch page as its argument.
	during := processCommandLines(t)
	if !strings.Contains(during, page) {
		t.Fatalf("the launcher is not in the process list while it runs")
	}
	target, c := panelRedeem(t, address)
	after := processCommandLines(t)
	for name, ps := range map[string]string{"while the launcher ran": during, "after the opening was used": after} {
		for what, secret := range map[string]string{"opening code": code, "session": c.Value, "page path segment": strings.Split(target, "/")[2], "an opening address": "/open/"} {
			if strings.Contains(ps, secret) {
				t.Fatalf("%s: the %s is in a process's arguments", name, what)
			}
		}
	}
}

// The background server gets a small fixed environment, the private directory
// as its working directory, the null device for its three standard streams,
// and none of the caller's other descriptors.
func TestPanelServerInheritsNothingFromItsCaller(t *testing.T) {
	w := newPanelProcessWorld(t)
	marker := filepath.Join(panelShortDir(t), "inherited-descriptor")
	held, err := os.Create(marker)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	cmd, out, errs := w.command("panel", "--panel-directory", w.dir, "--directory", w.registry, "--offline")
	cmd.Env = append(cmd.Env, "KEEPGOING_TEST_SECRET_MARKER=fictional-secret-value", "AWS_SECRET_ACCESS_KEY=fictional-secret-value")
	cmd.ExtraFiles = []*os.File{held} // descriptor 3 in the caller, as a shell redirection would leave it
	cmd.Dir = w.root
	if err := cmd.Run(); err != nil {
		t.Fatalf("open: %v %s %s", err, out, errs)
	}
	rec := w.record()
	pid := strconv.Itoa(rec.PID)
	var environment, descriptors, cwd string
	if runtime.GOOS == "linux" {
		b, err := os.ReadFile("/proc/" + pid + "/environ")
		if err != nil {
			t.Skipf("cannot read the server's environment: %v", err)
		}
		environment = strings.ReplaceAll(string(b), "\x00", "\n")
		entries, _ := os.ReadDir("/proc/" + pid + "/fd")
		for _, e := range entries {
			target, _ := os.Readlink("/proc/" + pid + "/fd/" + e.Name())
			descriptors += e.Name() + " " + target + "\n"
		}
		cwd, _ = os.Readlink("/proc/" + pid + "/cwd")
	} else {
		b, err := exec.Command("ps", "eww", "-o", "command=", "-p", pid).Output()
		if err != nil {
			t.Skipf("cannot read the server's environment: %v", err)
		}
		environment = string(b)
		b, err = exec.Command("lsof", "-a", "-p", pid, "-d", "0-64,cwd", "-Fn").Output()
		if err != nil {
			t.Skipf("cannot list the server's descriptors: %v", err)
		}
		descriptors = string(b)
		cwd = descriptors
	}
	for _, leaked := range []string{"KEEPGOING_TEST_SECRET_MARKER", "fictional-secret-value", "AWS_SECRET_ACCESS_KEY", panelOpenerEnv, panelOpenedEnv} {
		if strings.Contains(environment, leaked) {
			t.Fatalf("the server's environment contains %s", leaked)
		}
	}
	if !strings.Contains(environment, "PATH=/usr/bin:/bin:/usr/sbin:/sbin") {
		t.Fatalf("the server's environment is not the fixed one: %s", environment)
	}
	if strings.Contains(descriptors, marker) {
		t.Fatalf("the server holds the caller's descriptor:\n%s", descriptors)
	}
	if !strings.Contains(cwd, w.dir) || strings.Contains(cwd, "n"+w.root+"\n") {
		t.Fatalf("the server's working directory is not its private directory:\n%s", cwd)
	}
	if !strings.Contains(descriptors, os.DevNull) {
		t.Fatalf("the server's standard streams are not the null device:\n%s", descriptors)
	}
}

func TestPanelConcurrentCommandsEndWithOneServer(t *testing.T) {
	w := newPanelProcessWorld(t)
	const callers = 6
	var wg sync.WaitGroup
	codes := make([]int, callers)
	outs := make([]string, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i], outs[i], _ = w.open()
		}(i)
	}
	wg.Wait()
	started := 0
	for i := range codes {
		if codes[i] != exitOK {
			t.Fatalf("caller %d: %d %s", i, codes[i], outs[i])
		}
		if strings.HasPrefix(outs[i], "Started") {
			started++
		}
	}
	if started != 1 {
		t.Fatalf("%d callers started a server", started)
	}
	rec := w.record()
	if n := w.serverProcesses(); n != 1 {
		t.Fatalf("%d server processes", n)
	}
	for _, page := range w.launched(callers) {
		if a := w.launchAddress(page); !strings.HasPrefix(a, fmt.Sprintf("http://127.0.0.1:%d/open/", rec.Port)) {
			t.Fatalf("an address for another server: %s", a)
		}
	}
	// A server started by hand beside it gives way.
	if code, _, _ := w.run("__panel-server", "--panel-directory", w.dir, "--directory", w.registry); code == exitOK {
		t.Fatal("a second server ran beside the first")
	}
	if again := w.record(); again != rec {
		t.Fatal("the second server replaced the record")
	}
	// The internal server command bounds its idle limit as the public one does.
	if code, _, _ := w.run("__panel-server", "--panel-directory", w.dir+"-other", "--directory", w.registry, "--idle", "48h"); code != exitUsage {
		t.Fatalf("an idle limit above a day was accepted: %d", code)
	}
}

func TestPanelServerExitsWhenIdleAndLeavesLoopsAlone(t *testing.T) {
	w := newPanelProcessWorld(t)
	before := treeDigest(t, w.root)
	cmd, _, _ := w.command("__panel-server", "--panel-directory", w.dir, "--directory", w.registry, "--idle", "1500ms", "--offline")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	var rec panelRecord
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		var ok bool
		if rec, ok = panelLive(context.Background(), w.dir); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the server did not start")
		}
	}
	// Page requests keep it alive past its idle limit.
	page, c := panelSession(t, w.dir, rec)
	for end := time.Now().Add(2500 * time.Millisecond); time.Now().Before(end); time.Sleep(200 * time.Millisecond) {
		if resp, _ := dockRequest(t, http.MethodGet, page+dockAPIPath, withCookie(c)); resp.StatusCode != http.StatusOK {
			t.Fatalf("in use: %d", resp.StatusCode)
		}
	}
	select {
	case err := <-exited:
		t.Fatalf("exited while in use: %v", err)
	default:
	}
	idleFrom := time.Now()
	for {
		select {
		case err := <-exited:
			if err != nil {
				t.Fatalf("idle exit was not clean: %v", err)
			}
			if waited := time.Since(idleFrom); waited < time.Second {
				t.Fatalf("exited %s after its last request", waited)
			}
			for _, name := range []string{panelRecordName, panelSocketName} {
				if _, err := os.Lstat(filepath.Join(w.dir, name)); !os.IsNotExist(err) {
					t.Fatalf("%s was left behind: %v", name, err)
				}
			}
			if treeDigest(t, w.root) != before {
				t.Fatal("a file in the registry or a loop's directory changed across start, use and idle exit")
			}
			return
		case <-time.After(150 * time.Millisecond):
			// Asking for status, loading a static file and being refused
			// are not use.
			_, _ = panelControl(context.Background(), w.dir, panelRequest{Op: "status"})
			dockRequest(t, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/panel.css", rec.Port), nil)
			dockRequest(t, http.MethodGet, page, nil)
			if time.Since(idleFrom) > 8*time.Second {
				t.Fatal("still running 8 seconds after its last request")
			}
		}
	}
}

// The 30-minute rule on the server's own clock: it exits 30 minutes after the
// last use, use resets the count, status requests, static files and refused
// requests do not, and on exit the record and the socket are gone and no loop
// file has changed.
func TestPanelIdleRuleOnAFakeClock(t *testing.T) {
	root := t.TempDir()
	registryDir := dockScratch(t, root, dockNow)
	runsClock(t, dockNow)
	before := treeDigest(t, root)
	dir, err := panelDirectory(filepath.Join(panelShortDir(t), "panel"))
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	now := time.Date(2030, 3, 4, 9, 0, 0, 0, time.UTC)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	advance := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
	ticks := make(chan time.Time)
	done := make(chan int, 1)
	go func() {
		done <- runPanelServer(context.Background(), panelServerConfig{dir: dir, registry: registryDir, idle: panelDefaultIdle, refresh: dockDefaultRefresh, staleAfter: defaultStaleAfter, offline: true, now: clock, ticks: ticks})
	}()
	var rec panelRecord
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		var ok bool
		if rec, ok = panelLive(context.Background(), dir); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the server did not start")
		}
	}
	if rec.IdleSeconds != 1800 || !rec.StartedAt.Equal(now) {
		t.Fatalf("record: %+v", rec)
	}
	// tick asks the idle question once and reports whether the server exited.
	tick := func() bool {
		t.Helper()
		select {
		case ticks <- clock():
		case code := <-done:
			done <- code
			return true
		case <-time.After(5 * time.Second):
			t.Fatal("the idle check is not running")
		}
		select {
		case code := <-done:
			done <- code
			return true
		case <-time.After(150 * time.Millisecond):
			return false
		}
	}
	status := func() panelReply {
		t.Helper()
		reply, err := panelControl(context.Background(), dir, panelRequest{Op: "status"})
		if err != nil {
			t.Fatal(err)
		}
		return reply
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", rec.Port)
	page, c := panelSession(t, dir, rec) // use, at 09:00
	advance(29*time.Minute + 59*time.Second)
	if tick() {
		t.Fatal("exited one second before 30 minutes without use")
	}
	if s := status(); s.IdleRemainingSeconds != 1 || s.SinceUseSeconds != 1799 {
		t.Fatalf("status says %d seconds left, %d since use", s.IdleRemainingSeconds, s.SinceUseSeconds)
	}
	// Reading the page is use: the 30 minutes start again.
	if resp, _ := dockRequest(t, http.MethodGet, page+dockAPIPath, withCookie(c)); resp.StatusCode != http.StatusOK {
		t.Fatalf("page read: %d", resp.StatusCode)
	}
	if s := status(); s.IdleRemainingSeconds != 1800 || s.SinceUseSeconds != 0 {
		t.Fatalf("after use, status says %d seconds left", s.IdleRemainingSeconds)
	}
	advance(29 * time.Minute)
	// None of these is use: asking for status, the stylesheet and script the
	// expired page loads, a page request without a session, a wrong opening
	// address, a refused host, and a path that is not there.
	dockRequest(t, http.MethodGet, base+"/panel.css", nil)
	dockRequest(t, http.MethodGet, base+"/expired.js", nil)
	dockRequest(t, http.MethodGet, page+dockAPIPath, nil)
	dockRequest(t, http.MethodGet, base+"/open/"+strings.Repeat("0", 64), nil)
	dockRequest(t, http.MethodGet, page, func(r *http.Request) { r.AddCookie(c); r.Host = "evil.example.com" })
	dockRequest(t, http.MethodGet, base+"/healthz", nil)
	if s := status(); s.IdleRemainingSeconds != 60 {
		t.Fatalf("requests that are not use changed the time left to %d", s.IdleRemainingSeconds)
	}
	if tick() {
		t.Fatal("exited 29 minutes after the last use")
	}
	// A new opening address is use too.
	panelOpenAddress(t, dir, rec, "")
	advance(29*time.Minute + 59*time.Second)
	if tick() {
		t.Fatal("exited before 30 minutes after an opening address was made")
	}
	advance(time.Second)
	if !tick() {
		t.Fatal("still running 30 minutes after the last use")
	}
	if code := <-done; code != exitOK {
		t.Fatalf("idle exit code %d", code)
	}
	for _, name := range []string{panelRecordName, panelSocketName} {
		if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s was left behind: %v", name, err)
		}
	}
	if panelServerHeld(dir) {
		t.Fatal("the lock is still held after the idle exit")
	}
	if treeDigest(t, root) != before {
		t.Fatal("a file in the registry or a loop's directory changed across start, use and idle exit")
	}
}

// The page is told which command serves it, and nothing else.
func TestPanelAndDockSayWhichCommandServesThePage(t *testing.T) {
	dir, rec, _ := panelServeInProcess(t, 45*time.Minute)
	page, c := panelSession(t, dir, rec)
	resp, body := dockRequest(t, http.MethodGet, page+dockServerPath, withCookie(c))
	if resp.StatusCode != http.StatusOK || strings.TrimSpace(body) != `{"schemaVersion":1,"servedBy":"panel","idleSeconds":2700}` {
		t.Fatalf("panel: %d %s", resp.StatusCode, body)
	}
	if resp, _ := dockRequest(t, http.MethodGet, page+dockServerPath, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("without a session: %d", resp.StatusCode)
	}
	base, _, _ := dockServe(t)
	if resp, body := dockRequest(t, http.MethodGet, base+dockServerPath, nil); resp.StatusCode != http.StatusOK || strings.TrimSpace(body) != `{"schemaVersion":1,"servedBy":"dock"}` {
		t.Fatalf("dock: %d %s", resp.StatusCode, body)
	}
	// The expired page shows no button until its script runs, and that
	// script is plain.
	own, _ := dockUI.ReadFile("dockui/expired.js")
	if !strings.Contains(panelExpiredPage, `id="copy-row" hidden`) || !strings.Contains(panelExpiredPage, `<script src="/expired.js" defer></script>`) || strings.Contains(panelExpiredPage, "onclick") {
		t.Fatal("the expired page's copy button is not hidden by default or uses inline script")
	}
	for _, banned := range []string{"http://", "https://", "eval(", "innerHTML", "document.cookie", "localStorage", "fetch("} {
		if strings.Contains(string(own), banned) {
			t.Fatalf("expired.js contains %q", banned)
		}
	}
	// Neither page that carries an address has a script.
	for name, text := range map[string]string{"launch page": panelLaunchPage, "opened page": panelOpenedPage} {
		if strings.Contains(text, "<script") || strings.Contains(text, "style=") {
			t.Fatalf("the %s has script or inline style", name)
		}
	}
}

func TestPanelRefusesWhereItIsUnverified(t *testing.T) {
	previous := panelSupported
	panelSupported = func() bool { return false }
	t.Cleanup(func() { panelSupported = previous })
	d, _ := dockFixtureReader(t)
	for _, args := range [][]string{{}, {"--no-open"}, {"status"}, {"stop"}} {
		var out, errs bytes.Buffer
		if code := panelCommand(env{context.Background(), &out, &errs}, args); code != exitFailed || !strings.Contains(errs.String(), "has not been verified on Windows yet") || !strings.Contains(errs.String(), "keepgoing dock") || out.Len() != 0 {
			t.Fatalf("%v: %d %s", args, code, errs.String())
		}
	}
	if code := panelServerCommand(env{context.Background(), io.Discard, io.Discard}, []string{"--panel-directory", t.TempDir()}); code != exitUsage {
		t.Fatalf("the server started: %d", code)
	}
	// Listing needs no server and works everywhere.
	var out, errs bytes.Buffer
	if code := panelCommand(env{context.Background(), &out, &errs}, []string{"list", "--directory", d.directory, "--offline"}); code != exitOK {
		t.Fatalf("list: %d %s", code, errs.String())
	}
}

// A command probes the server's lock by taking it for an instant. A server
// that starts at that instant waits for it; it does not give up.
func TestPanelServerStartsWhileItsLockIsBeingProbed(t *testing.T) {
	root := t.TempDir()
	registryDir := dockScratch(t, root, dockNow)
	runsClock(t, dockNow)
	dir, err := panelDirectory(filepath.Join(panelShortDir(t), "panel"))
	if err != nil {
		t.Fatal(err)
	}
	probe, err := lock.AcquireNamed(dir, panelServerLock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() {
		done <- runPanelServer(ctx, panelServerConfig{dir: dir, registry: registryDir, idle: time.Hour, refresh: dockDefaultRefresh, staleAfter: defaultStaleAfter, offline: true, now: time.Now})
	}()
	defer func() { cancel(); <-done }()
	time.Sleep(400 * time.Millisecond)
	select {
	case code := <-done:
		done <- code
		t.Fatalf("the server gave up while its lock was being probed: %d", code)
	default:
	}
	_ = probe.Release()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if _, ok := panelLive(context.Background(), dir); ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the server did not start after the probe let go")
		}
	}
}

// A server removes only the socket it created, and exits when the lock it
// holds is no longer the file at its path.
func TestPanelServerRemovesOnlyItsOwnSocketAndExitsWithoutItsLock(t *testing.T) {
	root := t.TempDir()
	registryDir := dockScratch(t, root, dockNow)
	runsClock(t, dockNow)
	dir, err := panelDirectory(filepath.Join(panelShortDir(t), "panel"))
	if err != nil {
		t.Fatal(err)
	}
	ticks := make(chan time.Time)
	done := make(chan int, 1)
	go func() {
		done <- runPanelServer(context.Background(), panelServerConfig{dir: dir, registry: registryDir, idle: time.Hour, refresh: dockDefaultRefresh, staleAfter: defaultStaleAfter, offline: true, now: time.Now, ticks: ticks})
	}()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if _, ok := panelLive(context.Background(), dir); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the server did not start")
		}
	}
	// A check with the lock in place changes nothing.
	ticks <- time.Now()
	select {
	case code := <-done:
		t.Fatalf("exited with its lock in place: %d", code)
	case <-time.After(150 * time.Millisecond):
	}
	// Another server's files take the place of this one's: a new lock file,
	// a socket and a record that are not its own.
	lockPath, socket := filepath.Join(dir, panelServerLock+".lock"), filepath.Join(dir, panelSocketName)
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(socket); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(socket, []byte("another server's socket"), 0o600); err != nil {
		t.Fatal(err)
	}
	other := panelRecord{SchemaVersion: 1, PID: os.Getpid(), Port: 49152, StartedAt: time.Now().UTC().Truncate(time.Second), IdleSeconds: 1800, Instance: strings.Repeat("9", 32), Registry: registryDir, Version: "fixture"}
	if err := writePanelRecord(dir, other); err != nil {
		t.Fatal(err)
	}
	ticks <- time.Now()
	select {
	case code := <-done:
		if code != exitOK {
			t.Fatalf("exit code %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("still running after its lock file was replaced")
	}
	if b, err := os.ReadFile(socket); err != nil || string(b) != "another server's socket" {
		t.Fatalf("it removed a socket it did not create: %v %q", err, b)
	}
	if rec, ok := readPanelRecord(dir); !ok || rec != other {
		t.Fatalf("it removed a record it did not write: %+v %v", rec, ok)
	}
}
