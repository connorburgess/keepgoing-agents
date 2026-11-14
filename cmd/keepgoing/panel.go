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
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/envvar"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/registry"
	"github.com/connorburgess/keepgoing-agents/internal/session"
	"github.com/connorburgess/keepgoing-agents/internal/snapshot"
	"github.com/connorburgess/keepgoing-agents/internal/userdir"
)

// keepgoing panel opens the page that keepgoing dock serves, without an
// address to copy. It finds a background server this user already started, or
// starts one, and opens the browser at a single-use address that the server
// exchanges for a session cookie. The parts:
//
//   - panel-server.lock, held by the server for its whole life. It is the
//     only evidence that a server is running: a crash releases it, and nothing
//     else can hold it. A second server cannot start beside the first.
//   - panel.sock, a local socket in the same private directory that only this
//     user can reach. The server is found, asked for an opening, asked for its
//     status and stopped through it, so nothing secret is ever placed in
//     another program's arguments or environment.
//   - panel.json: the server's port, process ID, start time, idle limit and
//     instance ID, for a person to read. It holds no secret and is never
//     relied on: the server rewrites it when it is missing or wrong.
//   - panel-open.lock serializes callers, so two concurrent commands end with
//     one server.
//
// Nothing here reads or writes a loop's files beyond the reads the page
// already makes, and stopping the server never affects supervision.

const (
	panelRecordName = "panel.json"
	panelSocketName = "panel.sock"
	// Both are listed in migrateLockNames (migrate.go); so must any new
	// lock file in the panel directory.
	panelOpenLock    = "panel-open"
	panelServerLock  = "panel-server"
	panelDefaultIdle = 30 * time.Minute
	panelMinIdle     = time.Minute
	panelMaxIdle     = 24 * time.Hour
	// panelOpenTTL is how long an opening address can be used, once.
	panelOpenTTL   = 30 * time.Second
	panelStartWait = 10 * time.Second
	// panelOpenerEnv names a program to run instead of the platform's browser
	// the default web browser. It receives the path of the launch page as its
	// only argument.
	panelOpenerEnv = envvar.Prefix + string(envvar.PanelOpener)
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
	// Changes: started with --allow-changes. Since StartedAt, as the mode
	// never changes while the server runs.
	Changes bool `json:"changes,omitempty"`
}

var (
	panelInstancePattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
	panelKeyPattern      = regexp.MustCompile(`^l[0-9a-f]{12}$`)
	panelCodePattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	panelLaunchPattern   = regexp.MustCompile(`^open-[0-9a-f]{32}\.html$`)
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

// The panel's runtime files live under the user's state directory, beside
// the session claims. Builds before this kept them under the configuration
// directory, which is a different place on Linux; see panelRetireLegacy.
var (
	panelStateBase  = userdir.StateHome
	panelLegacyBase = userdir.ConfigHome
)

// panelDirName is the name of the tool's directory under those two places:
// looptender, or keepgoing on a computer that already has one; see userdir.
func panelDirName() string {
	state, _ := panelStateBase()
	config, _ := panelLegacyBase()
	return userdir.NameIn(config, state)
}

// panelDirectory resolves the private directory for the record, the locks and
// the socket: never a loop's directory and never inside a Git checkout. It
// creates the directory when missing.
func panelDirectory(path string) (string, error) {
	bad := errors.New("the panel directory must be a private path outside Git checkouts")
	if path == "" {
		base, err := panelStateBase()
		if err != nil {
			return "", bad
		}
		path = filepath.Join(base, panelDirName(), "panel")
		// A panel server keeps the directory it was started in. If the name
		// in use has changed since (see userdir), a server may be running
		// under the other name: every panel command then uses that server's
		// directory, and none starts a second one beside it.
		if other := panelRunningUnderOtherName(base); other != "" {
			path = other
		}
	}
	return panelPrivateDirectory(path, bad)
}

// panelRunningUnderOtherName returns the panel directory under the tool's
// other directory name when a panel server is running there and none is
// running under the name in use. It creates nothing.
func panelRunningUnderOtherName(base string) string {
	name, other := panelDirName(), userdir.LegacyName
	if name == userdir.LegacyName {
		other = userdir.Name
	}
	running := func(dir string) bool {
		held, err := lock.Held(dir, panelServerLock)
		return err == nil && held
	}
	candidate := filepath.Join(base, other, "panel")
	if running(candidate) && !running(filepath.Join(base, name, "panel")) {
		return candidate
	}
	return ""
}

// panelServerUnderOtherName returns the default panel directory under the
// name that dir is not under, when a panel server is running there and it is
// a different directory from dir. It creates nothing.
func panelServerUnderOtherName(dir string) string {
	base, err := panelStateBase()
	if err != nil {
		return ""
	}
	for _, name := range []string{userdir.Name, userdir.LegacyName} {
		candidate := filepath.Join(base, name, "panel")
		if session.CleanPath(candidate) == session.CleanPath(dir) {
			continue
		}
		if held, err := lock.Held(candidate, panelServerLock); err == nil && held {
			return candidate
		}
	}
	return ""
}

// panelSameDefaultRegistry reports whether a and b are both the default
// registry, under the tool's two directory names.
func panelSameDefaultRegistry(a, b string) bool {
	base, err := panelLegacyBase() // the user configuration directory
	if err != nil {
		return false
	}
	defaults := map[string]bool{}
	for _, name := range []string{userdir.Name, userdir.LegacyName} {
		if p, err := canonicalFuturePath(filepath.Join(base, name, "registry")); err == nil {
			defaults[p] = true
		}
	}
	return defaults[a] && defaults[b]
}

// panelLegacyNotice is what a read-only command says when it finds a server
// an earlier build started at the old location. It does not act on it.
const panelLegacyNotice = "A panel started by an earlier build is running at %s; keepgoing panel stop ends it."

// panelLegacy returns the directory an earlier build used, when it is a
// different place from the default one now and is a private directory of
// this user's. It creates and changes nothing.
func panelLegacy() (string, bool) {
	base, err := panelLegacyBase()
	if err != nil {
		return "", false
	}
	now, err := panelStateBase()
	if err != nil {
		return "", false
	}
	// An earlier build only ever used the earlier name. Where that directory
	// exists, the earlier name is the one in use now as well.
	legacy := filepath.Join(base, userdir.LegacyName, "panel")
	if session.CleanPath(legacy) == session.CleanPath(filepath.Join(now, panelDirName(), "panel")) {
		return "", false // the same place on this platform
	}
	if info, err := os.Lstat(legacy); err != nil || !info.IsDir() || registry.CheckDirectory(legacy) != nil {
		return "", false // nothing there, or nothing of ours to touch
	}
	return legacy, true
}

// panelLegacyRunning says whether a server an earlier build started is
// running at the old location. Read-only: with no lock file there, nothing
// can hold it, and none is created.
func panelLegacyRunning() (string, bool) {
	legacy, ok := panelLegacy()
	if !ok {
		return "", false
	}
	if _, err := os.Lstat(filepath.Join(legacy, panelServerLock+".lock")); err != nil {
		return "", false
	}
	return legacy, panelServerHeld(legacy)
}

// panelRetireLegacy deals once with the directory an earlier build used. A
// panel server that build started is found there and stopped, so it is not
// left running unseen; then its runtime files and the directory are
// removed, and it is never looked at again. A server that does not stop is
// left, and the next command tries again. Only keepgoing panel (opening) and
// keepgoing panel stop call it; status and list only report what they find.
// It returns the old directory when it stopped a server there.
func panelRetireLegacy() (string, bool) {
	legacy, ok := panelLegacy()
	if !ok {
		return "", false
	}
	stopped := false
	if panelServerHeld(legacy) {
		if _, err := panelControl(context.Background(), legacy, panelRequest{Op: "stop"}); err != nil {
			return "", false
		}
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
			if !panelServerHeld(legacy) {
				stopped = true
				break
			}
		}
		if !stopped {
			return "", false
		}
	}
	panelRemoveLeftovers(legacy)
	for _, name := range []string{panelOpenLock + ".lock", panelServerLock + ".lock"} {
		_ = os.Remove(filepath.Join(legacy, name))
	}
	_ = os.Remove(legacy) // only if it is now empty
	return legacy, stopped
}

func panelPrivateDirectory(path string, bad error) (string, error) {
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
	// Checked before anything is created.
	if n, limit := len(filepath.Join(dir, panelSocketName)), panelSocketLimit(); n > limit {
		// Never fall back to anything weaker than the private socket.
		return "", fmt.Errorf("the panel directory's path is too long for a local socket (%d characters, at most %d); pass a shorter --panel-directory", n, limit)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", bad
	}
	// A directory that already existed with wider access is refused above;
	// one just created is private.
	if registry.CheckDirectory(dir) != nil {
		return "", bad
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
	if !validPanelRecord(rec) {
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

// panelRequest is one message on the private socket.
type panelRequest struct {
	Op string `json:"op"` // open, status or stop
	// Key is the page key of the loop to open; empty opens the overview.
	Key string `json:"key,omitempty"`
	// File asks for the opening as a private file to hand to a browser
	// launcher, so the opening address is in no program's arguments.
	File bool `json:"file,omitempty"`
	// At is when the request was made. The server ignores a request that
	// reaches it late, so one that waited while the server was suspended is
	// not carried out after the caller was told it had failed.
	At time.Time `json:"at"`
}

// panelRequestMaxAge is how late a request may reach the server, and
// panelControlWait how long a caller waits for the answer. The wait is the
// longer of the two on purpose: by the time a caller gives up and reports a
// failure, the request it sent is already too old to be carried out.
const (
	panelRequestMaxAge = 2 * time.Second
	panelControlWait   = 3 * time.Second
)

type panelReply struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	// Address is the opening address; File is the name, in the panel's
	// private directory, of a page that goes to it.
	Address string `json:"address,omitempty"`
	File    string `json:"file,omitempty"`
	// Status: the server's own account of itself.
	Record               *panelRecord `json:"record,omitempty"`
	IdleRemainingSeconds int          `json:"idleRemainingSeconds,omitempty"`
	// SinceUseSeconds is how long ago a page last read from the server.
	SinceUseSeconds int `json:"sinceUseSeconds,omitempty"`
}

func panelControl(ctx context.Context, dir string, req panelRequest) (panelReply, error) {
	var reply panelReply
	ctx, cancel := context.WithTimeout(ctx, panelControlWait)
	defer cancel()
	c, err := (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(dir, panelSocketName))
	if err != nil {
		return reply, errors.New("the panel did not answer on its private socket")
	}
	defer c.Close()
	deadline, _ := ctx.Deadline()
	_ = c.SetDeadline(deadline)
	req.At = time.Now()
	if err := json.NewEncoder(c).Encode(req); err != nil {
		return reply, errors.New("the panel did not accept the request")
	}
	if json.NewDecoder(io.LimitReader(c, 4096)).Decode(&reply) != nil {
		return panelReply{}, errors.New("the panel's answer could not be read")
	}
	if !reply.OK {
		if reply.Error == "" {
			reply.Error = "the panel refused the request"
		}
		return reply, errors.New(reply.Error)
	}
	return reply, nil
}

// panelPresence is what is known about a panel server.
type panelPresence int

const (
	// panelAbsent: nothing holds the server's lock, so no server is running,
	// whatever files are left in the directory.
	panelAbsent panelPresence = iota
	// panelRunning: a server holds the lock and answered on its socket.
	panelRunning
	// panelNotAnswering: a server holds the lock and did not answer: it is
	// starting, stopping, suspended or stuck.
	panelNotAnswering
)

// panelServerHeld reports whether a server holds the lock. The operating
// system releases the lock when its holder exits, so this cannot be left
// behind by a crash or imitated by another program.
func panelServerHeld(dir string) bool {
	lk, err := lock.AcquireNamed(dir, panelServerLock)
	if err == nil {
		_ = lk.Release()
		return false
	}
	return errors.Is(err, lock.ErrHeld)
}

func validPanelRecord(rec panelRecord) bool {
	return rec.SchemaVersion == 1 && rec.PID > 0 && rec.Port >= 1 && rec.Port <= 65535 && !rec.StartedAt.IsZero() && rec.IdleSeconds > 0 && panelInstancePattern.MatchString(rec.Instance)
}

// panelFind says whether a server is running and, when it answers, returns
// its own account of itself. The record file is not consulted: a missing,
// stale or forged panel.json changes nothing here.
func panelFind(ctx context.Context, dir string) (panelPresence, panelRecord) {
	// The lock is probed by taking it for an instant, so two commands
	// probing at the same moment can each see the other's hold. A hold with
	// no answer is therefore looked at again before it is believed.
	for attempt := 0; ; attempt++ {
		if !panelServerHeld(dir) {
			return panelAbsent, panelRecord{}
		}
		reply, err := panelControl(ctx, dir, panelRequest{Op: "status"})
		if err == nil && reply.Record != nil && validPanelRecord(*reply.Record) {
			return panelRunning, *reply.Record
		}
		if attempt == 2 || ctx.Err() != nil {
			return panelNotAnswering, panelRecord{}
		}
		time.Sleep(60 * time.Millisecond)
	}
}

// panelLive returns the running server's account of itself, if one answers.
func panelLive(ctx context.Context, dir string) (panelRecord, bool) {
	presence, rec := panelFind(ctx, dir)
	return rec, presence == panelRunning
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
	changes    bool
}

// panelServerEnvironment is everything the background server is given: where
// the user's files are, the startup BB identity, a fixed search path, and the locale and time zone.
// Nothing else the caller had in its environment reaches it.
func panelServerEnvironment() []string {
	env := []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin"}
	for _, name := range []string{"BB_THREAD_ID", "HOME", "USER", "LOGNAME", "TMPDIR", "TZ", "LANG", "LC_ALL", "LC_CTYPE", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR"} {
		if v, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+v)
		}
	}
	return env
}

const panelNotAnsweringText = "the panel is running but not answering. Try again in a moment; keepgoing panel status says whether it has recovered"

// panelEnsure returns a running server, starting one when none is running.
// The caller holds the panel-open lock.
func panelEnsure(ctx context.Context, o panelOptions) (panelRecord, bool, error) {
	// A server that holds the lock and does not answer may be starting or
	// stopping: give it the time a start is given before saying so.
	deadline := time.Now().Add(panelStartWait)
	for {
		presence, rec := panelFind(ctx, o.dir)
		if presence == panelRunning {
			return rec, false, nil
		}
		if presence == panelAbsent {
			break
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return panelRecord{}, false, errors.New(panelNotAnsweringText)
		}
		time.Sleep(100 * time.Millisecond)
	}
	// No server holds the lock, so whatever record, socket or launch page is
	// here belongs to nothing. They are removed, never left for a person.
	panelRemoveLeftovers(o.dir)
	exe, err := os.Executable()
	if err != nil {
		return panelRecord{}, false, errors.New("cannot find this executable to start the panel")
	}
	// The registry directory is passed resolved, so the server does not
	// depend on the caller's environment or working directory to find it.
	registryDir, err := registryDirectory(o.registry)
	if err != nil {
		return panelRecord{}, false, errors.New("registry directory unavailable")
	}
	args := []string{"__panel-server", "--panel-directory", o.dir, "--directory", registryDir, "--idle", o.idle.String(), "--refresh", o.refresh.String(), "--stale-after", o.staleAfter.String()}
	if o.offline {
		args = append(args, "--offline")
	}
	if o.changes {
		args = append(args, "--allow-changes")
	}
	cmd := exec.Command(exe, args...)
	if err := detachDaemon(cmd); err != nil {
		return panelRecord{}, false, errors.New("cannot start a background process on this platform")
	}
	// A fixed small environment, the private directory as its working
	// directory, the null device for stdin, stdout and stderr (it keeps no
	// log), and no other descriptor of this process.
	cmd.Env = panelServerEnvironment()
	cmd.Dir = o.dir
	panelCloseInheritedOnExec()
	if err := cmd.Start(); err != nil {
		return panelRecord{}, false, errors.New("cannot start the panel")
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	deadline = time.Now().Add(panelStartWait)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		if presence, rec := panelFind(ctx, o.dir); presence == panelRunning {
			// Another server may have won the lock at the same moment, for
			// example one started by hand: then that one is reused.
			return rec, rec.PID == cmd.Process.Pid, nil
		}
		select {
		case <-exited:
			if rec, ok := panelLive(ctx, o.dir); ok {
				return rec, false, nil
			}
			return panelRecord{}, false, errors.New("the panel exited while starting")
		case <-time.After(50 * time.Millisecond):
		}
	}
	_ = cmd.Process.Kill()
	return panelRecord{}, false, errors.New("the panel did not answer within 10 seconds")
}

// panelRemoveLeftovers deletes the runtime files of a server that is gone.
// Called only when nothing holds the server's lock.
func panelRemoveLeftovers(dir string) {
	_ = os.Remove(filepath.Join(dir, panelRecordName))
	_ = os.Remove(filepath.Join(dir, panelSocketName))
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if panelLaunchPattern.MatchString(e.Name()) {
				_ = os.Remove(filepath.Join(dir, e.Name()))
			}
		}
	}
}

// panelRequestOpening asks the server for an opening and checks the answer before
// anything is done with it: the socket is trusted to be the server, and its
// reply is still only ever a loopback opening address for the server's own
// port, or the name of a launch page inside the private directory.
func panelRequestOpening(ctx context.Context, dir string, rec panelRecord, key string, asFile bool) (string, error) {
	reply, err := panelControl(ctx, dir, panelRequest{Op: "open", Key: key, File: asFile})
	if err != nil {
		return "", err
	}
	bad := errors.New("the panel's answer was not an opening for this page")
	if !asFile {
		code, ok := strings.CutPrefix(reply.Address, "http://127.0.0.1:"+strconv.Itoa(rec.Port)+"/open/")
		if !ok || !panelCodePattern.MatchString(code) || reply.File != "" {
			return "", bad
		}
		return reply.Address, nil
	}
	if !panelLaunchPattern.MatchString(reply.File) || reply.Address != "" {
		return "", bad
	}
	path := filepath.Join(dir, reply.File)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0) {
		return "", bad
	}
	return path, nil
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

const panelUsage = `Usage: keepgoing panel [--loop NAME] [--no-open] [--idle 30m] [--allow-changes] [--directory private-registry]
       keepgoing panel status [--json]
       keepgoing panel stop
       keepgoing panel list [--json] [--directory private-registry]

Open the keepgoing panel, the page about every registered loop, in your
browser. The panel's background process, for this computer only, is started when
none is running and stops by itself when unused. It is read-only unless it was started
with --allow-changes, which lets the page pause, stop and resume loops and replace a loop’s own build, each
after a review and a confirmation. Supervision is not affected by starting,
stopping or closing it.`

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
	idle := fs.Duration("idle", panelDefaultIdle, "stop the panel's background process after this long without a request (1m to 24h)")
	directory := fs.String("directory", "", "private registry directory")
	panelDir := fs.String("panel-directory", "", "private directory for the panel's runtime record and socket")
	refresh := fs.Duration("refresh", dockDefaultRefresh, "how often the page reads again (5s to 10m)")
	staleAfter := fs.Duration("stale-after", defaultStaleAfter, "age at which a check or child listing is shown as stale")
	offline := fs.Bool("offline", false, "do not ask each loop's local daemon for its next check")
	changes := fs.Bool("allow-changes", false, "let the page pause, stop and resume loops and replace a loop’s own build (a panel this command starts; a running one keeps its own mode)")
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
	if *panelDir == "" {
		if legacy, stopped := panelRetireLegacy(); stopped {
			fmt.Fprintf(e.stdout, "Stopped a panel an earlier build started at %s.\n", legacy)
		}
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
	rec, started, err := panelEnsure(e.ctx, panelOptions{dir: dir, registry: *directory, idle: *idle, refresh: *refresh, staleAfter: *staleAfter, offline: *offline, changes: *changes})
	_ = lk.Release()
	if err != nil {
		e.errf("panel: %v", err)
		return exitFailed
	}
	if !started {
		// A server started before the name in use changed (see userdir)
		// serves the default registry under the other name: it is the
		// running panel all the same, not one for another registry.
		if want, _ := registryDirectory(*directory); rec.Registry != want && !panelSameDefaultRegistry(rec.Registry, want) {
			e.errf("panel: a panel for another registry is already running; run keepgoing panel stop first")
			return exitFailed
		}
		// A running server never changes mode, in either direction.
		switch {
		case *changes && !rec.Changes:
			e.errf("panel: a read-only panel is already running; run keepgoing panel stop, then keepgoing panel --allow-changes")
			return exitFailed
		case !*changes && rec.Changes:
			e.errf("panel: the running panel allows changes (it was started with --allow-changes); run keepgoing panel stop first to open it read-only, or keepgoing panel --allow-changes to open it as it is")
			return exitFailed
		}
	}
	// The browser is found before an opening is made, so that when there is
	// none nothing is written and nothing is launched.
	var browser func(page string) []string
	why := ""
	if !*noOpen {
		if browser, err = panelBrowser(e.ctx); err != nil {
			why = fmt.Sprintf("No browser was started: %v. ", err)
		}
	}
	opening, err := panelRequestOpening(e.ctx, dir, rec, key, browser != nil)
	if err != nil {
		e.errf("panel: %v", err)
		return exitFailed
	}
	mode := ""
	if rec.Changes {
		mode = " Changes are allowed from the page: pause, stop, resume and replace a loop’s own build, each after a review. " + panelExposure + " keepgoing panel stop turns them off."
	}
	if browser == nil {
		fmt.Fprintf(e.stdout, why+"Open this address in a browser on this computer within 30 seconds. It works once:\n\n  %s\n\nAnyone who can read this terminal can use it during those 30 seconds. Run keepgoing panel again for a new one. keepgoing panel stop ends the panel's background process; supervision is not affected.%s\n", opening, mode)
		return exitOK
	}
	// The browser is given the path of a private page that goes to the
	// opening address. The server deletes that page when the opening is used
	// or after 30 seconds. Nothing that can open the panel is in any
	// program's arguments, and the page is opened by the application that
	// handles web links, never by whatever opens .html files.
	argv := browser(opening)
	cmd := exec.Command(argv[0], argv[1:]...)
	if err := cmd.Start(); err != nil {
		e.errf("panel: could not start a browser. Run keepgoing panel --no-open and open the address it prints.")
		return exitFailed
	}
	go func() { _ = cmd.Wait() }()
	what := "Opened the keepgoing panel in your browser."
	if started {
		what = "Started the keepgoing panel and opened it in your browser."
	}
	fmt.Fprintf(e.stdout, "%s It stops by itself %s after the last page stops reading from it; supervision is not affected.%s\n", what, time.Duration(rec.IdleSeconds)*time.Second, mode)
	return exitOK
}

type panelStatus struct {
	SchemaVersion int `json:"schemaVersion"`
	// Running: a server holds the lock. Answering: it also answered.
	Running              bool       `json:"running"`
	Answering            bool       `json:"answering"`
	StartedAt            *time.Time `json:"startedAt,omitempty"`
	Port                 int        `json:"port,omitempty"`
	IdleSeconds          int        `json:"idleSeconds,omitempty"`
	IdleRemainingSeconds *int       `json:"idleRemainingSeconds,omitempty"`
	// InUse: a page read from it within the last minute. A page left open
	// and visible keeps reading, so the server does not go idle under it.
	InUse bool `json:"inUse"`
	// ChangesAllowed: it was started with --allow-changes, and has allowed
	// them for ChangesAllowedSeconds (since it started).
	ChangesAllowed        bool `json:"changesAllowed"`
	ChangesAllowedSeconds *int `json:"changesAllowedSeconds,omitempty"`
	// EarlierBuildPanel is the old location when a server an earlier build
	// started is running there. Reported only; keepgoing panel stop ends it.
	EarlierBuildPanel string `json:"earlierBuildPanel,omitempty"`
	// Exposure is what the page's footer says about allowing changes, for a
	// program that offers to start the panel with them.
	Exposure string `json:"exposure"`
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
	st := panelStatus{SchemaVersion: 1, Exposure: panelExposure}
	if *panelDir == "" {
		if legacy, running := panelLegacyRunning(); running {
			st.EarlierBuildPanel = legacy
		}
	}
	if panelServerHeld(dir) {
		st.Running = true
		if reply, err := panelControl(e.ctx, dir, panelRequest{Op: "status"}); err == nil && reply.Record != nil && validPanelRecord(*reply.Record) {
			started, remaining := reply.Record.StartedAt.UTC(), reply.IdleRemainingSeconds
			st.Answering, st.StartedAt, st.Port, st.IdleSeconds, st.IdleRemainingSeconds = true, &started, reply.Record.Port, reply.Record.IdleSeconds, &remaining
			st.InUse = reply.SinceUseSeconds < 60
			if reply.Record.Changes {
				allowed := max(0, int(time.Since(started)/time.Second))
				st.ChangesAllowed, st.ChangesAllowedSeconds = true, &allowed
			}
		}
	}
	if *asJSON {
		_ = json.NewEncoder(e.stdout).Encode(st)
		return exitOK
	}
	if st.EarlierBuildPanel != "" {
		fmt.Fprintf(e.stdout, panelLegacyNotice+"\n", st.EarlierBuildPanel)
	}
	switch {
	case !st.Running:
		fmt.Fprintln(e.stdout, "The keepgoing panel is not running. Run keepgoing panel to start and open it.")
	case !st.Answering:
		fmt.Fprintln(e.stdout, "The keepgoing panel is running but not answering. It may be starting or stopping; if this does not change, it is stuck, and ending its process is the way out. Supervision is not affected.")
	default:
		fmt.Fprintf(e.stdout, "The keepgoing panel is running on this computer only, port %d, since %s.\n", st.Port, st.StartedAt.Local().Format("2006-01-02 15:04 MST"))
		idle := time.Duration(st.IdleSeconds) * time.Second
		if st.InUse {
			fmt.Fprintf(e.stdout, "In use: a page read from it in the last minute. It stops by itself %s after the last page stops reading; a page left open and visible keeps it running.\n", idle)
		} else {
			fmt.Fprintf(e.stdout, "It stops by itself after %s without a page reading from it: in about %s if nothing does.\n", idle, (time.Duration(*st.IdleRemainingSeconds) * time.Second).Round(time.Minute))
		}
		if st.ChangesAllowed {
			fmt.Fprintf(e.stdout, "Changes allowed for %s: the page can pause, stop and resume loops, and replace a loop's own build, each after a review. %s keepgoing panel stop turns them off.\n", dockSpan(time.Duration(*st.ChangesAllowedSeconds)*time.Second), panelExposure)
		} else {
			fmt.Fprintln(e.stdout, "Read-only: started without --allow-changes.")
		}
		fmt.Fprintln(e.stdout, "keepgoing panel opens it; keepgoing panel stop ends it. Supervision is not affected either way.")
	}
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
	if *panelDir == "" {
		if legacy, stopped := panelRetireLegacy(); stopped {
			fmt.Fprintf(e.stdout, "Stopped a panel an earlier build started at %s.\n", legacy)
		}
	}
	// A second server can be running under the tool's other directory name:
	// one started before the name in use changed, or by a build from before
	// the rename. Stop means no panel is left running, so it is stopped too.
	other := ""
	if *panelDir == "" {
		other = panelServerUnderOtherName(dir)
	}
	wasRunning, err := panelStopAt(e, dir)
	if err != nil {
		e.errf("panel stop: %v", err)
		return exitFailed
	}
	if other != "" {
		also, err := panelStopAt(e, other)
		if err != nil {
			e.errf("panel stop: the panel running at %s: %v", other, err)
			return exitFailed
		}
		if also {
			fmt.Fprintf(e.stdout, "Stopped a second panel, running at %s.\n", other)
		}
		wasRunning = wasRunning || also
	}
	if !wasRunning {
		fmt.Fprintln(e.stdout, "The keepgoing panel is not running.")
		return exitOK
	}
	fmt.Fprintln(e.stdout, "Stopped the keepgoing panel. Supervision is not affected.")
	return exitOK
}

// panelStopAt stops the panel server in dir and waits for it to finish. It
// reports whether one was running.
func panelStopAt(e env, dir string) (bool, error) {
	lk, err := acquirePanelOpenLock(e.ctx, dir)
	if err != nil {
		return false, err
	}
	defer lk.Release()
	presence, _ := panelFind(e.ctx, dir)
	if presence == panelAbsent {
		// Nothing is running; files a crash left behind are cleared.
		panelRemoveLeftovers(dir)
		return false, nil
	}
	if _, err := panelControl(e.ctx, dir, panelRequest{Op: "stop"}); err != nil {
		return true, errors.New(panelNotAnsweringText)
	}
	// The server holds its lock until its record and socket are gone, so the
	// lock being free proves it has finished, not only stopped answering.
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if !panelServerHeld(dir) {
			return true, nil
		}
	}
	return true, errors.New("the panel was asked to stop and has not finished")
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
	// CanAll is which changes can be made to every loop at once
	// (client review|apply --all): pause and stop, while any loop can be.
	CanAll []string `json:"canAll"`
	// EarlierBuildPanel: as in panel status.
	EarlierBuildPanel string `json:"earlierBuildPanel,omitempty"`
}

type panelListLoop struct {
	// Key is the loop's key on the page; panel --loop accepts it.
	Key      string `json:"key"`
	Name     string `json:"name"`
	State    string `json:"state"`
	Phrase   string `json:"phrase"`
	NeedsYou bool   `json:"needsYou"`
	// NoProgress is the no-progress flag's sentence when it is raised, in
	// the page's words; empty otherwise. It is never a need.
	NoProgress string `json:"noProgress,omitempty"`
	// Can is which changes fit the loop's control flags as read, as the
	// page offers them: pause, stop and resume. Empty when the flags could
	// not be read. client review and client apply take them.
	Can []string `json:"can"`
	// OverdueSince is set when the loop needs a person because its checks
	// have stopped: the time of its last check. A new value is a new gap, so
	// a client can tell people once per gap.
	OverdueSince *time.Time `json:"overdueSince,omitempty"`
}

func buildPanelList(r dockReport) panelList {
	out := panelList{SchemaVersion: 1, ReadAt: r.ReadAt, Registry: r.Registry.Availability, Complete: r.Complete, AllClear: r.AllClear,
		NeedsYou: len(r.NeedsYou), Unread: r.Unread, Omitted: r.Registry.Omitted, Loops: []panelListLoop{}}
	needs := map[string]bool{}
	overdue := map[string]*time.Time{}
	for _, n := range r.NeedsYou {
		needs[n.Key] = true
		if n.Kind == "overdue" && n.Since != nil {
			overdue[n.Key] = n.Since
		}
	}
	out.CanAll = []string{}
	for _, l := range r.Loops {
		for _, a := range []string{"pause", "stop"} {
			if slices.Contains(l.Can, a) && !slices.Contains(out.CanAll, a) {
				out.CanAll = append(out.CanAll, a)
			}
		}
		row := panelListLoop{Key: l.Key, Name: l.Name, State: l.State, Phrase: l.Phrase, NeedsYou: needs[l.Key], Can: l.Can, OverdueSince: overdue[l.Key]}
		if np := l.NoProgress; np != nil && np.Availability == "flagged" {
			row.NoProgress = np.Text
		}
		out.Loops = append(out.Loops, row)
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
	client := fs.String("client", "", "the calling program's NAME@VERSION (bb-plugin, macos-menu), recorded for the panel's Versions view in a panel directory that already exists")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *staleAfter <= 0 {
		e.errf("panel list: invalid arguments; usage: keepgoing panel list [--json] [--directory private-registry] [--client NAME@VERSION]")
		return exitUsage
	}
	// Recorded after the answer, and never changing it.
	defer noteClient(*client, "")
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
	if legacy, running := panelLegacyRunning(); running {
		list.EarlierBuildPanel = legacy
	}
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
		if list.EarlierBuildPanel != "" {
			fmt.Fprintf(w, panelLegacyNotice+"\n", list.EarlierBuildPanel)
		}
		_ = w.Flush()
	}
	if list.Registry != "available" {
		return exitFailed
	}
	return exitOK
}
