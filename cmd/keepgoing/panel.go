package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
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
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/registry"
	"github.com/connorburgess/keepgoing-agents/internal/snapshot"
)

// keepgoing panel opens the page that keepgoing dock serves, without an
// address to copy. It finds a background server this user already started, or
// starts one, and opens the browser at a single-use address that the server
// exchanges for a session cookie. The parts:
//
//   - panel.json, in a private directory that is never a loop's: the server's
//     port, process ID, start time, idle limit and instance ID. It holds no
//     secret. A record is trusted only when the server it names answers its
//     health request with the same instance ID.
//   - panel.sock, a local socket in the same directory that only this user
//     can reach. Opening addresses, status and stop go through it, so nothing
//     secret is ever placed in another program's arguments or environment.
//   - two locks: panel-open serializes callers, so two concurrent commands
//     end with one server; panel-server is held by the server for its
//     lifetime, so a second server cannot start beside it.
//
// Nothing here reads or writes a loop's files beyond the reads the page
// already makes, and stopping the server never affects supervision.

const (
	panelRecordName  = "panel.json"
	panelSocketName  = "panel.sock"
	panelOpenLock    = "panel-open"
	panelServerLock  = "panel-server"
	panelDefaultIdle = 30 * time.Minute
	panelMinIdle     = time.Minute
	panelMaxIdle     = 24 * time.Hour
	// panelOpenTTL is how long an opening address can be used, once.
	panelOpenTTL   = 30 * time.Second
	panelStartWait = 10 * time.Second
	// panelOpenerEnv names a program to run instead of the platform's browser
	// launcher. It receives the opening address as its only argument.
	panelOpenerEnv = "KEEPGOING_PANEL_OPENER"
)

// panelRecord is the runtime record. It carries no token, code or cookie.
type panelRecord struct {
	SchemaVersion int       `json:"schemaVersion"`
	PID           int       `json:"pid"`
	Port          int       `json:"port"`
	StartedAt     time.Time `json:"startedAt"`
	IdleSeconds   int       `json:"idleSeconds"`
	// Instance identifies one run of the server. The health endpoint returns
	// it, which is how a record is matched to the server that wrote it.
	Instance string `json:"instance"`
	// Registry is the registry directory the server reads.
	Registry string `json:"registry"`
	Version  string `json:"version"`
}

var (
	panelInstancePattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
	panelKeyPattern      = regexp.MustCompile(`^l[0-9a-f]{12}$`)
)

// panelSupported is false where the background server has never been run.
// keepgoing panel refuses there and says so; keepgoing dock and panel list
// work everywhere. A test replaces it.
var panelSupported = func() bool { return runtime.GOOS != "windows" }

const panelUnsupported = "keepgoing panel has not been verified on Windows yet, so it does not start there. Run keepgoing dock and open the address it prints."

// panelSocketLimit is the longest socket path the platform accepts.
func panelSocketLimit() int {
	switch runtime.GOOS {
	case "darwin", "freebsd", "openbsd", "netbsd", "dragonfly":
		return 103
	default:
		return 107
	}
}

// panelDirectory resolves the private directory for the record, the locks and
// the socket: the same parent the registry uses, never a loop's directory and
// never inside a Git checkout. It creates the directory when missing.
func panelDirectory(path string) (string, error) {
	bad := errors.New("the panel directory must be a private path outside Git checkouts")
	if path == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return "", bad
		}
		path = filepath.Join(base, "keepgoing", "panel")
	}
	dir, err := canonicalFuturePath(path)
	if err != nil {
		return "", bad
	}
	for p := dir; ; p = filepath.Dir(p) {
		if _, err := os.Lstat(filepath.Join(p, ".git")); err == nil || !errors.Is(err, os.ErrNotExist) {
			return "", bad
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	if registry.CheckDirectory(dir) != nil {
		return "", bad
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", bad
	}
	// A directory that already existed with wider access is refused above;
	// one just created is private.
	if registry.CheckDirectory(dir) != nil {
		return "", bad
	}
	if n, limit := len(filepath.Join(dir, panelSocketName)), panelSocketLimit(); n > limit {
		// Never fall back to anything weaker than the private socket.
		return "", fmt.Errorf("the panel directory's path is too long for a local socket (%d characters, at most %d); pass a shorter --panel-directory", n, limit)
	}
	return dir, nil
}

func readPanelRecord(dir string) (panelRecord, bool) {
	var rec panelRecord
	path := filepath.Join(dir, panelRecordName)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 || (runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0) {
		return rec, false
	}
	f, err := snapshot.Open(path)
	if err != nil {
		return rec, false
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(b) > 4096 || json.Unmarshal(b, &rec) != nil {
		return panelRecord{}, false
	}
	if rec.SchemaVersion != 1 || rec.PID <= 0 || rec.Port < 1 || rec.Port > 65535 || rec.StartedAt.IsZero() || rec.IdleSeconds <= 0 || !panelInstancePattern.MatchString(rec.Instance) {
		return panelRecord{}, false
	}
	return rec, true
}

func writePanelRecord(dir string, rec panelRecord) error {
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".panel-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(b, '\n')); err == nil {
		err = f.Chmod(0o600)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return snapshot.Replace(f.Name(), filepath.Join(dir, panelRecordName))
}

// panelAnswers reports whether the server a record names is running: its
// health request must return the record's instance ID. A reused process ID or
// port cannot do that.
func panelAnswers(ctx context.Context, rec panelRecord) bool {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(rec.Port)+"/healthz", nil)
	if err != nil {
		return false
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var reply struct {
		Instance string `json:"instance"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 256)).Decode(&reply) != nil {
		return false
	}
	return reply.Instance == rec.Instance
}

// panelLive returns the record of a running server, if there is one.
func panelLive(ctx context.Context, dir string) (panelRecord, bool) {
	rec, ok := readPanelRecord(dir)
	if !ok || !panelAnswers(ctx, rec) {
		return panelRecord{}, false
	}
	return rec, true
}

// panelRequest is one message on the private socket.
type panelRequest struct {
	Op string `json:"op"` // open, status or stop
	// Key is the page key of the loop to open; empty opens the overview.
	Key string `json:"key,omitempty"`
}

type panelReply struct {
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Address string `json:"address,omitempty"`
	// Status fields.
	IdleRemainingSeconds int `json:"idleRemainingSeconds,omitempty"`
}

func panelControl(ctx context.Context, dir string, req panelRequest) (panelReply, error) {
	var reply panelReply
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	c, err := (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(dir, panelSocketName))
	if err != nil {
		return reply, errors.New("the panel server did not answer on its private socket")
	}
	defer c.Close()
	deadline, _ := ctx.Deadline()
	_ = c.SetDeadline(deadline)
	if err := json.NewEncoder(c).Encode(req); err != nil {
		return reply, errors.New("the panel server did not accept the request")
	}
	if json.NewDecoder(io.LimitReader(c, 4096)).Decode(&reply) != nil {
		return panelReply{}, errors.New("the panel server's answer could not be read")
	}
	if !reply.OK {
		if reply.Error == "" {
			reply.Error = "the panel server refused the request"
		}
		return reply, errors.New(reply.Error)
	}
	return reply, nil
}

// acquirePanelOpenLock waits for the caller lock, so that of two concurrent
// commands one starts the server and the other finds it running.
func acquirePanelOpenLock(ctx context.Context, dir string) (*lock.Lock, error) {
	deadline := time.Now().Add(panelStartWait + 5*time.Second)
	for {
		lk, err := lock.AcquireNamed(dir, panelOpenLock)
		if err == nil {
			return lk, nil
		}
		if !errors.Is(err, lock.ErrHeld) || time.Now().After(deadline) || ctx.Err() != nil {
			return nil, errors.New("another keepgoing panel command is still running")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

type panelOptions struct {
	dir        string // the panel's private directory
	registry   string // the --directory value, as given
	idle       time.Duration
	refresh    time.Duration
	staleAfter time.Duration
	offline    bool
}

// panelEnsure returns a running server, starting one when none answers. The
// caller holds the panel-open lock.
func panelEnsure(ctx context.Context, o panelOptions) (panelRecord, bool, error) {
	if rec, ok := panelLive(ctx, o.dir); ok {
		return rec, false, nil
	}
	// Whatever record is there names no running server. The new server
	// replaces it; the socket file is replaced by the server too.
	exe, err := os.Executable()
	if err != nil {
		return panelRecord{}, false, errors.New("cannot find this executable to start the panel server")
	}
	args := []string{"__panel-server", "--panel-directory", o.dir, "--idle", o.idle.String(), "--refresh", o.refresh.String(), "--stale-after", o.staleAfter.String()}
	if o.registry != "" {
		args = append(args, "--directory", o.registry)
	}
	if o.offline {
		args = append(args, "--offline")
	}
	cmd := exec.Command(exe, args...)
	if err := detachDaemon(cmd); err != nil {
		return panelRecord{}, false, errors.New("cannot start a background process on this platform")
	}
	// Stdin, stdout and stderr are the null device: the server keeps no log.
	if err := cmd.Start(); err != nil {
		return panelRecord{}, false, errors.New("cannot start the panel server")
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	deadline := time.Now().Add(panelStartWait)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		if rec, ok := readPanelRecord(o.dir); ok && rec.PID == cmd.Process.Pid && panelAnswers(ctx, rec) {
			return rec, true, nil
		}
		select {
		case <-exited:
			// It may have lost to a server that was starting at the same
			// moment outside this lock, for example one started by hand.
			if rec, ok := panelLive(ctx, o.dir); ok {
				return rec, false, nil
			}
			return panelRecord{}, false, errors.New("the panel server exited while starting")
		case <-time.After(50 * time.Millisecond):
		}
	}
	_ = cmd.Process.Kill()
	return panelRecord{}, false, errors.New("the panel server did not answer within 10 seconds")
}

// panelLoopKey resolves --loop to the page's key for that loop. The value may
// be a loop name, a registration ID or a key printed by panel list.
func panelLoopKey(registryDir, value string) (string, error) {
	if panelKeyPattern.MatchString(value) {
		return value, nil
	}
	dir, err := registryDirectory(registryDir)
	if err != nil {
		return "", errors.New("registry directory unavailable")
	}
	f, err := registry.Load(dir)
	if err != nil {
		return "", errors.New("the registry could not be read")
	}
	var found []string
	for _, entry := range f.Entries {
		if entry.ID == value {
			return dockKey(entry.ID), nil
		}
		if entry.Loop == value {
			found = append(found, entry.ID)
		}
	}
	switch len(found) {
	case 0:
		return "", errors.New("no registered loop has that name; see keepgoing panel list")
	case 1:
		return dockKey(found[0]), nil
	}
	return "", errors.New("more than one registered loop has that name; pass its registration ID from keepgoing registry list")
}

func panelOpener(address string) *exec.Cmd {
	if custom := os.Getenv(panelOpenerEnv); custom != "" {
		return exec.Command(custom, address)
	}
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", address)
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", address)
	default:
		return exec.Command("xdg-open", address)
	}
}

const panelUsage = `Usage: keepgoing panel [--loop NAME] [--no-open] [--idle 30m] [--directory private-registry]
       keepgoing panel status [--json]
       keepgoing panel stop
       keepgoing panel list [--json] [--directory private-registry]

Open the keepgoing panel, the read-only page about every registered loop, in
your browser. A background server for this computer only is started when none
is running and stops by itself when unused. Supervision is not affected by
starting, stopping or closing it.`

func panelCommand(e env, args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "status":
			return panelStatusCommand(e, args[1:])
		case "stop":
			return panelStopCommand(e, args[1:])
		case "list":
			return panelListCommand(e, args[1:])
		}
	}
	if !panelSupported() {
		e.errf("panel: %s", panelUnsupported)
		return exitFailed
	}
	fs := flag.NewFlagSet("panel", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	loop := fs.String("loop", "", "open this loop's page: its name, registration ID or key")
	noOpen := fs.Bool("no-open", false, "print the opening address and do not start a browser")
	idle := fs.Duration("idle", panelDefaultIdle, "stop the background server after this long without a request (1m to 24h)")
	directory := fs.String("directory", "", "private registry directory")
	panelDir := fs.String("panel-directory", "", "private directory for the panel's runtime record and socket")
	refresh := fs.Duration("refresh", dockDefaultRefresh, "how often the page reads again (5s to 10m)")
	staleAfter := fs.Duration("stale-after", defaultStaleAfter, "age at which a check or child listing is shown as stale")
	offline := fs.Bool("offline", false, "do not ask each loop's local daemon for its next check")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(e.stdout, panelUsage)
			fs.SetOutput(e.stdout)
			fs.PrintDefaults()
			return exitOK
		}
		e.errf("panel: invalid arguments; see panel --help")
		return exitUsage
	}
	if fs.NArg() != 0 || *idle < panelMinIdle || *idle > panelMaxIdle || *refresh < dockMinRefresh || *refresh > dockMaxRefresh || *staleAfter <= 0 {
		e.errf("panel: idle 1m-24h, refresh 5s-10m, a positive stale-after; no positional arguments except status, stop or list")
		return exitUsage
	}
	if _, err := registryDirectory(*directory); err != nil {
		e.errf("panel: registry directory unavailable")
		return exitUsage
	}
	dir, err := panelDirectory(*panelDir)
	if err != nil {
		e.errf("panel: %v", err)
		return exitUsage
	}
	key := ""
	if *loop != "" {
		if key, err = panelLoopKey(*directory, *loop); err != nil {
			e.errf("panel: %v", err)
			return exitFailed
		}
	}
	lk, err := acquirePanelOpenLock(e.ctx, dir)
	if err != nil {
		e.errf("panel: %v", err)
		return exitFailed
	}
	rec, started, err := panelEnsure(e.ctx, panelOptions{dir: dir, registry: *directory, idle: *idle, refresh: *refresh, staleAfter: *staleAfter, offline: *offline})
	_ = lk.Release()
	if err != nil {
		e.errf("panel: %v", err)
		return exitFailed
	}
	if !started {
		if want, _ := registryDirectory(*directory); rec.Registry != want {
			e.errf("panel: a panel for another registry is already running; run keepgoing panel stop first")
			return exitFailed
		}
	}
	reply, err := panelControl(e.ctx, dir, panelRequest{Op: "open", Key: key})
	if err != nil {
		e.errf("panel: %v", err)
		return exitFailed
	}
	if *noOpen {
		fmt.Fprintf(e.stdout, "Open this address in a browser on this computer within 30 seconds. It works once:\n\n  %s\n\nRun keepgoing panel again for a new one. keepgoing panel stop ends the background server; supervision is not affected.\n", reply.Address)
		return exitOK
	}
	// The opening address is the only thing given to another program. It is
	// single use and short lived; the session it becomes never leaves the
	// server and the browser.
	cmd := panelOpener(reply.Address)
	if err := cmd.Start(); err != nil {
		e.errf("panel: could not start a browser. Run keepgoing panel --no-open and open the address it prints.")
		return exitFailed
	}
	go func() { _ = cmd.Wait() }()
	what := "Opened the keepgoing panel in your browser."
	if started {
		what = "Started the keepgoing panel and opened it in your browser."
	}
	fmt.Fprintf(e.stdout, "%s It stops by itself after %s without a request; supervision is not affected.\n", what, time.Duration(rec.IdleSeconds)*time.Second)
	return exitOK
}

type panelStatus struct {
	SchemaVersion        int        `json:"schemaVersion"`
	Running              bool       `json:"running"`
	StartedAt            *time.Time `json:"startedAt,omitempty"`
	Port                 int        `json:"port,omitempty"`
	IdleSeconds          int        `json:"idleSeconds,omitempty"`
	IdleRemainingSeconds *int       `json:"idleRemainingSeconds,omitempty"`
}

func panelStatusCommand(e env, args []string) int {
	if !panelSupported() {
		e.errf("panel status: %s", panelUnsupported)
		return exitFailed
	}
	fs := flag.NewFlagSet("panel status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	asJSON := fs.Bool("json", false, "print JSON")
	panelDir := fs.String("panel-directory", "", "private directory for the panel's runtime record and socket")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		e.errf("panel status: invalid arguments; usage: keepgoing panel status [--json]")
		return exitUsage
	}
	dir, err := panelDirectory(*panelDir)
	if err != nil {
		e.errf("panel status: %v", err)
		return exitUsage
	}
	st := panelStatus{SchemaVersion: 1}
	if rec, ok := panelLive(e.ctx, dir); ok {
		started := rec.StartedAt.UTC()
		st.Running, st.StartedAt, st.Port, st.IdleSeconds = true, &started, rec.Port, rec.IdleSeconds
		if reply, err := panelControl(e.ctx, dir, panelRequest{Op: "status"}); err == nil {
			remaining := reply.IdleRemainingSeconds
			st.IdleRemainingSeconds = &remaining
		}
	}
	if *asJSON {
		_ = json.NewEncoder(e.stdout).Encode(st)
		return exitOK
	}
	if !st.Running {
		fmt.Fprintln(e.stdout, "The keepgoing panel is not running. Run keepgoing panel to start and open it.")
		return exitOK
	}
	fmt.Fprintf(e.stdout, "The keepgoing panel is running on this computer only, port %d, since %s.\n", st.Port, st.StartedAt.Local().Format("2006-01-02 15:04 MST"))
	idle := time.Duration(st.IdleSeconds) * time.Second
	if st.IdleRemainingSeconds != nil {
		fmt.Fprintf(e.stdout, "It stops by itself after %s without a request: in about %s if nothing asks.\n", idle, (time.Duration(*st.IdleRemainingSeconds) * time.Second).Round(time.Minute))
	} else {
		fmt.Fprintf(e.stdout, "It stops by itself after %s without a request.\n", idle)
	}
	fmt.Fprintln(e.stdout, "keepgoing panel opens it; keepgoing panel stop ends it. Supervision is not affected either way.")
	return exitOK
}

func panelStopCommand(e env, args []string) int {
	if !panelSupported() {
		e.errf("panel stop: %s", panelUnsupported)
		return exitFailed
	}
	fs := flag.NewFlagSet("panel stop", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	panelDir := fs.String("panel-directory", "", "private directory for the panel's runtime record and socket")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		e.errf("panel stop: invalid arguments; usage: keepgoing panel stop")
		return exitUsage
	}
	dir, err := panelDirectory(*panelDir)
	if err != nil {
		e.errf("panel stop: %v", err)
		return exitUsage
	}
	lk, err := acquirePanelOpenLock(e.ctx, dir)
	if err != nil {
		e.errf("panel stop: %v", err)
		return exitFailed
	}
	defer lk.Release()
	rec, ok := panelLive(e.ctx, dir)
	if !ok {
		fmt.Fprintln(e.stdout, "The keepgoing panel is not running.")
		return exitOK
	}
	if _, err := panelControl(e.ctx, dir, panelRequest{Op: "stop"}); err != nil {
		e.errf("panel stop: %v", err)
		return exitFailed
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if panelAnswers(e.ctx, rec) {
			continue
		}
		// The server holds its lock until its record and socket are gone, so
		// taking the lock proves it has finished, not only stopped answering.
		if held, err := lock.AcquireNamed(dir, panelServerLock); err == nil {
			_ = held.Release()
			fmt.Fprintln(e.stdout, "Stopped the keepgoing panel. Supervision is not affected.")
			return exitOK
		}
	}
	e.errf("panel stop: the panel server was asked to stop and has not finished")
	return exitFailed
}

// panelList is the picker without a server: each loop's name and its state in
// the page's own words, from the same code that builds the page. It carries
// no registration ID, thread ID, path or note.
type panelList struct {
	SchemaVersion int             `json:"schemaVersion"`
	ReadAt        time.Time       `json:"readAt"`
	Registry      string          `json:"registry"` // available or unavailable
	Complete      bool            `json:"complete"`
	AllClear      bool            `json:"allClear"`
	NeedsYou      int             `json:"needsYou"`
	Unread        int             `json:"unread"`
	Omitted       int             `json:"omitted"`
	Loops         []panelListLoop `json:"loops"`
}

type panelListLoop struct {
	// Key is the loop's key on the page; panel --loop accepts it.
	Key      string `json:"key"`
	Name     string `json:"name"`
	State    string `json:"state"`
	Phrase   string `json:"phrase"`
	NeedsYou bool   `json:"needsYou"`
}

func buildPanelList(r dockReport) panelList {
	out := panelList{SchemaVersion: 1, ReadAt: r.ReadAt, Registry: r.Registry.Availability, Complete: r.Complete, AllClear: r.AllClear,
		NeedsYou: len(r.NeedsYou), Unread: r.Unread, Omitted: r.Registry.Omitted, Loops: []panelListLoop{}}
	needs := map[string]bool{}
	for _, n := range r.NeedsYou {
		needs[n.Key] = true
	}
	for _, l := range r.Loops {
		out.Loops = append(out.Loops, panelListLoop{Key: l.Key, Name: l.Name, State: l.State, Phrase: l.Phrase, NeedsYou: needs[l.Key]})
	}
	return out
}

func panelListCommand(e env, args []string) int {
	fs := flag.NewFlagSet("panel list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	asJSON := fs.Bool("json", false, "print JSON")
	directory := fs.String("directory", "", "private registry directory")
	staleAfter := fs.Duration("stale-after", defaultStaleAfter, "age at which a check is shown as stale")
	offline := fs.Bool("offline", false, "do not ask each loop's local daemon for its next check")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *staleAfter <= 0 {
		e.errf("panel list: invalid arguments; usage: keepgoing panel list [--json] [--directory private-registry]")
		return exitUsage
	}
	if _, err := registryDirectory(*directory); err != nil {
		e.errf("panel list: registry directory unavailable")
		return exitUsage
	}
	reader := &dockReader{ctx: e.ctx, directory: *directory, refresh: dockDefaultRefresh, staleAfter: *staleAfter, query: inspectDaemon}
	if *offline {
		reader.query = func(ctx context.Context, dir, current string, _ bool) daemonStatus {
			return inspectDaemon(ctx, dir, current, true)
		}
	}
	ctx, cancel := context.WithTimeout(e.ctx, dockReadTimeout)
	defer cancel()
	list := buildPanelList(reader.read(ctx, false))
	if e.ctx.Err() != nil {
		e.errf("panel list: cancelled")
		return exitFailed
	}
	if *asJSON {
		if err := json.NewEncoder(e.stdout).Encode(list); err != nil {
			return exitFailed
		}
	} else {
		w := bufio.NewWriter(e.stdout)
		switch {
		case list.Registry != "available":
			fmt.Fprintln(w, "The registry could not be read.")
		case len(list.Loops) == 0:
			fmt.Fprintln(w, "No loops are registered. Register one with keepgoing registry add.")
		}
		for _, l := range list.Loops {
			fmt.Fprintf(w, "%s: %s\n", l.Name, l.Phrase)
		}
		switch {
		case list.NeedsYou == 1:
			fmt.Fprintln(w, "1 loop needs you or could not be read. Run keepgoing panel to see why.")
		case list.NeedsYou > 1:
			fmt.Fprintf(w, "%d loops need you or could not be read. Run keepgoing panel to see why.\n", list.NeedsYou)
		case list.AllClear:
			fmt.Fprintln(w, "Nothing needs you.")
		case list.Registry == "available":
			fmt.Fprintln(w, "Not every loop was read, so this cannot say that nothing needs you.")
		}
		_ = w.Flush()
	}
	if list.Registry != "available" {
		return exitFailed
	}
	return exitOK
}
