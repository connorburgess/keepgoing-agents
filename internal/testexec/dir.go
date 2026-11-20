package testexec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// maxLaunches bounds how often RunOnce starts a program the host keeps
// ending by a signal.
const maxLaunches = 20

// EnvironmentFailure begins the message of every failure that is the host's
// doing and says nothing about the code under test. Grep for it.
const EnvironmentFailure = "ENVIRONMENT FAILURE"

// A RefusedError says the host ended a program by a signal on every launch.
// On macOS that is the first-run check refusing a file it has not seen: the
// kernel logs "ASP: Security policy would not allow process: <pid>, <path>"
// and kills the process as it starts. See "Executables that tests run" in
// docs/testing.md.
type RefusedError struct {
	Path     string
	Launches int
	Err      error
}

func (e *RefusedError) Error() string {
	return fmt.Sprintf("%s: the host ended %s by a signal on each of %d launches (%v). Nothing in the tests does that; on macOS look for it with: /usr/bin/log show --last 1h --predicate 'eventMessage CONTAINS \"Security policy would not allow process\"'. Run the tests again.",
		EnvironmentFailure, e.Path, e.Launches, e.Err)
}

func (e *RefusedError) Unwrap() error { return e.Err }

// RunOnce runs the program at path until it ends on its own, whatever its
// exit code, so that the host has checked it before a test depends on it. A
// launch ended by a signal is the host refusing the program and is tried
// again, up to a bound; the error is then a *RefusedError. Any other failure
// to start is returned as it is.
func RunOnce(path string, env []string, args ...string) error {
	return runOnce(path, env, false, args...)
}

// runOnce is RunOnce; with mustSucceed, a run that ends on its own with a
// non-zero exit is an error too, not retried: the program ran, and failed.
func runOnce(path string, env []string, mustSucceed bool, args ...string) error {
	var last error
	for n := 1; n <= maxLaunches; n++ {
		last = launch(path, env, args)
		var exit *exec.ExitError
		switch {
		case last == nil:
			return nil
		case !errors.As(last, &exit):
			return fmt.Errorf("testexec: %s could not be started: %w", path, last)
		case exit.Exited() && mustSucceed:
			return fmt.Errorf("testexec: %s exited %d on its first run", path, exit.ExitCode())
		case exit.Exited():
			return nil
		}
	}
	return &RefusedError{Path: path, Launches: maxLaunches, Err: last}
}

// launch starts the program once and waits for it. Tests of the retry rule
// replace it, so they do not depend on how the host treats a real process.
var launch = func(path string, env, args []string) error {
	cmd := exec.Command(path, args...)
	cmd.Env = env
	return cmd.Run()
}

// buildTimeout bounds one go build. A build that hangs fails the test binary
// that asked for it instead of running into the CI job's limit; the bound
// leaves room for a build with a cold cache.
var buildTimeout = 5 * time.Minute

// Dir is one directory of executables shared by every test of a test binary.
// Everything put in it is run once as it is added, so no test is the first to
// run a file, and the host's first-run check (slow, and on a busy machine
// sometimes a refusal that kills the process) is passed outside any test.
//
// Create it once, in TestMain or behind a sync.Once, and hand tests its Path:
// as a PATH, or as the place a configuration names. Do not copy or link its
// files into a directory per test. A new path is a file the host has not
// seen, which is what this type exists to avoid.
type Dir struct {
	// Path is the directory.
	Path string
}

// NewDir creates the directory.
func NewDir(path string) (*Dir, error) {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return nil, err
	}
	return &Dir{Path: path}, nil
}

// File is the path of name in the directory, with the host's executable
// suffix.
func (d *Dir) File(name string) string {
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(d.Path, name)
}

// Build compiles a Go package or source file into the directory as name and
// runs it once with args. Choose arguments that make the program end at
// once without doing its work; any exit code will do.
func (d *Dir) Build(name, pkg string, env []string, args ...string) (string, error) {
	out := d.File(name)
	gobin, err := exec.LookPath("go")
	if err != nil {
		gobin = (&Dir{Path: filepath.Join(runtime.GOROOT(), "bin")}).File("go")
	}
	ctx, cancel := context.WithTimeout(context.Background(), buildTimeout)
	defer cancel()
	if b, err := exec.CommandContext(ctx, gobin, "build", "-o", out, pkg).CombinedOutput(); err != nil {
		if ctx.Err() != nil {
			err = fmt.Errorf("no result after %s: %w", buildTimeout, ctx.Err())
		}
		return "", fmt.Errorf("testexec: go build %s: %v\n%s", pkg, err, b)
	}
	return out, RunOnce(out, env, args...)
}

// Alias gives an executable already in the directory a second name, for a
// program that plays several parts by the name it is run as, and runs it
// once under that name with args.
func (d *Dir) Alias(existing, name string, env []string, args ...string) (string, error) {
	src, dst := d.File(existing), d.File(name)
	if err := os.Link(src, dst); err != nil {
		body, err := os.ReadFile(src)
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(dst, body, 0o700); err != nil {
			return "", err
		}
	}
	return dst, RunOnce(dst, env, args...)
}

// Host links one command from the host, such as git, into the directory, so
// that the directory can be a test's whole PATH without exposing whatever
// else is installed beside that command.
func (d *Dir) Host(name string) error {
	found, err := exec.LookPath(name)
	if err != nil {
		return fmt.Errorf("testexec: the tests need %s: %w", name, err)
	}
	return os.Symlink(found, d.File(name))
}
