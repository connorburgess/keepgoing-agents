package scenario

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/connorburgess/keepgoing-agents/internal/adapter/runner"
	"github.com/connorburgess/keepgoing-agents/internal/report"
)

// keepgoingBin and standinBin are built once for the whole suite.
var keepgoingBin, standinBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "keepgoing-scenario-build-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	code := func() int {
		defer os.RemoveAll(dir)
		keepgoingBin = os.Getenv("KEEPGOING_TEST_BINARY")
		if keepgoingBin == "" {
			keepgoingBin = filepath.Join(dir, "keepgoing"+exe)
			if err := goBuild(keepgoingBin, "../../cmd/keepgoing"); err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 2
			}
		} else if !filepath.IsAbs(keepgoingBin) {
			fmt.Fprintln(os.Stderr, "KEEPGOING_TEST_BINARY must be an absolute path")
			return 2
		}
		standinBin = filepath.Join(dir, "standin"+exe)
		if err := goBuild(standinBin, "./standin"); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		return m.Run()
	}()
	os.Exit(code)
}

var exe = map[bool]string{true: ".exe"}[runtime.GOOS == "windows"]

func goBuild(out, pkg string) error {
	gobin, err := exec.LookPath("go")
	if err != nil {
		gobin = filepath.Join(runtime.GOROOT(), "bin", "go"+exe)
	}
	cmd := exec.Command(gobin, "build", "-o", out, pkg)
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("go build %s: %v\n%s", pkg, err, b)
	}
	return nil
}

// Thread IDs in every scenario. Fictional.
const (
	primaryThread = "thr_example_primary"
	childThread   = "thr_example_child"
	scenarioTask  = "Finish the fictional example task and report what changed."
	webhookKey    = "fictional-scenario-signing-key-0123456789"
)

func webhookSecret() string {
	return "whsec_" + base64.StdEncoding.EncodeToString([]byte(webhookKey))
}

// World is one scenario: a private directory holding a project, a keepgoing
// setup, stand-in agents, a fake connectivity target, a fake webhook
// receiver and an injected clock. Every keepgoing command runs the built
// binary as a separate process with an environment built from nothing, so no
// real account, agent, network or user state is reachable.
type World struct {
	t       *testing.T
	Name    string
	Root    string
	Project string
	Config  string
	Now     time.Time
	Net     *Connectivity
	Hooks   *Receiver
	Threads map[string]*Thread
	// Headless is the CLI of a headless loop, or "" for a BB loop.
	Headless string

	bin, world, home string
	env              []string

	sendsSeen, callsSeen, hooksSeen, probesSeen int
	sendsTotal                                  int
	Journal                                     []*Step
}

// NewWorld builds a scenario world starting at the given time.
func NewWorld(t *testing.T, start time.Time) *World {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := &World{t: t, Name: t.Name(), Root: root, Now: start.UTC(), Threads: map[string]*Thread{},
		bin: filepath.Join(root, "bin"), world: filepath.Join(root, "world"), home: filepath.Join(root, "home"),
		Project: filepath.Join(root, "project")}
	for _, d := range []string{w.bin, w.world, w.home, w.Project, filepath.Join(root, "tmp")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	w.install("bb")
	w.install("claude")
	w.install("codex")
	w.linkGit()
	w.Net = newConnectivity(t)
	w.Net.world = w.world
	w.Hooks = newReceiver(t)
	w.env = []string{
		"PATH=" + w.bin,
		"HOME=" + w.home,
		"USERPROFILE=" + w.home,
		"XDG_CONFIG_HOME=" + filepath.Join(w.home, ".config"),
		"XDG_STATE_HOME=" + filepath.Join(w.home, ".local", "state"),
		"TMPDIR=" + filepath.Join(root, "tmp"),
		"LANG=C",
		"SCENARIO_WORLD=" + w.world,
		"KEEPGOING_WEBHOOK_URL=" + w.Hooks.URL,
		"KEEPGOING_WEBHOOK_SECRET=" + webhookSecret(),
		// Any connectivity check keepgoing makes goes to the fake target.
		"KEEPGOING_NET_PROBE_URL=" + w.Net.URL,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Example", "GIT_AUTHOR_EMAIL=example@example.com",
		"GIT_COMMITTER_NAME=Example", "GIT_COMMITTER_EMAIL=example@example.com",
	}
	if runtime.GOOS == "windows" {
		w.env = append(w.env, "SystemRoot="+os.Getenv("SystemRoot"))
	}
	w.git("init", "-q")
	w.Commit("test: initialize fictional project")
	t.Cleanup(func() {
		if t.Failed() {
			t.Log(w.Transcript())
		}
	})
	return w
}

func (w *World) install(name string) {
	w.t.Helper()
	dst := filepath.Join(w.bin, name+exe)
	if err := os.Link(standinBin, dst); err != nil {
		b, err := os.ReadFile(standinBin)
		if err != nil {
			w.t.Fatal(err)
		}
		if err := os.WriteFile(dst, b, 0o700); err != nil {
			w.t.Fatal(err)
		}
	}
}

// linkGit exposes only git from the host, so a real agent CLI that happens to
// sit beside it on the host PATH can never be found.
func (w *World) linkGit() {
	w.t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		w.t.Fatal("scenario tests need git: ", err)
	}
	if err := os.Symlink(git, filepath.Join(w.bin, "git"+exe)); err != nil {
		w.t.Fatal(err)
	}
}

func (w *World) git(args ...string) {
	w.t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "commit.gpgsign=false", "-c", "core.hooksPath=" + filepath.Join(w.Root, "no-hooks")}, args...)...)
	cmd.Dir = w.Project
	cmd.Env = w.env
	if out, err := cmd.CombinedOutput(); err != nil {
		w.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// Commit records progress in the project, as an agent's work would.
func (w *World) Commit(msg string) {
	w.t.Helper()
	w.git("commit", "--allow-empty", "-q", "-m", msg)
}

// Advance moves the injected clock.
func (w *World) Advance(d time.Duration) { w.Now = w.Now.Add(d) }

// At sets the injected clock; time never moves backwards.
func (w *World) At(t time.Time) {
	w.t.Helper()
	if t.Before(w.Now) {
		w.t.Fatalf("clock moved backwards: %s -> %s", w.Now, t)
	}
	w.Now = t.UTC()
}

func (w *World) stamp() string { return w.Now.Format(time.RFC3339) }

// Result is one process run of the built binary.
type Result struct {
	Args           []string
	Code           int
	Stdout, Stderr string
}

// Run runs the built keepgoing binary with the given arguments.
func (w *World) Run(args ...string) Result { return w.RunAs("", args...) }

// RunAs runs keepgoing as if called from the given BB thread.
func (w *World) RunAs(thread string, args ...string) Result {
	w.t.Helper()
	cmd := exec.Command(keepgoingBin, args...)
	cmd.Dir = w.Root
	cmd.Env = append(append([]string{}, w.env...), "BB_THREAD_ID="+thread)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		code = exit.ExitCode()
	case err != nil:
		w.t.Fatalf("keepgoing %v: %v", args, err)
	}
	return Result{Args: args, Code: code, Stdout: out.String(), Stderr: errb.String()}
}

// Must runs keepgoing and fails the scenario on a non-zero exit.
func (w *World) Must(args ...string) Result {
	w.t.Helper()
	r := w.Run(args...)
	if r.Code != 0 {
		w.t.Fatalf("keepgoing %v: exit %d\n%s%s", args, r.Code, r.Stdout, r.Stderr)
	}
	return r
}

// Init creates a BB loop with `keepgoing init`, exactly as a user would, then
// subscribes the fake receiver to the given report events.
func (w *World) Init(events ...string) {
	w.t.Helper()
	dir := filepath.Join(w.Root, "setup")
	w.Must("init", "--non-interactive", "--name", "example-loop", "--agent", "bb", "--thread", primaryThread,
		"--provider", "claude-code", "--project", w.Project, "--directory", dir, "--task", scenarioTask)
	w.Config = filepath.Join(dir, "keepgoing.yaml")
	w.Thread(primaryThread)
	if len(events) > 0 {
		w.EditConfig(func(loop map[string]any) {
			loop["report"] = map[string]any{"events": events, "timeout": "5s"}
		})
	}
}

// EditConfig rewrites the loop section of the configuration.
func (w *World) EditConfig(edit func(loop map[string]any)) {
	w.t.Helper()
	b, err := os.ReadFile(w.Config)
	if err != nil {
		w.t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(b, &doc); err != nil {
		w.t.Fatal(err)
	}
	loop, _ := doc["loop"].(map[string]any)
	edit(loop)
	if b, err = yaml.Marshal(doc); err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(w.Config, b, 0o600); err != nil {
		w.t.Fatal(err)
	}
}

// BinPath is the path of a stand-in executable, for configuration.
func (w *World) BinPath(name string) string { return filepath.Join(w.bin, name+exe) }

// Fallbacks writes fallbacks.yaml beside the configuration and points the
// loop at it. %[1]s in the text is replaced by the stand-in directory.
func (w *World) Fallbacks(text string, loop func(map[string]any)) {
	w.t.Helper()
	path := filepath.Join(filepath.Dir(w.Config), "fallbacks.yaml")
	if err := os.WriteFile(path, []byte(fmt.Sprintf(text, w.bin)), 0o600); err != nil {
		w.t.Fatal(err)
	}
	w.EditConfig(func(l map[string]any) {
		l["fallbacks"] = "fallbacks.yaml"
		if loop != nil {
			loop(l)
		}
	})
}

// SpawnOutcomes queues what the next `bb thread spawn` calls do: ok, lost,
// unlisted or refuse (see the stand-in).
func (w *World) SpawnOutcomes(modes ...string) {
	w.t.Helper()
	if err := os.WriteFile(filepath.Join(w.world, "spawn.modes"), []byte(strings.Join(modes, "\n")), 0o600); err != nil {
		w.t.Fatal(err)
	}
}

// ListUnderTitle makes a created thread appear in the place listing, as when
// someone restores the title of a renamed thread.
func (w *World) ListUnderTitle(id, title string) {
	w.t.Helper()
	var listed []map[string]any
	b, err := os.ReadFile(filepath.Join(w.world, "place.list"))
	if err == nil {
		json.Unmarshal(b, &listed)
	}
	w.writeJSON("place.list", append(listed, map[string]any{"id": id, "title": title}))
}

// ProbeLimited makes claude probes of a model report a rate limit (or pass
// again when limited is false).
func (w *World) ProbeLimited(model string, limited bool) {
	w.t.Helper()
	path := filepath.Join(w.world, "probe-limited."+model)
	if !limited {
		os.Remove(path)
		return
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		w.t.Fatal(err)
	}
}

// Control runs a control command (enable, pause, resume, stop, complete…).
func (w *World) Control(cmd string, extra ...string) *Step {
	w.t.Helper()
	r := w.Run(append([]string{cmd, "-c", w.Config}, extra...)...)
	return w.record(cmd, r, nil)
}

// AgentRuns runs a keepgoing command from inside the agent's thread, as the
// task prompt tells the agent to (for example `complete`).
func (w *World) AgentRuns(cmd string, extra ...string) *Step {
	w.t.Helper()
	r := w.RunAs(primaryThread, append([]string{cmd, "-c", w.Config}, extra...)...)
	return w.record("agent: "+cmd, r, nil)
}

// Tick runs one `keepgoing tick --now` at the world's clock and records every
// facet of the step.
func (w *World) Tick() *Step {
	w.t.Helper()
	w.settle()
	r := w.Run("tick", "-c", w.Config, "--now", w.stamp())
	var line map[string]any
	if r.Code == 0 {
		if err := json.Unmarshal([]byte(strings.TrimSpace(r.Stdout)), &line); err != nil {
			w.t.Fatalf("tick output is not one JSON line: %v\n%s", err, r.Stdout)
		}
	}
	return w.record("tick", r, line)
}

// settle waits until a detached headless turn has ended, so a check sees
// its result rather than the moment it was started. It waits on the turn's
// own record, not on a timer; the bound only catches a turn that hangs.
func (w *World) settle() {
	w.t.Helper()
	if w.Headless == "" {
		return
	}
	dir := runner.Dir(filepath.Join(filepath.Dir(w.Config), "state", "targets", "primary"))
	deadline := time.Now().Add(30 * time.Second)
	for {
		st, err := dir.Status()
		if err == nil && st.Phase != runner.Running {
			return
		}
		if time.Now().After(deadline) {
			w.t.Fatalf("the headless turn did not end: %v %+v", err, st)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// InitHeadless creates a headless Claude Code or Codex loop with `keepgoing
// init`, using the stand-in CLI found on PATH.
func (w *World) InitHeadless(cli string, events ...string) {
	w.t.Helper()
	dir := filepath.Join(w.Root, "setup")
	w.Must("init", "--non-interactive", "--name", "example-loop", "--agent", cli,
		"--project", w.Project, "--directory", dir, "--task", scenarioTask)
	w.Config = filepath.Join(dir, "keepgoing.yaml")
	w.Headless = cli
	if len(events) > 0 {
		w.EditConfig(func(loop map[string]any) {
			loop["report"] = map[string]any{"events": events, "timeout": "5s"}
		})
	}
}

// HeadlessMode overrides what the next headless turns do (see the
// stand-in); "" follows the network again.
func (w *World) HeadlessMode(mode string) {
	w.t.Helper()
	if err := os.WriteFile(filepath.Join(w.world, "headless.mode"), []byte(mode), 0o600); err != nil {
		w.t.Fatal(err)
	}
}

// Step is everything observable after one command: what was sent to the
// agents, what holds exist, what was reported, and what status, timeline and
// runs say.
type Step struct {
	Label     string
	At        time.Time
	Result    Result
	Tick      map[string]any
	Sent      []Send
	Calls     [][]string
	Probes    [][]string
	Reports   []Report
	NetChecks int
	Status    Status
	Timeline  Timeline
	Runs      Runs
}

// Send is one message or retry an agent received.
type Send struct {
	Kind    string `json:"kind"` // tell or retry
	Thread  string `json:"thread"`
	Mode    string `json:"mode"`
	Title   string `json:"title,omitempty"`
	Message string `json:"message"`
}

// Holds lists the holds in force, from status.
func (s *Step) Holds() []string {
	c := s.Status.Control
	var h []string
	for name, on := range map[string]bool{"awaitingUser": c.AwaitingUser, "paused": c.Paused, "stop": c.Stop, "completed": c.Completed} {
		if on {
			h = append(h, name)
		}
	}
	if !c.Enabled {
		h = append(h, "disabled")
	}
	return h
}

func (s *Step) action() string { a, _ := s.Tick["action"].(string); return a }
func (s *Step) reason() string { r, _ := s.Tick["reason"].(string); return r }

// Status is the part of `status --json` the scenarios read.
type Status struct {
	ControlState string `json:"controlState"`
	Target       string `json:"target"`
	TargetAgent  struct {
		BBThread string `json:"bbThread"`
		Model    string `json:"model"`
	} `json:"targetAgent"`
	NewThreads struct {
		Created int  `json:"created"`
		Pending bool `json:"pending"`
	} `json:"newThreads"`
	Control struct {
		Enabled, Paused, Stop, AwaitingUser, Completed bool
		Note                                           string
	} `json:"control"`
	State struct {
		LastCheck struct {
			At      time.Time `json:"at"`
			Action  string    `json:"action"`
			Reason  string    `json:"reason"`
			Outcome string    `json:"outcome"`
		} `json:"lastCheck"`
	} `json:"state"`
	Raw string `json:"-"`
}

// Timeline is the part of `timeline --json` the scenarios read.
type Timeline struct {
	Control struct {
		State string `json:"state"`
	} `json:"control"`
	Summary struct {
		LatestCheck *struct {
			Sequence    int       `json:"sequence"`
			At          time.Time `json:"at"`
			Description string    `json:"description"`
		} `json:"latestCheck"`
		RetainedChecks int `json:"retainedChecks"`
		HeldChecks     int `json:"heldChecks"`
	} `json:"summary"`
	Raw string `json:"-"`
}

// Runs is the part of `runs --json` the scenarios read.
type Runs struct {
	Loops []struct {
		Control struct {
			Availability string `json:"availability"`
			State        string `json:"state"`
		} `json:"control"`
		Current *struct {
			ID       string `json:"id"`
			State    string `json:"state"`
			Attempts struct {
				Total  int       `json:"total"`
				Latest []Attempt `json:"latest"`
			} `json:"attempts"`
		} `json:"current"`
		Recent []struct {
			State    string `json:"state"`
			Attempts struct {
				Total int `json:"total"`
			} `json:"attempts"`
		} `json:"recent"`
	} `json:"loops"`
	Raw string `json:"-"`
}

// Attempt is one send attempt as runs reports it.
type Attempt struct {
	Target  string `json:"target"`
	Action  string `json:"action"`
	Outcome string `json:"outcome"`
}

// ControlState is the loop's control state as runs reports it. A loop with
// no control file is disabled, which runs reports as not recorded.
func (r Runs) ControlState() string {
	if len(r.Loops) != 1 {
		return fmt.Sprintf("%d loops", len(r.Loops))
	}
	if c := r.Loops[0].Control; c.Availability == "not_recorded" {
		return "disabled"
	} else {
		return c.State
	}
}

// Attempts is the number of send attempts across every run listed.
func (r Runs) Attempts() int {
	n := 0
	for _, l := range r.Loops {
		if l.Current != nil {
			n += l.Current.Attempts.Total
		}
		for _, x := range l.Recent {
			n += x.Attempts.Total
		}
	}
	return n
}

func (w *World) record(label string, r Result, line map[string]any) *Step {
	w.t.Helper()
	// A headless turn started by this command runs detached; let it end so
	// this step records what it sent and the next one sees its result.
	w.settle()
	s := &Step{Label: label, At: w.Now, Result: r, Tick: line}
	var sends []Send
	readLines(w.t, filepath.Join(w.world, "sends.jsonl"), &sends)
	s.Sent = sends[w.sendsSeen:]
	w.sendsSeen = len(sends)
	w.sendsTotal += len(s.Sent)
	var calls [][]string
	readLines(w.t, filepath.Join(w.world, "calls.jsonl"), &calls)
	s.Calls = calls[w.callsSeen:]
	w.callsSeen = len(calls)
	var probes [][]string
	readLines(w.t, filepath.Join(w.world, "probes.jsonl"), &probes)
	s.Probes = probes[w.probesSeen:]
	w.probesSeen = len(probes)
	all := w.Hooks.Events()
	s.Reports = all[w.hooksSeen:]
	w.hooksSeen = len(all)
	s.NetChecks = w.Net.take()
	w.read(s)
	w.Journal = append(w.Journal, s)
	// The stand-in agents respond to what they were sent, so the next step
	// observes the consequence.
	for _, m := range s.Sent {
		if m.Kind == "spawn" {
			// A created thread is already running its first turn.
			w.adopt(m.Thread)
			continue
		}
		if th := w.Threads[m.Thread]; th != nil {
			th.received(m)
		}
	}
	w.invariants(s)
	return s
}

func (w *World) read(s *Step) {
	w.t.Helper()
	st := w.Run("status", "-c", w.Config, "--json", "--now", w.stamp())
	if st.Code != 0 || json.Unmarshal([]byte(st.Stdout), &s.Status) != nil {
		w.t.Fatalf("status --json: exit %d\n%s%s", st.Code, st.Stdout, st.Stderr)
	}
	s.Status.Raw = st.Stdout
	// Timeline and runs read the wall clock; a long stale-after keeps their
	// answers independent of how far the injected clock is from it.
	tl := w.Run("timeline", "-c", w.Config, "--json", "--stale-after", "87600h")
	if tl.Code != 0 || json.Unmarshal([]byte(tl.Stdout), &s.Timeline) != nil {
		w.t.Fatalf("timeline --json: exit %d\n%s%s", tl.Code, tl.Stdout, tl.Stderr)
	}
	s.Timeline.Raw = tl.Stdout
	rn := w.Run("runs", "-c", w.Config, "--json", "--stale-after", "87600h")
	if rn.Code != 0 || json.Unmarshal([]byte(rn.Stdout), &s.Runs) != nil {
		w.t.Fatalf("runs --json: exit %d\n%s%s", rn.Code, rn.Stdout, rn.Stderr)
	}
	s.Runs.Raw = rn.Stdout
}

// invariants hold after every step of every scenario.
func (w *World) invariants(s *Step) {
	w.t.Helper()
	if s.Label == "tick" && len(w.Journal) > 1 {
		// Holds win: a check that starts on a hold sends nothing.
		before := w.Journal[len(w.Journal)-2].Status.Control
		if (before.AwaitingUser || before.Paused || before.Stop || before.Completed || !before.Enabled) && len(s.Sent) > 0 {
			w.Failf(s, "a check on a hold sent %s", describeSends(s.Sent))
		}
	}
	if s.Label == "tick" {
		// A recorded check exits 0 whatever the agent or network did, so an
		// external scheduler never disables supervision over an outage.
		if s.Result.Code != 0 {
			w.Failf(s, "tick exited %d: %s", s.Result.Code, s.Result.Stderr)
			return
		}
		lc := s.Status.State.LastCheck
		if !lc.At.Equal(s.At) || lc.Action != s.action() {
			w.Failf(s, "status lastCheck %s %q does not match the check at %s %q", lc.At, lc.Action, s.At, s.action())
		}
		if latest := s.Timeline.Summary.LatestCheck; latest == nil || !latest.At.Equal(s.At) || !strings.Contains(latest.Description, "action "+s.action()) {
			w.Failf(s, "timeline latest check does not describe this check (%s %s)", s.At, s.action())
		}
	}
	// timeline spells states with underscores (awaiting_user), status with
	// spaces (awaiting user).
	if got, want := strings.ReplaceAll(s.Timeline.Control.State, "_", " "), s.Status.ControlState; got != want {
		w.Failf(s, "timeline control state %q, status %q", got, want)
	}
	if got := strings.ReplaceAll(s.Runs.ControlState(), "_", " "); got != s.Status.ControlState {
		w.Failf(s, "runs control state %q, status %q", got, s.Status.ControlState)
	}
	// Every delivery is recorded as an attempt. An attempt that failed
	// before anything arrived (a refused creation) has no delivery.
	if got := s.Runs.Attempts(); got < w.sendsTotal {
		w.Failf(s, "runs records %d send attempts; the agents received %d", got, w.sendsTotal)
	}
	for _, c := range s.Calls {
		// Besides thread commands, only the two read-only questions to BB's
		// retry plugin are expected.
		plugin := len(c) == 4 && c[0] == "provider-retry" && (c[1] == "explain" || c[1] == "status") && c[3] == "--json"
		if !plugin && (len(c) < 2 || c[0] != "thread") {
			w.Failf(s, "unexpected bb command %v", c)
		}
	}
}

// Failf fails the scenario with the step it happened at.
func (w *World) Failf(s *Step, format string, args ...any) {
	w.t.Helper()
	w.t.Errorf("%s at %s (%s): %s", w.Name, s.At.Format(time.RFC3339), s.Label, fmt.Sprintf(format, args...))
}

// Check fails the scenario when err is not nil.
func (w *World) Check(s *Step, name string, err error) {
	w.t.Helper()
	if err != nil {
		w.Failf(s, "%s: %v", name, err)
	}
}

// Waiting names open work a known failure is waiting on.
type Waiting string

const (
	// WaitNetworkGate is pull request #154: wait for connectivity before
	// re-prompting or handing off.
	WaitNetworkGate Waiting = "network gating, PR #154"
	// WaitChildRecovery is the lane recovering a direct child's failed turn.
	WaitChildRecovery Waiting = "child recovery, in progress"
	// WaitCaptivePortal has no open work: PR #154 counts any HTTP answer,
	// including a captive portal's, as online.
	WaitCaptivePortal Waiting = "captive portal detection, no open work"
	// WaitStallClock has no open work: the stall clock keeps running while
	// the network is down, so a turn that works again after a long outage
	// is nudged at once.
	WaitStallClock Waiting = "stall clock across an outage, no open work"
	// WaitResumeEpisode is the reliability lane's decision that an explicit
	// resume starts a new error episode. Until then resume keeps the spent
	// re-prompts, so a loop held on an error is held again at once.
	WaitResumeEpisode Waiting = "resume starts a new error episode, reliability lane"
)

// Known records an assertion that fails today because of open work. It never
// skips: the assertion runs on every build. While it fails the scenario
// passes and logs a line starting KNOWN FAILURE. When it starts to pass the
// scenario fails with FIXED KNOWN FAILURE, so whoever lands the work deletes
// the marker and the assertion is enforced from then on.
//
//	go test ./internal/scenario -v | grep 'KNOWN FAILURE'
//
// An assertion waiting on more than one piece of work names each of them.
func (w *World) Known(s *Step, waiting []Waiting, name string, err error) {
	w.t.Helper()
	var names []string
	for _, x := range waiting {
		names = append(names, string(x))
	}
	on := strings.Join(names, "; ")
	if err != nil {
		w.t.Logf("KNOWN FAILURE [%s] %s at %s: %s: %v", on, w.Name, s.At.Format(time.RFC3339), name, err)
		return
	}
	w.t.Errorf("FIXED KNOWN FAILURE [%s] %s at %s: %s now passes; remove its Known marker so it is enforced", on, w.Name, s.At.Format(time.RFC3339), name)
}

// Waits is shorthand for a list of open work.
func Waits(w ...Waiting) []Waiting { return w }

// Transcript renders the journal, printed when a scenario fails.
func (w *World) Transcript() string {
	var b strings.Builder
	fmt.Fprintf(&b, "scenario journal for %s:\n", w.Name)
	for i, s := range w.Journal {
		fmt.Fprintf(&b, "%2d %s %-16s exit=%d action=%s reason=%q holds=%v net=%d\n", i, s.At.Format(time.RFC3339), s.Label, s.Result.Code, s.action(), s.reason(), s.Holds(), s.NetChecks)
		for _, m := range s.Sent {
			fmt.Fprintf(&b, "     sent %s to %s (%s), %d bytes\n", m.Kind, m.Thread, m.Mode, len(m.Message))
		}
		for _, r := range s.Reports {
			fmt.Fprintf(&b, "     reported %s\n", r.Type)
		}
		if s.Result.Stderr != "" {
			fmt.Fprintf(&b, "     stderr: %s\n", strings.TrimSpace(s.Result.Stderr))
		}
	}
	return b.String()
}

func readLines[T any](t *testing.T, path string, out *[]T) {
	t.Helper()
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var v T
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		*out = append(*out, v)
	}
}

// Thread is a stand-in BB thread. The test sets what `bb thread show`,
// `thread log` and `thread list` return; the stand-in records what the
// thread is sent.
type Thread struct {
	w        *World
	ID       string
	Parent   string
	status   string
	events   []map[string]any
	children []string
	// OnSend decides how the thread reacts to a message or retry. The
	// default starts a turn that runs while the provider is reachable and
	// fails with a network error while it is not.
	OnSend func(th *Thread, m Send)
}

// The fictional BB project and environment every thread lives in.
const (
	exampleProject     = "proj_example"
	exampleEnvironment = "env_example"
)

// adopt starts tracking a thread the stand-in created with `thread spawn`.
func (w *World) adopt(id string) *Thread {
	th := &Thread{w: w, ID: id, status: "active", OnSend: reactToNetwork}
	w.Threads[id] = th
	th.writeLog()
	return th
}

// Thread returns the stand-in thread with this ID, creating it idle.
func (w *World) Thread(id string) *Thread {
	if th := w.Threads[id]; th != nil {
		return th
	}
	th := &Thread{w: w, ID: id, OnSend: reactToNetwork}
	w.Threads[id] = th
	th.Set("idle")
	th.writeLog()
	return th
}

// RateLimited records a structured usage limit that resets at reset: the
// turn fails and BB marks the window blocked.
func (th *Thread) RateLimited(reset time.Time) {
	th.Event("provider/error", map[string]any{"willRetry": false, "errorInfo": map[string]any{"category": "rate-limit", "httpStatusCode": 429, "providerCode": "rate_limit_event"}})
	th.Event("provider/rateLimits/updated", map[string]any{"rateLimits": map[string]any{"status": "blocked", "windows": []any{map[string]any{"status": "blocked", "resetsAtMs": reset.UnixMilli()}}}})
	th.Set("error")
}

// Child adds a direct child thread.
func (th *Thread) Child(id string) *Thread {
	c := th.w.Thread(id)
	c.Parent = th.ID
	c.Set(c.status)
	th.children = append(th.children, id)
	list := []map[string]any{}
	for _, cid := range th.children {
		list = append(list, map[string]any{"id": cid, "visibility": "hidden"})
	}
	th.w.writeJSON(th.ID+".children", list)
	return c
}

// Set changes the status `bb thread show` reports.
func (th *Thread) Set(status string) {
	th.status = status
	t := map[string]any{"id": th.ID, "status": status, "projectId": exampleProject, "environmentId": exampleEnvironment}
	if th.Parent != "" {
		t["parentThreadId"] = th.Parent
	}
	th.w.writeJSON(th.ID+".show", map[string]any{"thread": t})
}

// Event appends a log event stamped with the world's clock.
func (th *Thread) Event(typ string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	th.events = append(th.events, map[string]any{"seq": len(th.events) + 1, "type": typ, "createdAt": th.w.Now.UnixMilli(), "data": data})
	th.writeLog()
}

func (th *Thread) writeLog() {
	if th.events == nil {
		th.events = []map[string]any{}
	}
	th.w.writeJSON(th.ID+".log", th.events)
}

// Status is what `bb thread show` currently reports.
func (th *Thread) Status() string { return th.status }

// StartTurn begins a turn: the thread is active.
func (th *Thread) StartTurn() {
	th.Event("turn/started", nil)
	th.Set("active")
}

// FinishTurn ends a turn cleanly: the thread is idle.
func (th *Thread) FinishTurn() {
	th.Event("turn/completed", nil)
	th.Set("idle")
}

// NetworkError records a lost connection that BB has given up on: the
// thread is in error.
func (th *Thread) NetworkError(willRetry bool) {
	th.Event("provider/error", map[string]any{"willRetry": willRetry, "errorInfo": map[string]any{"category": "network", "httpStatusCode": nil, "providerCode": "econnreset"}})
	th.Set("error")
}

// RetryDeclined records, as BB's retry plugin does when a turn fails with an
// error it does not retry, a decision to decline made now.
func (th *Thread) RetryDeclined() {
	th.w.writeJSON(th.ID+".retry-decision", map[string]any{"threadId": th.ID, "diagnostic": map[string]any{
		"requestId": "creq_example1", "observedAt": th.w.Now.UnixMilli(), "availability": map[string]any{"kind": "not-routed"},
		"decision": map[string]any{"kind": "decline", "reason": "not-retryable"},
	}})
}

func (th *Thread) received(m Send) {
	if th.OnSend != nil {
		th.OnSend(th, m)
	}
}

// reactToNetwork is the default stand-in agent: a message or retry starts a
// turn, which fails at once if the provider is unreachable.
func reactToNetwork(th *Thread, m Send) {
	th.StartTurn()
	if !th.w.Net.Reachable() {
		th.NetworkError(false)
	}
}

func (w *World) writeJSON(name string, v any) {
	w.t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		w.t.Fatal(err)
	}
	tmp := filepath.Join(w.world, name+".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		w.t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(w.world, name)); err != nil {
		w.t.Fatal(err)
	}
}

// Connectivity is the fake connectivity target. keepgoing reaches it through
// KEEPGOING_NET_PROBE_URL; the stand-in agents use Reachable to decide
// whether their provider can be reached.
type Connectivity struct {
	URL   string
	world string
	mode  atomic.Value // "online", "offline" or "portal"
	hits  atomic.Int64
}

func newConnectivity(t *testing.T) *Connectivity {
	c := &Connectivity{}
	c.mode.Store("online")
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/portal" {
			rw.Header().Set("Content-Type", "text/html")
			io.WriteString(rw, "<html><body>Fictional sign-in page</body></html>")
			return
		}
		c.hits.Add(1)
		switch c.mode.Load() {
		case "offline":
			// Drop the connection: the client sees a transport error.
			if hj, ok := rw.(http.Hijacker); ok {
				if conn, _, err := hj.Hijack(); err == nil {
					conn.Close()
					return
				}
			}
			panic(http.ErrAbortHandler)
		case "portal":
			http.Redirect(rw, r, "/portal", http.StatusFound)
		default:
			rw.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(srv.Close)
	c.URL = srv.URL + "/zen"
	return c
}

// Online, Offline and Portal set what the connectivity target and the
// providers behind it do.
// The stand-in CLIs read the same state from net.mode.
func (c *Connectivity) Online()  { c.set("online") }
func (c *Connectivity) Offline() { c.set("offline") }
func (c *Connectivity) Portal()  { c.set("portal") }

func (c *Connectivity) set(mode string) {
	c.mode.Store(mode)
	if c.world != "" {
		tmp := filepath.Join(c.world, "net.mode.tmp")
		os.WriteFile(tmp, []byte(mode), 0o600)
		os.Rename(tmp, filepath.Join(c.world, "net.mode"))
	}
}

// Reachable says whether a provider can be reached: not offline and not
// behind a captive portal.
func (c *Connectivity) Reachable() bool { return c.mode.Load() == "online" }

func (c *Connectivity) take() int { return int(c.hits.Swap(0)) }

// Receiver is the fake webhook receiver. It accepts only correctly signed
// reports.
type Receiver struct {
	URL    string
	mu     sync.Mutex
	events []Report
	bad    int
}

// Report is one delivered webhook.
type Report struct {
	Type string         `json:"type"`
	Body map[string]any `json:"-"`
}

func newReceiver(t *testing.T) *Receiver {
	r := &Receiver{}
	key := webhookSecret()
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		ts, _ := strconv.ParseInt(req.Header.Get("webhook-timestamp"), 10, 64)
		want, _ := report.Sign(key, req.Header.Get("webhook-id"), time.Unix(ts, 0), body)
		r.mu.Lock()
		defer r.mu.Unlock()
		if want == nil || req.Header.Get("webhook-signature") != want["webhook-signature"] {
			r.bad++
			rw.WriteHeader(http.StatusUnauthorized)
			return
		}
		var v map[string]any
		json.Unmarshal(body, &v)
		typ, _ := v["type"].(string)
		r.events = append(r.events, Report{Type: typ, Body: v})
		rw.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.bad > 0 {
			t.Errorf("%d reports arrived with a bad signature", r.bad)
		}
	})
	r.URL = srv.URL
	return r
}

// Events returns every report delivered so far.
func (r *Receiver) Events() []Report {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Report(nil), r.events...)
}
