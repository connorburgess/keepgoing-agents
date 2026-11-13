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
// expired, and for a page address whose session is gone. It is plain HTML
// with no script or style, so it needs nothing the content policy forbids.
const panelExpiredPage = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light dark">
<meta name="robots" content="noindex">
<title>keepgoing: open the panel again</title>
</head>
<body>
<main>
<h1>This address no longer opens the keepgoing panel</h1>
<p>An opening address works once, for 30 seconds, and a page address works only in the browser session that opened it.</p>
<p>Run <code>keepgoing panel</code> again to open the panel.</p>
<p>Your loops are not affected: the panel only shows them.</p>
</main>
</body>
</html>
`

type panelOpening struct {
	expires time.Time
	key     string // the loop to show, or "" for the overview
}

// panelState is the server's secrets and clock. None of it is written to
// disk, logged, or passed to another process.
type panelState struct {
	instance string
	segment  string // the random path segment the page is served under
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

// mint creates an opening address. Only the private socket calls it.
func (s *panelState) mint(key string) (string, error) {
	if key != "" && !panelKeyPattern.MatchString(key) {
		return "", errors.New("not a loop key")
	}
	code, err := panelRandom(32)
	if err != nil {
		return "", errors.New("no random source")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for c, o := range s.openings {
		if !now.Before(o.expires) {
			delete(s.openings, c)
		}
	}
	if len(s.openings) >= panelMaxOpenings {
		return "", errors.New("too many opening addresses are waiting; try again in 30 seconds")
	}
	s.openings[code] = panelOpening{expires: now.Add(panelOpenTTL), key: key}
	return s.base() + "/open/" + code, nil
}

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
	delete(s.openings, found)
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
	c, err := r.Cookie(panelCookieName)
	if err != nil || !s.validSession(c.Value) {
		return "", false
	}
	return rest, true
}

// panelHandler puts the health and opening endpoints in front of the page.
// Every request passes the same network guard first.
type panelHandler struct {
	state *panelState
	guard dockGuard
	page  *dockServer
}

func (h *panelHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	dockSecurityHeaders(w.Header())
	if !h.guard.allow(r) {
		http.Error(w, "refused", http.StatusForbidden)
		return
	}
	// Only a page load, a page request with a session or a new opening
	// address counts as use. Health requests and refused requests do not
	// keep the server alive.
	switch {
	case r.URL.Path == "/healthz":
		// Nothing but the instance ID: no version, time, port or count.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"instance": h.state.instance})
	case strings.HasPrefix(r.URL.Path, "/open/"):
		h.open(w, r)
	case strings.HasPrefix(r.URL.Path, "/s/"):
		if _, ok := h.state.authorize(r); !ok {
			h.expired(w, http.StatusForbidden)
			return
		}
		h.state.touch()
		h.page.ServeHTTP(w, r)
	default:
		http.NotFound(w, r)
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
	w.Header().Set("Location", target)
	w.WriteHeader(http.StatusSeeOther)
}

// panelControlServe answers the private socket: one request per connection,
// bounded in size and time, served one at a time.
func panelControlServe(l net.Listener, state *panelState, stop func()) {
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
		} else {
			switch req.Op {
			case "open":
				state.touch()
				if address, err := state.mint(req.Key); err != nil {
					reply.Error = err.Error()
				} else {
					reply.OK, reply.Address = true, address
				}
			case "status":
				// Asking does not keep the server alive.
				reply.OK = true
				reply.IdleRemainingSeconds = max(0, int(state.idleRemaining()/time.Second))
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
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *panelDir == "" || *idle <= 0 || *refresh < dockMinRefresh || *refresh > dockMaxRefresh || *staleAfter <= 0 {
		return exitUsage
	}
	dir, err := panelDirectory(*panelDir)
	if err != nil {
		return exitUsage
	}
	registryDir, err := registryDirectory(*directory)
	if err != nil {
		return exitUsage
	}
	// Held for the server's whole life: a second server cannot run beside it,
	// and a crash releases it.
	lk, err := lock.AcquireNamed(dir, panelServerLock)
	if err != nil {
		return exitFailed
	}
	defer lk.Release()
	ln, err := dockListen(0)
	if err != nil {
		return exitFailed
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	state, err := newPanelState(port, *idle, time.Now)
	if err != nil {
		return exitFailed
	}
	// The lock proves no server owns a socket file left here.
	socket := filepath.Join(dir, panelSocketName)
	_ = os.Remove(socket)
	control, err := net.Listen("unix", socket)
	if err != nil {
		return exitFailed
	}
	defer os.Remove(socket)
	defer control.Close()
	if err := os.Chmod(socket, 0o600); err != nil {
		return exitFailed
	}
	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	go panelControlServe(control, state, cancel)

	reader := &dockReader{ctx: ctx, directory: *directory, refresh: *refresh, staleAfter: *staleAfter, query: inspectDaemon}
	if *offline {
		reader.query = func(ctx context.Context, dir, current string, _ bool) daemonStatus {
			return inspectDaemon(ctx, dir, current, true)
		}
	}
	handler := &panelHandler{state: state, guard: newDockGuard(port), page: newDockServer(port, state, reader)}
	rec := panelRecord{SchemaVersion: 1, PID: os.Getpid(), Port: port, StartedAt: time.Now().UTC().Truncate(time.Second), IdleSeconds: int(*idle / time.Second),
		Instance: state.instance, Registry: registryDir, Version: version}
	if rec.IdleSeconds < 1 {
		rec.IdleSeconds = 1
	}
	if err := writePanelRecord(dir, rec); err != nil {
		return exitFailed
	}
	defer func() {
		// Leave a record another server has since written.
		if cur, ok := readPanelRecord(dir); ok && cur.Instance == state.instance {
			_ = os.Remove(filepath.Join(dir, panelRecordName))
		}
	}()
	go func() {
		tick := min(max(*idle/4, 20*time.Millisecond), 5*time.Second)
		t := time.NewTicker(tick)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if state.idleRemaining() <= 0 {
					cancel()
					return
				}
			}
		}
	}()
	return dockServeHandler(ctx, ln, handler, io.Discard)
}
