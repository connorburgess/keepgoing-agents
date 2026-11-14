package conformance

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/adapter/factory"
	"github.com/connorburgess/keepgoing-agents/internal/adapter/runner"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
)

// rig is the stand-in side of a built-in adapter under test.
type rig struct {
	dir      string // the stand-in's own records
	stateDir string
	turns    bool // the agent runs one turn per process
	supports map[Situation]bool
}

func (r *rig) Supports(s Situation) bool     { return r.supports[s] }
func (r *rig) CodedClass() decide.ErrorClass { return decide.ErrRateLimit }
func (r *rig) ReportedSession() string       { return standInSession }
func (r *rig) set(s Situation) error {
	return os.WriteFile(filepath.Join(r.dir, "situation"), []byte(s), 0o600)
}

func (r *rig) Sent() ([]Sent, error) {
	f, err := os.Open(filepath.Join(r.dir, "sent.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Sent
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		var s Sent
		if err := json.Unmarshal(sc.Bytes(), &s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, sc.Err()
}

// Stage puts the stand-in in a situation. An agent that runs one turn per
// process is only ever in a situation because a turn ended that way, so a
// turn is run; a working or hanging one is left running.
func (r *rig) Stage(ctx context.Context, a adapter.Adapter, s Situation) error {
	if err := r.set(s); err != nil {
		return err
	}
	if !r.turns {
		return nil
	}
	os.Remove(filepath.Join(r.dir, "quit"))
	before, _ := r.running()
	if err := a.Send(ctx, "stand-in staging turn", adapter.ModeAuto); err != nil {
		return err
	}
	if s == Active || s == Hanging {
		return nil
	}
	if s == Crashed {
		// End the turn's wrapper before it can record anything, then let the
		// agent it started go.
		for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(10 * time.Millisecond) {
			if now, _ := r.running(); now > before {
				break
			}
			if time.Now().After(deadline) {
				return errors.New("the stand-in agent did not begin")
			}
		}
		st, err := runner.Dir(filepath.Join(r.stateDir, "targets", "primary")).Status()
		if err != nil || st.Phase != runner.Running {
			return fmt.Errorf("the staging turn reads as %s: %v", st.Phase, err)
		}
		wrapper, err := os.FindProcess(st.Record.PID)
		if err != nil {
			return err
		}
		if err := wrapper.Kill(); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(r.dir, "quit"), nil, 0o600); err != nil {
			return err
		}
	}
	return r.Settle(ctx, a)
}

// running counts the stand-in agents that began and have not finished.
func (r *rig) running() (int, error) {
	b, err := os.ReadFile(filepath.Join(r.dir, "live"))
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	return strings.Count(string(b), "begin\n") - strings.Count(string(b), "finish\n"), err
}

// Settle waits on the runner's own record of the turn, not on a timer.
func (r *rig) Settle(ctx context.Context, a adapter.Adapter) error {
	if !r.turns {
		return nil
	}
	dir := runner.Dir(filepath.Join(r.stateDir, "targets", "primary"))
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		// A turn whose output cannot be read has still ended: go by its phase.
		st, err := dir.Status()
		if st.Phase != "" && st.Phase != runner.Running {
			return nil
		}
		if time.Now().After(deadline) {
			_ = a.Stop(ctx)
			return fmt.Errorf("the stand-in turn did not end: %v", err)
		}
	}
}

// turnRig is the rig of an agent that runs one turn per process. Only such an
// agent has turn records that foreign work can be planted in.
type turnRig struct{ *rig }

// Running implements AgentCounter.
func (r turnRig) Running() (int, error) { return r.running() }

// StartForeign starts a process this supervisor did not start and records its
// PID as an old build would have recorded a turn. A stop must not reach it.
func (r turnRig) StartForeign() (func() bool, func(), error) {
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), envPart+"=sleep", envDir+"="+r.dir)
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()
	dir := filepath.Join(r.stateDir, "targets", "primary")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, err
	}
	rec, _ := json.Marshal(runner.Record{PID: cmd.Process.Pid, StartedAt: time.Now(), Argv0: "foreign"})
	if err := os.WriteFile(filepath.Join(dir, "run.json"), rec, 0o600); err != nil {
		return nil, nil, err
	}
	alive := func() bool {
		select {
		case <-exited:
			return false
		default:
			return true
		}
	}
	end := func() {
		cmd.Process.Kill()
		<-exited
		os.Remove(filepath.Join(dir, "run.json"))
	}
	return alive, end, nil
}

// subject builds one built-in adapter over fresh directories, with this test
// binary standing in for its agent.
func subject(t *testing.T, kind string) Subject {
	t.Helper()
	root := t.TempDir()
	r := &rig{dir: filepath.Join(root, "stand-in"), stateDir: filepath.Join(root, "state")}
	project, home, temp := filepath.Join(root, "project"), filepath.Join(root, "home"), filepath.Join(root, "tmp")
	for _, dir := range []string{r.dir, r.stateDir, project, home, temp} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(project, "README.md"), []byte("fictional project\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The adapter, its wrapper and the stand-in all see a home and a temporary
	// directory that are watched, so a write to either is caught.
	for _, name := range []string{"HOME", "USERPROFILE"} {
		t.Setenv(name, home)
	}
	for _, name := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(name, temp)
	}
	// A binary built with the race detector waits a second at exit by default.
	env := map[string]string{envPart: kind, envDir: r.dir, "GORACE": "atexit_sleep_ms=0"}
	all := map[Situation]bool{Idle: true, Active: true, FailedCoded: true, FailedProse: true, Garbage: true, Hanging: true, Oversized: true,
		ExitNonZero: true, FailedUnmapped: true, FailedStderr: true}
	var agent config.Agent
	s := Subject{Name: kind, Rig: r, Protected: []string{project, home, temp}, CallLimit: 10 * time.Second, StateDir: r.stateDir}
	switch kind {
	case "process":
		all[Waiting] = true
		self := os.Args[0]
		agent = config.Agent{Adapter: "process", Process: &config.Process{
			StatusCommand: []string{self, "status"}, ResumeCommand: []string{self, "resume"}, StopCommand: []string{self, "stop"},
			Env: env, Timeout: config.Duration{Duration: 2 * time.Second}}}
		s.Declared = Declared{Stoppable: true, StructuredErrors: true, StructuredWaiting: true}
	case "claude":
		r.turns = true
		agent = config.Agent{Adapter: "claude", Claude: &config.Claude{CLI: os.Args[0], Workdir: project, Env: env}}
	case "codex":
		r.turns = true
		agent = config.Agent{Adapter: "codex", Codex: &config.Codex{CLI: os.Args[0], Workdir: project, Env: env}}
	}
	if spec, ok := runner.SpecFor(kind); ok {
		s.Rig = turnRig{r}
		all[Crashed] = true
		c := spec.Capabilities
		s.Declared = Declared{ExactResume: c.ExactResume, Stoppable: c.Stoppable, StructuredErrors: c.StructuredErrors, StructuredWaiting: c.StructuredWaiting}
	}
	r.supports = all
	s.New = func() (adapter.Adapter, error) { return factory.New(agent, r.stateDir, "primary") }
	return s
}

// known lists, per adapter, the contracts it does not keep today and why.
// A known failure is reported and does not fail the suite. When one starts to
// pass, the suite fails until its entry is removed, so a fix is recorded.
//
// An entry names the case and the words of the failure it expects. A case
// that fails for any other reason is an ordinary failure, so a gap closed in
// a way that breaks the case differently is not hidden behind its entry.
type gap struct{ why, says string }

var known = map[string]map[string]gap{
	"process": {
		"bounded-output": {"the status command's output is read without a bound", "output far larger than any real status was read as"},
	},
	"claude": {
		"bounded-output": {"a turn's output is read without a bound", "output far larger than any real status was read as"},
	},
	"codex": {
		"bounded-output": {"a turn's output is read without a bound", "output far larger than any real status was read as"},
	},
}

// Every built-in adapter that can run without a provider is run against the
// same contracts.
func TestBuiltInAdaptersKeepTheContract(t *testing.T) {
	for _, kind := range []string{"process", "claude", "codex"} {
		t.Run(kind, func(t *testing.T) {
			results := Run(context.Background(), subject(t, kind), Options{Fresh: func() (Subject, error) { return subject(t, kind), nil }})
			seen := map[string]bool{}
			for _, r := range results {
				seen[r.ID] = true
				gap, isKnown := known[kind][r.ID]
				switch {
				case r.Outcome == Fail && isKnown && strings.Contains(r.Detail, gap.says):
					t.Logf("KNOWN GAP [%s] %s: %s (%s)", kind, r.ID, gap.why, r.Detail)
				case r.Outcome == Fail && isKnown:
					t.Errorf("%s: %s is a known gap, but it now fails differently; fix it or update its entry\n  expected: %s\n  got: %s", kind, r.ID, gap.says, r.Detail)
				case r.Outcome == Fail:
					t.Errorf("%s: %s\n  contract: %s\n  %s", kind, r.ID, r.Contract, r.Detail)
				case isKnown:
					t.Errorf("FIXED KNOWN GAP [%s] %s no longer fails (%s); remove its entry from known", kind, r.ID, r.Outcome)
				case r.Outcome == Skipped:
					t.Logf("skipped [%s] %s: %s", kind, r.ID, r.Detail)
				}
			}
			for _, c := range Cases() {
				if !seen[c.ID] {
					t.Errorf("%s: case %s produced no result", kind, c.ID)
				}
			}
			for id := range known[kind] {
				if !seen[id] {
					t.Errorf("known gap %s names a case that does not exist", id)
				}
			}
		})
	}
}
