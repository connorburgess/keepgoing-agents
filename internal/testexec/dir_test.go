//go:build unix

package testexec

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// probeSource is a program for these tests. It appends the name it was run
// as to $PROBE_LOG. While $PROBE_KILLS names a file holding a number greater
// than zero, it lowers the number and kills itself, as the host does to a
// program it refuses; "always" kills it every time.
const probeSource = `package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func main() {
	if f, err := os.OpenFile(os.Getenv("PROBE_LOG"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		f.WriteString(filepath.Base(os.Args[0]) + " " + strings.Join(os.Args[1:], ",") + "\n")
		f.Close()
	}
	if kills := os.Getenv("PROBE_KILLS"); kills != "" {
		b, _ := os.ReadFile(kills)
		left := strings.TrimSpace(string(b))
		if n, _ := strconv.Atoi(left); n > 0 || left == "always" {
			if left != "always" {
				os.WriteFile(kills, []byte(strconv.Itoa(n-1)), 0o600)
			}
			syscall.Kill(os.Getpid(), syscall.SIGKILL)
		}
	}
	os.Exit(3)
}
`

type probe struct {
	dir        *Dir
	log, kills string
	env        []string
}

func newProbe(t *testing.T) *probe {
	t.Helper()
	root := t.TempDir()
	src := filepath.Join(root, "probe.go")
	if err := os.WriteFile(src, []byte(probeSource), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := NewDir(filepath.Join(root, "bin"))
	if err != nil {
		t.Fatal(err)
	}
	p := &probe{dir: d, log: filepath.Join(root, "log"), kills: filepath.Join(root, "kills")}
	p.env = []string{"PROBE_LOG=" + p.log, "PROBE_KILLS=" + p.kills}
	if _, err := d.Build("probe", src, p.env, "built"); err != nil {
		t.Fatal(err)
	}
	return p
}

func (p *probe) runs(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(p.log)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// Everything added to a Dir is run exactly once as it is added, under the
// name it was added as, and a non-zero exit is not a failure.
func TestDirRunsEachExecutableOnceAsItIsAdded(t *testing.T) {
	p := newProbe(t)
	alias, err := p.dir.Alias("probe", "other-name", p.env, "aliased")
	if err != nil {
		t.Fatal(err)
	}
	if alias != filepath.Join(p.dir.Path, "other-name") {
		t.Fatalf("alias path %s", alias)
	}
	if got := strings.Join(p.runs(t), "; "); got != "probe built; other-name aliased" {
		t.Fatalf("first runs: %s", got)
	}
	if err := p.dir.Host("sh"); err != nil {
		t.Fatal(err)
	}
	if target, err := os.Readlink(filepath.Join(p.dir.Path, "sh")); err != nil || !filepath.IsAbs(target) {
		t.Fatalf("host link: %q %v", target, err)
	}
	if err := p.dir.Host("no-such-command-for-testexec"); err == nil {
		t.Fatal("a missing host command was linked")
	}
}

// A launch the host ends by a signal is tried again until the program ends
// on its own.
func TestRunOnceRetriesALaunchEndedByASignal(t *testing.T) {
	p := newProbe(t)
	if err := os.WriteFile(p.kills, []byte("3"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RunOnce(p.dir.File("probe"), p.env, "retried"); err != nil {
		t.Fatalf("three refused launches then one that ran: %v", err)
	}
	// The build's own run, three launches that were killed, one that ran.
	if got := len(p.runs(t)); got != 5 {
		t.Fatalf("%d launches, want 5: %v", got, p.runs(t))
	}
}

// A program refused on every launch is an environment failure that names
// the path, after a bounded number of launches.
func TestRunOnceReportsARefusalAsAnEnvironmentFailure(t *testing.T) {
	p := newProbe(t)
	if err := os.WriteFile(p.kills, []byte("always"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := p.dir.File("probe")
	err := RunOnce(path, p.env)
	var refused *RefusedError
	if !errors.As(err, &refused) || refused.Path != path || refused.Launches != maxLaunches {
		t.Fatalf("%T %v", err, err)
	}
	if !strings.HasPrefix(err.Error(), EnvironmentFailure+": the host ended "+path+" by a signal on each of 20 launches") {
		t.Fatalf("message: %s", err)
	}
	if got := len(p.runs(t)); got != 1+maxLaunches {
		t.Fatalf("%d launches, want %d", got, 1+maxLaunches)
	}
	// Adding such a program to a Dir fails the same way.
	if _, err := p.dir.Alias("probe", "refused", p.env); !errors.As(err, &refused) || refused.Path != p.dir.File("refused") {
		t.Fatalf("alias of a refused program: %v", err)
	}
}

// A program that cannot be started at all is not a refusal.
func TestRunOnceDoesNotCallAMissingProgramARefusal(t *testing.T) {
	err := RunOnce(filepath.Join(t.TempDir(), "absent"), nil)
	var refused *RefusedError
	if err == nil || errors.As(err, &refused) || strings.Contains(err.Error(), EnvironmentFailure) {
		t.Fatalf("%v", err)
	}
}

// A build that does not finish within buildTimeout fails, saying so.
func TestBuildHasATimeLimit(t *testing.T) {
	old := buildTimeout
	buildTimeout = time.Nanosecond
	defer func() { buildTimeout = old }()
	d, err := NewDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Build("example", "../../cmd/keepgoing", nil); err == nil || !strings.Contains(err.Error(), "no result after 1ns") {
		t.Fatalf("%v", err)
	}
}
