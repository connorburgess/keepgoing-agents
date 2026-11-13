package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/health"
	"github.com/connorburgess/keepgoing-agents/internal/incident"
	"github.com/connorburgess/keepgoing-agents/internal/registry"
)

// keepgoing dock serves one read-only page about every registered loop on the
// loopback interface. The page consumes a single versioned JSON endpoint. The
// layers are separate so that remote reach can be added later without a
// rewrite: dockGuard decides which network requests are acceptable at all
// (loopback host, same origin, read methods), dockAuth decides who may read
// (here, an unguessable per-run token in the path), and dockReader builds the
// report from the existing read paths. Nothing in this file writes, controls a
// loop, or talks to a provider.

//go:embed dockui/index.html dockui/dock.css dockui/dock.js
var dockUI embed.FS

const (
	dockDefaultRefresh = 15 * time.Second
	dockMinRefresh     = 5 * time.Second
	dockMaxRefresh     = 10 * time.Minute
	// dockMaxResponse bounds the JSON a read may produce. The projection
	// already caps every list and string, so reaching this is a defect.
	dockMaxResponse = 2 << 20
	// dockReadTimeout bounds one whole read of the registry.
	dockReadTimeout = 20 * time.Second
	// dockReadParallel bounds how many loops are read at once.
	dockReadParallel = 4
	dockAPIPath      = "api/v1/dock"
)

func dockCommand(e env, args []string) int {
	fs := flag.NewFlagSet("dock", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	directory := fs.String("directory", "", "private registry directory")
	port := fs.Int("port", 0, "loopback port to listen on; 0 picks a free one")
	refresh := fs.Duration("refresh", dockDefaultRefresh, "how often the page reads again (5s to 10m)")
	staleAfter := fs.Duration("stale-after", defaultStaleAfter, "age at which a check or child listing is shown as stale")
	offline := fs.Bool("offline", false, "do not ask each loop's local daemon for its next check")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(e.stdout, "Usage: keepgoing dock [--port N] [--refresh 15s] [--stale-after 30m] [--directory private-registry] [--offline]")
			fmt.Fprintln(e.stdout, "Serve a read-only page about every registered loop on 127.0.0.1 until interrupted. Supervision is not affected.")
			fs.SetOutput(e.stdout)
			fs.PrintDefaults()
			return exitOK
		}
		e.errf("dock: invalid arguments; see dock --help")
		return exitUsage
	}
	if fs.NArg() != 0 || *port < 0 || *port > 65535 || *refresh < dockMinRefresh || *refresh > dockMaxRefresh || *staleAfter <= 0 {
		e.errf("dock: port 0-65535, refresh 5s-10m, a positive stale-after; no positional arguments")
		return exitUsage
	}
	if _, err := registryDirectory(*directory); err != nil {
		e.errf("dock: registry directory unavailable")
		return exitUsage
	}
	ln, err := dockListen(*port)
	if err != nil {
		e.errf("dock: cannot listen on 127.0.0.1:%d", *port)
		return exitFailed
	}
	token, err := dockNewToken()
	if err != nil {
		ln.Close()
		e.errf("dock: no random source for the access token")
		return exitFailed
	}
	reader := &dockReader{directory: *directory, refresh: *refresh, staleAfter: *staleAfter, query: inspectDaemon}
	if *offline {
		reader.query = func(ctx context.Context, dir, current string, _ bool) daemonStatus {
			return inspectDaemon(ctx, dir, current, true)
		}
	}
	srv := newDockServer(ln.Addr().(*net.TCPAddr).Port, dockTokenAuth{token: token}, reader)
	fmt.Fprintf(e.stdout, "keepgoing dock is serving a read-only page for this computer only:\n\n  %s\n\nPress Ctrl-C to stop it. Stopping the page leaves supervision running.\n", srv.url(token))
	return srv.serve(e.ctx, ln, e.stderr)
}

// dockListen binds the IPv4 loopback address only, never a wildcard address.
func dockListen(port int) (net.Listener, error) {
	return net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
}

func dockNewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// dockAuth decides whether a request may read. It receives the path after
// the guard has accepted the request and returns the remaining resource path.
// The token implementation is the only one today; a remote mode must replace
// it with real authentication before any control is exposed.
type dockAuth interface {
	authorize(r *http.Request) (resource string, ok bool)
}

// dockTokenAuth requires /<token>/ as the first path segment. The token is
// compared in constant time and never logged.
type dockTokenAuth struct{ token string }

func (a dockTokenAuth) authorize(r *http.Request) (string, bool) {
	p := strings.TrimPrefix(r.URL.Path, "/")
	seg, rest, found := strings.Cut(p, "/")
	if !found || a.token == "" || len(seg) != len(a.token) || subtle.ConstantTimeCompare([]byte(seg), []byte(a.token)) != 1 {
		return "", false
	}
	return rest, true
}

// dockGuard accepts only requests that could have come from the page itself
// on this computer: a loopback Host (against DNS rebinding), no foreign
// Origin, no cross-site fetch metadata, and only GET or HEAD.
type dockGuard struct{ hosts map[string]bool }

func newDockGuard(port int) dockGuard {
	p := strconv.Itoa(port)
	return dockGuard{hosts: map[string]bool{"127.0.0.1:" + p: true, "localhost:" + p: true}}
}

func (g dockGuard) allow(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	if !g.hosts[strings.ToLower(r.Host)] {
		return false
	}
	if o := r.Header.Get("Origin"); o != "" {
		host, ok := strings.CutPrefix(strings.ToLower(o), "http://")
		if !ok || !g.hosts[host] {
			return false
		}
	}
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
	default:
		return false
	}
	return true
}

type dockServer struct {
	port   int
	guard  dockGuard
	auth   dockAuth
	reader *dockReader
}

func newDockServer(port int, auth dockAuth, reader *dockReader) *dockServer {
	return &dockServer{port: port, guard: newDockGuard(port), auth: auth, reader: reader}
}

func (s *dockServer) url(token string) string {
	return fmt.Sprintf("http://127.0.0.1:%d/%s/", s.port, token)
}

func (s *dockServer) serve(ctx context.Context, ln net.Listener, stderr io.Writer) int {
	hs := &http.Server{Handler: s, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: dockReadTimeout + 10*time.Second,
		IdleTimeout: time.Minute, MaxHeaderBytes: 16 << 10, ErrorLog: nil}
	done := make(chan error, 1)
	go func() { done <- hs.Serve(ln) }()
	select {
	case <-ctx.Done():
		shut, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = hs.Shutdown(shut)
		<-done
		return exitOK
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return exitOK
		}
		fmt.Fprintln(stderr, "dock: the server stopped unexpectedly")
		return exitFailed
	}
}

func dockSecurityHeaders(h http.Header) {
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("Cache-Control", "no-store")
}

func (s *dockServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	dockSecurityHeaders(w.Header())
	if !s.guard.allow(r) {
		http.Error(w, "refused", http.StatusForbidden)
		return
	}
	resource, ok := s.auth.authorize(r)
	if !ok {
		http.Error(w, "refused", http.StatusForbidden)
		return
	}
	switch resource {
	case "":
		s.asset(w, "dockui/index.html", "text/html; charset=utf-8")
	case "dock.css":
		s.asset(w, "dockui/dock.css", "text/css; charset=utf-8")
	case "dock.js":
		s.asset(w, "dockui/dock.js", "text/javascript; charset=utf-8")
	case dockAPIPath:
		identifiers := r.URL.Query().Get("identifiers") == "1"
		body, err := s.reader.json(r.Context(), identifiers)
		if err != nil {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	default:
		http.NotFound(w, r)
	}
}

func (s *dockServer) asset(w http.ResponseWriter, name, contentType string) {
	b, err := dockUI.ReadFile(name)
	if err != nil {
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", contentType)
	_, _ = w.Write(b)
}

// dockReader builds the report. Reads are serialized and a result is reused
// for two seconds, so several open tabs do not multiply the reads.
type dockReader struct {
	directory  string
	refresh    time.Duration
	staleAfter time.Duration
	query      clientDaemonQuery
	now        func() time.Time

	mu    sync.Mutex
	cache map[bool]dockCached
}

type dockCached struct {
	at   time.Time
	body []byte
}

func (d *dockReader) clock() time.Time {
	if d.now != nil {
		return d.now().UTC()
	}
	return time.Now().UTC()
}

func (d *dockReader) json(ctx context.Context, identifiers bool) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if c, ok := d.cache[identifiers]; ok && time.Since(c.at) < 2*time.Second {
		return c.body, nil
	}
	ctx, cancel := context.WithTimeout(ctx, dockReadTimeout)
	defer cancel()
	report := d.read(ctx, identifiers)
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(report); err != nil {
		return nil, err
	}
	if buf.Len() > dockMaxResponse {
		return nil, errors.New("dock report exceeds its bound")
	}
	if d.cache == nil {
		d.cache = map[bool]dockCached{}
	}
	d.cache[identifiers] = dockCached{at: time.Now(), body: buf.Bytes()}
	return buf.Bytes(), nil
}

// read uses the same code paths as registry list, client status, status and
// runs --registry-id. It takes no lock and starts nothing.
func (d *dockReader) read(ctx context.Context, identifiers bool) dockReport {
	now := d.clock()
	dir, err := registryDirectory(d.directory)
	if err != nil {
		return buildDockReport(nil, now, d.refresh, d.staleAfter, identifiers, false, 0)
	}
	f, err := registry.Load(dir)
	if err != nil {
		return buildDockReport(nil, now, d.refresh, d.staleAfter, identifiers, false, 0)
	}
	entries := f.Entries
	omitted := 0
	if len(entries) > dockMaxLoops {
		omitted, entries = len(entries)-dockMaxLoops, entries[:dockMaxLoops]
	}
	inputs := make([]dockInput, len(entries))
	sem := make(chan struct{}, dockReadParallel)
	var wg sync.WaitGroup
	for i, entry := range entries {
		wg.Add(1)
		go func(i int, entry registry.Entry) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			inputs[i] = d.readLoop(ctx, dir, entry, f.Entries, now)
		}(i, entry)
	}
	wg.Wait()
	return buildDockReport(inputs, now, d.refresh, d.staleAfter, identifiers, true, omitted)
}

func (d *dockReader) readLoop(ctx context.Context, dir string, entry registry.Entry, all []registry.Entry, now time.Time) dockInput {
	in := dockInput{Entry: entry}
	// Registered status asks the local daemon once; status reuses that answer.
	var daemon daemonStatus
	query := func(ctx context.Context, stateDir, current string, offline bool) daemonStatus {
		daemon = d.query(ctx, stateDir, current, offline)
		return daemon
	}
	in.Client = readClientStatus(ctx, dir, entry.ID, false, false, d.staleAfter, now, query)
	if in.Client.Snapshot == nil {
		return in
	}
	if cfg, ok := clientConfig(entry); ok {
		in.Chain = &dockChainInput{Primary: cfg.Loop.PrimaryCandidate(), FailoverOn: cfg.Loop.Errors.FailoverOn}
		if fb := cfg.FallbackSet; fb != nil {
			in.Chain.Candidates = fb.Candidates
			// The same read as fallbacks list.
			if reg, err := health.Load(cfg.Loop.StateDir); err == nil {
				out := summarize(cfg, reg, now)
				in.Chain.Summary = &out
				use := usageOf(reg, cfg.Loop.PrimaryCandidate(), now)
				in.Chain.PrimaryUse = &use
			}
		}
		reuse := func(context.Context, string, string, bool) daemonStatus { return daemon }
		if out, err := readStatus(ctx, cfg, now, d.staleAfter, reuse); err == nil {
			in.Status = &out
		}
	}
	if c := in.Client.Snapshot.Controls; c != nil && c.AwaitingUser {
		// The retained history timeline reads, for when and why the hold began.
		if j, err := incident.Load(entry.StateDir); err == nil {
			in.Hold = dockHoldFrom(j)
		}
	}
	loop, _, bad := readRegisteredRuns(entry, all, true, runsMaxLimit, 0, d.staleAfter)
	in.Runs, in.RunsKnown = loop, loop.Binding == recordAvailable
	_ = bad // A partial runs read still carries its blocker's own availability.
	return in
}
