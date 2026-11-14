package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/lock"
)

// The background panel server. It serves the same page and data endpoint as
// keepgoing dock, behind the same network guard, with a different answer to
// "who may read": a session cookie that only an opening address can create,
// and an opening address that only the private socket can create.

const (
	panelCookieName  = "keepgoing_panel"
	panelMaxSessions = 16
	panelMaxOpenings = 16
)

// panelExpiredPage is shown for an opening address that was used or has
// expired, and for a page address whose session is gone. It has no script,
// and its only stylesheet is the panel's own, served without a session at
// /panel.css, so it looks like the page it stands in for. Its one script,
// /expired.js, shows a button that copies the command; the page reads the
// same without it. Both are files of this server, as the content policy
// requires.
const panelExpiredPage = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light dark">
<meta name="referrer" content="no-referrer">
<meta name="robots" content="noindex">
<title>keepgoing: this link has expired</title>
<link rel="icon" href="data:,">
<link rel="stylesheet" href="/panel.css">
<script src="/expired.js" defer></script>
</head>
<body>
<header class="bar">
  <div class="bar-in">
    <p class="brand"><svg viewBox="0 0 20 20" aria-hidden="true" class="mark"><path d="M15.5 6.2A6.5 6.5 0 1 0 16.5 10"/><path d="M16.8 3.4v3.4h-3.4"/></svg><span>keepgoing</span></p>
  </div>
</header>
<main class="notice">
  <h1>This link to the keepgoing panel has expired</h1>
  <p class="lede">Run <code class="command" id="command">keepgoing panel</code> in a terminal to open the panel again.</p>
  <p class="copy" id="copy-row" hidden><button type="button" class="btn" id="copy">Copy the command</button> <span id="copied" role="status" aria-live="polite"></span></p>
  <p>Each link opens the panel once, within 30 seconds. An open panel stops working when the browser session ends or the panel server stops.</p>
  <p>Your loops are not affected. The panel only shows them.</p>
</main>
</body>
</html>
`

type panelOpening struct {
	expires time.Time
	key     string // the loop to show, or "" for the overview
	file    string // the launch page that goes to this opening, if one was written
}

// panelLaunchPage is the private file handed to the browser launcher. Its
// only job is to take the browser to the opening address, so that address is
// never in a program's arguments. It is not served by the panel.
const panelLaunchPage = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="color-scheme" content="light dark">
<meta name="referrer" content="no-referrer">
<meta http-equiv="refresh" content="0; url=%[1]s">
<title>Opening the keepgoing panel</title>
</head>
<body>
<p>Opening the keepgoing panel. <a href="%[1]s">Continue</a> if nothing happens.</p>
</body>
</html>
`

// panelOpenedPage is the answer to a good opening address. It sets the
// session cookie and sends the browser on to the page. It is a page, not a
// redirect, on purpose: the opening is reached from a local file, which the
// browser treats as another site, and a strict cookie is not sent along a
// redirect that began on another site. A navigation that this page starts is
// same-site, so the cookie goes with it.
const panelOpenedPage = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light dark">
<meta name="referrer" content="no-referrer">
<meta name="robots" content="noindex">
<meta http-equiv="refresh" content="0; url=%[1]s">
<title>keepgoing</title>
<link rel="icon" href="data:,">
<link rel="stylesheet" href="/panel.css">
</head>
<body>
<main class="notice">
  <p>Opening the keepgoing panel. <a href="%[1]s">Continue</a> if nothing happens.</p>
</main>
</body>
</html>
`

// panelState is the server's secrets and clock. None of it is written to
// disk, logged, or passed to another process.
type panelState struct {
	instance string
	segment  string // the random path segment the page is served under
	dir      string // the private directory, for launch pages
	port     int
	idle     time.Duration
	now      func() time.Time

	mu       sync.Mutex
	openings map[string]panelOpening
	sessions []string // newest last
	last     atomic.Int64
}

func panelRandom(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func newPanelState(port int, idle time.Duration, now func() time.Time) (*panelState, error) {
	instance, err := panelRandom(16)
	if err != nil {
		return nil, err
	}
	segment, err := panelRandom(16)
	if err != nil {
		return nil, err
	}
	s := &panelState{instance: instance, segment: segment, port: port, idle: idle, now: now, openings: map[string]panelOpening{}}
	s.touch()
	return s, nil
}

func (s *panelState) touch() { s.last.Store(s.now().UnixNano()) }

func (s *panelState) idleRemaining() time.Duration {
	return s.idle - s.now().Sub(time.Unix(0, s.last.Load()))
}

func (s *panelState) base() string { return fmt.Sprintf("http://127.0.0.1:%d", s.port) }

func (s *panelState) pagePath() string { return "/s/" + s.segment + "/" }

// mint creates an opening. Only the private socket calls it. With asFile it
// also writes the launch page and returns that file's name; otherwise it
// returns the opening address.
func (s *panelState) mint(key string, asFile bool) (address, file string, err error) {
	if key != "" && !panelKeyPattern.MatchString(key) {
		return "", "", errors.New("not a loop key")
	}
	code, err := panelRandom(32)
	if err != nil {
		return "", "", errors.New("no random source")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeLocked()
	if len(s.openings) >= panelMaxOpenings {
		return "", "", errors.New("too many opening addresses are waiting; try again in 30 seconds")
	}
	address = s.base() + "/open/" + code
	opening := panelOpening{expires: s.now().Add(panelOpenTTL), key: key}
	if asFile {
		name, err := panelRandom(16)
		if err != nil || s.dir == "" {
			return "", "", errors.New("no random source")
		}
		opening.file = "open-" + name + ".html"
		// Created private and never replaced: O_EXCL refuses a name that
		// already exists, a link included.
		f, err := os.OpenFile(filepath.Join(s.dir, opening.file), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return "", "", errors.New("the launch page could not be written")
		}
		_, werr := fmt.Fprintf(f, panelLaunchPage, address)
		if cerr := f.Close(); werr != nil || cerr != nil {
			_ = os.Remove(filepath.Join(s.dir, opening.file))
			return "", "", errors.New("the launch page could not be written")
		}
		// Gone after 30 seconds whether or not it was used.
		file := opening.file
		time.AfterFunc(panelOpenTTL, func() { _ = os.Remove(filepath.Join(s.dir, file)) })
		s.openings[code] = opening
		return "", opening.file, nil
	}
	s.openings[code] = opening
	return address, "", nil
}

// purgeLocked forgets expired openings and deletes their launch pages.
func (s *panelState) purgeLocked() {
	now := s.now()
	for c, o := range s.openings {
		if !now.Before(o.expires) {
			s.dropLocked(c)
		}
	}
}

func (s *panelState) dropLocked(code string) {
	if o, ok := s.openings[code]; ok && o.file != "" && s.dir != "" {
		_ = os.Remove(filepath.Join(s.dir, o.file))
	}
	delete(s.openings, code)
}

// removeLaunchPages deletes every launch page still on disk, at exit.
func (s *panelState) removeLaunchPages() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.openings {
		s.dropLocked(c)
	}
}

func (s *panelState) sinceUse() time.Duration { return s.now().Sub(time.Unix(0, s.last.Load())) }

// redeem uses an opening code once. Every stored code is compared in constant
// time, and a code is deleted whether or not it had expired.
func (s *panelState) redeem(code string) (panelOpening, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var found string
	for c := range s.openings {
		if len(c) == len(code) && subtle.ConstantTimeCompare([]byte(c), []byte(code)) == 1 {
			found = c
		}
	}
	if found == "" {
		return panelOpening{}, false
	}
	o := s.openings[found]
	s.dropLocked(found) // the launch page goes with the code
	return o, s.now().Before(o.expires)
}

func (s *panelState) newSession() (string, error) {
	value, err := panelRandom(32)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions = append(s.sessions, value)
	if len(s.sessions) > panelMaxSessions {
		s.sessions = s.sessions[len(s.sessions)-panelMaxSessions:]
	}
	return value, nil
}

func (s *panelState) validSession(value string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	ok := false
	for _, v := range s.sessions {
		if len(v) == len(value) && subtle.ConstantTimeCompare([]byte(v), []byte(value)) == 1 {
			ok = true
		}
	}
	return ok
}

// authorize implements dockAuth: the page lives under /s/<segment>/ and is
// readable only with a session cookie. The segment keeps the browser from
// sending the cookie to another server on 127.0.0.1, because cookies are not
// scoped by port; it is not an access check by itself.
func (s *panelState) authorize(r *http.Request) (string, bool) {
	rest, ok := strings.CutPrefix(r.URL.Path, s.pagePath())
	if !ok {
		return "", false
	}
	// Exactly one session cookie: a request carrying two is refused, so a
	// second cookie of the same name cannot stand in front of the real one.
	var sessions []string
	for _, c := range r.Cookies() {
		if c.Name == panelCookieName {
			sessions = append(sessions, c.Value)
		}
	}
	if len(sessions) != 1 || !s.validSession(sessions[0]) {
		return "", false
	}
	return rest, true
}

// sessionOf is the request's one session cookie; call it after authorize.
func (s *panelState) sessionOf(r *http.Request) string {
	for _, c := range r.Cookies() {
		if c.Name == panelCookieName {
			return c.Value
		}
	}
	return ""
}

// panelHandler puts the opening endpoint in front of the page.
// Every request passes the same network guard first.
type panelHandler struct {
	state *panelState
	guard dockGuard
	page  *dockServer
	// changes is set only on a server started with --allow-changes. Without
	// it no write request is routed at all.
	changes *panelChanges
}

// allowOpening is the guard for an opening address. It is the page guard
// with one more accepted case: a top-level navigation that began on another
// site, which is what a browser sends when the private launch page takes it
// to the address. Such a request is a document navigation, a GET, and has no
// Origin; anything else that began on another site is refused as before.
func (h *panelHandler) allowOpening(r *http.Request) bool {
	if h.guard.allow(r) {
		return true
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	if !h.guard.hosts[strings.ToLower(r.Host)] || r.Header.Get("Origin") != "" {
		return false
	}
	return r.Header.Get("Sec-Fetch-Site") == "cross-site" && r.Header.Get("Sec-Fetch-Mode") == "navigate" && r.Header.Get("Sec-Fetch-Dest") == "document"
}

func (h *panelHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	dockSecurityHeaders(w.Header())
	if r.Method == http.MethodPost {
		// A change: its own stricter guard, then the session, then the
		// anti-forgery value the change handler checks.
		if h.changes == nil || !panelAllowPost(r, h.state.port) {
			http.Error(w, "refused", http.StatusForbidden)
			return
		}
		rest, ok := h.state.authorize(r)
		if !ok {
			http.Error(w, "refused", http.StatusForbidden)
			return
		}
		h.state.touch()
		h.changes.serve(w, r, rest, h.state.sessionOf(r))
		return
	}
	opening := strings.HasPrefix(r.URL.Path, "/open/")
	if (opening && !h.allowOpening(r)) || (!opening && !h.guard.allow(r)) {
		http.Error(w, "refused", http.StatusForbidden)
		return
	}
	// Only a page request with a session or a good opening address counts
	// as use. The two static files and refused requests do not keep the
	// server alive.
	switch {
	case r.URL.Path == "/panel.css":
		// The stylesheet holds nothing private; the expired page needs it
		// without a session.
		h.page.asset(w, "dockui/dock.css", "text/css; charset=utf-8")
	case r.URL.Path == "/expired.js":
		h.page.asset(w, "dockui/expired.js", "text/javascript; charset=utf-8")
	case opening:
		h.open(w, r)
	case strings.HasPrefix(r.URL.Path, "/s/"):
		rest, ok := h.state.authorize(r)
		if !ok {
			h.expired(w, http.StatusForbidden)
			return
		}
		h.state.touch()
		if rest == dockServerPath {
			h.served(w)(h.state.sessionOf(r))
			return
		}
		h.page.ServeHTTP(w, r)
	default:
		http.NotFound(w, r)
	}
}

// served answers api/v1/server for a session: which command serves the page,
// whether it allows changes and since when, and, when it does, the session's
// anti-forgery value for preparing one.
func (h *panelHandler) served(w http.ResponseWriter) func(session string) {
	return func(session string) {
		out := h.page.served
		now := h.state.now().UTC()
		if h.changes == nil {
			out.Changes = "off"
			out.ChangesText = txt("Read-only: started without --allow-changes. To pause, stop or resume from here, run keepgoing panel stop, then keepgoing panel --allow-changes.")
		} else {
			since := h.changes.since
			out.Changes, out.ChangesSince, out.Form = "on", &since, h.changes.prepareValue(session)
			out.ChangesText = txt("Changes allowed for "+dockSpan(now.Sub(since))+": started with keepgoing panel --allow-changes at ", tm(since, "clock"), ". "+panelExposure+" keepgoing panel stop turns them off.")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}
}

func (h *panelHandler) expired(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, panelExpiredPage)
}

func (h *panelHandler) open(w http.ResponseWriter, r *http.Request) {
	// HEAD must not spend a code: only a real page load does.
	if r.Method != http.MethodGet {
		http.Error(w, "refused", http.StatusMethodNotAllowed)
		return
	}
	opening, ok := h.state.redeem(strings.TrimPrefix(r.URL.Path, "/open/"))
	if !ok {
		h.expired(w, http.StatusGone)
		return
	}
	h.state.touch()
	session, err := h.state.newSession()
	if err != nil {
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	// A session cookie: no Expires or Max-Age, so it ends with the browser
	// session, and with this server whichever comes first.
	http.SetCookie(w, &http.Cookie{Name: panelCookieName, Value: session, Path: h.state.pagePath(), HttpOnly: true, SameSite: http.SameSiteStrictMode})
	target := h.state.pagePath()
	if opening.key != "" {
		target += "#/loop/" + opening.key
	}
	// The target is this server's own path and a validated key.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, panelOpenedPage, target)
}

// panelControlServe answers the private socket: one request per connection,
// bounded in size and time, served one at a time.
func panelControlServe(l net.Listener, state *panelState, record func() panelRecord, stop func()) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		_ = c.SetDeadline(time.Now().Add(2 * time.Second))
		var req panelRequest
		reply := panelReply{}
		if json.NewDecoder(io.LimitReader(c, 1024)).Decode(&req) != nil {
			reply.Error = "the request could not be read"
		} else if age := time.Since(req.At); req.At.IsZero() || age > panelRequestMaxAge || age < -panelRequestMaxAge {
			// It waited too long to be what its sender still wants.
			reply.Error = "the request reached the panel too late"
			req.Op = ""
		} else {
			switch req.Op {
			case "open":
				state.touch()
				if address, file, err := state.mint(req.Key, req.File); err != nil {
					reply.Error = err.Error()
				} else {
					reply.OK, reply.Address, reply.File = true, address, file
				}
			case "status":
				// Asking does not keep the server alive. The server gives its
				// own account of itself and puts its record back if the file
				// is missing or is not its own.
				rec := record()
				reply.OK, reply.Record = true, &rec
				reply.IdleRemainingSeconds = max(0, int(state.idleRemaining()/time.Second))
				reply.SinceUseSeconds = max(0, int(state.sinceUse()/time.Second))
			case "stop":
				reply.OK = true
			default:
				reply.Error = "unknown request"
			}
		}
		_ = json.NewEncoder(c).Encode(reply)
		_ = c.Close()
		if reply.OK && req.Op == "stop" {
			stop()
			return
		}
	}
}

// panelListenPrivate binds the control socket so that it is private from the
// instant it exists, not made private afterwards, and reports the file it
// created. It refuses a socket that came out reachable by anyone else.
func panelListenPrivate(path string) (net.Listener, os.FileInfo, error) {
	restore := panelRestrictUmask()
	l, err := net.Listen("unix", path)
	restore()
	if err != nil {
		return nil, nil, err
	}
	// Closing must not remove the file by name: by then it may be another
	// server's. The caller removes it only if it is still this one.
	if u, ok := l.(*net.UnixListener); ok {
		u.SetUnlinkOnClose(false)
	}
	info, err := os.Lstat(path)
	if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0) {
		_ = l.Close()
		_ = os.Remove(path)
		return nil, nil, errors.New("the control socket was not created private")
	}
	return l, info, nil
}

// panelServerConfig is what one run of the background server needs. now and
// ticks exist so a test can run the idle rule on its own clock: ticks, when
// set, replaces the timer that asks whether the server has been idle too long.
type panelServerConfig struct {
	dir        string // the panel's private directory, already resolved
	registry   string // the --directory value, as given
	idle       time.Duration
	refresh    time.Duration
	staleAfter time.Duration
	offline    bool
	// changes: started with --allow-changes, so the page may pause, stop and
	// resume loops. It never changes while the server runs.
	changes bool
	now     func() time.Time
	ticks   <-chan time.Time
}

// panelServerCommand is the background server, run by keepgoing panel as
// "__panel-server". It is not started at login or by any other command.
func panelServerCommand(e env, args []string) int {
	fs := flag.NewFlagSet("__panel-server", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	panelDir := fs.String("panel-directory", "", "")
	directory := fs.String("directory", "", "")
	idle := fs.Duration("idle", panelDefaultIdle, "")
	refresh := fs.Duration("refresh", dockDefaultRefresh, "")
	staleAfter := fs.Duration("stale-after", defaultStaleAfter, "")
	offline := fs.Bool("offline", false, "")
	changes := fs.Bool("allow-changes", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *panelDir == "" || *idle <= 0 || *idle > panelMaxIdle || *refresh < dockMinRefresh || *refresh > dockMaxRefresh || *staleAfter <= 0 || !panelSupported() {
		return exitUsage
	}
	dir, err := panelDirectory(*panelDir)
	if err != nil {
		return exitUsage
	}
	return runPanelServer(e.ctx, panelServerConfig{dir: dir, registry: *directory, idle: *idle, refresh: *refresh, staleAfter: *staleAfter, offline: *offline, changes: *changes, now: time.Now})
}

func runPanelServer(parent context.Context, c panelServerConfig) int {
	dir := c.dir
	registryDir, err := registryDirectory(c.registry)
	if err != nil {
		return exitUsage
	}
	// Held for the server's whole life: a second server cannot run beside it,
	// and a crash releases it. Commands probe this lock by taking it for an
	// instant, so a first refusal may be a probe and is tried again briefly.
	var lk *lock.Lock
	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(40 * time.Millisecond) {
		var err error
		if lk, err = lock.AcquireNamed(dir, panelServerLock); err == nil {
			break
		}
		if !errors.Is(err, lock.ErrHeld) || time.Now().After(deadline) {
			return exitFailed
		}
	}
	defer lk.Release()
	// The lock is only worth something while it is the file at this path. If
	// that file is deleted or replaced, another server can take a new one, so
	// this server watches for it and exits.
	lockPath := filepath.Join(dir, panelServerLock+".lock")
	lockFile, err := os.Stat(lockPath)
	if err != nil {
		return exitFailed
	}
	lockIsOurs := func() bool {
		now, err := os.Stat(lockPath)
		return err == nil && os.SameFile(lockFile, now)
	}
	ln, err := dockListen(0)
	if err != nil {
		return exitFailed
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	state, err := newPanelState(port, c.idle, c.now)
	if err != nil {
		return exitFailed
	}
	state.dir = dir
	defer state.removeLaunchPages()
	// The lock proves no server owns a socket file left here.
	socket := filepath.Join(dir, panelSocketName)
	_ = os.Remove(socket)
	control, socketFile, err := panelListenPrivate(socket)
	if err != nil {
		return exitFailed
	}
	// Only the socket this server created is ever removed: if another server
	// has since put its own at this path, it is left alone.
	defer func() {
		_ = control.Close()
		if now, err := os.Lstat(socket); err == nil && os.SameFile(socketFile, now) {
			_ = os.Remove(socket)
		}
	}()
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	reader := &dockReader{ctx: ctx, directory: c.registry, refresh: c.refresh, staleAfter: c.staleAfter, query: inspectDaemon}
	if c.offline {
		reader.query = func(ctx context.Context, dir, current string, _ bool) daemonStatus {
			return inspectDaemon(ctx, dir, current, true)
		}
	}
	page := newDockServer(port, state, reader)
	page.served = dockServed{SchemaVersion: 1, ServedBy: "panel", IdleSeconds: max(1, int(c.idle/time.Second))}
	handler := &panelHandler{state: state, guard: newDockGuard(port), page: page}
	rec := panelRecord{SchemaVersion: 1, PID: os.Getpid(), Port: port, StartedAt: c.now().UTC().Truncate(time.Second), IdleSeconds: int(c.idle / time.Second),
		Instance: state.instance, Registry: registryDir, Version: version, Changes: c.changes}
	if c.changes {
		if handler.changes, err = newPanelChanges(ctx, c.registry, reader, rec.StartedAt, c.now); err != nil {
			return exitFailed
		}
	}
	if rec.IdleSeconds < 1 {
		rec.IdleSeconds = 1
	}
	if err := writePanelRecord(dir, rec); err != nil {
		return exitFailed
	}
	// The record is for people; the server keeps it true. Asked for its
	// status, it writes the file again if it is missing or names another.
	go panelControlServe(control, state, func() panelRecord {
		if cur, ok := readPanelRecord(dir); !ok || cur != rec {
			_ = writePanelRecord(dir, rec)
		}
		return rec
	}, cancel)
	defer func() {
		// Leave a record another server has since written.
		if cur, ok := readPanelRecord(dir); ok && cur.Instance == state.instance {
			_ = os.Remove(filepath.Join(dir, panelRecordName))
		}
	}()
	go func() {
		// Asked a few times per idle period, and at least every five seconds.
		ticks := c.ticks
		if ticks == nil {
			t := time.NewTicker(min(max(c.idle/4, 20*time.Millisecond), 5*time.Second))
			defer t.Stop()
			ticks = t.C
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticks:
				if state.idleRemaining() <= 0 || !lockIsOurs() {
					cancel()
					return
				}
			}
		}
	}()
	return dockServeHandler(ctx, ln, handler, io.Discard)
}
