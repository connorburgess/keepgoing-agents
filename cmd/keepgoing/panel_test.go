package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
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

// panelServeInProcess runs the background server's own command in this
// process against the fixture registry and returns its private directory,
// record and the fixture tree's root.
func panelServeInProcess(t *testing.T, idle time.Duration) (string, panelRecord, string) {
	t.Helper()
	root := t.TempDir()
	registryDir := dockScratch(t, root, dockNow)
	runsClock(t, dockNow)
	dir := filepath.Join(panelShortDir(t), "panel")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() {
		done <- panelServerCommand(env{ctx, io.Discard, io.Discard}, []string{"--panel-directory", dir, "--directory", registryDir, "--idle", idle.String(), "--offline"})
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

func panelOpenAddress(t *testing.T, dir, key string) string {
	t.Helper()
	reply, err := panelControl(context.Background(), dir, panelRequest{Op: "open", Key: key})
	if err != nil || !strings.HasPrefix(reply.Address, "http://127.0.0.1:") {
		t.Fatalf("open: %+v %v", reply, err)
	}
	return reply.Address
}

// panelSession redeems a fresh opening address and returns the page address
// and the session cookie.
func panelSession(t *testing.T, dir string, rec panelRecord) (string, *http.Cookie) {
	t.Helper()
	resp, _ := dockRequest(t, http.MethodGet, panelOpenAddress(t, dir, ""), nil)
	if resp.StatusCode != http.StatusSeeOther || len(resp.Cookies()) != 1 {
		t.Fatalf("opening: %d %v", resp.StatusCode, resp.Header)
	}
	return "http://127.0.0.1:" + strconv.Itoa(rec.Port) + resp.Header.Get("Location"), resp.Cookies()[0]
}

func TestPanelOpeningAddressWorksOnceAndBecomesASessionCookie(t *testing.T) {
	dir, rec, _ := panelServeInProcess(t, time.Hour)
	address := panelOpenAddress(t, dir, "l0123456789ab")

	// A HEAD request, as a link preview might send, does not spend it.
	if resp, _ := dockRequest(t, http.MethodHead, address, nil); resp.StatusCode != http.StatusMethodNotAllowed || len(resp.Cookies()) != 0 {
		t.Fatalf("HEAD: %d", resp.StatusCode)
	}
	resp, body := dockRequest(t, http.MethodGet, address, nil)
	if resp.StatusCode != http.StatusSeeOther || body != "" {
		t.Fatalf("first use: %d %q", resp.StatusCode, body)
	}
	location := resp.Header.Get("Location")
	if !strings.HasPrefix(location, "/s/") || !strings.HasSuffix(location, "/#/loop/l0123456789ab") {
		t.Fatalf("redirect to %q", location)
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
	// Neither the address nor the redirect carries the session.
	if strings.Contains(address, c.Value) || strings.Contains(location, c.Value) {
		t.Fatal("the session value appears in an address")
	}

	// A second use, and a code that never existed, get the plain page.
	for _, used := range []string{address, "http://127.0.0.1:" + strconv.Itoa(rec.Port) + "/open/" + strings.Repeat("0", 64), "http://127.0.0.1:" + strconv.Itoa(rec.Port) + "/open/"} {
		resp, body := dockRequest(t, http.MethodGet, used, nil)
		if resp.StatusCode != http.StatusGone || len(resp.Cookies()) != 0 || !strings.Contains(body, `Run <code class="command">keepgoing panel</code>`) || strings.Contains(body, "style=") || strings.Contains(body, "<script") {
			t.Fatalf("reused address: %d %q", resp.StatusCode, body)
		}
	}

	base := "http://127.0.0.1:" + strconv.Itoa(rec.Port)
	with := func(c *http.Cookie) func(*http.Request) {
		return func(r *http.Request) {
			if c != nil {
				r.AddCookie(c)
			}
		}
	}
	// The cookie reads the page, its assets and the data endpoint.
	for path, want := range map[string]string{"": "<!doctype html>", "dock.css": "", "dock.js": "", dockAPIPath: `"schemaVersion":1`} {
		resp, body := dockRequest(t, http.MethodGet, base+pagePath+path, with(c))
		if resp.StatusCode != http.StatusOK || !strings.Contains(strings.ToLower(body), strings.ToLower(want)) {
			t.Fatalf("%q with the session: %d", path, resp.StatusCode)
		}
	}
	// Without it, with another value, or under another path, nothing is read.
	other := *c
	other.Value = strings.Repeat("a", 64)
	for name, edit := range map[string]func(*http.Request){"no cookie": nil, "wrong cookie": with(&other)} {
		for _, path := range []string{"", dockAPIPath, "dock.js"} {
			resp, body := dockRequest(t, http.MethodGet, base+pagePath+path, edit)
			if resp.StatusCode != http.StatusForbidden || strings.Contains(body, "schemaVersion") || !strings.Contains(body, "keepgoing panel") {
				t.Fatalf("%s, %q: %d %q", name, path, resp.StatusCode, body)
			}
		}
	}
	for _, path := range []string{"/", "/s/", "/s/" + strings.Repeat("0", 32) + "/", "/" + c.Value + "/", "/api/v1/dock"} {
		resp, body := dockRequest(t, http.MethodGet, base+path, with(c))
		if resp.StatusCode == http.StatusOK || strings.Contains(body, "schemaVersion") {
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
	code := func(address string) string { return strings.TrimPrefix(address, "http://127.0.0.1:49152/open/") }
	first, err := s.mint("")
	if err != nil || len(code(first)) != 64 {
		t.Fatal(first, err)
	}
	now = now.Add(panelOpenTTL - time.Second)
	if _, ok := s.redeem(code(first)); !ok {
		t.Fatal("refused inside its 30 seconds")
	}
	if _, ok := s.redeem(code(first)); ok {
		t.Fatal("used twice")
	}
	second, _ := s.mint("")
	now = now.Add(panelOpenTTL)
	if _, ok := s.redeem(code(second)); ok {
		t.Fatal("accepted at its expiry")
	}
	// Expired codes do not accumulate, pending ones are capped, and a key
	// that is not a page key is refused.
	for i := 0; i < panelMaxOpenings; i++ {
		if _, err := s.mint(""); err != nil {
			t.Fatal(i, err)
		}
	}
	if _, err := s.mint(""); err == nil {
		t.Fatal("more pending opening addresses than the cap")
	}
	now = now.Add(panelOpenTTL)
	if _, err := s.mint(""); err != nil || len(s.openings) != 1 {
		t.Fatal(len(s.openings), err)
	}
	for _, bad := range []string{"../x", "l0123", "L0123456789AB", "l0123456789ab/../.."} {
		if _, err := s.mint(bad); err == nil {
			t.Fatalf("minted for key %q", bad)
		}
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
	session := func(r *http.Request) { r.AddCookie(c) }
	refused := map[string]func(*http.Request){
		"foreign host":       func(r *http.Request) { session(r); r.Host = "evil.example.com" },
		"rebound host":       func(r *http.Request) { session(r); r.Host = "evil.example.com:" + strconv.Itoa(rec.Port) },
		"foreign origin":     func(r *http.Request) { session(r); r.Header.Set("Origin", "https://evil.example.com") },
		"null origin":        func(r *http.Request) { session(r); r.Header.Set("Origin", "null") },
		"cross-site fetch":   func(r *http.Request) { session(r); r.Header.Set("Sec-Fetch-Site", "cross-site") },
		"same-site fetch":    func(r *http.Request) { session(r); r.Header.Set("Sec-Fetch-Site", "same-site") },
		"another port's tab": func(r *http.Request) { session(r); r.Header.Set("Origin", "http://127.0.0.1:1") },
	}
	fresh := panelOpenAddress(t, dir, "")
	for name, edit := range refused {
		for _, url := range []string{page, page + dockAPIPath, base + "/healthz", fresh} {
			resp, body := dockRequest(t, http.MethodGet, url, edit)
			if resp.StatusCode != http.StatusForbidden || len(resp.Cookies()) != 0 || strings.Contains(body, "instance") || strings.Contains(body, "schemaVersion") {
				t.Fatalf("%s, %s: %d %q", name, url, resp.StatusCode, body)
			}
			headers(resp, name)
		}
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions} {
		for _, url := range []string{page, page + dockAPIPath, base + "/healthz", fresh} {
			if resp, _ := dockRequest(t, method, url, session); resp.StatusCode != http.StatusForbidden {
				t.Fatalf("%s %s: %d", method, url, resp.StatusCode)
			}
		}
	}
	// None of that spent the opening address.
	if resp, _ := dockRequest(t, http.MethodGet, fresh, nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("the opening address was spent by a refused request: %d", resp.StatusCode)
	}
	// Health says nothing but the instance ID.
	resp, body := dockRequest(t, http.MethodGet, base+"/healthz", nil)
	headers(resp, "health")
	var reply map[string]any
	if resp.StatusCode != http.StatusOK || json.Unmarshal([]byte(body), &reply) != nil || len(reply) != 1 || reply["instance"] != rec.Instance {
		t.Fatalf("health: %d %q", resp.StatusCode, body)
	}
	// The stylesheet is the one thing served without a session; it is the
	// page's own and passes the same guard.
	css, cssBody := dockRequest(t, http.MethodGet, base+"/panel.css", nil)
	sheet, _ := dockUI.ReadFile("dockui/dock.css")
	if css.StatusCode != http.StatusOK || cssBody != string(sheet) || !strings.HasPrefix(css.Header.Get("Content-Type"), "text/css") {
		t.Fatalf("stylesheet: %d", css.StatusCode)
	}
	headers(css, "stylesheet")
	if resp, _ := dockRequest(t, http.MethodGet, base+"/panel.css", refused["foreign host"]); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("stylesheet for a foreign host: %d", resp.StatusCode)
	}
	for _, url := range []string{base + "/open/" + strings.Repeat("0", 64), page, base + "/nothing"} {
		resp, _ := dockRequest(t, http.MethodGet, url, nil)
		headers(resp, url)
	}
}

func TestPanelRecordHoldsNoSecretAndIsPrivate(t *testing.T) {
	dir, rec, _ := panelServeInProcess(t, time.Hour)
	page, c := panelSession(t, dir, rec)
	pending := panelOpenAddress(t, dir, "")
	segment := strings.Split(strings.TrimPrefix(page, "http://"), "/")[2]
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
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
	if info, err := os.Stat(dir); err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o700) {
		t.Fatalf("panel directory: %v %v", info.Mode(), err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, panelRecordName))
	var fields map[string]any
	if json.Unmarshal(b, &fields) != nil || len(fields) != 8 {
		t.Fatalf("record fields: %s", b)
	}
	// A record that is unreadable, too open or names nothing running is not
	// trusted.
	if _, ok := panelLive(context.Background(), t.TempDir()); ok {
		t.Fatal("an empty directory has a live server")
	}
	stale := rec
	stale.Instance = strings.Repeat("0", 32)
	if panelAnswers(context.Background(), stale) {
		t.Fatal("another instance ID was accepted for this port")
	}
}

func TestPanelDirectoryRefusesALongPathAndACheckout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("path limits differ")
	}
	long := filepath.Join(panelShortDir(t), strings.Repeat("a", 120))
	if _, err := panelDirectory(long); err == nil || !strings.Contains(err.Error(), "too long for a local socket") {
		t.Fatalf("long path: %v", err)
	}
	var out, errs bytes.Buffer
	if code := panelCommand(env{context.Background(), &out, &errs}, []string{"status", "--panel-directory", long}); code != exitUsage || !strings.Contains(errs.String(), "too long for a local socket") {
		t.Fatalf("status with a long path: %d %s", code, errs.String())
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

var (
	panelBinaryOnce sync.Once
	panelBinaryPath string
	panelBinaryErr  error
)

// panelBinary builds the command once for the tests that need real processes.
func panelBinary(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("detached process integration runs on macOS and Linux")
	}
	panelBinaryOnce.Do(func() {
		dir, err := os.MkdirTemp("", "keepgoing-panel-bin")
		if err != nil {
			panelBinaryErr = err
			return
		}
		panelBinaryPath = filepath.Join(dir, "keepgoing")
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if out, err := exec.CommandContext(ctx, "go", "build", "-o", panelBinaryPath, ".").CombinedOutput(); err != nil {
			panelBinaryErr = fmt.Errorf("build: %v %s", err, out)
		}
	})
	if panelBinaryErr != nil {
		t.Fatal(panelBinaryErr)
	}
	return panelBinaryPath
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
}

func newPanelProcessWorld(t *testing.T) *panelProcessWorld {
	t.Helper()
	w := &panelProcessWorld{t: t, bin: panelBinary(t), root: t.TempDir()}
	w.registry = dockScratch(t, w.root, time.Now().UTC())
	short := panelShortDir(t)
	w.dir = filepath.Join(short, "panel")
	w.opened = filepath.Join(short, "opened")
	launcher := filepath.Join(short, "launcher")
	// One short line per call, so concurrent calls cannot interleave.
	script := "#!/bin/sh\nprintf '%s %s\\n' \"$#\" \"$*\" >> \"" + w.opened + "\"\n"
	if err := os.WriteFile(launcher, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(panelOpenerEnv, launcher)
	t.Cleanup(func() { w.run("panel", "stop", "--panel-directory", w.dir) })
	return w
}

func (w *panelProcessWorld) run(args ...string) (int, string, string) {
	w.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, w.bin, args...)
	var out, errs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errs
	err := cmd.Run()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		w.t.Fatalf("%v: %v", args, err)
	}
	return code, out.String(), errs.String()
}

func (w *panelProcessWorld) open(extra ...string) (int, string, string) {
	return w.run(append([]string{"panel", "--panel-directory", w.dir, "--directory", w.registry, "--offline"}, extra...)...)
}

// launched waits for the stand-in launcher to have been called want times and
// returns the addresses it was given, requiring exactly one argument per call.
func (w *panelProcessWorld) launched(want int) []string {
	w.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		b, _ := os.ReadFile(w.opened)
		if strings.Count(string(b), "\n") >= want || time.Now().After(deadline) {
			var out []string
			for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
				if line == "" {
					continue
				}
				count, address, _ := strings.Cut(line, " ")
				if count != "1" || strings.Contains(address, " ") {
					w.t.Fatalf("the launcher was given %q", line)
				}
				out = append(out, address)
			}
			if len(out) != want {
				w.t.Fatalf("the launcher was called %d times, want %d", len(out), want)
			}
			return out
		}
		time.Sleep(20 * time.Millisecond)
	}
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
	addresses := w.launched(2)
	if addresses[0] == addresses[1] {
		t.Fatalf("launcher calls: %q", addresses)
	}
	prefix := fmt.Sprintf("http://127.0.0.1:%d/open/", first.Port)
	for _, a := range addresses {
		if !strings.HasPrefix(a, prefix) || len(strings.TrimPrefix(a, prefix)) != 64 {
			t.Fatalf("the launcher was given %q", a)
		}
	}

	// Use one address as a browser would, then look for the session anywhere
	// another program could see it.
	resp, _ := dockRequest(t, http.MethodGet, addresses[0], nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("opening: %d", resp.StatusCode)
	}
	c := resp.Cookies()[0]
	segment := strings.Split(resp.Header.Get("Location"), "/")[2]
	page := fmt.Sprintf("http://127.0.0.1:%d/s/%s/", first.Port, segment)
	if resp, body := dockRequest(t, http.MethodGet, page+dockAPIPath, func(r *http.Request) { r.AddCookie(c) }); resp.StatusCode != http.StatusOK || !strings.Contains(body, `"readOnly":true`) {
		t.Fatalf("data with the session: %d", resp.StatusCode)
	}
	ps := processCommandLines(t)
	if !strings.Contains(ps, "__panel-server") {
		t.Fatalf("the server is not in the process list")
	}
	launcherLog, _ := os.ReadFile(w.opened)
	for name, text := range map[string]string{"process list": ps, "launcher arguments": string(launcherLog), "command output": out} {
		for what, secret := range map[string]string{"session cookie": c.Value, "page path segment": segment} {
			if strings.Contains(text, secret) {
				t.Fatalf("the %s appears in the %s", what, name)
			}
		}
	}
	// The server writes no log: its private directory holds the record, the
	// socket and the two lock files, and nothing else.
	names := []string{}
	entries, _ := os.ReadDir(w.dir)
	for _, e := range entries {
		names = append(names, e.Name())
		if e.Type().IsRegular() {
			b, _ := os.ReadFile(filepath.Join(w.dir, e.Name()))
			if bytes.Contains(b, []byte(c.Value)) || bytes.Contains(b, []byte(segment)) {
				t.Fatalf("%s holds a secret", e.Name())
			}
		}
	}
	if got := strings.Join(names, " "); got != "panel-open.lock panel-server.lock panel.json panel.sock" {
		t.Fatalf("private directory holds: %s", got)
	}

	// --no-open prints an address and starts no browser; --loop opens that
	// loop's page.
	list := buildPanelList((&dockReader{directory: w.registry, refresh: dockDefaultRefresh, staleAfter: defaultStaleAfter, query: inspectDaemon}).read(context.Background(), false))
	code, out, errs = w.open("--no-open", "--loop", list.Loops[0].Name)
	if code != exitOK || !strings.Contains(out, prefix) || !strings.Contains(out, "within 30 seconds") || len(w.launched(2)) != 2 {
		t.Fatalf("--no-open: %d %s %s", code, out, errs)
	}
	printed := out[strings.Index(out, prefix):]
	printed = printed[:strings.IndexAny(printed, " \n")]
	if resp, _ := dockRequest(t, http.MethodGet, printed, nil); resp.StatusCode != http.StatusSeeOther || !strings.HasSuffix(resp.Header.Get("Location"), "#/loop/"+list.Loops[0].Key) {
		t.Fatalf("--loop redirect: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if code, _, errs := w.open("--loop", "no-such-loop"); code != exitFailed || !strings.Contains(errs, "no registered loop has that name") {
		t.Fatalf("unknown loop: %d %s", code, errs)
	}

	// Status names the port and the idle limit, never a secret.
	code, out, _ = w.run("panel", "status", "--panel-directory", w.dir)
	if code != exitOK || !strings.Contains(out, fmt.Sprintf("port %d", first.Port)) || !strings.Contains(out, "after 30m0s without a request") || strings.Contains(out, c.Value) || strings.Contains(out, segment) || strings.Contains(out, "/open/") {
		t.Fatalf("status: %d %s", code, out)
	}
	_, out, _ = w.run("panel", "status", "--json", "--panel-directory", w.dir)
	var st panelStatus
	if json.Unmarshal([]byte(out), &st) != nil || !st.Running || st.Port != first.Port || st.IdleSeconds != 1800 || st.IdleRemainingSeconds == nil || *st.IdleRemainingSeconds > 1800 || *st.IdleRemainingSeconds < 1700 {
		t.Fatalf("status json: %s", out)
	}

	// Stop ends the server, removes its record and socket, and the session
	// is gone. Nothing in any loop's files changed at any point.
	if code, out, errs := w.run("panel", "stop", "--panel-directory", w.dir); code != exitOK || !strings.Contains(out, "Stopped the keepgoing panel") {
		t.Fatalf("stop: %d %s %s", code, out, errs)
	}
	if _, ok := panelLive(context.Background(), w.dir); ok {
		t.Fatal("still answering after stop")
	}
	for _, name := range []string{panelRecordName, panelSocketName} {
		if _, err := os.Lstat(filepath.Join(w.dir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s was left behind: %v", name, err)
		}
	}
	if strings.Contains(processCommandLines(t), "--panel-directory "+w.dir+" ") {
		t.Fatal("the server process is still running")
	}
	if code, out, _ := w.run("panel", "stop", "--panel-directory", w.dir); code != exitOK || !strings.Contains(out, "not running") {
		t.Fatalf("second stop: %d %s", code, out)
	}
	if treeDigest(t, w.root) != before {
		t.Fatal("a file in the registry or a loop's directory changed across start, open, status and stop")
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
	servers := 0
	for _, line := range strings.Split(processCommandLines(t), "\n") {
		if strings.Contains(line, "__panel-server") && strings.Contains(line, "--panel-directory "+w.dir+" ") {
			servers++
		}
	}
	if servers != 1 {
		t.Fatalf("%d server processes", servers)
	}
	addresses := w.launched(callers)
	for _, a := range addresses {
		if !strings.HasPrefix(a, fmt.Sprintf("http://127.0.0.1:%d/open/", rec.Port)) {
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
}

func TestPanelStaleRecordIsReplaced(t *testing.T) {
	w := newPanelProcessWorld(t)
	if err := os.MkdirAll(w.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// A listener that is not a panel server holds the recorded port, and the
	// recorded process is this test: alive, and not a panel server.
	other, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	go http.Serve(other, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"instance":"`+strings.Repeat("1", 32)+`"}`)
	}))
	stale := panelRecord{SchemaVersion: 1, PID: os.Getpid(), Port: other.Addr().(*net.TCPAddr).Port, StartedAt: time.Now().Add(-time.Hour).UTC(), IdleSeconds: 1800, Instance: strings.Repeat("0", 32), Registry: w.registry, Version: "fixture"}
	if err := writePanelRecord(w.dir, stale); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.dir, panelSocketName), []byte("left behind"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := w.run("panel", "status", "--panel-directory", w.dir); code != exitOK || !strings.Contains(out, "not running") {
		t.Fatalf("status with a stale record: %d %s", code, out)
	}
	if code, out, _ := w.run("panel", "stop", "--panel-directory", w.dir); code != exitOK || !strings.Contains(out, "not running") {
		t.Fatalf("stop with a stale record: %d %s", code, out)
	}
	if code, out, errs := w.open(); code != exitOK || !strings.HasPrefix(out, "Started") {
		t.Fatalf("open over a stale record: %d %s %s", code, out, errs)
	}
	rec := w.record()
	if rec.Instance == stale.Instance || rec.Port == stale.Port || rec.PID == os.Getpid() {
		t.Fatalf("the stale record was kept: %+v", rec)
	}
	// A record nobody can parse is replaced too.
	w.run("panel", "stop", "--panel-directory", w.dir)
	if err := os.WriteFile(filepath.Join(w.dir, panelRecordName), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out, errs := w.open(); code != exitOK || !strings.HasPrefix(out, "Started") {
		t.Fatalf("open over an unreadable record: %d %s %s", code, out, errs)
	}
}

func TestPanelServerExitsWhenIdleAndLeavesLoopsAlone(t *testing.T) {
	w := newPanelProcessWorld(t)
	before := treeDigest(t, w.root)
	cmd := exec.Command(w.bin, "__panel-server", "--panel-directory", w.dir, "--directory", w.registry, "--idle", "1500ms", "--offline")
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
	// Page requests keep it alive past its idle limit; health requests and
	// refused requests do not.
	page, c := panelSession(t, w.dir, rec)
	for end := time.Now().Add(2500 * time.Millisecond); time.Now().Before(end); time.Sleep(200 * time.Millisecond) {
		if resp, _ := dockRequest(t, http.MethodGet, page+dockAPIPath, func(r *http.Request) { r.AddCookie(c) }); resp.StatusCode != http.StatusOK {
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
			// Asking for health or status, and being refused, is not use.
			panelAnswers(context.Background(), rec)
			_, _ = panelControl(context.Background(), w.dir, panelRequest{Op: "status"})
			dockRequest(t, http.MethodGet, page, nil)
			if time.Since(idleFrom) > 8*time.Second {
				t.Fatal("still running 8 seconds after its last request")
			}
		}
	}
}
